package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	log "xbot/logger"
	"xbot/protocol"
	"xbot/tools"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// WebSocket handler
// ---------------------------------------------------------------------------

// wsUpgrader returns a WebSocket upgrader with origin checking.
func (wc *WebChannel) wsUpgrader() *websocket.Upgrader {
	return &websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true // non-browser clients
			}
			u, err := url.Parse(origin)
			if err != nil {
				return false
			}
			// Allow same-origin or configured public URL
			if wc.config.PublicURL != "" {
				if pu, err := url.Parse(wc.config.PublicURL); err == nil && u.Host == pu.Host {
					return true
				}
			}
			// Always allow requests from the backend's own host (e.g. Vite proxy
			// sets Origin to the backend host, or direct browser access).
			if u.Host == r.Host {
				return true
			}
			// Allow localhost origins in development (Vite dev server on
			// a different port proxies to the backend).
			if u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" {
				return true
			}
			return false
		},
	}
}

func (wc *WebChannel) handleWS(w http.ResponseWriter, r *http.Request) {
	var senderID, username string
	var si *sessionInfo

	// Support token-based auth for CLI clients (RemoteBackend).
	// Query params: ?token=<runner_token>&client_type=cli
	if token := r.URL.Query().Get("token"); token != "" && r.URL.Query().Get("client_type") == "cli" {
		var err error
		senderID, err = wc.validateCLIToken(token)
		if err != nil {
			log.WithError(err).Warn("CLI token auth failed")
			jsonErrorResponse(w, http.StatusUnauthorized, "invalid token")
			return
		}
		username = "cli:" + senderID
	} else {
		// Authenticate via cookie (web browser clients)
		si = wc.validateSession(r)
		if si == nil {
			jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		senderID = "web-" + strconv.Itoa(si.userID)
		// If linked to Feishu account, use Feishu identity directly.
		// This makes the web user share the same session/persona/workspace/skills/agents
		// as their Feishu account — effectively the same user.
		if si.feishuUserID != "" {
			senderID = si.feishuUserID
		}
		username = si.username
	}

	// Resolve canonical user identity (injects user_id + role for agent layer).
	var wsUserID int64
	var wsRole string
	if wc.callbacks.IdentityResolver != nil {
		resolveChannel := "web"
		if si != nil && si.feishuUserID != "" {
			resolveChannel = "feishu"
		} else if senderID == "admin" || senderID == "cli_user" {
			resolveChannel = "cli"
		}
		wsUserID, wsRole, _ = wc.callbacks.IdentityResolver.Resolve(resolveChannel, senderID)
	}

	// Upgrade to WebSocket
	conn, err := wc.wsUpgrader().Upgrade(w, r, nil)
	if err != nil {
		log.WithError(err).Warn("WebSocket upgrade failed")
		return
	}

	isCLI := r.URL.Query().Get("client_type") == "cli"
	client := &Client{
		connType:        clientConnTypeWS,
		wsConn:          conn,
		sendCh:          make(chan protocol.WSMessage, webSendChBufSize),
		done:            make(chan struct{}),
		hub:             wc.hub,
		userID:          senderID,
		id:              strings.ReplaceAll(uuid.New().String(), "-", ""),
		isCLI:           isCLI,
		canonicalUserID: wsUserID,
		canonicalRole:   wsRole,
		statelessSig:    make(chan struct{}, 1),
	}

	if !wc.hub.addClient(client.id, client) {
		_ = conn.Close()
		return
	}

	// Immediately subscribe to senderID for server-pushed events (progress, stream, etc.)
	// CLI clients skip this — they subscribe to their business chatID (absolute path)
	// via an explicit "subscribe" message after connection. Subscribing CLI clients to
	// senderID ("admin") causes cross-session widget pushes to overwrite other windows.
	if !isCLI {
		chatID := senderID // p2p mode: chatID == senderID
		wc.hub.subscribe(client.id, chatID)
	}

	log.WithFields(log.Fields{
		"sender_id": senderID,
		"client_id": client.id,
		"username":  username,
	}).Info("Web client connected")

	// Reconnect sync: wait for client's sync message with last_seq,
	// then replay missed events from the event stream buffer.
	// This runs in a goroutine to not block the read pump startup.
	go wc.replayMissedEvents(client, senderID)

	// Write pump
	wc.wg.Add(1)
	go func() {
		defer wc.wg.Done()
		wc.writePump(client)
	}()

	// Read pump (blocks until disconnect)
	// si is nil for CLI token auth; readPump uses it only for username lookup
	wc.readPump(client, si)
}

// validateCLIToken validates a CLI auth token and returns the associated senderID.
// Two auth methods:
//  1. Admin token (WebChannelConfig.AdminToken) — senderID = "admin", full access
//  2. Runner token — per-user token from runner_tokens table
func (wc *WebChannel) validateCLIToken(token string) (string, error) {
	if token == "" {
		return "", fmt.Errorf("empty token")
	}
	// Check admin token first
	if wc.config.AdminToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(wc.config.AdminToken)) == 1 {
		return "admin", nil
	}
	// Fall back to runner token lookup
	db := tools.GetRunnerTokenDB()
	if db == nil {
		return "", fmt.Errorf("runner token auth not available")
	}
	store := tools.NewRunnerTokenStore(db)
	userID := store.FindByTokenInRunnerTokens(token)
	if userID == "" {
		return "", fmt.Errorf("invalid token")
	}
	return userID, nil
}

// replayMissedEvents replays buffered events with seq > client's last_seq.
// Waits up to 2s for the client's sync message, then replays.
func (wc *WebChannel) replayMissedEvents(client *Client, senderID string) {
	// Resolve the user's currently active session (channel + chatID, respects chat switching)
	sel := wc.GetCurrentSession(senderID)
	chatID := sel.ChatID
	// The client sends sync immediately after WS connect.
	// If no sync arrives within 2s, send current state anyway (backward compat).
	syncCh := make(chan uint64, 1)
	client.syncCh.Store(&syncCh)
	defer client.syncCh.Store(nil)

	var fromSeq uint64
	select {
	case lastSeq := <-syncCh:
		fromSeq = lastSeq
	case <-time.After(2 * time.Second):
		// No sync message — client is old version. Send current progress snapshot.
		if wc.callbacks.GetActiveProgress != nil {
			if p := wc.callbacks.GetActiveProgress(sel.Channel, chatID); p != nil {
				select {
				case client.sendCh <- protocol.WSMessage{
					Type:     protocol.MsgTypeProgress,
					TS:       time.Now().Unix(),
					Progress: p,
				}:
				default:
				}
				if p.StreamContent != "" || p.ReasoningStreamContent != "" {
					select {
					case client.sendCh <- protocol.WSMessage{
						Type: protocol.MsgTypeStreamContent,
						TS:   time.Now().Unix(),
						Progress: &protocol.ProgressEvent{
							StreamContent:          p.StreamContent,
							ReasoningStreamContent: p.ReasoningStreamContent,
						},
					}:
					default:
					}
				}
			}
		}
		wc.enqueuePendingAskUser(client, sel.Channel, chatID)
		return
	}

	// Replay missed events from buffer. If no progress event is replayed, send the
	// current active progress snapshot once so reconnecting clients can still
	// restore an in-flight turn when their last_seq is already up to date.
	es := wc.getEventStream(chatID)
	events := es.eventsAfter(fromSeq)
	sort.SliceStable(events, func(i, j int) bool { return events[i].Seq < events[j].Seq })
	replayedProgress := false
	for _, evt := range events {
		if evt.Type == protocol.MsgTypeAskUser {
			continue
		}
		if evt.Type == protocol.MsgTypeProgress {
			replayedProgress = true
		}
		select {
		case client.sendCh <- evt:
		default:
			log.Debug("Client sendCh full during replay, stopping")
			return
		}
	}
	if !replayedProgress && wc.callbacks.GetActiveProgress != nil {
		if p := wc.callbacks.GetActiveProgress(sel.Channel, chatID); p != nil {
			select {
			case client.sendCh <- protocol.WSMessage{Type: protocol.MsgTypeProgress, TS: time.Now().Unix(), Progress: p}:
			default:
			}
		}
	}

	wc.enqueuePendingAskUser(client, sel.Channel, chatID)
}

func (wc *WebChannel) enqueuePendingAskUser(client *Client, channelName, chatID string) bool {
	if wc.callbacks.WithPendingAskUser == nil {
		return false
	}
	return wc.callbacks.WithPendingAskUser(channelName, chatID, func(pending *protocol.ProgressEvent) bool {
		select {
		case client.sendCh <- protocol.WSMessage{
			Type:     protocol.MsgTypeAskUser,
			TS:       time.Now().Unix(),
			Channel:  channelName,
			ChatID:   chatID,
			Progress: pending,
		}:
			return true
		default:
			return false
		}
	})
}

func (wc *WebChannel) writeCurrentWSMessage(
	client *Client,
	msg protocol.WSMessage,
	write func(protocol.WSMessage) error,
) (bool, error) {
	if client.isCLI || msg.Type != protocol.MsgTypeAskUser {
		return true, write(msg)
	}
	if msg.Progress == nil || msg.Progress.RequestID == "" || wc.callbacks.WithPendingAskUser == nil {
		return false, nil
	}

	var current protocol.WSMessage
	written := wc.callbacks.WithPendingAskUser(msg.Channel, msg.ChatID, func(pending *protocol.ProgressEvent) bool {
		if pending.RequestID != msg.Progress.RequestID {
			return false
		}
		msg.Progress = pending
		current = msg
		return true
	})
	if !written {
		return false, nil
	}
	return true, write(current)
}

func (wc *WebChannel) writePump(c *Client) {
	defer c.wsConn.Close()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.statelessSig:
			// Drain all accumulated stateless messages (one per type — latest only).
			for _, msg := range c.drainStateless() {
				c.wsConn.SetWriteDeadline(time.Now().Add(30 * time.Second))
				if err := c.wsConn.WriteJSON(*msg); err != nil {
					log.WithError(err).Debug("WS write error (stateless)")
					return
				}
			}
		case msg, ok := <-c.sendCh:
			if !ok {
				c.wsConn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			// Internal pong — reply to client ping via single-writer goroutine.
			if msg.Type == "__pong__" {
				c.wsConn.WriteControl(websocket.PongMessage, []byte(msg.Content), time.Now().Add(5*time.Second))
				continue
			}
			_, err := wc.writeCurrentWSMessage(c, msg, func(current protocol.WSMessage) error {
				c.wsConn.SetWriteDeadline(time.Now().Add(30 * time.Second))
				return c.wsConn.WriteJSON(current)
			})
			if err != nil {
				log.WithError(err).Debug("WS write error")
				return
			}
		case <-ticker.C:
			if err := c.wsConn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.done:
			// Server shutdown — send close frame with GoingAway status
			c.wsConn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutdown"))
			return
		}
	}
}

func (wc *WebChannel) readPump(c *Client, si *sessionInfo) {
	defer func() {
		c.wsConn.Close()
		c.closeDone()
		wc.hub.removeClient(c.id)
		// Note: do NOT removeRoutes here — multiple clients may share the same
		// senderID. Routes are idempotent and re-registered on each message.
		log.WithField("sender_id", c.userID).Info("Web client disconnected")
	}()

	c.wsConn.SetReadLimit(10 << 20) // 10MB max message (agent replies with code blocks can be large)
	c.wsConn.SetReadDeadline(time.Now().Add(120 * time.Second))
	c.wsConn.SetPongHandler(func(string) error {
		c.wsConn.SetReadDeadline(time.Now().Add(120 * time.Second))
		return nil
	})
	// Route client pings through sendCh so writePump handles the pong.
	// This avoids any direct write from readPump (no mutex needed).
	c.wsConn.SetPingHandler(func(appData string) error {
		c.wsConn.SetReadDeadline(time.Now().Add(120 * time.Second))
		select {
		case c.sendCh <- protocol.WSMessage{Type: "__pong__", Content: appData}:
		default:
		}
		return nil
	})

	// Resolve username safely (si is nil for CLI token-authed clients)
	username := "cli-remote"
	var feishuUserID string
	if si != nil {
		username = si.username
		feishuUserID = si.feishuUserID
	}

	// NOTE: chatID is NOT resolved once here. It was previously set to
	// c.userID and frozen for the lifetime of the WS connection, which
	// meant chat switching via POST /api/chats/{id}/switch had no effect
	// on WS message routing — messages went to the old (default) session.
	// Now each message handler resolves chatID dynamically via
	// wc.GetCurrentSession(c.userID) so chat switches take effect immediately.

	for {
		_, raw, err := c.wsConn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway,
				websocket.CloseNormalClosure) {
				log.WithError(err).Debug("WS read error")
			}
			return
		}

		var msg protocol.WSClientMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.WithError(err).Debug("WS invalid message")
			continue
		}

		// Handle message type (default to "message" for backward compatibility)
		if msg.Type == "" {
			msg.Type = "message"
		}

		switch msg.Type {
		case protocol.MsgTypeSync:
			// Client reconnect sync: sends last_seq from history API response.
			// The replayMissedEvents goroutine is waiting on this.
			if ch := c.syncCh.Load(); ch != nil {
				lastSeq := uint64(0)
				var syncMsg struct {
					LastSeq uint64 `json:"last_seq"`
				}
				if err := json.Unmarshal(raw, &syncMsg); err == nil {
					lastSeq = syncMsg.LastSeq
				}
				select {
				case *ch <- lastSeq:
				default:
				}
			}
			continue
		case protocol.MsgTypeCancel:
			identity := inboundIdentity{
				SenderID:           c.userID,
				SenderName:         username,
				CanonicalUserID:    c.canonicalUserID,
				CanonicalRole:      c.canonicalRole,
				IsCLI:              c.isCLI,
				OverrideSenderID:   msg.SenderID,
				OverrideSenderName: msg.SenderName,
			}
			if si != nil {
				identity.WebUserID = si.userID
				identity.FeishuUserID = si.feishuUserID
			}
			if _, err := wc.dispatchCancel(context.Background(), identity, msg.Channel, msg.ChatID); err != nil {
				log.WithError(err).WithFields(log.Fields{"channel": msg.Channel, "chat_id": msg.ChatID, "user_id": c.userID}).Warn("Web client cancel denied")
			}
			continue
		case protocol.MsgTypeRPC:
			// CLI RemoteBackend RPC request — dispatch to server-side handler.
			//
			// RPC processing runs in a goroutine so readPump can continue
			// reading the next WebSocket message. Without this, a long-running
			// RPC (e.g. refresh_model_entries, which fetches /models from every
			// subscription with up to 8s timeout each) blocks readPump and
			// queues all subsequent RPCs — the CLI UI appears frozen for
			// model switches, settings, etc. until the slow RPC completes.
			//
			// Concurrency safety: each RPC carries a unique client-generated ID.
			// The response is matched by ID on the client side, so out-of-order
			// completion is safe. Dependent RPC sequences from the same caller
			// are naturally ordered because RemoteTransport.Call blocks until
			// the response arrives before the caller sends the next request.
			if wc.callbacks.RPCHandler == nil {
				continue
			}
			var rpcReq struct {
				ID     string          `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(raw, &rpcReq); err != nil {
				log.WithError(err).Debug("Invalid RPC message from CLI client")
				continue
			}
			go func(id, method string, params json.RawMessage, identity RPCIdentity) {
				var result json.RawMessage
				var rpcErr error
				func() {
					defer func() {
						if r := recover(); r != nil {
							log.WithField("method", method).
								WithField("rpc_id", id).
								WithField("stack", string(debug.Stack())).
								WithError(fmt.Errorf("%v", r)).
								Error("RPC handler panic")
							rpcErr = fmt.Errorf("internal error: %v", r)
						}
					}()
					result, rpcErr = wc.callbacks.RPCHandler(method, params, identity)
				}()
				rpcMsg := protocol.WSMessage{Type: protocol.MsgTypeRPCResponse, ID: id}
				if rpcErr != nil {
					rpcMsg.Error = rpcErr.Error()
				} else if result != nil {
					rpcMsg.Result = result
				}
				select {
				case c.sendCh <- rpcMsg:
				case <-time.After(10 * time.Second):
					log.WithField("rpc_id", id).WithField("method", method).
						Error("RPC response send timeout (10s)")
				}
			}(rpcReq.ID, rpcReq.Method, rpcReq.Params, RPCIdentity{
				SenderID:        c.userID,
				CanonicalUserID: c.canonicalUserID,
				CanonicalRole:   c.canonicalRole,
			})
			continue
		case protocol.MsgTypeSubscribe:
			// Subscribe to a business chatID so the Hub can route
			// progress/stream/outbound events to this WS client.
			var subMsg struct {
				ChatID string `json:"chat_id"`
			}
			if err := json.Unmarshal(raw, &subMsg); err != nil || subMsg.ChatID == "" {
				continue
			}
			// Authorization: web clients may subscribe to any chatID they own
			// (verified via userCurrentChat or owned chatroom list). CLI users
			// can subscribe to their business chatID (workspace path).
			if !c.isCLI {
				// Web client: allow subscribing to the user's current active chat
				// (set via POST /api/chats/{id}/switch), their default senderID,
				// or an authorized SubAgent tenant for a visible parent session.
				activeSel := wc.GetCurrentSession(c.userID)
				if subMsg.ChatID != c.userID && subMsg.ChatID != activeSel.ChatID {
					webUserID := 0
					if si != nil {
						webUserID = si.userID
					}
					channelName := "web"
					if webChatIDLooksLikeSubAgent(subMsg.ChatID) {
						channelName = "agent"
					}
					if !wc.canAccessSession(context.Background(), webUserID, c.userID, channelName, subMsg.ChatID) {
						log.WithFields(log.Fields{"client_id": c.id, "chat_id": subMsg.ChatID, "user_id": c.userID}).Warn("Hub: web client tried to subscribe to foreign chatID, denied")
						continue
					}
				}
			}
			wc.hub.subscribe(c.id, subMsg.ChatID)
			log.WithFields(log.Fields{"client_id": c.id, "chat_id": subMsg.ChatID}).Info("Hub: client subscribed to chatID")

			// Resend pending AskUser prompt if this session is waiting for user input.
			// This handles the case where the AskUser was triggered before the web
			// client subscribed (e.g. CLI session triggered AskUser, web user switches
			// to that session afterwards).
			pendingChannel := "cli"
			if !c.isCLI {
				pendingChannel = wc.GetCurrentSession(c.userID).Channel
			}
			wc.enqueuePendingAskUser(c, pendingChannel, subMsg.ChatID)
		case protocol.MsgTypeTUIControlResp:
			// Remote CLI TUI control response — route to pending request handler
			if msg.TUIControl != nil && msg.ID != "" && wc.hub.tuiRespFn != nil {
				wc.hub.tuiRespFn(msg.ID, msg.TUIControl)
			}
		case protocol.MsgTypeMessage:
			identity := inboundIdentity{
				SenderID:        c.userID,
				SenderName:      username,
				FeishuUserID:    feishuUserID,
				CanonicalUserID: c.canonicalUserID,
				CanonicalRole:   c.canonicalRole,
				IsCLI:           c.isCLI,
			}
			if si != nil {
				identity.WebUserID = si.userID
			}
			sel, err := wc.dispatchUserMessage(context.Background(), identity, msg)
			if err != nil {
				if !errors.Is(err, errEmptyMessage) {
					log.WithError(err).WithFields(log.Fields{"channel": msg.Channel, "chat_id": msg.ChatID, "user_id": c.userID}).Warn("Web client message denied")
				}
				continue
			}
			wc.hub.subscribe(c.id, sel.ChatID)
		case protocol.MsgTypeAskUserResponse:
			var resp protocol.AskUserResponse
			if err := json.Unmarshal(raw, &resp); err != nil {
				log.WithError(err).Debug("WS invalid ask_user_response")
				continue
			}
			identity := inboundIdentity{
				SenderID:        c.userID,
				SenderName:      username,
				FeishuUserID:    feishuUserID,
				CanonicalUserID: c.canonicalUserID,
				CanonicalRole:   c.canonicalRole,
				IsCLI:           c.isCLI,
			}
			if si != nil {
				identity.WebUserID = si.userID
			}
			if _, err := wc.dispatchAskUserResponse(context.Background(), identity, msg.Channel, msg.ChatID, resp); err != nil {
				log.WithError(err).WithFields(log.Fields{"channel": msg.Channel, "chat_id": msg.ChatID, "user_id": c.userID}).Warn("Web client ask_user_response denied")
			}
		default:
			log.WithField("type", msg.Type).Debug("WS unknown message type")
		}
	}

}

// isImageExt returns true if the file extension is a common image format.
func isImageExt(ext string) bool {
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".svg", ".tiff", ".tif":
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
