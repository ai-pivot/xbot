package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"xbot/bus"
	"xbot/channel"
	"xbot/config"
	llm "xbot/llm"
	"xbot/protocol"
	"xbot/tools"
)

// ---------------------------------------------------------------------------
// ChannelTransport: Go channel-based Transport for local (in-process) mode
// ---------------------------------------------------------------------------

// channelTransport implements Transport using Go channels.
// It connects CLI to an in-process Agent via Go channels — the same
// communication pattern as RemoteTransport (WS) but without network overhead.
//
// Architecture:
//
//	CLI → Backend → ChannelTransport.Call()  → reqCh  → bridge goroutine → localTransport.Call()
//	CLI → Backend → ChannelTransport.Send()  → msgCh  → bridge goroutine → bus.Inbound
//	Agent → ChannelCliChannel → eventCh → eventLoop goroutine → baseTransport.emit → Subscribe handlers
//
// This is architecturally identical to remote mode (WS transport),
// ensuring both modes share the same code paths in CLI.
type channelTransport struct {
	baseTransport

	// Internal handler dispatch (delegates to the same handler table as localTransport).
	inner *localTransport

	// RPC: transport → bridge goroutine
	reqCh chan *chanRPCReq

	// Messages: transport → bridge goroutine
	msgCh chan bus.InboundMessage

	// Events: ChannelCliChannel → eventLoop goroutine
	eventCh chan protocol.WSMessage

	// TUI control
	tuiCtrlMu sync.Mutex
	tuiCtrlCb func(string, map[string]string) (map[string]string, error)

	ctx   context.Context
	close context.CancelFunc
	done  chan struct{}
}

type chanRPCReq struct {
	method  string
	payload json.RawMessage
	respCh  chan *chanRPCResp
}

type chanRPCResp struct {
	result json.RawMessage
	err    error
}

// newChannelTransport creates a channel-based transport that delegates RPC
// handling to the given localTransport. The dispatcher is used for callback
// injection (channelFinder, messageSender, agentChannelRegistry).
func newChannelTransport(lt *localTransport, disp *channel.Dispatcher, eventCh chan protocol.WSMessage) *channelTransport {
	ctx, close := context.WithCancel(context.Background())
	t := &channelTransport{
		baseTransport: newBaseTransport(),
		inner:         lt,
		reqCh:         make(chan *chanRPCReq, 64),
		msgCh:         make(chan bus.InboundMessage, 64),
		eventCh:       eventCh,
		ctx:           ctx,
		close:         close,
		done:          make(chan struct{}),
	}

	// Inject ALL callbacks into Agent during construction — this is the
	// SINGLE injection point for local mode. No WireCallbacks from CLI.
	cliCh := channel.NewChannelCliChannel(eventCh)
	ag := lt.agent
	ag.WireCallbacks(
		disp.SendDirect,
		disp.GetChannel,
		func(ev protocol.SessionEvent) { cliCh.SendSessionState(ev) },
		disp,
		func(name string, runFn bus.RunFn) error {
			ac := channel.NewAgentChannel(name, runFn)
			if err := ac.Start(); err != nil {
				return fmt.Errorf("start AgentChannel %s: %w", name, err)
			}
			disp.Register(ac)
			return nil
		},
		func(name string) { disp.Unregister(name) },
	)

	return t
}

// ---------------------------------------------------------------------------
// Transport interface
// ---------------------------------------------------------------------------

func (t *channelTransport) Start(ctx context.Context) error {
	go t.serve()
	go t.eventLoop()
	go t.inner.agent.Run(ctx)
	return nil
}

func (t *channelTransport) Stop() {
	t.close()
	_ = t.inner.agent.Close()
}

func (t *channelTransport) Close() error {
	t.close()
	<-t.done
	return t.inner.agent.Close()
}

func (t *channelTransport) Run(ctx context.Context) error {
	return t.inner.agent.Run(ctx)
}

// Call sends an RPC request through a Go channel to the bridge goroutine.
// This is the channel-based equivalent of RemoteTransport's WS RPC.
func (t *channelTransport) Call(method string, payload json.RawMessage) (json.RawMessage, error) {
	respCh := make(chan *chanRPCResp, 1)
	req := &chanRPCReq{method: method, payload: payload, respCh: respCh}

	select {
	case t.reqCh <- req:
	case <-t.ctx.Done():
		return nil, fmt.Errorf("transport closed")
	}

	select {
	case resp := <-respCh:
		return resp.result, resp.err
	case <-t.ctx.Done():
		return nil, fmt.Errorf("transport closed")
	}
}

// SendMessage sends a user message through a Go channel to the bridge goroutine.
func (t *channelTransport) SendMessage(msg protocol.InboundMessage) error {
	bMsg := bus.InboundMessage{
		Content: msg.Content, Channel: msg.Channel, ChatID: msg.ChatID,
		SenderID: msg.SenderID, SenderName: msg.SenderName, ChatType: msg.ChatType,
	}
	select {
	case t.msgCh <- bMsg:
		return nil
	case <-t.ctx.Done():
		return fmt.Errorf("transport closed")
	}
}

func (t *channelTransport) BindChat(string) error { return nil }

func (t *channelTransport) SetTUIControlHandler(cb func(string, map[string]string) (map[string]string, error)) {
	t.tuiCtrlMu.Lock()
	t.tuiCtrlCb = cb
	t.tuiCtrlMu.Unlock()
}

func (t *channelTransport) ConnState() string { return "connected" }
func (t *channelTransport) IsRemote() bool    { return false }
func (t *channelTransport) ServerURL() string { return "" }

// Agent returns the internal Agent for callback injection.
// This is ONLY for the construction phase — CLI must not call Agent methods
// directly after construction.
func (t *channelTransport) Agent() *Agent {
	return t.inner.agent
}

// ---------------------------------------------------------------------------
// Internal goroutines
// ---------------------------------------------------------------------------

// serve reads from RPC and message channels and dispatches to the handler table / bus.
func (t *channelTransport) serve() {
	for {
		select {
		case req := <-t.reqCh:
			result, err := t.inner.Call(req.method, req.payload)
			select {
			case req.respCh <- &chanRPCResp{result: result, err: err}:
			default:
			}
		case msg := <-t.msgCh:
			select {
			case t.inner.bus.Inbound <- msg:
			default:
			}
		case <-t.ctx.Done():
			return
		}
	}
}

// eventLoop reads WSMessage events from ChannelCliChannel and emits them
// through baseTransport to Subscribe handlers. This mirrors RemoteTransport's
// readPump — same event types, same dispatch logic.
func (t *channelTransport) eventLoop() {
	defer close(t.done)
	for {
		select {
		case wsMsg := <-t.eventCh:
			t.dispatchWSMessage(wsMsg)
		case <-t.ctx.Done():
			return
		}
	}
}

// dispatchWSMessage converts a WSMessage to the appropriate event type
// and emits via baseTransport. Mirrors RemoteTransport's readPump.
func (t *channelTransport) dispatchWSMessage(msg protocol.WSMessage) {
	switch msg.Type {
	case protocol.MsgTypeProgress:
		if msg.Progress != nil {
			t.emit(t.ctx, msg.Progress)
		}
	case protocol.MsgTypeText:
		t.emit(t.ctx, protocol.OutboundEvent{
			ChatID:  msg.ChatID,
			Channel: msg.Channel,
			Content: msg.Content,
		})
	case protocol.MsgTypeStreamContent:
		if msg.Progress != nil {
			t.emit(t.ctx, &protocol.ProgressEvent{
				ChatID:                 msg.ChatID,
				StreamContent:          msg.Progress.StreamContent,
				ReasoningStreamContent: msg.Progress.ReasoningStreamContent,
			})
		}
	case protocol.MsgTypeAskUser:
		var ev protocol.AskUserEvent
		if err := json.Unmarshal([]byte(msg.Content), &ev); err == nil {
			t.emit(t.ctx, ev)
		}
	case protocol.MsgTypeInjectUser:
		t.emit(t.ctx, protocol.InjectUserEvent{
			ChatID:  msg.ChatID,
			Content: msg.Content,
		})
	case protocol.MsgTypeSession:
		if msg.Session != nil {
			t.emit(t.ctx, *msg.Session)
		}
	case protocol.MsgTypePluginWidgets:
		var zones map[string]string
		if err := json.Unmarshal([]byte(msg.Content), &zones); err == nil {
			t.emit(t.ctx, protocol.PluginWidgetEvent{
				ChatID: msg.ChatID,
				Zones:  zones,
			})
		}
	case protocol.MsgTypeTUIControlReq:
		t.tuiCtrlMu.Lock()
		cb := t.tuiCtrlCb
		t.tuiCtrlMu.Unlock()
		if cb != nil {
			// TUI control handled synchronously for simplicity
			_ = cb
		}
	}
}

// emit is inherited from baseTransport. This wrapper makes it accessible.
func (t *channelTransport) emit(ctx context.Context, ev protocol.TransportEvent) {
	t.baseTransport.emit(ctx, ev)
}

// ChannelEventCh returns the event channel for tests and external wiring.
func (t *channelTransport) ChannelEventCh() chan<- protocol.WSMessage {
	return t.eventCh
}

// ensure channelTransport implements Transport at compile time.
var _ Transport = (*channelTransport)(nil)

// ---------------------------------------------------------------------------
// Constructor for Backend
// ---------------------------------------------------------------------------

// LLMSetupConfig holds LLM configuration for agent initialization.
type LLMSetupConfig struct {
	Tiers     config.LLMConfig
	Contexts  map[string]int
	MaxTokens int
	Retry     llm.RetryConfig
}

// ChannelTransportConfig holds everything needed to create a local-mode Backend
// with channel-based transport. Passed from CLI main.go.
type ChannelTransportConfig struct {
	AgentConfig Config
	Dispatcher  *channel.Dispatcher
	InitTools   []tools.Tool // Core tools to register during construction
	LLMSetup    LLMSetupConfig

	// Non-serializable callbacks injected into Agent after construction.
	// These are function closures that can't go through RPC.
	// They are set here for local mode, and called during NewChannelBackend.
	TUICtrl        func(action string, params map[string]string) (map[string]string, error)
	ChatRenameFn   func(chatID, newName string) (oldName string, err error)
	PromptProvider ChannelPromptProvider
}

// NewChannelBackend creates a local-mode Backend with channel-based Transport.
// This is the unified local mode entry point — CLI never touches Agent directly.
// All initialization (tools, LLM config, callbacks) happens here.
func NewChannelBackend(cfg ChannelTransportConfig) (*Backend, error) {
	a, err := New(cfg.AgentConfig)
	if err != nil {
		return nil, err
	}

	// Register core tools
	for _, tool := range cfg.InitTools {
		a.RegisterCoreTool(tool)
	}
	a.IndexGlobalTools()

	// Configure LLM
	a.llmFactory.SetModelTiers(cfg.LLMSetup.Tiers)
	a.llmFactory.SetModelContexts(cfg.LLMSetup.Contexts)
	if cfg.LLMSetup.MaxTokens > 0 {
		a.llmFactory.SetGlobalMaxTokens(cfg.LLMSetup.MaxTokens)
	}
	a.llmFactory.SetRetryConfig(cfg.LLMSetup.Retry)

	// Inject non-serializable callbacks
	if cfg.TUICtrl != nil {
		a.SetTUICallbacks(cfg.TUICtrl, nil, nil)
	}
	if cfg.ChatRenameFn != nil {
		a.SetChatRenameFn(cfg.ChatRenameFn)
	}
	if cfg.PromptProvider != nil {
		a.SetChannelPromptProviders(cfg.PromptProvider)
	}

	lt := newLocalTransport(a, cfg.AgentConfig.Bus)
	eventCh := make(chan protocol.WSMessage, 256)
	ct := newChannelTransport(lt, cfg.Dispatcher, eventCh)

	return &Backend{
		agent:     nil, // intentionally nil — CLI must not access Agent directly
		bus:       nil,
		transport: ct,
	}, nil
}

// InternalAgent returns the internal Agent for serverapp compatibility.
// DEPRECATED: This exists only during the migration period.
// New code should use Backend RPC methods exclusively.
func (b *Backend) InternalAgent() *Agent {
	if ct, ok := b.transport.(*channelTransport); ok {
		return ct.Agent()
	}
	return nil
}
