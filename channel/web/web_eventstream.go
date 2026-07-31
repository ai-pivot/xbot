package web

import (
	"strings"
	"sync"
	"sync/atomic"

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

func parseSessionRouteKey(routeKey string) (channel, chatID string, ok bool) {
	channel, chatID, ok = strings.Cut(routeKey, "\x00")
	return channel, chatID, ok && channel != "" && chatID != ""
}

type eventStream struct {
	seq     atomic.Uint64
	mu      sync.Mutex
	buf     []protocol.WSMessage // ring buffer of seq-stamped events
	head    int
	tail    int
	count   int
	barrier *protocol.WSMessage
	// evictedThrough is the highest sequence removed because the bounded ring
	// was full. A resume cursor below this boundary requires an authoritative
	// history resync.
	evictedThrough uint64
}

func newEventStream() *eventStream {
	return &eventStream{
		buf: make([]protocol.WSMessage, eventStreamSize),
	}
}

// nextSeq atomically increments and returns the new seq.
func (es *eventStream) nextSeq() uint64 {
	return es.seq.Add(1)
}

// lastSeq returns the current seq (0 if no events yet).
func (es *eventStream) lastSeq() uint64 {
	return es.seq.Load()
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
func (es *eventStream) replayAfter(fromSeq uint64) ([]protocol.WSMessage, uint64) {
	es.mu.Lock()
	defer es.mu.Unlock()
	if es.count == 0 && (es.barrier == nil || es.barrier.Seq <= fromSeq) {
		if fromSeq < es.evictedThrough {
			return nil, es.evictedThrough
		}
		return nil, 0
	}
	result := make([]protocol.WSMessage, 0, es.count+1)
	if es.barrier != nil && es.barrier.Seq > fromSeq {
		result = append(result, *es.barrier)
	}
	for i := 0; i < es.count; i++ {
		idx := (es.head + i) % eventStreamSize
		if es.buf[idx].Seq > fromSeq {
			result = append(result, es.buf[idx])
		}
	}
	if fromSeq < es.evictedThrough {
		return result, es.evictedThrough
	}
	return result, 0
}

// getEventStream returns (or creates) the eventStream for a chatID.
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
		es = newEventStream()
		wc.evtBuf[chatID] = es
	}
	return es
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
