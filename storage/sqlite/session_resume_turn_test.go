package sqlite

import (
	"testing"

	"xbot/llm"
)

// TestGetLastUserTurnID verifies the resume-turn resolution query: the resume
// Run must REUSE the turn of the LAST non-display-only user message (the
// interrupted turn's owner), so the resumed work belongs to the same turn as
// the original user message (one assistant block, not one per restart).
func TestGetLastUserTurnID(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)

	// Empty session: no user message → 0 (caller falls back to a fresh turn id).
	if tid, err := svc.GetLastUserTurnID(tenantID); err != nil {
		t.Fatalf("GetLastUserTurnID on empty: %v", err)
	} else if tid != 0 {
		t.Fatalf("GetLastUserTurnID on empty = %d, want 0", tid)
	}

	// Turn 5: user + assistant reply (completed turn).
	u1 := llm.NewUserMessage("first question")
	u1.TurnID = 5
	if _, err := svc.AppendMessage(tenantID, u1); err != nil {
		t.Fatal(err)
	}
	a1 := llm.NewAssistantMessage("first answer")
	a1.TurnID = 5
	if _, err := svc.AppendMessage(tenantID, a1); err != nil {
		t.Fatal(err)
	}
	if tid, err := svc.GetLastUserTurnID(tenantID); err != nil {
		t.Fatalf("GetLastUserTurnID: %v", err)
	} else if tid != 5 {
		t.Fatalf("GetLastUserTurnID = %d, want 5", tid)
	}

	// Turn 8: user message interrupted by a graceful-shutdown restart
	// (assistant rows exist but no final reply). Still the last user message.
	u2 := llm.NewUserMessage("second question")
	u2.TurnID = 8
	if _, err := svc.AppendMessage(tenantID, u2); err != nil {
		t.Fatal(err)
	}
	a2 := llm.NewAssistantMessage("")
	a2.TurnID = 8
	a2.ToolCalls = []llm.ToolCall{{ID: "c1", Name: "Shell", Arguments: "{}"}}
	if _, err := svc.AppendMessage(tenantID, a2); err != nil {
		t.Fatal(err)
	}
	if tid, err := svc.GetLastUserTurnID(tenantID); err != nil {
		t.Fatalf("GetLastUserTurnID after interrupted turn: %v", err)
	} else if tid != 8 {
		t.Fatalf("GetLastUserTurnID = %d, want 8 (the interrupted turn's id — resume reuses it)", tid)
	}

	// Display-only user rows (cancel markers etc.) are skipped.
	u3 := llm.NewUserMessage("[interrupted]")
	u3.TurnID = 99
	u3.DisplayOnly = true
	if _, err := svc.AppendMessage(tenantID, u3); err != nil {
		t.Fatal(err)
	}
	if tid, err := svc.GetLastUserTurnID(tenantID); err != nil {
		t.Fatalf("GetLastUserTurnID after display-only: %v", err)
	} else if tid != 8 {
		t.Fatalf("GetLastUserTurnID = %d, want 8 (display-only rows skipped)", tid)
	}

	// A legacy user row without turn_id (pre-turn-id data) resolves to 0 —
	// the caller falls back to allocating a fresh turn id.
	u4 := llm.NewUserMessage("legacy row")
	u4.TurnID = 0
	if _, err := svc.AppendMessage(tenantID, u4); err != nil {
		t.Fatal(err)
	}
	if tid, err := svc.GetLastUserTurnID(tenantID); err != nil {
		t.Fatalf("GetLastUserTurnID after legacy row: %v", err)
	} else if tid != 0 {
		t.Fatalf("GetLastUserTurnID = %d, want 0 (legacy row has no turn_id)", tid)
	}
}

// TestGetMaxIterationForTurn verifies the resumed Run's iteration-number
// continuation query: the new Run must start at max+1 so iteration_history
// rows for the SAME turn stay contiguous (no (turn_id, iteration) collision
// with the interrupted Run's records).
func TestGetMaxIterationForTurn(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)

	if got, err := svc.GetMaxIterationForTurn(tenantID, 7); err != nil {
		t.Fatalf("GetMaxIterationForTurn on empty: %v", err)
	} else if got != 0 {
		t.Fatalf("GetMaxIterationForTurn on empty = %d, want 0", got)
	}

	// Interrupted Run persisted iterations 1..2 for turn 7.
	for i := 1; i <= 2; i++ {
		if err := svc.AppendIterationHistory(tenantID, 0, 7, IterationRecord{
			MessageID: 0, TurnID: 7, Iteration: i, Content: "iter",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := svc.GetMaxIterationForTurn(tenantID, 7); err != nil {
		t.Fatalf("GetMaxIterationForTurn: %v", err)
	} else if got != 2 {
		t.Fatalf("GetMaxIterationForTurn = %d, want 2 (the resumed Run must continue at 3)", got)
	}

	// A different turn's records must not leak in.
	if err := svc.AppendIterationHistory(tenantID, 0, 9, IterationRecord{
		MessageID: 0, TurnID: 9, Iteration: 30, Content: "other turn",
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := svc.GetMaxIterationForTurn(tenantID, 7); err != nil {
		t.Fatalf("GetMaxIterationForTurn after other-turn insert: %v", err)
	} else if got != 2 {
		t.Fatalf("GetMaxIterationForTurn = %d, want 2 (other turns excluded)", got)
	}
}
