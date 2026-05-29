package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"xbot/channel"
	"xbot/protocol"

	log "xbot/logger"
)

// ---------------------------------------------------------------------------
// GrpcPluginTransport — bidirectional JSON-RPC over stdin/stdout
//
// Wraps a plugin process's stdin/stdout pipes as a full-duplex JSON-RPC
// channel. The plugin acts as a full RPC client (like remote CLI over WS),
// receiving WSMessage events from xbot and sending RPC requests to xbot.
//
// Protocol (identical to WS):
//   - Plugin → xbot (RPC request):  {"id":"1","method":"send_inbound","params":{...}}
//   - Plugin → xbot (RPC response): {"id":"1","result":{...}}  (for xbot→plugin calls)
//   - xbot → Plugin (event push):   {"type":"progress","progress":{...}}
//   - xbot → Plugin (RPC request):  {"id":"2","method":"channel_send","params":{...}}
//   - xbot → Plugin (RPC response): {"id":"2","result":"ok"}  (for plugin's requests)
// ---------------------------------------------------------------------------

// GrpcPluginTransport manages bidirectional JSON-RPC with a plugin process.
// It implements channel.Channel for registration in the Dispatcher,
// channel.ProgressSender and channel.SessionStateSender for event push,
// and channel.UserMessageInjector for background message injection.
type GrpcPluginTransport struct {
	name    string
	process processIO

	// RPC dispatch: plugin→xbot requests are dispatched through this function.
	dispatch func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error)

	// Event push: WSMessage events are written to plugin stdin.
	eventCh chan protocol.WSMessage

	// xbot→plugin RPC: pending calls from xbot to the plugin.
	pending   map[string]chan *rpcResponse
	pendingMu sync.Mutex
	rpcID     atomic.Int64

	// Lifecycle
	writeMu   sync.Mutex // serializes writes to stdin (Call + PushEvent)
	closeCh   chan struct{}
	closeOnce sync.Once
}

// processIO abstracts the stdin/stdout pair of a plugin process.
// This allows testing with mock pipes.
type processIO interface {
	stdinWrite(v any) error
	stdoutRead() (json.RawMessage, error)
	close() error
}

// stdioPipes wraps os pipes from exec.Cmd.
type stdioPipes struct {
	stdin  io.Writer
	stdout io.Reader
	enc    *json.Encoder
	dec    *json.Decoder
}

func newStdioPipes(stdin io.Writer, stdout io.Reader) *stdioPipes {
	return &stdioPipes{
		stdin:  stdin,
		stdout: stdout,
		enc:    json.NewEncoder(stdin),
		dec:    json.NewDecoder(stdout),
	}
}

func (p *stdioPipes) stdinWrite(v any) error {
	return p.enc.Encode(v)
}

func (p *stdioPipes) stdoutRead() (json.RawMessage, error) {
	var raw json.RawMessage
	if err := p.dec.Decode(&raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func (p *stdioPipes) close() error {
	if closer, ok := p.stdin.(io.Closer); ok {
		closer.Close()
	}
	if closer, ok := p.stdout.(io.Closer); ok {
		closer.Close()
	}
	return nil
}

// GrpcPluginTransportConfig holds configuration for creating a GrpcPluginTransport.
type GrpcPluginTransportConfig struct {
	// Name is the channel name (from ChannelProviderDecl.Name).
	Name string

	// Stdin is the write end of the plugin's stdin pipe.
	Stdin io.Writer

	// Stdout is the read end of the plugin's stdout pipe.
	Stdout io.Reader

	// Dispatch is the RPC dispatch function for plugin→xbot calls.
	// Typically RPCTable.Dispatch wrapped with context injection.
	Dispatch func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error)

	// EventCh receives WSMessage events that should be pushed to the plugin.
	// The transport reads from this channel and writes to the plugin's stdin.
	EventCh chan protocol.WSMessage
}

// NewGrpcPluginTransport creates a new GrpcPluginTransport from config.
func NewGrpcPluginTransport(cfg GrpcPluginTransportConfig) *GrpcPluginTransport {
	return &GrpcPluginTransport{
		name:     cfg.Name,
		process:  newStdioPipes(cfg.Stdin, cfg.Stdout),
		dispatch: cfg.Dispatch,
		eventCh:  cfg.EventCh,
		pending:  make(map[string]chan *rpcResponse),
		closeCh:  make(chan struct{}),
	}
}

// NewGrpcPluginTransportWithIO creates a GrpcPluginTransport with a custom processIO
// (for testing).
func NewGrpcPluginTransportWithIO(name string, pio processIO, dispatch func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error), eventCh chan protocol.WSMessage) *GrpcPluginTransport {
	return &GrpcPluginTransport{
		name:     name,
		process:  pio,
		dispatch: dispatch,
		eventCh:  eventCh,
		pending:  make(map[string]chan *rpcResponse),
		closeCh:  make(chan struct{}),
	}
}

// EventCh returns the event channel for pushing WSMessage events to the plugin.
func (t *GrpcPluginTransport) EventCh() chan protocol.WSMessage {
	return t.eventCh
}

// ---------------------------------------------------------------------------
// Transport-like methods: xbot→plugin RPC calls
// ---------------------------------------------------------------------------

// Call sends an RPC request from xbot to the plugin and waits for the response.
// Used for server→plugin calls (e.g., channel_send equivalent).
func (t *GrpcPluginTransport) Call(method string, payload json.RawMessage) (json.RawMessage, error) {
	id := fmt.Sprintf("srv-%d", t.rpcID.Add(1))
	ch := make(chan *rpcResponse, 1)

	t.pendingMu.Lock()
	t.pending[id] = ch
	t.pendingMu.Unlock()

	req := protocol.WSClientMessage{
		Type:   protocol.MsgTypeRPC,
		ID:     id,
		Method: method,
		Params: payload,
	}

	t.writeMu.Lock()
	err := t.process.stdinWrite(req)
	t.writeMu.Unlock()

	if err != nil {
		t.pendingMu.Lock()
		delete(t.pending, id)
		t.pendingMu.Unlock()
		return nil, fmt.Errorf("grpc transport: write RPC %s: %w", method, err)
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("grpc transport: RPC %s: connection closed", method)
		}
		if resp.Error != "" {
			return nil, fmt.Errorf("grpc transport: RPC %s: %s", method, resp.Error)
		}
		return resp.Result, nil
	case <-time.After(30 * time.Second):
		t.pendingMu.Lock()
		delete(t.pending, id)
		t.pendingMu.Unlock()
		return nil, fmt.Errorf("grpc transport: RPC %s: timeout", method)
	case <-t.closeCh:
		t.pendingMu.Lock()
		delete(t.pending, id)
		t.pendingMu.Unlock()
		return nil, fmt.Errorf("grpc transport: RPC %s: transport closed", method)
	}
}

// PushEvent writes a WSMessage event to the plugin's stdin.
// Used for progress, stream_content, session state, etc.
func (t *GrpcPluginTransport) PushEvent(msg protocol.WSMessage) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	return t.process.stdinWrite(msg)
}

// ---------------------------------------------------------------------------
// channel.Channel interface
// ---------------------------------------------------------------------------

// compile-time checks
var (
	_ channel.Channel             = (*GrpcPluginTransport)(nil)
	_ channel.ProgressSender      = (*GrpcPluginTransport)(nil)
	_ channel.SessionStateSender  = (*GrpcPluginTransport)(nil)
	_ channel.UserMessageInjector = (*GrpcPluginTransport)(nil)
)

func (t *GrpcPluginTransport) Name() string                                    { return t.name }
func (t *GrpcPluginTransport) SetChatID(string)                                {}
func (t *GrpcPluginTransport) SetSendInboundFn(func(channel.InboundMsg) error) {}

func (t *GrpcPluginTransport) Start() error {
	go t.eventPushLoop()
	return nil
}

func (t *GrpcPluginTransport) Stop() {
	t.closeOnce.Do(func() {
		close(t.closeCh)
		// Unblock all pending RPC callers.
		t.pendingMu.Lock()
		for id, ch := range t.pending {
			select {
			case ch <- &rpcResponse{Error: "transport closed"}:
			default:
			}
			delete(t.pending, id)
		}
		t.pendingMu.Unlock()
	})
}

// Send implements channel.Channel.Send.
// Converts the OutboundMsg to a WSMessage and pushes it to the plugin.
func (t *GrpcPluginTransport) Send(msg channel.OutboundMsg) (string, error) {
	wsMsg := protocol.WSMessage{
		Type:     protocol.MsgTypeText,
		Content:  msg.Content,
		ChatID:   msg.ChatID,
		Channel:  msg.Channel,
		Metadata: msg.Metadata,
	}
	if err := t.PushEvent(wsMsg); err != nil {
		return "", fmt.Errorf("grpc transport send: %w", err)
	}
	return "", nil
}

// ---------------------------------------------------------------------------
// channel.ProgressSender interface
// ---------------------------------------------------------------------------

func (t *GrpcPluginTransport) SendProgress(chatID string, payload *protocol.ProgressEvent) {
	msg := protocol.WSMessage{
		Type:     protocol.MsgTypeProgress,
		ChatID:   chatID,
		Progress: payload,
	}
	if err := t.PushEvent(msg); err != nil {
		log.WithField("channel", t.name).WithError(err).Warn("Failed to push progress event")
	}
}

func (t *GrpcPluginTransport) SendStreamContent(chatID, content, reasoning string) {
	msg := protocol.WSMessage{
		Type:    protocol.MsgTypeStreamContent,
		ChatID:  chatID,
		Content: content,
	}
	if reasoning != "" {
		msg.Progress = &protocol.ProgressEvent{
			ReasoningStreamContent: reasoning,
		}
	}
	if err := t.PushEvent(msg); err != nil {
		log.WithField("channel", t.name).WithError(err).Warn("Failed to push stream content")
	}
}

// ---------------------------------------------------------------------------
// channel.SessionStateSender interface
// ---------------------------------------------------------------------------

func (t *GrpcPluginTransport) SendSessionState(ev protocol.SessionEvent) {
	msg := protocol.WSMessage{
		Type:    protocol.MsgTypeSession,
		Session: &ev,
	}
	if err := t.PushEvent(msg); err != nil {
		log.WithField("channel", t.name).WithError(err).Warn("Failed to push session state")
	}
}

// ---------------------------------------------------------------------------
// channel.UserMessageInjector interface
// ---------------------------------------------------------------------------

func (t *GrpcPluginTransport) InjectUserMessage(chatID, content string) {
	msg := protocol.WSMessage{
		Type:    protocol.MsgTypeInjectUser,
		ChatID:  chatID,
		Content: content,
	}
	if err := t.PushEvent(msg); err != nil {
		log.WithField("channel", t.name).WithError(err).Warn("Failed to inject user message")
	}
}

// ---------------------------------------------------------------------------
// Close / Run lifecycle
// ---------------------------------------------------------------------------

// Close stops the transport and releases resources.
func (t *GrpcPluginTransport) Close() error {
	t.Stop()
	return t.process.close()
}

// Run starts the readLoop that reads JSON-RPC from the plugin's stdout.
// Blocks until the context is cancelled or the plugin stdout is closed.
func (t *GrpcPluginTransport) Run(ctx context.Context) {
	t.readLoop(ctx)
}

// ---------------------------------------------------------------------------
// Internal: readLoop, eventPushLoop, response dispatch
// ---------------------------------------------------------------------------

// readLoop reads JSON lines from plugin stdout and routes them:
//   - RPC response (has "id" + "result"/"error", no "method") → deliver to pending Call
//   - RPC request (has "id" + "method") → dispatch via RPCTable, write response back
func (t *GrpcPluginTransport) readLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.closeCh:
			return
		default:
		}

		line, err := t.process.stdoutRead()
		if err != nil {
			if ctx.Err() != nil {
				return // normal shutdown
			}
			log.WithField("channel", t.name).WithError(err).Info("Plugin stdout closed")
			// Fail any pending call from xbot→plugin.
			t.pendingMu.Lock()
			for id, ch := range t.pending {
				select {
				case ch <- &rpcResponse{Error: "plugin stdout closed"}:
				default:
				}
				delete(t.pending, id)
			}
			t.pendingMu.Unlock()
			return
		}

		t.handleIncoming(line)
	}
}

// handleIncoming routes an incoming JSON message from the plugin.
func (t *GrpcPluginTransport) handleIncoming(raw json.RawMessage) {
	// Peek at the message to determine type.
	var peek struct {
		ID     string          `json:"id"`
		Method string          `json:"method"`
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	if err := json.Unmarshal(raw, &peek); err != nil {
		log.WithField("channel", t.name).WithError(err).Warn("Failed to parse plugin message")
		return
	}

	if peek.Method != "" {
		// RPC request from plugin → dispatch to RPCTable
		t.handlePluginRPC(peek.ID, peek.Method, raw)
	} else if peek.ID != "" {
		// RPC response from plugin → deliver to pending xbot→plugin call
		t.handlePluginResponse(peek.ID, peek.Result, peek.Error)
	}
	// else: unknown message type, ignore
}

// handlePluginRPC dispatches an RPC request from the plugin to xbot's RPCTable.
func (t *GrpcPluginTransport) handlePluginRPC(id, method string, raw json.RawMessage) {
	// Extract params from the raw message.
	var req struct {
		Params json.RawMessage `json:"params"`
	}
	json.Unmarshal(raw, &req)

	if t.dispatch == nil {
		t.writeRPCResponse(id, nil, "no dispatch function")
		return
	}

	ctx := context.Background()
	result, err := t.dispatch(ctx, method, req.Params)
	if err != nil {
		t.writeRPCResponse(id, nil, err.Error())
		return
	}
	t.writeRPCResponse(id, result, "")
}

// handlePluginResponse delivers a response from the plugin to a pending xbot→plugin call.
func (t *GrpcPluginTransport) handlePluginResponse(id string, result json.RawMessage, errMsg string) {
	t.pendingMu.Lock()
	ch, ok := t.pending[id]
	if ok {
		delete(t.pending, id)
	}
	t.pendingMu.Unlock()

	if ok {
		select {
		case ch <- &rpcResponse{ID: id, Result: result, Error: errMsg}:
		default:
		}
	}
}

// writeRPCResponse writes an RPC response back to the plugin's stdin.
func (t *GrpcPluginTransport) writeRPCResponse(id string, result json.RawMessage, errMsg string) {
	resp := rpcResponse{
		Type:   protocol.MsgTypeRPCResponse,
		ID:     id,
		Result: result,
		Error:  errMsg,
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if err := t.process.stdinWrite(resp); err != nil {
		log.WithField("channel", t.name).WithError(err).Warn("Failed to write RPC response")
	}
}

// eventPushLoop reads from eventCh and pushes WSMessage events to the plugin.
func (t *GrpcPluginTransport) eventPushLoop() {
	for {
		select {
		case <-t.closeCh:
			return
		case msg, ok := <-t.eventCh:
			if !ok {
				return
			}
			if err := t.PushEvent(msg); err != nil {
				log.WithField("channel", t.name).WithError(err).Warn("Failed to push event")
			}
		}
	}
}
