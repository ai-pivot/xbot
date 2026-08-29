package web

import (
	"testing"

	"xbot/protocol"
)

// pushSeqed is a test helper that mimics the production push path
// (seqMu-serialized nextSeq + push), keeping the ring seq-ascending.
func pushSeqed(es *eventStream, typ string, content string) protocol.WSMessage {
	m := protocol.WSMessage{Type: typ, Content: content}
	m.Seq = es.nextSeq()
	es.push(m)
	return m
}

// TestReplayAfter_TailWindow verifies replayAfter semantics across the full
// fromSeq range: full replay, partial suffix, steady state (client lagging a
// single event), and fully-caught-up.
func TestReplayAfter_TailWindow(t *testing.T) {
	es := newEventStream()
	pushed := make([]protocol.WSMessage, 0, 8)
	for i := 0; i < 8; i++ {
		pushed = append(pushed, pushSeqed(es, protocol.MsgTypeText, "m"))
	}

	// Client has nothing: replay everything in ring order.
	msgs, miss := es.replayAfter(0)
	if miss != 0 || len(msgs) != 8 {
		t.Fatalf("replayAfter(0) = %d msgs, miss=%d; want 8, 0", len(msgs), miss)
	}
	for i, m := range msgs {
		if m.Seq != pushed[i].Seq {
			t.Fatalf("full replay out of order at %d: seq=%d want %d", i, m.Seq, pushed[i].Seq)
		}
	}

	// Client has seq 3: suffix of 5 (seq 4..8).
	msgs, _ = es.replayAfter(3)
	if len(msgs) != 5 || msgs[0].Seq != 4 || msgs[4].Seq != 8 {
		t.Fatalf("replayAfter(3) = %d msgs [%d..%d]; want 5 [4..8]", len(msgs), msgs[0].Seq, msgs[len(msgs)-1].Seq)
	}

	// Steady state: client lags a single event — the hot path catchUpSSE hits
	// on every SSE echo. Must scan O(1) from the tail and allocate O(1).
	msgs, _ = es.replayAfter(7)
	if len(msgs) != 1 || msgs[0].Seq != 8 {
		t.Fatalf("steady replayAfter(7) = %+v; want [8]", msgs)
	}

	// Fully caught up: empty result.
	msgs, miss = es.replayAfter(8)
	if len(msgs) != 0 || miss != 0 {
		t.Fatalf("caught-up replayAfter(8) = %d msgs, miss=%d; want 0, 0", len(msgs), miss)
	}

	// fromSeq beyond the newest: empty, no overflow.
	msgs, _ = es.replayAfter(100)
	if len(msgs) != 0 {
		t.Fatalf("future replayAfter(100) = %d msgs; want 0", len(msgs))
	}
}

// TestReplayAfter_EvictionResync verifies capacity eviction advances the
// resync boundary and the tail window still returns the surviving suffix.
func TestReplayAfter_EvictionResync(t *testing.T) {
	es := newEventStream()
	for i := 0; i < eventStreamSize+10; i++ {
		pushSeqed(es, protocol.MsgTypeText, "m")
	}
	// Oldest 10 events were evicted: evictedThrough == 10.
	if es.evictedThrough != 10 {
		t.Fatalf("evictedThrough = %d; want 10", es.evictedThrough)
	}
	// Client at seq 5 (< evictedThrough): suffix + resync signal.
	msgs, miss := es.replayAfter(5)
	if miss != 10 {
		t.Fatalf("miss = %d; want 10 (evictedThrough)", miss)
	}
	if len(msgs) != eventStreamSize {
		t.Fatalf("suffix = %d; want %d (full ring)", len(msgs), eventStreamSize)
	}
	// Client inside the surviving window: no resync.
	_, miss = es.replayAfter(20)
	if miss != 0 {
		t.Fatalf("miss = %d; want 0 (within retained window)", miss)
	}
}

// TestReplayAfter_BarrierReplay verifies the barrier (replay reset marker)
// is replayed when the client has not seen it, and skipped otherwise.
func TestReplayAfter_BarrierReplay(t *testing.T) {
	es := newEventStream()
	pushSeqed(es, protocol.MsgTypeText, "old")
	barrier := protocol.WSMessage{Type: protocol.MsgTypeSession, SessionReset: true, Content: "rewound"}
	barrier.Seq = es.nextSeq()
	es.push(barrier) // barrier clears the ring and is stored separately
	a := pushSeqed(es, protocol.MsgTypeText, "a")
	b := pushSeqed(es, protocol.MsgTypeText, "b")

	// Client before the barrier: [barrier, a, b].
	msgs, _ := es.replayAfter(1)
	if len(msgs) != 3 || msgs[0].Seq != barrier.Seq || msgs[1].Seq != a.Seq || msgs[2].Seq != b.Seq {
		t.Fatalf("replayAfter(pre-barrier) = %+v; want [barrier, a, b]", msgs)
	}
	// Client at the barrier: [a, b] only.
	msgs, _ = es.replayAfter(barrier.Seq)
	if len(msgs) != 2 || msgs[0].Seq != a.Seq || msgs[1].Seq != b.Seq {
		t.Fatalf("replayAfter(barrier) = %+v; want [a, b]", msgs)
	}
	// Client past everything: empty.
	if msgs, _ = es.replayAfter(b.Seq); len(msgs) != 0 {
		t.Fatalf("caught-up = %+v; want none", msgs)
	}
}

// TestReplayAfter_RingSeqAscendingInvariant guards the invariant the tail-window
// scan depends on: ring contents are seq-ascending (head=oldest → tail=newest).
// Production keeps this via seqMu-serialized nextSeq+push (seqFn) and
// removeAt's order-preserving shift. If a future push path breaks ordering,
// the tail scan silently drops entries — this test fails first.
func TestReplayAfter_RingSeqAscendingInvariant(t *testing.T) {
	es := newEventStream()
	// Interleave stateless snapshots (merge + removeAt path) with stateful
	// events — the merge must keep the ring ascending.
	for i := 0; i < 32; i++ {
		pushSeqed(es, protocol.MsgTypeProgress, "stateful")
		pushSeqed(es, protocol.MsgTypeStreamContent, "stateless")
	}
	for i := 1; i < es.count; i++ {
		prev := es.buf[(es.head+i-1)%eventStreamSize].Seq
		cur := es.buf[(es.head+i)%eventStreamSize].Seq
		if prev >= cur {
			t.Fatalf("ring not ascending at offset %d: seq %d >= %d", i, prev, cur)
		}
	}
	// Stateless merging must not reorder: everything replayed after the last
	// seen stateless seq is exactly the trailing stateless snapshot.
	msgs, _ := es.replayAfter(es.lastSeq() - 1)
	if len(msgs) != 1 || msgs[0].Type != protocol.MsgTypeStreamContent {
		t.Fatalf("steady suffix after merge = %+v; want single freshest stateless", msgs)
	}
}

// BenchmarkReplayAfterSteadyState measures the hot path: a client lagging one
// event on a ring filled with 512 events. Tail-window scan must be O(1) with a
// minimal allocation (the old full-scan + count-sized prealloc measured
// 26473 ns/op + 155648 B/op — one 152KB slice per SSE-echoed event).
func BenchmarkReplayAfterSteadyState(b *testing.B) {
	es := newEventStream()
	for i := 0; i < eventStreamSize; i++ {
		pushSeqed(es, protocol.MsgTypeProgress, "stateful")
	}
	from := es.lastSeq() - 1
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		msgs, _ := es.replayAfter(from)
		if len(msgs) != 1 {
			b.Fatalf("steady replay = %d msgs; want 1", len(msgs))
		}
	}
}
