package agent

import (
	"testing"

	"xbot/protocol"
)

// TestGetActiveProgress_TurnIDIteration verifies that GetActiveProgress returns
// the TurnID and Iteration stored in the snapshot — the frontend relies on
// these to position live messages by turn (MessageList rows building).
func TestGetActiveProgress_TurnIDIteration(t *testing.T) {
	a := NewTestAgent()
	key := "web:chat-1"
	a.lastProgressSnapshot.Store(key, &protocol.ProgressEvent{
		ChatID: key, Phase: "tool_exec", TurnID: 7, Iteration: 3,
	})
	result := a.GetActiveProgress("web", "chat-1", protocol.FetchAll())
	if result == nil {
		t.Fatal("GetActiveProgress returned nil")
		return
	}
	if result.TurnID != 7 {
		t.Errorf("TurnID = %d, want 7 — active progress must carry turn_id", result.TurnID)
	}
	if result.Iteration != 3 {
		t.Errorf("Iteration = %d, want 3 — active progress must carry iteration", result.Iteration)
	}
}
