package web

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Per-CHAT frame sequencing + process epoch (STATE-MODEL P1 / C-1 / C-3 / C-13)
// ---------------------------------------------------------------------------
//
// Before this file, the transport sequence was PER ROUTE: every
// `channel\x00chatID` route had its own counter (eventStream.seq). A single
// logical chat that is mounted on two routes at once (e.g. `web\x00X` and
// `agent\x00X`) therefore produced two independent numberings — the client saw
// duplicate and/or regressing seq values and discarded fresh frames as "late"
// (contract C-1 violation).
//
// The contract is now: ONE monotonic counter per chat, shared by every route /
// connection that carries that chat. Route ring buffers still exist (replay,
// dedup) but they no longer own the counter — they hold a reference to the
// chat's counter.
//
// The epoch identifies the server process instance. It is stamped on every
// frame (WSMessage.Epoch) and on /api/session-state; a change means "seq
// restarted from zero" (server restart / replay buffer reset), which clients
// detect instead of guessing (C-13).

// serverEpoch is generated once per process. Time-based prefix keeps it
// sortable/diagnosable, the random suffix removes any chance of two processes
// starting in the same nanosecond colliding.
var serverEpoch = newServerEpoch()

func newServerEpoch() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x-%s", time.Now().UnixNano(), hex.EncodeToString(b[:]))
}

// ServerEpoch returns the current process's epoch. Stable for the lifetime of
// the process; a restart produces a new value.
func ServerEpoch() string { return serverEpoch }

// chatSeqCounter is the single per-chat frame counter. It is shared by every
// route of the same chat, so sequences assigned for one logical chat are
// strictly increasing and never reused regardless of which route carried them.
type chatSeqCounter struct {
	mu sync.Mutex
	n  uint64
}

func newChatSeqCounter() *chatSeqCounter { return &chatSeqCounter{} }

// next atomically increments and returns the new per-chat sequence number.
func (c *chatSeqCounter) next() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return c.n
}

// get returns the current (last assigned) sequence number without advancing.
func (c *chatSeqCounter) get() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// chatSeqFor returns (creating on first use) the counter for a chat id.
func (wc *WebChannel) chatSeqFor(chatID string) *chatSeqCounter {
	wc.chatSeqMu.Lock()
	defer wc.chatSeqMu.Unlock()
	if wc.chatSeqs == nil {
		wc.chatSeqs = make(map[string]*chatSeqCounter)
	}
	c, ok := wc.chatSeqs[chatID]
	if !ok {
		c = newChatSeqCounter()
		wc.chatSeqs[chatID] = c
	}
	return c
}

// currentCursor returns the (epoch, seq) watermark of a route's chat: the
// per-CHAT counter (not the route's ring high-water) — a snapshot must be
// stamped with a value every route of that chat agrees on.
func (wc *WebChannel) currentCursor(routeKey string) (string, uint64) {
	return serverEpoch, wc.chatSeqFor(chatIDForSeq(routeKey)).get()
}
