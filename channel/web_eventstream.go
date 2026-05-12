package channel

import (
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

type eventStream struct {
	seq   atomic.Uint64
	mu    sync.Mutex
	buf   []protocol.WSMessage // ring buffer of seq-stamped events
	head  int
	tail  int
	count int
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

// push appends a seq-stamped event to the ring buffer.
func (es *eventStream) push(msg protocol.WSMessage) {
	es.mu.Lock()
	defer es.mu.Unlock()
	if es.count == eventStreamSize {
		es.head = (es.head + 1) % eventStreamSize
		es.count--
	}
	es.buf[es.tail] = msg
	es.tail = (es.tail + 1) % eventStreamSize
	es.count++
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
}

// eventsAfter returns all buffered events with seq > fromSeq, in order.
func (es *eventStream) eventsAfter(fromSeq uint64) []protocol.WSMessage {
	es.mu.Lock()
	defer es.mu.Unlock()
	if es.count == 0 {
		return nil
	}
	var result []protocol.WSMessage
	for i := 0; i < es.count; i++ {
		idx := (es.head + i) % eventStreamSize
		if es.buf[idx].Seq > fromSeq {
			result = append(result, es.buf[idx])
		}
	}
	return result
}

// getEventStream returns (or creates) the eventStream for a chatID.
func (wc *WebChannel) getEventStream(chatID string) *eventStream {
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
