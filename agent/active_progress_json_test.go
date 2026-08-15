package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	channelpkg "xbot/channel"
	"xbot/protocol"
)

// TestGetActiveProgress_JSONFields verifies that after a real turn_started +
// structured event sequence, GetActiveProgress returns JSON containing
// turn_id and iteration — the frontend hydrate path reads these to position
// live messages by turn.
func TestGetActiveProgress_JSONFields(t *testing.T) {
	a := NewTestAgent()
	key := "web:chat-1"
	a.channelRange = func(fn func(name string, ch channelpkg.Channel) bool) {
		fn("web", &recordingProgressChannel{name: "web"})
	}

	// 1. turn_started with TurnID=9
	a.emitTurnStartedForTest(key, 9)

	// 2. First structured event (thinking, iteration 1)
	handler := a.buildProgressEventHandler("chat-1", "web")
	handler(&ProgressEvent{Structured: &StructuredProgress{
		Seq: 1, Phase: PhaseThinking, Iteration: 1, TurnID: 9,
	}})

	// 3. GetActiveProgress → JSON
	result := a.GetActiveProgress("web", "chat-1", protocol.FetchAll())
	if result == nil {
		t.Fatal("GetActiveProgress returned nil")
	}
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["turn_id"] != float64(9) {
		t.Errorf("BUG REPRODUCED: active_progress JSON turn_id = %v (want 9) — frontend can't position live msg", m["turn_id"])
	}
	if m["iteration"] != float64(1) {
		t.Errorf("active_progress JSON iteration = %v (want 1)", m["iteration"])
	}
}

func (a *Agent) emitTurnStartedForTest(key string, turnID uint64) {
	a.lastProgressSnapshot.Store(key, &protocol.ProgressEvent{
		ChatID: key, Phase: "turn_started", Seq: 1, TurnID: turnID,
		TurnStart: &protocol.TurnStartInfo{Trigger: "user", Content: "hi"},
	})
	_ = context.Background()
	_ = sync.Mutex{}
}

func TestBuildProgressPayload_TodosSerialization(t *testing.T) {
	a := NewTestAgent()
	ch := &recordingProgressChannel{name: "web"}
	a.channelRange = func(fn func(name string, ch channelpkg.Channel) bool) {
		fn("web", ch)
	}
	handler := a.buildProgressEventHandler("chat-1", "web")
	handler(&ProgressEvent{Structured: &StructuredProgress{
		Seq: 1, Phase: PhaseThinking, Iteration: 1, TurnID: 1,
		Todos: []TodoProgressItem{{ID: 1, Text: "任务1", Done: false}, {ID: 2, Text: "任务2", Done: true}},
	}})
	if len(ch.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(ch.events))
	}
	ev := ch.events[0]
	if len(ev.Todos) != 2 {
		t.Fatalf("BUG: payload.Todos = %d (want 2) — todos lost in buildProgressPayload", len(ev.Todos))
	}
	if ev.Todos[0].Text != "任务1" {
		t.Errorf("payload.Todos[0].Text = %q (want 任务1)", ev.Todos[0].Text)
	}
}

// TestGetActiveProgress_GapTooLarge_ResyncRequired verifies the gap-too-large
// guard: when an incremental pull (from_iter >= 0) would transfer more than
// maxIncrementalIterations iteration-history entries, GetActiveProgress
// returns ResyncRequired=true and an EMPTY IterationHistory — the client is
// signalled to reload from DB instead of consuming a huge delta.
func TestGetActiveProgress_GapTooLarge_ResyncRequired(t *testing.T) {
	a := NewTestAgent()
	key := "web:chat-gap"
	a.channelRange = func(fn func(name string, ch channelpkg.Channel) bool) {
		fn("web", &recordingProgressChannel{name: "web"})
	}

	// 1. turn_started
	a.emitTurnStartedForTest(key, 42)

	// 2. Emit maxIncrementalIterations + 11 structured events (iterations 1..N).
	//    The LAST event is not snapshotted (snapshot happens on the NEXT
	//    iteration's advance), so emitting 41 events snapshots 1..40 —
	//    an incremental pull from watermark 0 would transfer 40 entries > cap.
	handler := a.buildProgressEventHandler("chat-gap", "web")
	for i := 1; i <= maxIncrementalIterations+11; i++ {
		handler(&ProgressEvent{Structured: &StructuredProgress{
			Seq: uint64(i), Phase: PhaseThinking, Iteration: i, TurnID: 42,
		}})
	}

	// 3a. Incremental pull from watermark 0 → 40 iterations > 30 cap → resync.
	res := a.GetActiveProgress("web", "chat-gap", protocol.FetchSinceWatermark(0))
	if res == nil {
		t.Fatal("GetActiveProgress returned nil")
	}
	if !res.ResyncRequired {
		t.Errorf("incremental gap (40 iters > %d cap) should set ResyncRequired", maxIncrementalIterations)
	}
	if len(res.IterationHistory) != 0 {
		t.Errorf("ResyncRequired should carry EMPTY IterationHistory (got %d entries)", len(res.IterationHistory))
	}

	// 3b. Small incremental pull (gap 2) returns the delta normally.
	res2 := a.GetActiveProgress("web", "chat-gap", protocol.FetchSinceWatermark(maxIncrementalIterations+8))
	if res2 == nil {
		t.Fatal("GetActiveProgress returned nil")
	}
	if res2.ResyncRequired {
		t.Errorf("small incremental gap should NOT set ResyncRequired")
	}
	if len(res2.IterationHistory) != 2 {
		t.Errorf("small gap should return 2 iterations (got %d)", len(res2.IterationHistory))
	}

	// 3c. FetchAll (from_iter=-1, /su switch / initial restore) is exempt —
	//     always returns everything, never resync.
	res3 := a.GetActiveProgress("web", "chat-gap", protocol.FetchAll())
	if res3 == nil {
		t.Fatal("GetActiveProgress returned nil")
	}
	if res3.ResyncRequired {
		t.Errorf("FetchAll should never set ResyncRequired")
	}
	if len(res3.IterationHistory) != maxIncrementalIterations+10 {
		t.Errorf("FetchAll should return all %d iterations (got %d)", maxIncrementalIterations+10, len(res3.IterationHistory))
	}
}
