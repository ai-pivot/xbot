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
