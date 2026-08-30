package channel

import (
	"testing"

	"xbot/llm"
	"xbot/storage/sqlite"
)

// TestConvert_SubAgentFinalTurnIDZeroOrder reproduces the SubAgent-session
// rendering bug (user reported 2026-08-30): after a SubAgent completes,
// opening its session shows the USER message BELOW the assistant reply.
//
// Root shape (DB evidence, agent-tenant sessions like
// web:chat_*/explore:*): the SubAgent completion paths
// (interactive.go spawn/send AppendMessage) never stamp TurnID on the
// final assistant message, and the user row (eager-save) carries no TurnID
// either — while the Run's intermediate rows (PersistenceBridge) DO carry
// turn_id=1:
//
//	id=1380156 user      turn=0   (spawn task, eager-save, no TurnID stamp)
//	id=1380170 assistant turn=1   (intermediate, PersistenceBridge stamps)
//	...
//	id=1380462 tool      turn=1
//	id=1380484 assistant turn=0   (final reply, AppendMessage without TurnID!)
//
// deriveTurnIDs Pass 2 (backward, all roles) derives the user row to 1, but
// the final assistant row sits at the END of the slice — the backward scan
// hits it FIRST with lastTurnID=0, so it stays 0. Convert then renders the
// final reply with TurnID=0; the frontend orders rows by (turnID, roleRank)
// and a turnID=0 persisted row is pinned to the TOP (sortKey -1) — the
// reply renders ABOVE the user message that triggered it.
func TestConvert_SubAgentFinalTurnIDZeroOrder(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "explore the subscription integration"},
		{ID: 101, Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "Grep", Arguments: "{}"}}, TurnID: 1},
		{Role: "tool", ToolCallID: "c1", ToolName: "Grep", Content: "found 5 matches", TurnID: 1},
		{ID: 103, Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c2", Name: "Read", Arguments: "{}"}}, TurnID: 1},
		{Role: "tool", ToolCallID: "c2", ToolName: "Read", Content: "file body", TurnID: 1},
		// Final reply — SubAgent AppendMessage path (interactive.go: never
		// stamps TurnID) + Detail JSON (out.IterationHistory, the real shape).
		{ID: 104, Role: "assistant", Content: "探索完成。所有 12 个位置已查清。", TurnID: 0, Detail: `[{"iteration":1,"content":"中间迭代","tools":[]},{"iteration":2,"content":"探索完成。所有 12 个位置已查清。","tools":[]}]`},
	}

	turnIterMap := map[uint64][]sqlite.IterationRecord{
		1: {
			{TurnID: 1, Iteration: 1, Tools: `[{"name":"Grep","status":"done"}]`},
			{TurnID: 1, Iteration: 2, Content: "探索完成。所有 12 个位置已查清。", Tools: "[]"},
		},
	}

	history := ConvertMessagesToHistoryWithIterations(msgs, turnIterMap)

	var userRow, finalRow *HistoryMessage
	for i := range history {
		switch {
		case history[i].Role == "user":
			userRow = &history[i]
		case history[i].Role == "assistant" && history[i].Iterations != nil:
			finalRow = &history[i]
		}
	}
	if userRow == nil || finalRow == nil {
		t.Fatalf("expected user + final assistant rows, got %+v", history)
	}
	// The user row must carry the turn's id (derived) and the final reply must
	// NOT be orphaned at turn 0 — otherwise the frontend (turnID, roleRank)
	// sort pins the reply above/away from its user message.
	if userRow.TurnID != 1 {
		t.Errorf("user row turn = %d, want 1 (derived from following intermediate rows)", userRow.TurnID)
	}
	if finalRow.TurnID != 1 {
		t.Errorf("final assistant row turn = %d, want 1 — turnID=0 orphans the reply (frontend pins it to the TOP, rendering it above the user message that triggered it)", finalRow.TurnID)
	}
	// Row order: user first, then the reply.
	userIdx, finalIdx := -1, -1
	for i, h := range history {
		if h.Role == "user" {
			userIdx = i
		}
		if h.Role == "assistant" && h.Iterations != nil {
			finalIdx = i
		}
	}
	if userIdx > finalIdx {
		t.Errorf("rendered order has user (idx %d) AFTER final assistant (idx %d) — rows: %+v", userIdx, finalIdx, history)
	}
}
