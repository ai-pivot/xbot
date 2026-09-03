package channel

import (
	"encoding/json"
	"testing"

	"xbot/llm"
)

// TestConvertMessagesToHistory_DerivesAssistantTurnID verifies that
// ConvertMessagesToHistory derives turnID for assistant messages with
// turn_id=0 (legacy data written before handleRunOutput set TurnID).
// Without this, flushPending's pendingTurnID=0 → the tool_summary has
// turnID=0 → the frontend's turnID:role dedup in loadMore can't match it
// against the final assistant (which has the real turnID from the same
// turn's user message derivation). The result: duplicate assistant messages
// at batch boundaries within a super-long turn.
func TestConvertMessagesToHistory_DerivesAssistantTurnID(t *testing.T) {
	// Simulate legacy data: user has turnID=5, but intermediate assistant
	// (with ToolCalls) has turnID=0 (pre-fix handleRunOutput).
	msgs := []llm.ChatMessage{
		{ID: 10, Role: "user", Content: "hello", TurnID: 5},
		{ID: 11, Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "tc1", Name: "Shell", Arguments: "{}"}}, TurnID: 0},
		{ID: 12, Role: "tool", ToolCallID: "tc1", Content: "output"},
		{ID: 13, Role: "user", Content: "next"},
	}
	history := ConvertMessagesToHistory(msgs)

	// flushPending produces a tool_summary for the assistant at ID=11.
	// Its TurnID must be derived from the user message (ID=10, TurnID=5),
	// NOT left at 0.
	var summary *HistoryMessage
	for i := range history {
		if history[i].Role == "assistant" && len(history[i].Iterations) > 0 && history[i].Content == "" {
			summary = &history[i]
			break
		}
	}
	if summary == nil {
		t.Fatal("expected a tool_summary assistant (Content empty, Iterations non-empty) from flushPending")
	}
	if summary.TurnID != 5 {
		t.Fatalf("tool_summary TurnID = %d, want 5 (derived from user message) — "+
			"without this, the frontend's turnID:role dedup in loadMore can't match "+
			"this tool_summary against the final assistant (same turn, different batch)", summary.TurnID)
	}
}

// TestConvertMessagesToHistory_SuperLongTurnBatchBoundary simulates the
// loadMore batch boundary scenario: batch 2 (older) has the turn's beginning
// (user + intermediate assistant), batch 1 (newer) has the turn's end
// (final assistant with Detail). Both must produce HistoryMessages with the
// same turnID:role so the frontend can dedup.
func TestConvertMessagesToHistory_SuperLongTurnBatchBoundary(t *testing.T) {
	detail, err := json.Marshal([]map[string]any{{"iteration": 1, "content": "final reply"}})
	if err != nil {
		t.Fatal(err)
	}

	// Batch 1 (newer, initial load): final assistant with Detail
	batch1 := ConvertMessagesToHistory([]llm.ChatMessage{
		{ID: 200, Role: "assistant", Content: "final reply", Detail: string(detail), TurnID: 5},
	})
	// Batch 2 (older, loadMore): user + intermediate assistant (no Detail)
	batch2 := ConvertMessagesToHistory([]llm.ChatMessage{
		{ID: 100, Role: "user", Content: "hello", TurnID: 5},
		{ID: 101, Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "tc1", Name: "Shell", Arguments: "{}"}}, TurnID: 5},
		{ID: 102, Role: "tool", ToolCallID: "tc1", Content: "output"},
	})

	// Batch 1 should have one assistant with turnID=5
	if len(batch1) != 1 || batch1[0].Role != "assistant" {
		t.Fatalf("batch1: expected 1 assistant, got %+v", batch1)
	}
	if batch1[0].TurnID != 5 {
		t.Fatalf("batch1 assistant TurnID = %d, want 5", batch1[0].TurnID)
	}

	// Batch 2 should have user(5) + assistant(5, tool_summary from flushPending)
	if len(batch2) != 2 {
		t.Fatalf("batch2: expected 2 messages (user + tool_summary), got %d", len(batch2))
	}
	if batch2[0].Role != "user" || batch2[0].TurnID != 5 {
		t.Fatalf("batch2[0]: expected user(5), got %+v", batch2[0])
	}
	if batch2[1].Role != "assistant" {
		t.Fatalf("batch2[1]: expected assistant, got %+v", batch2[1])
	}
	// The tool_summary MUST have turnID=5 so the frontend's turnID:role
	// dedup can match it against batch1's assistant(5) and drop it.
	if batch2[1].TurnID != 5 {
		t.Fatalf("batch2 tool_summary TurnID = %d, want 5 — "+
			"without matching turnID, loadMore's turnID:role dedup fails and "+
			"the same turn's iterations render twice at the batch boundary", batch2[1].TurnID)
	}
}
