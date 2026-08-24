package sqlite

import (
	"strings"
	"testing"

	"xbot/llm"
)

// TestReplayForDisplayKeepsPreCompressionMessages verifies that the display
// replay shows ALL messages (including pre-compression), unlike Replay()
// which replaces them with the compress snapshot.
//
// Bug: web frontend uses Replay() for history display, which hides
// pre-compression messages even though they're still in the DB
// (session_messages is append-only).
func TestReplayForDisplayKeepsPreCompressionMessages(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)

	// Pre-compression messages.
	user1ID, _ := svc.AppendMessage(tenantID, llm.NewUserMessage("old user 1"))
	_ = user1ID
	svc.AppendMessage(tenantID, llm.NewAssistantMessage("old answer 1"))
	svc.AppendMessage(tenantID, llm.NewUserMessage("old user 2"))

	// Compress record — replaces all prior messages with a summary.
	if _, err := svc.AppendContextSnapshot(tenantID, HistoryRecordCompress, []llm.ChatMessage{
		{Role: "user", Content: "[Compacted context]\n\nSummary of old conversation"},
		{Role: "user", Content: "This conversation was compacted from a longer session."},
	}); err != nil {
		t.Fatal(err)
	}

	// Post-compression messages.
	svc.AppendMessage(tenantID, llm.NewUserMessage("new user"))
	svc.AppendMessage(tenantID, llm.NewAssistantMessage("new answer"))

	// Replay (for LLM context) — should only show summary + new messages.
	replay, err := svc.Replay(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Messages) != 4 {
		t.Fatalf("Replay() should show 4 messages (2 summary + 2 new), got %d: %+v",
			len(replay.Messages), replay.Messages)
	}

	// ReplayForDisplay — should show ALL messages including pre-compression.
	displayReplay, err := svc.ReplayForDisplay(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	// Expected: 3 pre-compression + 1 [Compacted context] marker + 2 post-compression = 6.
	// The instruction message ("This conversation was compacted...") is skipped.
	if len(displayReplay.Messages) != 6 {
		t.Fatalf("ReplayForDisplay() should show 6 messages (3 old + 1 marker + 2 new), got %d: %+v",
			len(displayReplay.Messages), displayReplay.Messages)
	}

	// Verify pre-compression messages are preserved.
	if displayReplay.Messages[0].Content != "old user 1" {
		t.Fatalf("first message should be 'old user 1', got %q", displayReplay.Messages[0].Content)
	}
	if displayReplay.Messages[2].Content != "old user 2" {
		t.Fatalf("third message should be 'old user 2', got %q", displayReplay.Messages[2].Content)
	}

	// Verify [Compacted context] marker is present.
	hasMarker := false
	for _, m := range displayReplay.Messages {
		if strings.HasPrefix(m.Content, "[Compacted context]") {
			hasMarker = true
			break
		}
	}
	if !hasMarker {
		t.Fatal("ReplayForDisplay() should include [Compacted context] marker")
	}

	// Verify post-compression messages are present.
	if displayReplay.Messages[5].Content != "new answer" {
		t.Fatalf("last message should be 'new answer', got %q", displayReplay.Messages[5].Content)
	}
}

// TestGetHistoryBeforeForDisplayPagination verifies that GetHistoryBeforeForDisplay
// returns all messages (including pre-compression) and the correct total count
// for has_more pagination.
func TestGetHistoryBeforeForDisplayPagination(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)

	// Add 10 pre-compression messages.
	for i := 0; i < 10; i++ {
		svc.AppendMessage(tenantID, llm.NewUserMessage("old msg"))
	}

	// Compress.
	if _, err := svc.AppendContextSnapshot(tenantID, HistoryRecordCompress, []llm.ChatMessage{
		{Role: "user", Content: "[Compacted context]\n\nSummary"},
	}); err != nil {
		t.Fatal(err)
	}

	// Add 5 post-compression messages.
	for i := 0; i < 5; i++ {
		svc.AppendMessage(tenantID, llm.NewUserMessage("new msg"))
	}

	// GetHistoryBefore (current, uses Replay) — only sees summary + 5 new = 6.
	replayMsgs, err := svc.GetHistoryBefore(tenantID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayMsgs) != 6 {
		t.Fatalf("GetHistoryBefore (Replay) should return 6, got %d", len(replayMsgs))
	}

	// GetHistoryBeforeForDisplay — sees all 10 + 1 marker + 5 = 16.
	displayMsgs, total, err := svc.GetHistoryBeforeForDisplay(tenantID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if total != 16 {
		t.Fatalf("total should be 16, got %d", total)
	}
	if len(displayMsgs) != 16 {
		t.Fatalf("GetHistoryBeforeForDisplay should return 16, got %d", len(displayMsgs))
	}

	// Verify pagination: limit=10 should return last 10, hasMore=true.
	displayMsgs2, total2, err := svc.GetHistoryBeforeForDisplay(tenantID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total2 != 16 {
		t.Fatalf("total should be 16, got %d", total2)
	}
	if len(displayMsgs2) != 10 {
		t.Fatalf("GetHistoryBeforeForDisplay(limit=10) should return 10, got %d", len(displayMsgs2))
	}
}
