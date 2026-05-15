package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"xbot/bus"
	"xbot/channel"
	"xbot/protocol"

	log "xbot/logger"
)

// Client is the unified client for both local and remote modes.
// All methods are RPC calls through Transport.
//
// In local mode: Transport = ChannelTransport, events arrive via eventCh.
// In remote mode: Transport = RemoteTransport, events arrive via WebSocket.
type Client struct {
	transport Transport
	eventCh   chan protocol.WSMessage // nil in remote mode
	msgBus    *bus.MessageBus         // nil in remote mode — used by SendInbound

	// Event subscription (shared with baseTransport pattern)
	base baseTransport

	// Lifecycle management
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// NewClient creates a new Client.
//
// Parameters:
//   - transport: the RPC transport (ChannelTransport for local, RemoteTransport for remote)
//   - eventCh: channel for receiving server-pushed events (nil for remote mode)
//   - msgBus: message bus for sending inbound messages (nil for remote mode)
func NewClient(transport Transport, eventCh chan protocol.WSMessage, msgBus *bus.MessageBus) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{
		transport: transport,
		eventCh:   eventCh,
		msgBus:    msgBus,
		base:      newBaseTransport(),
		ctx:       ctx,
		cancel:    cancel,
		done:      make(chan struct{}),
	}
	if eventCh != nil {
		go c.eventLoop()
	}
	return c
}

// ---------------------------------------------------------------------------
// eventLoop — reads from eventCh and dispatches to subscribers
// ---------------------------------------------------------------------------

func (c *Client) eventLoop() {
	defer close(c.done)
	for {
		select {
		case wsMsg, ok := <-c.eventCh:
			if !ok {
				return
			}
			c.dispatchWSMessage(wsMsg)
		case <-c.ctx.Done():
			return
		}
	}
}

// dispatchWSMessage converts a WSMessage to the appropriate event type
// and dispatches to matching subscribers via baseTransport.
func (c *Client) dispatchWSMessage(msg protocol.WSMessage) {
	switch msg.Type {
	case protocol.MsgTypeProgress:
		if msg.Progress != nil {
			c.base.emit(c.ctx, msg.Progress)
		}
	case protocol.MsgTypeText:
		c.base.emit(c.ctx, protocol.OutboundEvent{
			ChatID:  msg.ChatID,
			Channel: msg.Channel,
			Content: msg.Content,
		})
	case protocol.MsgTypeStreamContent:
		if msg.Progress != nil {
			c.base.emit(c.ctx, &protocol.ProgressEvent{
				ChatID:                 msg.Progress.ChatID,
				StreamContent:          msg.Progress.StreamContent,
				ReasoningStreamContent: msg.Progress.ReasoningStreamContent,
			})
		}
	case protocol.MsgTypeAskUser:
		var ev protocol.AskUserEvent
		if err := json.Unmarshal([]byte(msg.Content), &ev); err == nil {
			c.base.emit(c.ctx, ev)
		}
	case protocol.MsgTypeInjectUser:
		c.base.emit(c.ctx, protocol.InjectUserEvent{
			ChatID:  msg.ChatID,
			Content: msg.Content,
		})
	case protocol.MsgTypeSession:
		if msg.Session != nil {
			c.base.emit(c.ctx, *msg.Session)
		}
	case protocol.MsgTypePluginWidgets:
		var zones map[string]string
		if err := json.Unmarshal([]byte(msg.Content), &zones); err == nil {
			c.base.emit(c.ctx, protocol.PluginWidgetEvent{
				ChatID: msg.ChatID,
				Zones:  zones,
			})
		}
	}
}

// ---------------------------------------------------------------------------
// Generic RPC helpers — mirrors Backend.call / callVoid
// ---------------------------------------------------------------------------

// call marshals req, calls transport, unmarshals into result.
func (c *Client) call(method string, req any, result any) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("%s: marshal: %w", method, err)
	}
	raw, err := c.transport.Call(method, payload)
	if err != nil {
		return err
	}
	if result != nil && len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, result); err != nil {
			return fmt.Errorf("%s: unmarshal: %w", method, err)
		}
	}
	return nil
}

// callVoid is fire-and-forget: errors are logged, not returned.
func (c *Client) callVoid(method string, req any) {
	if err := c.call(method, req, nil); err != nil {
		log.WithError(err).WithField("method", method).Warn("Client: call failed")
	}
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Start initializes the client. For remote mode, starts the transport.
// For local mode, the eventLoop is already started in NewClient.
func (c *Client) Start(ctx context.Context) error {
	// For remote mode, if transport implements AgentRunner, start it.
	if runner, ok := c.transport.(AgentRunner); ok {
		return runner.Start(ctx)
	}
	return nil
}

// Stop cancels the client context, stopping the eventLoop.
func (c *Client) Stop() {
	c.cancel()
	if runner, ok := c.transport.(AgentRunner); ok {
		runner.Stop()
	}
}

// Close releases transport resources.
func (c *Client) Close() error {
	return c.transport.Close()
}

// Run blocks until the context is done.
// For remote mode, delegates to the transport's Run method.
// For local mode, waits on the done channel or context cancellation.
func (c *Client) Run(ctx context.Context) error {
	if runner, ok := c.transport.(AgentRunner); ok {
		return runner.Run(ctx)
	}
	// Local mode: block until context done or client stopped.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return nil
	case <-c.ctx.Done():
		return c.ctx.Err()
	}
}

// ---------------------------------------------------------------------------
// Transport identity
// ---------------------------------------------------------------------------

func (c *Client) IsRemote() bool {
	if rt, ok := c.transport.(interface{ IsRemote() bool }); ok {
		return rt.IsRemote()
	}
	return false
}

func (c *Client) ConnState() string {
	if rt, ok := c.transport.(interface{ ConnState() string }); ok {
		return rt.ConnState()
	}
	return "connected" // local mode is always connected
}

func (c *Client) ServerURL() string {
	if rt, ok := c.transport.(interface{ ServerURL() string }); ok {
		return rt.ServerURL()
	}
	return "" // local mode has no server URL
}

// ---------------------------------------------------------------------------
// Communication — EventRouter
// ---------------------------------------------------------------------------

// SendInbound sends a user message to the agent.
// Local mode: writes to the message bus.
// Remote mode: sends via WebSocket (RemoteTransport.SendMessage).
func (c *Client) SendInbound(msg bus.InboundMessage) error {
	if c.msgBus != nil {
		// Local mode — write directly to the bus.
		c.msgBus.Inbound <- msg
		return nil
	}
	// Remote mode — use RemoteTransport.SendMessage if available.
	type messageSender interface {
		SendMessage(msg protocol.InboundMessage) error
	}
	if sender, ok := c.transport.(messageSender); ok {
		return sender.SendMessage(protocol.InboundMessage{
			MessagePayload: bus.MessagePayload{
				Content:    msg.Content,
				Channel:    msg.Channel,
				ChatID:     msg.ChatID,
				SenderID:   msg.SenderID,
				SenderName: msg.SenderName,
				ChatType:   msg.ChatType,
			},
		})
	}
	return fmt.Errorf("Client: no message bus or remote sender configured")
}

// Subscribe registers an event handler matching the given pattern.
func (c *Client) Subscribe(pattern protocol.EventPattern, handler protocol.EventHandler) (cancel func()) {
	return c.base.Subscribe(pattern, handler)
}

// BindChat registers this connection to receive events for the given chat.
// For remote mode, delegates to RemoteTransport.BindChat.
func (c *Client) BindChat(chatID string) error {
	type chatBinder interface {
		BindChat(chatID string) error
	}
	if binder, ok := c.transport.(chatBinder); ok {
		return binder.BindChat(chatID)
	}
	return nil // local mode: no-op
}

// ---------------------------------------------------------------------------
// Callback injection — forwarded to transport if it supports them
// ---------------------------------------------------------------------------

func (c *Client) SetTUIControlHandler(cb func(action string, params map[string]string) (map[string]string, error)) {
	type tuiSetter interface {
		SetTUIControlHandler(func(action string, params map[string]string) (map[string]string, error))
	}
	if setter, ok := c.transport.(tuiSetter); ok {
		setter.SetTUIControlHandler(cb)
	}
	// Local mode: no-op (handled by ServerCore)
}

func (c *Client) WireCallbacks(
	directSend func(msg bus.OutboundMessage) (string, error),
	channelFinder func(name string) (channel.Channel, bool),
	sessionStateHandler func(ev protocol.SessionEvent),
	messageSender bus.MessageSender,
	registerAgentChannel func(name string, runFn bus.RunFn) error,
	unregisterAgentChannel func(name string),
) {
	type wireSetter interface {
		WireCallbacks(
			func(msg bus.OutboundMessage) (string, error),
			func(name string) (channel.Channel, bool),
			func(ev protocol.SessionEvent),
			bus.MessageSender,
			func(name string, runFn bus.RunFn) error,
			func(name string),
		)
	}
	if setter, ok := c.transport.(wireSetter); ok {
		setter.WireCallbacks(directSend, channelFinder, sessionStateHandler, messageSender, registerAgentChannel, unregisterAgentChannel)
	}
	// Local mode: no-op (handled by ServerCore)
}

func (c *Client) SetChatRenameFn(fn func(chatID, newName string) (oldName string, err error)) {
	type renameSetter interface {
		SetChatRenameFn(func(chatID, newName string) (oldName string, err error))
	}
	if setter, ok := c.transport.(renameSetter); ok {
		setter.SetChatRenameFn(fn)
	}
	// Local mode: no-op (handled by ServerCore)
}

// ---------------------------------------------------------------------------
// Raw RPC
// ---------------------------------------------------------------------------

// CallRPC sends a raw RPC request and returns the raw JSON response.
func (c *Client) CallRPC(method string, params any) (json.RawMessage, error) {
	payload, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return c.transport.Call(method, payload)
}

//go:generate go run ../cmd/genclient/main.go -output client_rpc_generated.go
