package web

import (
	"strings"
	"sync"

	"xbot/protocol"
)

// ---------------------------------------------------------------------------
// Event stream — seq-stamped ring buffer for replay / dedup
// ---------------------------------------------------------------------------

// eventStream tracks monotonic seq and buffers recent events per chatID.
// Used for:
//  1. Dedup: each event carries seq, frontend ignores stale (seq <= lastSeen)
//  2. Replay: on WS reconnect, server sends events with seq > client's last_seq
const eventStreamSize = 512

func sessionRouteKey(channel, chatID string) string {
	if channel == "" {
		channel = "web"
	}
	return channel + "\x00" + chatID
}

// canonicalChatID strips repeated channel prefixes from an inbound chat id.
//
// The session key is `channel + ":" + chatID`; a client that sends a *full key*
// (`web:chat_X`) in `chat_id` would otherwise create the ghost session
// `web:web:chat_X`. This was the root cause of "send and cancel hit different
// sessions" (2026-10-17): some client paths canonicalized, others did not.
// One canonical form at the transport boundary makes message/cancel/history/
// ask/SSE all agree, independent of client version.
//
// Only the *same* channel's prefix is stripped (a `cli:` id passed to the web
// channel must stay a distinct session). Empty chatID is returned unchanged so
// callers can keep their existing "missing => default/current" semantics.
func canonicalChatID(channel, chatID string) string {
	chatID = strings.TrimSpace(chatID)
	if channel == "" || channel == "agent" {
		// agent 路由的 chat key 是**全键**（含 role/instance），不参与剥前缀；
		// 把当前"恰好不撞"变成显式契约。
		return chatID
	}
	prefix := channel + ":"
	for strings.HasPrefix(chatID, prefix) {
		chatID = strings.TrimPrefix(chatID, prefix)
	}
	return chatID
}

// normalizeWSInboundChatID applies the same canonicalization to every inbound WS
// command (message/cancel/ask_user/sync/subscribe). Kept as a named seam so the
// wire contract is testable without a live websocket.
func normalizeWSInboundChatID(msg *protocol.WSClientMessage) {
	if msg == nil {
		return
	}
	msg.ChatID = canonicalChatID(msg.Channel, msg.ChatID)
}

func parseSessionRouteKey(routeKey string) (channel, chatID string, ok bool) {
	channel, chatID, ok = strings.Cut(routeKey, "\x00")
	return channel, chatID, ok && channel != "" && chatID != ""
}

type eventStream struct {
	// seq is the chat's SINGLE frame counter (shared across every route of the
	// same chat — see web_seq.go). The ring buffer below is per route.
	seq     *chatSeqCounter
	mu      sync.Mutex
	buf     []protocol.WSMessage // ring buffer of seq-stamped events
	head    int
	tail    int
	count   int
	barrier *protocol.WSMessage
	// last is this ROUTE's ring high-water mark (highest seq ever pushed here).
	// The sequence counter itself is per CHAT (es.seq); a route's ring may hold
	// a sparse subset of it when the same chat is mounted on several routes, so
	// the two values are deliberately distinct.
	last uint64
	// evictedThrough is the highest sequence removed because the bounded ring
	// was full. A resume cursor below this boundary requires an authoritative
	// history resync.
	evictedThrough uint64
}

func newEventStream(seq *chatSeqCounter) *eventStream {
	if seq == nil {
		seq = newChatSeqCounter()
	}
	return &eventStream{
		seq: seq,
		buf: make([]protocol.WSMessage, eventStreamSize),
	}
}

// nextSeq atomically increments and returns the new seq (per chat).
func (es *eventStream) nextSeq() uint64 {
	return es.seq.next()
}

// lastSeq returns this ROUTE's ring high-water mark (0 if no events yet).
// It is NOT the chat counter: with one counter shared by several routes, a
// route only sees the frames addressed to it (the rest are numbered but never
// pushed here). Replay cursors are route-scoped, so this is the value they mean.
func (es *eventStream) lastSeq() uint64 {
	es.mu.Lock()
	defer es.mu.Unlock()
	return es.last
}

// push appends a seq-stamped event to the ring buffer. Consecutive stateless
// snapshots share one slot. Stream fields are cumulative but arrive in
// independent messages, so replacing a stream snapshot must merge those fields.
func (es *eventStream) push(msg protocol.WSMessage) {
	es.mu.Lock()
	defer es.mu.Unlock()
	if isSSEReplayBarrier(msg) {
		msgCopy := msg
		es.barrier = &msgCopy
		es.head = 0
		es.tail = 0
		es.count = 0
		es.evictedThrough = 0
		// The barrier IS the newest event of this route: it resets the replay
		// window, so the route's high-water becomes the barrier's seq (a resume
		// cursor below it must be answered with a resync, never silently dropped).
		if msg.Seq > es.last {
			es.last = msg.Seq
		}
		return
	}
	if !isStatefulSSEEvent(msg) {
		key := statelessSlotKey(&msg)
		for i := es.count - 1; i >= 0; i-- {
			idx := (es.head + i) % eventStreamSize
			previous := es.buf[idx]
			if isSSEStreamMergeBoundary(msg, previous) {
				break
			}
			if statelessSlotKey(&previous) == key {
				msg = mergeStatelessEvent(previous, msg)
				es.removeAt(i)
				break
			}
		}
	}
	if es.count == eventStreamSize {
		if dropped := es.buf[es.head].Seq; dropped > es.evictedThrough {
			es.evictedThrough = dropped
		}
		es.head = (es.head + 1) % eventStreamSize
		es.count--
	}
	es.buf[es.tail] = msg
	es.tail = (es.tail + 1) % eventStreamSize
	es.count++
	if msg.Seq > es.last {
		es.last = msg.Seq
	}
}

// removeAt removes a logical ring offset while preserving sequence order.
// The caller holds es.mu.
func (es *eventStream) removeAt(offset int) {
	for i := offset; i < es.count-1; i++ {
		to := (es.head + i) % eventStreamSize
		from := (es.head + i + 1) % eventStreamSize
		es.buf[to] = es.buf[from]
	}
	es.tail = (es.tail - 1 + eventStreamSize) % eventStreamSize
	es.buf[es.tail] = protocol.WSMessage{}
	es.count--
}

// isStatefulSSEEvent classifies messages after normalizeSSEEvent has split
// stream-only progress into stream_content. Every remaining progress event is
// structured and must be retained independently for reconnect replay.
func isStatefulSSEEvent(msg protocol.WSMessage) bool {
	return msg.Type == protocol.MsgTypeProgress || isStatefulMsg(msg)
}

func isSSEStreamMergeBoundary(current, previous protocol.WSMessage) bool {
	if current.Type != protocol.MsgTypeStreamContent || current.Progress == nil || !isStatefulSSEEvent(previous) {
		return false
	}
	if previous.Type != protocol.MsgTypeProgress || previous.Progress == nil {
		return true
	}
	currentSource := current.Progress.ChatID
	previousSource := previous.Progress.ChatID
	return currentSource == "" || previousSource == "" || currentSource == previousSource
}

func mergeStatelessEvent(previous, current protocol.WSMessage) protocol.WSMessage {
	if current.Type != protocol.MsgTypeStreamContent || previous.Progress == nil || current.Progress == nil {
		return current
	}

	merged := *current.Progress
	if len(previous.Progress.StreamContent) > len(merged.StreamContent) {
		merged.StreamContent = previous.Progress.StreamContent
	}
	if len(previous.Progress.ReasoningStreamContent) > len(merged.ReasoningStreamContent) {
		merged.ReasoningStreamContent = previous.Progress.ReasoningStreamContent
	}
	if len(previous.Progress.StreamingTools) > len(merged.StreamingTools) {
		merged.StreamingTools = previous.Progress.StreamingTools
	}
	if previous.Progress.StreamTokens > merged.StreamTokens {
		merged.StreamTokens = previous.Progress.StreamTokens
	}
	current.Progress = &merged
	return current
}

// clear drops buffered events without resetting the monotonic sequence.
// Used on session reset (/new) so reconnect replay cannot resurrect progress
// events from the previous session.
func (es *eventStream) clear() {
	es.mu.Lock()
	defer es.mu.Unlock()
	es.head = 0
	es.tail = 0
	es.count = 0
	es.barrier = nil
	es.evictedThrough = 0
}

// replayAfter returns the retained suffix and the highest missing sequence.
// Missing stateless snapshots removed by replacement are not treated as
// overflow; only capacity eviction advances the resync boundary.
//
// Ring contents are seq-ascending (head=oldest → tail=newest): seq is
// assigned and pushed under the Hub's seqMu (seqFn → nextSeq + push are
// serialized), and stateless merge's removeAt preserves order. We scan
// backwards from the tail to find the newest entry the client already has
// (seq <= fromSeq) and replay only the suffix after it — steady state
// (client lagging a single event) scans O(1) and allocates only what it
// returns, instead of preallocating the full ring (512 × 296B ≈ 152KB)
// on every SSE-echoed event.
func (es *eventStream) replayAfter(fromSeq uint64) ([]protocol.WSMessage, uint64) {
	es.mu.Lock()
	defer es.mu.Unlock()
	if es.count == 0 && (es.barrier == nil || es.barrier.Seq <= fromSeq) {
		if fromSeq < es.evictedThrough {
			return nil, es.evictedThrough
		}
		return nil, 0
	}
	// prefix = number of leading ring entries with seq <= fromSeq
	// (all ring entries are older-or-equal to the suffix by the ascending
	// invariant above; a full scan miss leaves prefix=0 = replay all).
	prefix := 0
	for i := es.count - 1; i >= 0; i-- {
		if es.buf[(es.head+i)%eventStreamSize].Seq <= fromSeq {
			prefix = i + 1
			break
		}
	}
	suffixLen := es.count - prefix
	if suffixLen == 0 && (es.barrier == nil || es.barrier.Seq <= fromSeq) {
		if fromSeq < es.evictedThrough {
			return nil, es.evictedThrough
		}
		return nil, 0
	}
	result := make([]protocol.WSMessage, 0, suffixLen+1) // +1 for barrier
	if es.barrier != nil && es.barrier.Seq > fromSeq {
		result = append(result, *es.barrier)
	}
	for i := prefix; i < es.count; i++ {
		result = append(result, es.buf[(es.head+i)%eventStreamSize])
	}
	if fromSeq < es.evictedThrough {
		return result, es.evictedThrough
	}
	return result, 0
}

// getEventStream returns (or creates) the eventStream for a chatID.
// The eventStream ring is per route; the seq counter it stamps with is per
// CHAT (web_seq.go), so every route of the same chat shares one numbering.
func (wc *WebChannel) getEventStream(chatID string) *eventStream {
	if !strings.ContainsRune(chatID, '\x00') {
		chatID = sessionRouteKey("web", chatID)
	}
	wc.evtBufMu.Lock()
	defer wc.evtBufMu.Unlock()
	if wc.evtBuf == nil {
		wc.evtBuf = make(map[string]*eventStream)
	}
	es, ok := wc.evtBuf[chatID]
	if !ok {
		es = newEventStream(wc.chatSeqFor(chatIDForSeq(chatID)))
		wc.evtBuf[chatID] = es
	}
	return es
}

// chatIDForSeq maps a route key (`channel\x00chatID`) to the chat identity used
// for sequencing. Contract C-1: one counter per CHAT, not per route — a chat
// mounted on both `web\x00X` and `agent\x00X` must share a single sequence.
func chatIDForSeq(routeKey string) string {
	if _, chatID, ok := parseSessionRouteKey(routeKey); ok {
		return chatID
	}
	return routeKey
}

// clearReplayStream drops buffered events while preserving the route's
// monotonic sequence. The Hub calls this with seqMu held immediately before it
// sequences a history_rewound event.
func (wc *WebChannel) clearReplayStream(routeKey string) {
	wc.evtBufMu.Lock()
	es := wc.evtBuf[routeKey]
	wc.evtBufMu.Unlock()
	if es != nil {
		es.clear()
	}
}

// clearSessionTransportState drops replay and request-dedup state after a
// session is deleted. Lock ordering matches event publication: seqMu then
// evtBufMu, so an in-flight publisher cannot restore an older stream entry.
func (wc *WebChannel) clearSessionTransportState(channel, chatID string) {
	routeKey := sessionRouteKey(channel, chatID)
	wc.hub.seqMu.Lock()
	wc.evtBufMu.Lock()
	delete(wc.evtBuf, routeKey)
	wc.evtBufMu.Unlock()
	wc.hub.seqMu.Unlock()

	wc.inboundRequestsMu.Lock()
	for key := range wc.inboundRequests {
		if key.channel == channel && key.chatID == chatID {
			delete(wc.inboundRequests, key)
		}
	}
	wc.inboundRequestsMu.Unlock()
}
