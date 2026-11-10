package web

import (
	"testing"

	"xbot/protocol"
)

// newSeqTestChannel builds a minimal WebChannel wired the same way NewWebChannel
// wires the frame path (hub.seqFn → stampAndBuffer → per-chat counter + ring).
func newSeqTestChannel() *WebChannel {
	wc := &WebChannel{hub: newHub()}
	wc.evtBuf = make(map[string]*eventStream)
	wc.hub.seqFn = wc.stampAndBuffer
	wc.hub.resetReplayFn = wc.clearReplayStream
	return wc
}

// publishProgress pushes one progress frame onto a route.
func publishProgress(wc *WebChannel, channelName, chatID string) {
	wc.hub.sendToSession(channelName, chatID, protocol.WSMessage{
		Type:     protocol.MsgTypeProgress,
		Channel:  channelName,
		ChatID:   chatID,
		Progress: &protocol.ProgressEvent{ChatID: channelName + ":" + chatID, Iteration: 1},
	})
}

// ringSeqs returns the seqs retained by a route's replay ring (oldest first).
func ringSeqs(wc *WebChannel, channelName, chatID string) []uint64 {
	events, _ := wc.getEventStream(sessionRouteKey(channelName, chatID)).replayAfter(0)
	out := make([]uint64, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Seq)
	}
	return out
}

// TestSeqPerChatAcrossRoutes — CI criterion G-1 (contract C-1).
//
// The same chat mounted on TWO routes (web + agent) must share ONE counter: the
// union of the sequences observed on both routes is exactly 1..N — no duplicate
// numbers, no regression, no gap in assignment order. Before the per-chat
// counter, each route counted independently, so the union looked like
// 1,1,2,2,... and a client either discarded fresh frames as "late" or rendered
// a stale state.
func TestSeqPerChatAcrossRoutes(t *testing.T) {
	wc := newSeqTestChannel()
	const frames = 10
	for i := 0; i < frames; i++ {
		if i%2 == 0 {
			publishProgress(wc, "web", "shared-chat")
		} else {
			publishProgress(wc, "agent", "shared-chat")
		}
	}

	seen := map[uint64]int{}
	for _, seq := range append(ringSeqs(wc, "web", "shared-chat"), ringSeqs(wc, "agent", "shared-chat")...) {
		seen[seq]++
	}
	if len(seen) != frames {
		t.Fatalf("expected %d distinct seq values across both routes, got %d: %v", frames, len(seen), seen)
	}
	for seq, count := range seen {
		if count != 1 {
			t.Fatalf("seq %d was assigned %d times — per-chat counter must never re-issue a number", seq, count)
		}
	}
	for want := uint64(1); want <= frames; want++ {
		if seen[want] != 1 {
			t.Fatalf("seq %d missing from the assignment sequence (got %v)", want, seen)
		}
	}

	// A second chat must NOT share the counter (isolation) — it starts at 1.
	publishProgress(wc, "web", "other-chat")
	if seqs := ringSeqs(wc, "web", "other-chat"); len(seqs) != 1 || seqs[0] != 1 {
		t.Fatalf("independent chat must start its own sequence at 1, got %v", seqs)
	}
}

// TestSeqNeverRegressesAcrossRoutes — the assignment order must match the ring
// order on every route: a client that receives route A then route B must never
// see a number go backwards (contract C-1).
func TestSeqNeverRegressesAcrossRoutes(t *testing.T) {
	wc := newSeqTestChannel()
	var all []uint64
	for i := 0; i < 6; i++ {
		channelName := "web"
		if i%3 == 0 {
			channelName = "agent"
		}
		publishProgress(wc, channelName, "chat-x")
		all = append(all, ringSeqs(wc, channelName, "chat-x")...)
	}
	// Collect unique values in first-seen order and assert monotonicity.
	seen := make(map[uint64]bool, len(all))
	prev := uint64(0)
	for _, seq := range all {
		if seen[seq] {
			continue
		}
		seen[seq] = true
		if seq <= prev {
			t.Fatalf("seq regressed: saw %d after %d (full: %v)", seq, prev, all)
		}
		prev = seq
	}
}

// TestFramesCarryEpochAndChatID — contract C-3: every frame carries
// (epoch, seq, chat_id). The epoch is stable for the whole process.
func TestFramesCarryEpochAndChatID(t *testing.T) {
	wc := newSeqTestChannel()
	publishProgress(wc, "web", "chat-epoch")

	events, _ := wc.getEventStream(sessionRouteKey("web", "chat-epoch")).replayAfter(0)
	if len(events) != 1 {
		t.Fatalf("expected 1 buffered frame, got %d", len(events))
	}
	ev := events[0]
	if ev.Seq != 1 {
		t.Fatalf("first frame must carry seq 1, got %d", ev.Seq)
	}
	if ev.Epoch == "" {
		t.Fatal("frame carries no epoch — restart detection (C-13) becomes impossible")
	}
	if ev.Epoch != ServerEpoch() {
		t.Fatalf("frame epoch %q != process epoch %q", ev.Epoch, ServerEpoch())
	}
	if ev.ChatID != "chat-epoch" {
		t.Fatalf("frame chat_id = %q, want the business chat id", ev.ChatID)
	}
	if ev.RouteChannel != "web" {
		t.Fatalf("frame route_channel = %q, want web", ev.RouteChannel)
	}
}

// TestDoneAndFinalAreSSEEvents — the new frame types must ride the same ordered,
// replayable SSE stream as progress_structured (otherwise a reconnect could
// never recover a committed iteration).
func TestDoneAndFinalAreSSEEvents(t *testing.T) {
	if !isSSEEventType(protocol.MsgTypeDone) {
		t.Fatal("done must be sequenced + replayable like every other frame")
	}
	if !isSSEEventType(protocol.MsgTypeFinal) {
		t.Fatal("final must be sequenced + replayable like every other frame")
	}
}

// TestFrameSenderStampsSeq — the TurnFrameSender path (used by the agent) goes
// through the same stamping as every other frame.
func TestFrameSenderStampsSeq(t *testing.T) {
	wc := newSeqTestChannel()
	wc.SendIterationDone("web:chat-frames", &protocol.IterationBlock{
		TurnID: 3, Iteration: 1, Content: "hello", Tools: []protocol.ToolProgress{},
	})
	wc.SendTurnFinal("web:chat-frames", &protocol.TurnFinalFrame{TurnID: 3, Content: "hello"})

	events, _ := wc.getEventStream(sessionRouteKey("web", "chat-frames")).replayAfter(0)
	if len(events) != 2 {
		t.Fatalf("expected 2 buffered frames, got %d", len(events))
	}
	if events[0].Type != protocol.MsgTypeDone || events[0].Done == nil {
		t.Fatalf("first frame = %q, payload %+v", events[0].Type, events[0].Done)
	}
	if events[1].Type != protocol.MsgTypeFinal || events[1].Final == nil {
		t.Fatalf("second frame = %q, payload %+v", events[1].Type, events[1].Final)
	}
	if events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("frame seqs = %d,%d — want 1,2", events[0].Seq, events[1].Seq)
	}
	for _, ev := range events {
		if ev.Epoch == "" {
			t.Fatal("frame-level epoch missing")
		}
	}
}
