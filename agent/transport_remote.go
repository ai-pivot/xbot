package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xbot/bus"
	"xbot/channel"
	"xbot/protocol"

	"github.com/gorilla/websocket"
	log "xbot/logger"
)

// ==========================================================================
// RPC protocol types (shared between Transport client and server handler)
// ==========================================================================

// rpcResponse is sent by the server back to the client.
type rpcResponse struct {
	Type   string          `json:"type"`             // "rpc_response"
	ID     string          `json:"id"`               // matches request ID
	Result json.RawMessage `json:"result,omitempty"` // JSON result (nil for void methods)
	Error  string          `json:"error,omitempty"`  // error message (empty = success)
}

// ==========================================================================
// RemoteTransport type definition & constructor
// ==========================================================================

// RemoteTransport connects to a remote xbot server via WebSocket.
// It implements the Transport interface by composing:
//   - Transport interface: Call, Close (WebSocket RPC transport)
//   - AgentRunner interface: Start, Stop, Run (lifecycle management)
//   - EventRouter interface: SendMessage, BindChat, Subscribe, ConnState (event routing)
//   - CallbackRegistry interface: WireCallbacks, SetTUIControlHandler
//
// Internal WebSocket plumbing (connect, readPump, pingLoop, reconnectLoop)
// is grouped at the bottom of this file.
type RemoteTransport struct {
	baseTransport

	// eventBase, when set, overrides baseTransport for local event dispatch.
	// This bridges events from the transport to Client's subscriber base.
	eventBase *baseTransport

	serverURL string
	token     string

	// WS connection
	conn      *websocket.Conn
	connMu    sync.Mutex
	done      chan struct{}
	closeOnce sync.Once

	// readPump lifecycle — WaitGroup ensures old readPump exits
	// before reconnect spawns a new one, preventing goroutine leaks.
	readPumpWg sync.WaitGroup

	// Event seq tracking — tracks the highest seq from server events
	// so that on reconnect we send last_seq and only replay missed events.
	lastSeq atomic.Uint64

	// Reconnect
	reconnectCh chan struct{}

	// Connection state — tracks WS liveness for CLI header bar indicator
	connState string // "connected" | "disconnected" | "reconnecting"

	// TUI control request callback (for server-initiated TUI operations in remote mode).
	// This is an RPC-style request-response, not a fire-and-forget event.
	tuiControlReqCb func(action string, params map[string]string) (map[string]string, error)

	// RPC pending calls: requestID → response channel
	rpcMu      sync.Mutex
	pending    map[string]chan *rpcResponse
	rpcCounter atomic.Int64

	// eventCh forwards raw WS messages to Client.eventLoop for unified dispatch.
	eventCh chan protocol.WSMessage
}

// RemoteTransportConfig holds the configuration for connecting to a remote server.
type RemoteTransportConfig struct {
	ServerURL string // e.g. "ws://localhost:8080" or "wss://example.com"
	Token     string // runner token for authentication
}

// NewRemoteTransport creates a RemoteTransport that connects to the given server URL.
func NewRemoteTransport(cfg RemoteTransportConfig) *RemoteTransport {
	return &RemoteTransport{
		baseTransport: newBaseTransport(),
		serverURL:     cfg.ServerURL,
		token:         cfg.Token,
		done:          make(chan struct{}),
		reconnectCh:   make(chan struct{}, 1),
		pending:       make(map[string]chan *rpcResponse),
		eventCh:       make(chan protocol.WSMessage, 256),
	}
}

// ==========================================================================
// Transport interface (Call + Close)
// Satisfies the RPC transport contract used by Backend for all server-side
// method invocations (ListModels, GetSettings, etc.).
// ==========================================================================

// Call sends an RPC request and waits for a response.
// method is the RPC method name. payload is already marshaled JSON (json.RawMessage).
func (t *RemoteTransport) Call(method string, payload json.RawMessage) (json.RawMessage, error) {
	// Lock order: connMu → rpcMu (never reverse, to prevent deadlock).
	t.connMu.Lock()
	if t.conn == nil {
		t.connMu.Unlock()
		return nil, fmt.Errorf("not connected to server")
	}
	id := fmt.Sprintf("rpc-%d", t.rpcCounter.Add(1))
	ch := make(chan *rpcResponse, 1)
	t.rpcMu.Lock()
	t.pending[id] = ch
	t.rpcMu.Unlock()
	req := protocol.WSClientMessage{Type: protocol.MsgTypeRPC, ID: id, Method: method, Params: payload}
	// Set write deadline to avoid blocking indefinitely on dead connections.
	t.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := t.conn.WriteJSON(req); err != nil {
		t.conn.SetWriteDeadline(time.Time{})
		t.connMu.Unlock()
		t.rpcMu.Lock()
		delete(t.pending, id)
		t.rpcMu.Unlock()
		return nil, fmt.Errorf("send RPC %s: %w", method, err)
	}
	t.conn.SetWriteDeadline(time.Time{})
	t.connMu.Unlock()
	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("RPC %s: connection closed", method)
		}
		if resp.Error != "" {
			return nil, fmt.Errorf("RPC %s: %s", method, resp.Error)
		}
		return resp.Result, nil
	case <-time.After(30 * time.Second):
		t.rpcMu.Lock()
		delete(t.pending, id)
		t.rpcMu.Unlock()
		return nil, fmt.Errorf("RPC %s: timeout", method)
	case <-t.done:
		t.rpcMu.Lock()
		delete(t.pending, id)
		t.rpcMu.Unlock()
		return nil, fmt.Errorf("RPC %s: backend stopped", method)
	}
}

// EventCh returns the channel for raw WS messages, used by Client.eventLoop.
func (t *RemoteTransport) EventCh() chan protocol.WSMessage {
	return t.eventCh
}

// Close stops the WebSocket connection.
func (t *RemoteTransport) Close() error {
	t.Stop()
	return nil
}

// handleRPCResponse dispatches an incoming RPC response to the waiting caller.
func (t *RemoteTransport) handleRPCResponse(msg *protocol.WSMessage) {
	if msg.ID == "" {
		return
	}
	t.rpcMu.Lock()
	ch, ok := t.pending[msg.ID]
	if ok {
		delete(t.pending, msg.ID)
	}
	t.rpcMu.Unlock()
	if ok {
		ch <- &rpcResponse{
			ID:     msg.ID,
			Result: msg.Result,
			Error:  msg.Error,
		}
	}
}

// ==========================================================================
// AgentRunner interface (Start + Stop + Run)
// Manages the transport lifecycle: establish connection, run goroutines,
// and block until context cancellation.
// ==========================================================================

// Start connects to the remote server via WebSocket and starts background goroutines.
func (t *RemoteTransport) Start(ctx context.Context) error {
	if err := t.connect(ctx); err != nil {
		return fmt.Errorf("connect to %s: %w", t.serverURL, err)
	}
	t.readPumpWg.Add(1)
	go t.readPump(ctx)
	go t.reconnectLoop(ctx)
	go t.pingLoop(ctx)
	return nil
}

// Stop closes the WebSocket connection and unblocks all pending RPC calls.
func (t *RemoteTransport) Stop() {
	t.closeOnce.Do(func() {
		close(t.done)
		t.connMu.Lock()
		if t.conn != nil {
			t.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			t.conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "client shutdown"))
			t.conn.Close()
			t.conn = nil
		}
		t.connMu.Unlock()
		// Unblock all pending RPC calls (non-blocking write, consistent with readPump)
		t.rpcMu.Lock()
		for id, ch := range t.pending {
			select {
			case ch <- &rpcResponse{Error: "connection closed"}:
			default:
			}
			delete(t.pending, id)
		}
		t.rpcMu.Unlock()
	})
}

// Run blocks until the context is cancelled.
func (t *RemoteTransport) Run(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// ==========================================================================
// EventRouter interface (SendMessage + BindChat + Subscribe + ConnState)
// Handles outbound event routing and inbound message dispatch.
// Subscribe is inherited from baseTransport.
// ==========================================================================

// SendMessage sends a user message to the remote server via WebSocket.
func (t *RemoteTransport) SendMessage(msg protocol.InboundMessage) error {
	var writeErr error
	t.connMu.Lock()
	if t.conn == nil {
		t.connMu.Unlock()
		return fmt.Errorf("not connected to server")
	}
	t.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	defer t.conn.SetWriteDeadline(time.Time{})
	msgType := "message"
	if strings.TrimSpace(strings.ToLower(msg.Content)) == "/cancel" {
		msgType = "cancel"
	}
	outMsg := protocol.WSClientMessage{
		Type:       msgType,
		Content:    msg.Content,
		Channel:    msg.Channel,
		ChatID:     msg.ChatID,
		SenderID:   msg.SenderID,
		SenderName: msg.SenderName,
		ChatType:   msg.ChatType,
	}
	writeErr = t.conn.WriteJSON(outMsg)
	if writeErr != nil {
		// Write failure means the connection is dead.
		t.conn = nil
	}
	t.connMu.Unlock()

	if writeErr != nil {
		select {
		case t.reconnectCh <- struct{}{}:
		default:
		}
		t.setConnState("disconnected")
	}
	return writeErr
}

// BindChat registers this connection to receive events for chatID.
// Must be called after connect() with the business chatID (e.g. "/home/user").
func (t *RemoteTransport) BindChat(chatID string) error {
	t.connMu.Lock()
	defer t.connMu.Unlock()
	if t.conn == nil {
		return fmt.Errorf("not connected to server")
	}
	subMsg := protocol.WSClientMessage{Type: protocol.MsgTypeSubscribe, ChatID: chatID}
	t.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	defer t.conn.SetWriteDeadline(time.Time{})
	if err := t.conn.WriteJSON(subMsg); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	return nil
}

// ConnState returns the current connection state string.
func (t *RemoteTransport) ConnState() string {
	t.connMu.Lock()
	defer t.connMu.Unlock()
	return t.connState
}

// ShareEventBase sets the baseTransport used for local event dispatch.
// When set, emitLocal uses this instead of the embedded baseTransport,
// bridging events from the transport to Client's subscriber base.
func (t *RemoteTransport) ShareEventBase(base *baseTransport) {
	t.eventBase = base
}

// emitLocal dispatches a transport event to the correct subscriber base.
// Uses the shared eventBase when available, otherwise falls back to the
// embedded baseTransport.
func (t *RemoteTransport) emitLocal(ctx context.Context, event protocol.TransportEvent) {
	if t.eventBase != nil {
		t.eventBase.emit(ctx, event)
	} else {
		t.emit(ctx, event)
	}
}

// setConnState updates connState and emits a protocol event if state changed.
func (t *RemoteTransport) setConnState(state string) {
	t.connMu.Lock()
	prev := t.connState
	t.connState = state
	t.connMu.Unlock()
	if prev != state {
		t.emitLocal(context.Background(), protocol.ConnStateEvent{State: state})
	}
}

// IsRemote returns true — the agent loop runs on the server.
func (t *RemoteTransport) IsRemote() bool { return true }

// ServerURL returns the configured server URL for display purposes.
func (t *RemoteTransport) ServerURL() string { return t.serverURL }

// ==========================================================================
// CallbackRegistry interface (SetTUIControlHandler + WireCallbacks)
// These methods satisfy Client callback requirements.
// WireCallbacks is no-op for remote transport —
// the server wires these directly on the Agent.
// ==========================================================================

// SetTUIControlHandler registers the TUI control request handler for remote mode.
// This is an RPC-style request-response mechanism (not fire-and-forget).
func (t *RemoteTransport) SetTUIControlHandler(cb func(action string, params map[string]string) (map[string]string, error)) {
	t.tuiControlReqCb = cb
}

// WireCallbacks is noop for remote transport — server.go wires these directly on Agent.
func (t *RemoteTransport) WireCallbacks(
	func(msg channel.OutboundMsg) (string, error),
	func(name string) (channel.Channel, bool),
	bus.MessageSender,
	func(name string, runFn bus.RunFn) error,
	func(name string),
) {
}

// ==========================================================================
// WebSocket connection management (internal)
// connect, readPump, pingLoop, reconnectLoop — the low-level WS plumbing.
// ==========================================================================

// connect establishes a WebSocket connection to the server.
func (t *RemoteTransport) connect(ctx context.Context) error {
	u, err := url.Parse(t.serverURL)
	if err != nil {
		return fmt.Errorf("invalid server URL: %w", err)
	}
	switch u.Scheme {
	case "", "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}
	wsPath := u.Path
	if wsPath == "" || wsPath == "/" {
		wsPath = "/ws"
	}
	u.Path = wsPath
	q := u.Query()
	q.Set("client_type", "cli")
	if t.token != "" {
		q.Set("token", t.token)
	}
	u.RawQuery = q.Encode()
	wsURL := u.String()
	log.WithField("url", wsURL).Info("Connecting to remote xbot server...")
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("WS dial: %w", err)
	}

	// Set up pong handler to detect server liveness.
	// Server sends pings every 30s; pong handler resets read deadline.
	conn.SetPongHandler(func(_ string) error {
		conn.SetReadDeadline(time.Now().Add(120 * time.Second))
		return nil
	})
	// Initial read deadline — if no data (including pongs) in 120s, connection is dead.
	conn.SetReadDeadline(time.Now().Add(120 * time.Second))

	// Atomically replace connection and send sync message to avoid
	// racing with concurrent writes from other goroutines.
	t.connMu.Lock()
	old := t.conn
	t.conn = conn

	// Send sync message so server replays missed events from eventStream buffer.
	// This enables mid-turn reconnect: a new CLI terminal sees recent progress/stream
	// events without waiting for the 2s timeout fallback.
	syncMsg := struct {
		Type    string `json:"type"`
		LastSeq uint64 `json:"last_seq"`
	}{
		Type:    protocol.MsgTypeSync,
		LastSeq: t.lastSeq.Load(),
	}
	if err := conn.WriteJSON(syncMsg); err != nil {
		log.WithError(err).Warn("Failed to send sync message")
	}
	t.connMu.Unlock()
	if old != nil {
		old.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "reconnecting"))
		old.Close()
	}
	log.Info("Connected to remote xbot server")
	t.setConnState("connected")

	return nil
}

// readPump reads messages from the WebSocket connection and dispatches them.
func (t *RemoteTransport) readPump(ctx context.Context) {
	defer t.readPumpWg.Done()
	for {
		select {
		case <-t.done:
			return
		case <-ctx.Done():
			return
		default:
		}
		t.connMu.Lock()
		conn := t.conn
		t.connMu.Unlock()
		if conn == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.WithError(err).Warn("WS connection lost (read error)")
			} else {
				log.WithError(err).Info("WS connection closed")
			}
			// Unblock all pending RPC callers so they don't hang until timeout.
			t.rpcMu.Lock()
			for id, ch := range t.pending {
				select {
				case ch <- &rpcResponse{Error: "connection lost"}:
				default:
				}
				delete(t.pending, id)
			}
			t.rpcMu.Unlock()
			// Clear conn so subsequent Call() returns immediately instead of
			// blocking for 30s on a dead connection (freezes BubbleTea event loop).
			// Only clear if this readPump still owns the connection — connect()
			// may have replaced t.conn with a new one while we were waiting.
			// Relies on identity comparison (t.conn == conn): connect() MUST
			// create a new *websocket.Conn object each time. Connection pool
			// reuse would break this guard and cause the new connection to be
			// cleared by a stale readPump.
			var emitDisconnected bool
			t.connMu.Lock()
			if t.conn == conn {
				t.conn = nil
				select {
				case t.reconnectCh <- struct{}{}:
				default:
				}
				emitDisconnected = true
			}
			t.connMu.Unlock()
			if emitDisconnected {
				t.setConnState("disconnected")
			}
			return
		}
		var msg protocol.WSMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.WithError(err).Debug("Invalid WS message from server")
			continue
		}
		// Track highest seq for reconnect sync.
		if msg.Seq > 0 {
			for {
				old := t.lastSeq.Load()
				if msg.Seq <= old || t.lastSeq.CompareAndSwap(old, msg.Seq) {
					break
				}
			}
		}

		// RPC responses are handled inline (match pending callers).
		// TUI control requests need WS write-back, handled inline.
		// All other events are forwarded to Client.eventLoop via eventCh.
		switch msg.Type {
		case protocol.MsgTypeRPCResponse:
			t.handleRPCResponse(&msg)
		case protocol.MsgTypeTUIControlReq:
			t.handleTUIControlRequest(ctx, &msg)
		default:
			select {
			case t.eventCh <- msg:
			case <-ctx.Done():
				return
			}
		}
	}
}

// handleTUIControlRequest processes a server-initiated TUI control request.
func (t *RemoteTransport) handleTUIControlRequest(ctx context.Context, msg *protocol.WSMessage) {
	// Process in goroutine so readPump stays responsive — required for RPC calls within handlers.
	if t.tuiControlReqCb != nil && msg.TUIControl != nil {
		reqID := msg.ID
		action := msg.TUIControl.Action
		params := msg.TUIControl.Params
		go func() {
			result, err := t.tuiControlReqCb(action, params)
			resp := protocol.WSClientMessage{
				Type: protocol.MsgTypeTUIControlResp,
				ID:   reqID,
				TUIControl: &protocol.TUIControlPayload{
					Action: action,
				},
			}
			if err != nil {
				resp.TUIControl.Error = err.Error()
			} else {
				resp.TUIControl.Result = result
			}
			t.connMu.Lock()
			if t.conn != nil {
				if writeErr := t.conn.WriteJSON(resp); writeErr != nil {
					log.WithError(writeErr).Debug("Failed to send tui_control_resp")
				}
			}
			t.connMu.Unlock()
		}()
	}
	// Forward TUI control event to Client.eventLoop
	if msg.TUIControl != nil {
		select {
		case t.eventCh <- protocol.WSMessage{Type: protocol.MsgTypeTUIControlReq, TUIControl: msg.TUIControl}:
		default:
		}
	}
}

// pingLoop sends WebSocket pings every 25 seconds.
// The server sends pings every 30s and expects pongs within 60s.
// Client pings prevent the server's read deadline from expiring.
func (t *RemoteTransport) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-t.done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.sendPing()
		}
	}
}

// sendPing sends a WebSocket ping frame to the server.
// On write failure, it triggers a reconnect — the connection is dead even
// if readPump hasn't detected it yet (e.g. server died without closing TCP).
func (t *RemoteTransport) sendPing() {
	var pingFailed bool
	t.connMu.Lock()
	if t.conn != nil {
		if err := t.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
			log.WithError(err).Warn("WS ping failed, connection dead")
			t.conn = nil
			pingFailed = true
		}
	}
	t.connMu.Unlock()

	if pingFailed {
		select {
		case t.reconnectCh <- struct{}{}:
		default:
		}
		t.setConnState("disconnected")
	}
}

// reconnectLoop handles exponential-backoff reconnection with event replay.
func (t *RemoteTransport) reconnectLoop(ctx context.Context) {
	for {
		select {
		case <-t.done:
			return
		case <-ctx.Done():
			return
		case <-t.reconnectCh:
			t.setConnState("reconnecting")
			consecutiveFailures := 0
			delay := time.Second
			for {
				select {
				case <-t.done:
					return
				case <-ctx.Done():
					return
				default:
				}
				log.WithField("delay", delay).Info("Reconnecting to server...")
				timer := time.NewTimer(delay)
				select {
				case <-t.done:
					timer.Stop()
					return
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				if err := t.connect(ctx); err != nil {
					consecutiveFailures++
					log.WithError(err).Warn("Reconnect failed")
					// Notify user after every 3 failures via emit.
					if consecutiveFailures%3 == 0 {
						t.emitLocal(ctx, protocol.OutboundEvent{
							Channel: "cli",
							ChatID:  "remote",
							Content: fmt.Sprintf("Connection lost, reconnecting (attempt %d)...", consecutiveFailures),
						})
					}
					// Retry every second.
					delay = time.Second
					continue
				}
				log.Info("Reconnected to server")
				consecutiveFailures = 0
				// Start readPump BEFORE emitting ReconnectEvent, so the new
				// reader is ready to receive RPC responses (BindChat etc.).
				t.readPumpWg.Add(1)
				go t.readPump(ctx)
				t.emitLocal(ctx, protocol.ReconnectEvent{})
				break
			}
		}
	}
}
