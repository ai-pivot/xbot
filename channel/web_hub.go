package channel

import (
	"sync"
	"sync/atomic"

	"xbot/protocol"

	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// Hub: WebSocket connection hub (routing + lifecycle)
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

	tuiRespFn func(id string, payload *protocol.TUIControlPayload) // set by RemoteCLIChannel
}

func newHub() *Hub {
	return &Hub{
		conns:   make(map[string]*Client),
		subs:    make(map[string]map[string]bool),
		offline: make(map[string]*ringBuffer),
	}
}

// addClient registers a WS connection for lifecycle management.
// Use subscribe() to register it for message routing.
func (h *Hub) addClient(clientID string, c *Client) {
	h.mu.Lock()
	h.conns[clientID] = c
	h.mu.Unlock()
}

// removeClient removes a WS connection and all its subscriptions.
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
func (h *Hub) subscribe(clientID, chatID string) {
	h.mu.Lock()
	if h.subs[chatID] == nil {
		h.subs[chatID] = make(map[string]bool)
		// Flush any offline messages for this chatID
		h.offMu.Lock()
		if buf, ok := h.offline[chatID]; ok {
			msgs := buf.flush()
			for _, msg := range msgs {
				if c, ok := h.conns[clientID]; ok {
					select {
					case c.sendCh <- msg:
					default:
					}
				}
			}
			delete(h.offline, chatID)
		}
		h.offMu.Unlock()
	}
	h.subs[chatID][clientID] = true
	h.mu.Unlock()
}

// sendToClient sends a message to all clients subscribed to a chatID.
// If no clients are subscribed, buffers the message for later delivery.
func (h *Hub) sendToClient(chatID string, msg protocol.WSMessage) bool {
	h.mu.RLock()
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
			continue
		}
		select {
		case c.sendCh <- msg:
			sent = true
		default:
			// sendCh full, skip
		}
	}
	if !sent {
		h.offMu.Lock()
		buf, ok := h.offline[chatID]
		if !ok {
			buf = newRingBuffer(webOfflineMsgBufSize)
			h.offline[chatID] = buf
		}
		buf.push(msg)
		h.offMu.Unlock()
	}
	return sent
}

func (c *Client) closeDone() {
	c.closeOnce.Do(func() { close(c.done) })
}

func (h *Hub) stopAll() {
	h.mu.Lock()
	for _, c := range h.conns {
		c.closeDone()
	}
	h.conns = make(map[string]*Client)
	h.subs = make(map[string]map[string]bool)
	h.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Client: a single WebSocket connection
// ---------------------------------------------------------------------------

// Client represents a single WebSocket client
type Client struct {
	conn      *websocket.Conn
	sendCh    chan protocol.WSMessage
	done      chan struct{}
	closeOnce sync.Once
	hub       *Hub
	userID    string
	id        string                      // unique client ID (UUID), generated at connection time
	syncCh    atomic.Pointer[chan uint64] // for reconnect sync: client sends last_seq
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

func (rb *ringBuffer) push(msg protocol.WSMessage) {
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
