package web

import (
	"net/http"
	"sync"
	"sync/atomic"

	log "xbot/logger"
	"xbot/protocol"

	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// Hub: connection hub (routing + lifecycle)
// ---------------------------------------------------------------------------
//
// Routing is by business chatID (e.g. "/home/smith/src/xbot" or feishuUserID).
// Auth identity (c.userID, e.g. "admin") is NOT used for routing.
type Hub struct {
	mu      sync.RWMutex
	conns   map[string]*Client         // clientID → Client (lifecycle management)
	subs    map[string]map[string]bool // chatID → set of clientIDs (message routing)
	offline map[string]*ringBuffer     // chatID → offline message buffer
	offMu   sync.Mutex
	seqMu   sync.Mutex
	seqFn   func(string, protocol.WSMessage) protocol.WSMessage
	stopped bool

	tuiRespFn func(id string, payload *protocol.TUIControlPayload) // set by RemoteCLIChannel
}

func newHub() *Hub {
	return &Hub{
		conns:   make(map[string]*Client),
		subs:    make(map[string]map[string]bool),
		offline: make(map[string]*ringBuffer),
	}
}

// addClient registers a transport connection for lifecycle management.
// Use subscribe() to register it for message routing.
func (h *Hub) addClient(clientID string, c *Client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped {
		return false
	}
	h.conns[clientID] = c
	return true
}

// removeClient removes a transport connection and all its subscriptions.
func (h *Hub) removeClient(clientID string) {
	h.mu.Lock()
	delete(h.conns, clientID)
	for chatID, clients := range h.subs {
		delete(clients, clientID)
		if len(clients) == 0 {
			delete(h.subs, chatID)
		}
	}
	h.mu.Unlock()
}

// subscribe registers a client to receive messages for a given chatID.
// Idempotent — safe to call on every message from the client.
// Removes any previous subscription for this client (single-chat-per-connection model).
func (h *Hub) subscribe(clientID, chatID string) bool {
	h.mu.Lock()
	if h.stopped || h.conns[clientID] == nil {
		h.mu.Unlock()
		return false
	}
	// Remove old subscription(s) for this client (single active chat per WS connection).
	// Without this, the client accumulates subscriptions to multiple chatIDs and
	// receives events from sessions the user has already switched away from.
	for cid, clients := range h.subs {
		if cid != chatID && clients[clientID] {
			delete(clients, clientID)
			if len(clients) == 0 {
				delete(h.subs, cid)
			}
		}
	}
	if h.subs[chatID] == nil {
		h.subs[chatID] = make(map[string]bool)
	}
	// SSE replays from eventStream, its ordered source of truth. The legacy
	// offline ring remains exclusively for WebSocket clients.
	if c := h.conns[clientID]; c != nil && c.connType != clientConnTypeSSE {
		h.offMu.Lock()
		if buf, ok := h.offline[chatID]; ok {
			msgs := buf.flush()
			for _, msg := range msgs {
				deliveryMsg := msg
				if c.isCLI && isSSEEventType(deliveryMsg.Type) {
					deliveryMsg.Seq = 0
				}
				select {
				case c.sendCh <- deliveryMsg:
				default:
					log.WithFields(log.Fields{"client_id": clientID, "chat_id": chatID, "msg_type": msg.Type}).Warn("Hub.subscribe flush: sendCh full, dropping buffered message")
				}
			}
			delete(h.offline, chatID)
		}
		h.offMu.Unlock()
	}
	h.subs[chatID][clientID] = true
	h.mu.Unlock()
	return true
}

// sendToClient sends a message to all clients subscribed to a chatID.
// If no clients are subscribed, buffers the message for later delivery.
func (h *Hub) sendToClient(chatID string, msg protocol.WSMessage) bool {
	isSSEEvent := isSSEEventType(msg.Type)
	if !isSSEEvent {
		return h.deliverToSubscribers(chatID, msg, msg, false)
	}

	h.seqMu.Lock()
	defer h.seqMu.Unlock()
	sequencedMsg := h.sequenceEventLocked(chatID, normalizeSSEEvent(msg))
	return h.deliverToSubscribers(chatID, msg, sequencedMsg, true)
}

// sendSSEEventIf atomically checks and publishes a sequenced Web event.
// prepare runs under seqMu so ordinary publishers cannot enter between a
// replay check and the fallback event it guards. It must not acquire an
// application lock whose holder can publish another Hub event.
func (h *Hub) sendSSEEventIf(chatID string, prepare func() (protocol.WSMessage, bool)) bool {
	h.seqMu.Lock()
	defer h.seqMu.Unlock()

	msg, ok := prepare()
	if !ok || !isSSEEventType(msg.Type) {
		return false
	}
	sequencedMsg := h.sequenceEventLocked(chatID, msg)
	return h.deliverToSubscribers(chatID, msg, sequencedMsg, true)
}

func (h *Hub) deliverToSubscribers(chatID string, msg, sequencedMsg protocol.WSMessage, isSSEEvent bool) bool {
	h.mu.RLock()
	if h.stopped {
		h.mu.RUnlock()
		return false
	}
	// Copy subscriber keys to a slice to avoid iterating the map while
	// removeClient() may concurrently delete from it (data race).
	chatIDs, ok := h.subs[chatID]
	var subscriberIDs []string
	if ok {
		for cid := range chatIDs {
			subscriberIDs = append(subscriberIDs, cid)
		}
	}
	h.mu.RUnlock()

	sent := false
	for _, cid := range subscriberIDs {
		h.mu.RLock()
		c := h.conns[cid]
		h.mu.RUnlock()
		if c == nil {
			log.WithFields(log.Fields{"client_id": cid, "chat_id": chatID}).Debug("Hub.sendToClient: subscriber conn nil, skipping")
			continue
		}
		if c.connType == clientConnTypeSSE && !isSSEEvent {
			continue
		}
		deliveryMsg := msg
		if !c.isCLI {
			deliveryMsg.Seq = sequencedMsg.Seq
			if c.connType == clientConnTypeSSE {
				deliveryMsg = sequencedMsg
			}
		}
		if !isStatefulMsg(deliveryMsg) && c.connType != clientConnTypeSSE {
			// Stateless messages (stream_content, etc.) are superseded
			// by newer ones of the same type — store only the latest per type
			// in the stateless slot so writePump always sends the freshest snapshot.
			c.storeStateless(&deliveryMsg)
			sent = true
		} else {
			// Stateful WebSocket messages and all SSE events use best-effort
			// sendCh delivery. If sendCh is full (client network slow),
			// the event is dropped from the push path — but the server's
			// authoritative state (lastProgressSnapshot + iterationHistories) is
			// already updated BEFORE the push. The client detects the gap via
			// Seq jump and triggers an immediate GetActiveProgress RPC (snapshot +
			// log replay), which bypasses the Hub's sendCh entirely (RPC goes
			// through direct WS write). This is the Raft model: AppendEntries
			// (push) is best-effort, InstallSnapshot (pull) is authoritative.
			select {
			case c.sendCh <- deliveryMsg:
				sent = true
			default:
				log.WithFields(log.Fields{"client_id": cid, "chat_id": chatID, "msg_type": msg.Type}).Warn("Hub.sendToClient: sendCh full, dropping message (will be recovered via snapshot pull)")
			}
		}
	}
	if !sent {
		h.mu.RLock()
		if h.stopped {
			h.mu.RUnlock()
			return false
		}
		h.offMu.Lock()
		buf, ok := h.offline[chatID]
		if !ok {
			buf = newRingBuffer(webOfflineMsgBufSize)
			h.offline[chatID] = buf
		}
		bufferedMsg := msg
		if isSSEEvent {
			bufferedMsg.Seq = sequencedMsg.Seq
		}
		buf.push(bufferedMsg)
		h.offMu.Unlock()
		h.mu.RUnlock()
	}
	return sent
}

func (h *Hub) sequenceEventLocked(chatID string, msg protocol.WSMessage) protocol.WSMessage {
	if msg.Seq == 0 && h.seqFn != nil {
		return h.seqFn(chatID, msg)
	}
	return msg
}

func normalizeSSEEvent(msg protocol.WSMessage) protocol.WSMessage {
	if msg.Type == protocol.MsgTypeProgress && !isStatefulMsg(msg) {
		msg.Type = protocol.MsgTypeStreamContent
	}
	return msg
}

func (c *Client) closeDone() {
	c.closeOnce.Do(func() { close(c.done) })
}

// storeStateless saves a stateless message in a per-source slot (overwriting any
// previous message from the same source) and nudges writePump via statelessSig.
// The slot key combines msg type + Progress.ChatID so that different SubAgents
// each retain their own latest snapshot (e.g. two concurrent SubAgents' stream_content
// coexist without evicting each other).
func (c *Client) storeStateless(msg *protocol.WSMessage) {
	key := statelessSlotKey(msg)
	c.statelessMu.Lock()
	if c.statelessMap == nil {
		c.statelessMap = make(map[string]*protocol.WSMessage, 4)
	}
	c.statelessMap[key] = msg
	c.statelessMu.Unlock()
	// Non-blocking signal — writePump will drain all accumulated slots.
	select {
	case c.statelessSig <- struct{}{}:
	default:
	}
}

// statelessSlotKey returns a unique key per message source. For progress/stream
// messages it uses the type + Progress.ChatID (which carries the originating
// SubAgent session key). Types without a Progress payload fall back to type only.
func statelessSlotKey(msg *protocol.WSMessage) string {
	if msg.Progress != nil && msg.Progress.ChatID != "" {
		return msg.Type + "|" + msg.Progress.ChatID
	}
	return msg.Type
}

// drainStateless atomically swaps out all accumulated stateless messages and
// returns them as a slice. Called by writePump when statelessSig fires.
func (c *Client) drainStateless() []*protocol.WSMessage {
	c.statelessMu.Lock()
	old := c.statelessMap
	c.statelessMap = make(map[string]*protocol.WSMessage, len(old))
	c.statelessMu.Unlock()
	if len(old) == 0 {
		return nil
	}
	out := make([]*protocol.WSMessage, 0, len(old))
	for _, m := range old {
		out = append(out, m)
	}
	return out
}

func (h *Hub) stopAll() {
	h.mu.Lock()
	h.stopped = true
	for _, c := range h.conns {
		c.closeDone()
	}
	h.conns = make(map[string]*Client)
	h.subs = make(map[string]map[string]bool)
	h.mu.Unlock()
}

// broadcastSessionState preserves the existing WS broadcast while limiting SSE
// delivery to clients subscribed to the event's authorized chat.
func (h *Hub) broadcastSessionState(chatID string, msg protocol.WSMessage) {
	h.seqMu.Lock()
	defer h.seqMu.Unlock()
	sequencedMsg := h.sequenceEventLocked(chatID, msg)

	h.mu.RLock()
	clients := make([]*Client, 0, len(h.conns))
	for clientID, c := range h.conns {
		if c.connType == clientConnTypeSSE && !h.subs[chatID][clientID] {
			continue
		}
		clients = append(clients, c)
	}
	h.mu.RUnlock()
	for _, c := range clients {
		deliveryMsg := msg
		if !c.isCLI {
			deliveryMsg = sequencedMsg
		}
		select {
		case c.sendCh <- deliveryMsg:
		default:
			log.WithFields(log.Fields{"client_id": c.userID, "msg_type": msg.Type}).Debug("Hub.broadcastSessionState: sendCh full, skipping")
		}
	}
}

// broadcastToCLI sends a message only to CLI-type clients.
// Used for session state events that are only relevant to remote CLI sessions.
func (h *Hub) broadcastToCLI(msg protocol.WSMessage) {
	h.mu.RLock()
	var clients []*Client
	for _, c := range h.conns {
		if c.isCLI {
			clients = append(clients, c)
		}
	}
	h.mu.RUnlock()
	for _, c := range clients {
		if !isStatefulMsg(msg) {
			c.storeStateless(&msg)
		} else {
			select {
			case c.sendCh <- msg:
			default:
				log.WithFields(log.Fields{"client_id": c.userID, "msg_type": msg.Type}).Debug("Hub.broadcastToCLI: sendCh full, skipping")
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Client: a single WebSocket or SSE connection
// ---------------------------------------------------------------------------

const (
	clientConnTypeWS  = "ws"
	clientConnTypeSSE = "sse"
)

// Client represents a single connected transport client.
type Client struct {
	connType         string
	wsConn           *websocket.Conn
	w                http.ResponseWriter
	flusher          http.Flusher
	sendCh           chan protocol.WSMessage
	done             chan struct{}
	closeOnce        sync.Once
	hub              *Hub
	userID           string
	chatID           string
	lastSentSeq      uint64
	id               string                      // unique client ID (UUID), generated at connection time
	syncCh           atomic.Pointer[chan uint64] // for reconnect sync: client sends last_seq
	isCLI            bool                        // true if client_type=cli (runner token auth)
	canonicalUserID  int64                       // canonical user ID (from IdentityResolver)
	canonicalRole    string                      // user role ("admin" | "user")
	sseWriteCanceled atomic.Bool

	// statelessSlot holds the latest stateless message per type (progress,
	// stream_content, etc.).  Each type is kept at most once — newer values
	// silently overwrite older ones so only the freshest snapshot is ever
	// written to the WebSocket.  Protected by statelessMu.
	statelessMu  sync.Mutex
	statelessMap map[string]*protocol.WSMessage // msg type → latest message
	statelessSig chan struct{}                  // cap-1 signal: writePump checks slot
}

func isSSEEventType(msgType string) bool {
	switch msgType {
	case protocol.MsgTypeText,
		protocol.MsgTypeProgress,
		protocol.MsgTypeStreamContent,
		protocol.MsgTypeAskUser,
		protocol.MsgTypeCard,
		protocol.MsgTypeUserEcho,
		protocol.MsgTypeInjectUser,
		protocol.MsgTypePluginWidgets,
		protocol.MsgTypeSession,
		protocol.MsgTypeRunnerStatus,
		protocol.MsgTypeSyncProgress:
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// ring buffer for offline messages
// ---------------------------------------------------------------------------

type ringBuffer struct {
	buf   []protocol.WSMessage
	size  int
	head  int
	tail  int
	count int
}

func newRingBuffer(size int) *ringBuffer {
	return &ringBuffer{
		buf:  make([]protocol.WSMessage, size),
		size: size,
	}
}

// isStatefulMsg returns true for message types where every intermediate event
// matters (text, inject_user, ask_user, etc.). Returns false for state-snapshot
// types where only the latest value is meaningful (stream_content, etc.).
//
// Structured progress events (phase transitions, iteration deltas, PhaseDone,
// HistoryCompacted) are stateful — they carry iteration history deltas that
// must be delivered reliably and in order. Losing one means a permanently
// missing iteration in the TUI. Stream-only progress events (just
// StreamContent/ReasoningStreamContent) remain stateless.
func isStatefulMsg(msg protocol.WSMessage) bool {
	switch msg.Type {
	case protocol.MsgTypeStreamContent,
		protocol.MsgTypeSyncProgress, protocol.MsgTypeRunnerStatus:
		return false
	case protocol.MsgTypeProgress:
		if msg.Progress != nil {
			p := msg.Progress
			if p.Phase != "" || p.Iteration > 0 ||
				len(p.IterationHistory) > 0 || p.HistoryCompacted {
				return true
			}
		}
		return false
	default:
		return true
	}
}

func (rb *ringBuffer) push(msg protocol.WSMessage) {
	if !isStatefulMsg(msg) {
		// State-snapshot types: only keep the latest one.
		// Scan backwards to find an existing message of the same type.
		for i := rb.count - 1; i >= 0; i-- {
			idx := (rb.head + i) % rb.size
			if rb.buf[idx].Type == msg.Type {
				rb.buf[idx] = msg // replace in-place
				return
			}
		}
		// No existing message of this type — fall through to normal push.
	}
	if rb.count == rb.size {
		rb.head = (rb.head + 1) % rb.size
		rb.count--
	}
	rb.buf[rb.tail] = msg
	rb.tail = (rb.tail + 1) % rb.size
	rb.count++
}

func (rb *ringBuffer) flush() []protocol.WSMessage {
	result := make([]protocol.WSMessage, 0, rb.count)
	for rb.count > 0 {
		result = append(result, rb.buf[rb.head])
		rb.head = (rb.head + 1) % rb.size
		rb.count--
	}
	rb.head = 0
	rb.tail = 0
	return result
}
