package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"xbot/bus"
	"xbot/channel"
	"xbot/clipanic"

	"github.com/gorilla/websocket"
	log "xbot/logger"
)

// ---------------------------------------------------------------------------
// RPC protocol types (shared between Transport client and server handler)
// ---------------------------------------------------------------------------

// rpcResponse is sent by the server back to the client.
type rpcResponse struct {
	Type   string          `json:"type"`             // "rpc_response"
	ID     string          `json:"id"`               // matches request ID
	Result json.RawMessage `json:"result,omitempty"` // JSON result (nil for void methods)
	Error  string          `json:"error,omitempty"`  // error message (empty = success)
}

// ---------------------------------------------------------------------------
// RemoteTransport — WebSocket-based transport for remote CLI
// ---------------------------------------------------------------------------

// RemoteTransport connects to a remote xbot server via WebSocket.
// It implements the Transport interface: sending messages, RPC calls,
// and receiving server-pushed events (progress, stream, outbound).
type RemoteTransport struct {
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

	// Outbound message callback (for final agent replies)
	outboundMu sync.RWMutex
	outboundCb func(bus.OutboundMessage)

	// Progress callback (for streaming + structured progress)
	progressMu sync.RWMutex
	progressCb func(*channel.CLIProgressPayload)

	// Reconnect
	reconnectCh   chan struct{}
	onReconnectCb func() // called after successful reconnect (for history reload)

	// Connection state — tracks WS liveness for CLI header bar indicator
	connState     string // "connected" | "disconnected" | "reconnecting"
	onConnStateCb func(state string)

	// Injected user message callback (for bg task notifications from server)
	injectUserCb func(content string)

	// Plugin widget push callback (for real-time widget zone updates from server)
	pluginWidgetsCb func(zones map[string]string, chatID string)

	// RPC pending calls: requestID → response channel
	rpcMu      sync.Mutex
	pending    map[string]chan *rpcResponse
	rpcCounter atomic.Int64
}

// RemoteTransportConfig holds the configuration for connecting to a remote server.
type RemoteTransportConfig struct {
	ServerURL string // e.g. "ws://localhost:8080" or "wss://example.com"
	Token     string // runner token for authentication
}

// NewRemoteTransport creates a RemoteTransport that connects to the given server URL.
func NewRemoteTransport(cfg RemoteTransportConfig) *RemoteTransport {
	return &RemoteTransport{
		serverURL:   cfg.ServerURL,
		token:       cfg.Token,
		done:        make(chan struct{}),
		reconnectCh: make(chan struct{}, 1),
		pending:     make(map[string]chan *rpcResponse),
	}
}

// ---------------------------------------------------------------------------
// WS incoming message types (server → client)
// ---------------------------------------------------------------------------

// wsIncomingMessage represents a message received from the server.
// Supports all message types: text, progress_structured, stream_content, rpc_response, ask_user.
type wsIncomingMessage struct {
	Type            string                     `json:"type"`
	ID              string                     `json:"id,omitempty"`
	Content         string                     `json:"content,omitempty"`
	OriginalContent string                     `json:"original_content,omitempty"`
	TS              int64                      `json:"ts,omitempty"`
	Seq             uint64                     `json:"seq,omitempty"`
	Progress        *channel.WsProgressPayload `json:"progress,omitempty"`
	ProgressHistory string                     `json:"progress_history,omitempty"`
	Result          json.RawMessage            `json:"result,omitempty"`
	Error           string                     `json:"error,omitempty"`
	Channel         string                     `json:"channel,omitempty"`
	ChatID          string                     `json:"chat_id,omitempty"`
	SessionReset    bool                       `json:"session_reset,omitempty"`
}

// wsOutgoingMessage represents a message sent to the server.
type wsOutgoingMessage struct {
	Type       string          `json:"type"`
	Content    string          `json:"content,omitempty"`
	ID         string          `json:"id,omitempty"`
	Method     string          `json:"method,omitempty"`
	Params     json.RawMessage `json:"params,omitempty"`
	Channel    string          `json:"channel,omitempty"`
	ChatID     string          `json:"chat_id,omitempty"`
	SenderID   string          `json:"sender_id,omitempty"`
	SenderName string          `json:"sender_name,omitempty"`
	ChatType   string          `json:"chat_type,omitempty"`
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Start connects to the remote server via WebSocket and starts the read pump.
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

// Stop closes the WebSocket connection.
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

// ---------------------------------------------------------------------------
// Message I/O
// ---------------------------------------------------------------------------

// SendMessage sends a user message to the remote server via WebSocket.
func (t *RemoteTransport) SendMessage(msg Message) error {
	t.connMu.Lock()
	defer t.connMu.Unlock()
	if t.conn == nil {
		return fmt.Errorf("not connected to server")
	}
	// Set write deadline to avoid blocking indefinitely on dead connections.
	t.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	defer t.conn.SetWriteDeadline(time.Time{}) // reset

	msgType := "message"
	if msg.Cancel {
		msgType = "cancel"
	}

	outMsg := wsOutgoingMessage{
		Type:       msgType,
		Content:    msg.Content,
		Channel:    msg.Channel,
		ChatID:     msg.ChatID,
		SenderID:   msg.SenderID,
		SenderName: msg.SenderName,
		ChatType:   msg.ChatType,
	}
	return t.conn.WriteJSON(outMsg)
}

// OnOutbound registers a callback for agent reply messages received from the server.
func (t *RemoteTransport) OnOutbound(callback func(bus.OutboundMessage)) {
	t.outboundMu.Lock()
	defer t.outboundMu.Unlock()
	t.outboundCb = callback
}

// Bus returns nil for RemoteTransport (no local message bus).
func (t *RemoteTransport) Bus() *bus.MessageBus { return nil }

// IsRemote returns true — the agent loop runs on the server.
func (t *RemoteTransport) IsRemote() bool { return true }

// ServerURL returns the configured server URL for display purposes.
func (t *RemoteTransport) ServerURL() string { return t.serverURL }

// OnProgress registers a callback for streaming progress events.
func (t *RemoteTransport) OnProgress(callback func(*channel.CLIProgressPayload)) {
	t.progressMu.Lock()
	defer t.progressMu.Unlock()
	t.progressCb = callback
}

// OnReconnect registers a callback invoked after a successful WS reconnection.
// Used to reload history and re-sync state that may have changed during disconnect.
func (t *RemoteTransport) OnReconnect(callback func()) {
	t.onReconnectCb = callback
}

// OnConnStateChange registers a callback invoked when the WS connection state changes.
// States: "connected", "disconnected", "reconnecting".
// Used by CLI to update the header bar connection indicator in real-time.
func (t *RemoteTransport) OnConnStateChange(callback func(state string)) {
	t.onConnStateCb = callback
}

// OnInjectUserMessage registers a callback invoked when the server injects a user
// message into the CLI (e.g. background task completion notification in remote mode).
// The CLI displays it as a user message and starts the agent turn display.
func (t *RemoteTransport) OnInjectUserMessage(callback func(content string)) {
	t.injectUserCb = callback
}

// OnPluginWidgets registers a callback invoked when the server pushes widget zone
// content updates. This is the real-time push path — no polling needed.
func (t *RemoteTransport) OnPluginWidgets(callback func(zones map[string]string, chatID string)) {
	t.pluginWidgetsCb = callback
}

// ConnState returns the current connection state string.
func (t *RemoteTransport) ConnState() string {
	t.connMu.Lock()
	defer t.connMu.Unlock()
	return t.connState
}

// setConnState updates connState and fires the callback if state changed.
// Must be called with connMu held OR from a single-threaded context.
func (t *RemoteTransport) setConnState(state string) {
	t.connMu.Lock()
	prev := t.connState
	t.connState = state
	cb := t.onConnStateCb
	t.connMu.Unlock()
	if prev != state && cb != nil {
		cb(state)
	}
}

// ---------------------------------------------------------------------------
// WebSocket connection
// ---------------------------------------------------------------------------

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

	// Atomically replace connection to avoid race with Stop().
	t.connMu.Lock()
	old := t.conn
	t.conn = conn
	t.connMu.Unlock()
	if old != nil {
		old.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "reconnecting"))
		old.Close()
	}
	log.Info("Connected to remote xbot server")
	t.setConnState("connected")

	// Send sync message so server replays missed events from eventStream buffer.
	// This enables mid-turn reconnect: a new CLI terminal sees recent progress/stream
	// events without waiting for the 2s timeout fallback.
	syncMsg := struct {
		Type    string `json:"type"`
		LastSeq uint64 `json:"last_seq"`
	}{
		Type:    "sync",
		LastSeq: t.lastSeq.Load(),
	}
	if err := conn.WriteJSON(syncMsg); err != nil {
		log.WithError(err).Warn("Failed to send sync message")
	}

	return nil
}

// Subscribe registers this connection to receive events for chatID.
// Must be called after connect() with the business chatID (e.g. "/home/user").
func (t *RemoteTransport) Subscribe(chatID string) error {
	t.connMu.Lock()
	conn := t.conn
	t.connMu.Unlock()
	if conn == nil {
		return fmt.Errorf("not connected to server")
	}
	subMsg := wsOutgoingMessage{Type: "subscribe", ChatID: chatID}
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	defer conn.SetWriteDeadline(time.Time{})
	if err := conn.WriteJSON(subMsg); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Read pump — dispatches server messages
// ---------------------------------------------------------------------------

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
			// Use non-blocking write instead of close(ch) to avoid double-close
			// panic if Stop() runs concurrently (both hold rpcMu but close on
			// buffered-chan can still race if channel is already drained).
			t.rpcMu.Lock()
			for id, ch := range t.pending {
				select {
				case ch <- &rpcResponse{Error: "connection lost"}:
				default:
				}
				delete(t.pending, id)
			}
			t.rpcMu.Unlock()
			select {
			case t.reconnectCh <- struct{}{}:
			default:
			}
			t.setConnState("disconnected")
			return
		}
		var msg wsIncomingMessage
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
		switch msg.Type {
		case "rpc_response":
			t.handleRPCResponse(&msg)
		case "text":
			outMsg := bus.OutboundMessage{
				Content:  msg.Content,
				Channel:  msg.Channel,
				ChatID:   msg.ChatID,
				Metadata: make(map[string]string),
			}
			if outMsg.Channel == "" {
				outMsg.Channel = "remote"
			}
			if msg.ID != "" {
				outMsg.Metadata["message_id"] = msg.ID
			}
			if msg.ProgressHistory != "" {
				outMsg.Metadata["progress_history"] = msg.ProgressHistory
			}
			if msg.SessionReset {
				outMsg.Metadata["session_reset"] = "true"
			}
			t.outboundMu.RLock()
			cb := t.outboundCb
			t.outboundMu.RUnlock()
			if cb != nil {
				log.WithField("msg_type", msg.Type).WithField("content_len", len(msg.Content)).Info("RemoteTransport: dispatching outbound message")
				func() {
					defer func() {
						if r := recover(); r != nil {
							clipanic.Report("agent.RemoteTransport.OnOutbound", outMsg, r)
							log.WithField("panic", r).Warn("RemoteTransport outbound callback panicked")
						}
					}()
					cb(outMsg)
				}()
				log.Debug("RemoteTransport: outbound callback returned")
			} else {
				log.Warn("Received server reply but no outbound callback registered")
			}
		case "progress_structured":
			t.dispatchProgress(convertWsProgressToCLI(msg.Progress))
		case "stream_content":
			t.dispatchProgress(&channel.CLIProgressPayload{
				ChatID:                 msg.Progress.ChatID,
				StreamContent:          msg.Progress.GetStreamContent(),
				ReasoningStreamContent: msg.Progress.GetReasoningStreamContent(),
			})
		case "ask_user":
			if msg.Progress != nil {
				if len(msg.Progress.Questions) > 0 {
					qJSON, _ := json.Marshal(msg.Progress.Questions)
					outMsg := bus.OutboundMessage{
						Channel:     "cli",
						WaitingUser: true,
						Metadata: map[string]string{
							"ask_questions": string(qJSON),
						},
					}
					if msg.Progress.RequestID != "" {
						outMsg.Metadata["request_id"] = msg.Progress.RequestID
					}
					t.outboundMu.RLock()
					cb := t.outboundCb
					t.outboundMu.RUnlock()
					if cb != nil {
						func() {
							defer func() {
								if r := recover(); r != nil {
									clipanic.Report("agent.RemoteTransport.OnAskUser", outMsg, r)
									log.WithField("panic", r).Warn("RemoteTransport ask_user callback panicked")
								}
							}()
							cb(outMsg)
						}()
					} else {
						log.Warn("Received ask_user but no outbound callback registered")
					}
				}
			}
		case "inject_user":
			if t.injectUserCb != nil && msg.Content != "" {
				log.WithField("content_len", len(msg.Content)).Info("RemoteTransport: dispatching inject_user")
				func() {
					defer func() {
						if r := recover(); r != nil {
							clipanic.Report("agent.RemoteTransport.OnInjectUserMessage", msg.Content, r)
							log.WithField("panic", r).Warn("RemoteTransport inject_user callback panicked")
						}
					}()
					t.injectUserCb(msg.Content)
				}()
			}
		case "plugin_widgets":
			// Server push: widget zone content updated.
			// Parse and cache directly — no RPC round-trip needed.
			// chatID in the message identifies which session this push targets.
			if t.pluginWidgetsCb != nil {
				var zones map[string]string
				if err := json.Unmarshal([]byte(msg.Content), &zones); err == nil {
					t.pluginWidgetsCb(zones, msg.ChatID)
				}
			}
		}
	}
}

func (t *RemoteTransport) handleRPCResponse(msg *wsIncomingMessage) {
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

func (t *RemoteTransport) dispatchProgress(payload *channel.CLIProgressPayload) {
	if payload == nil {
		return
	}
	t.progressMu.RLock()
	cb := t.progressCb
	t.progressMu.RUnlock()
	if cb != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					clipanic.Report("agent.RemoteTransport.OnProgress", payload, r)
					log.WithField("panic", r).Warn("RemoteTransport progress callback panicked")
				}
			}()
			cb(payload)
		}()
	}
}

func convertWsProgressToCLI(wp *channel.WsProgressPayload) *channel.CLIProgressPayload {
	if wp == nil {
		return nil
	}
	payload := &channel.CLIProgressPayload{
		ChatID:                 wp.ChatID,
		Phase:                  wp.Phase,
		Iteration:              wp.Iteration,
		Thinking:               wp.Thinking,
		Reasoning:              wp.Reasoning,
		StreamContent:          wp.StreamContent,
		ReasoningStreamContent: wp.ReasoningStreamContent,
		HistoryCompacted:       wp.HistoryCompacted,
	}
	for _, t := range wp.ActiveTools {
		payload.ActiveTools = append(payload.ActiveTools, channel.CLIToolProgress{
			Name: t.Name, Label: t.Label, Status: t.Status,
			Elapsed: t.Elapsed, Summary: t.Summary, Detail: t.Detail, Args: t.Args, ToolHints: t.ToolHints,
			Iteration: t.Iteration,
		})
	}
	for _, t := range wp.CompletedTools {
		payload.CompletedTools = append(payload.CompletedTools, channel.CLIToolProgress{
			Name: t.Name, Label: t.Label, Status: t.Status,
			Elapsed: t.Elapsed, Summary: t.Summary, Detail: t.Detail, Args: t.Args, ToolHints: t.ToolHints,
			Iteration: t.Iteration,
		})
	}
	for _, sa := range wp.SubAgents {
		payload.SubAgents = append(payload.SubAgents, convertWsSubAgent(sa))
	}
	for _, td := range wp.Todos {
		payload.Todos = append(payload.Todos, channel.CLITodoItem(td))
	}
	if wp.TokenUsage != nil {
		payload.TokenUsage = &channel.CLITokenUsage{
			PromptTokens:     wp.TokenUsage.PromptTokens,
			CompletionTokens: wp.TokenUsage.CompletionTokens,
			TotalTokens:      wp.TokenUsage.TotalTokens,
			CacheHitTokens:   wp.TokenUsage.CacheHitTokens,
			MaxOutputTokens:  wp.TokenUsage.MaxOutputTokens,
		}
	}
	return payload
}

func convertWsSubAgent(sa channel.WsSubAgent) channel.CLISubAgent {
	r := channel.CLISubAgent{Role: sa.Role, Instance: sa.Instance, Status: sa.Status, Desc: sa.Desc}
	for _, c := range sa.Children {
		r.Children = append(r.Children, convertWsSubAgent(c))
	}
	return r
}

// ---------------------------------------------------------------------------
// Ping loop — sends WebSocket pings to keep connection alive
// ---------------------------------------------------------------------------

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
func (t *RemoteTransport) sendPing() {
	t.connMu.Lock()
	defer t.connMu.Unlock()
	if t.conn == nil {
		return
	}
	if err := t.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
		log.WithError(err).Warn("WS ping failed")
	}
}

// ---------------------------------------------------------------------------
// Reconnect
// ---------------------------------------------------------------------------

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
			for delay := time.Second; delay <= 30*time.Second; delay *= 2 {
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
					// Notify user after 3 consecutive failures via outbound callback.
					if consecutiveFailures == 3 {
						t.outboundMu.RLock()
						cb := t.outboundCb
						t.outboundMu.RUnlock()
						if cb != nil {
							cb(bus.OutboundMessage{
								Channel: "remote",
								Content: fmt.Sprintf("Connection lost, reconnecting (attempt %d)...", consecutiveFailures),
							})
						}
					}
					continue
				}
				log.Info("Reconnected to server")
				consecutiveFailures = 0
				// Notify CLI to reload history and re-sync state.
				// Run in goroutine — callback may make slow RPC calls that
				// should not block the reconnectLoop.
				if t.onReconnectCb != nil {
					go t.onReconnectCb()
				}
				t.readPumpWg.Add(1)
				go t.readPump(ctx)
				break
			}
		}
	}
}

// ---------------------------------------------------------------------------
// RPC call (Transport interface)
// ---------------------------------------------------------------------------

// Call sends an RPC request and waits for a response (implements Transport interface).
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
	req := wsOutgoingMessage{Type: "rpc", ID: id, Method: method, Params: payload}
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

// Close stops the WebSocket connection (implements Transport interface).
func (t *RemoteTransport) Close() error {
	t.Stop()
	return nil
}
