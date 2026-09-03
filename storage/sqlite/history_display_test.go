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

// TestGetHistoryBeforeForDisplay_WindowEquivalence guards the windowed
// implementation (replayForDisplayWindow) against the full-scan fold oracle:
// for every (beforeID, limit) combination the windowed result must equal the
// full ReplayForDisplay + in-memory slicing the old path used. This is the
// behavior-preservation contract for the O(window) rewrite of a formerly
// O(entire-table) loadMore path.
func TestGetHistoryBeforeForDisplay_WindowEquivalence(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)

	// 10 pre-compression messages + compress marker + 8 post-compression
	// messages + a second compress marker + 4 tail messages, with a
	// display-only message interleaved (fold must skip it).
	for i := 0; i < 10; i++ {
		svc.AppendMessage(tenantID, llm.NewUserMessage("old msg"))
	}
	if _, err := svc.AppendContextSnapshot(tenantID, HistoryRecordCompress, []llm.ChatMessage{
		{Role: "user", Content: "[Compacted context]\n\nSummary 1"},
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		svc.AppendMessage(tenantID, llm.NewUserMessage("mid msg"))
	}
	if _, err := svc.AppendContextSnapshot(tenantID, HistoryRecordCompress, []llm.ChatMessage{
		{Role: "user", Content: "[Compacted context]\n\nSummary 2"},
	}); err != nil {
		t.Fatal(err)
	}
	svc.AppendMessage(tenantID, llm.ChatMessage{Role: "user", Content: "display-only", DisplayOnly: true})
	for i := 0; i < 4; i++ {
		svc.AppendMessage(tenantID, llm.NewUserMessage("tail msg"))
	}

	// Oracle: the full fold (pre-rewrite behavior) sliced in memory.
	full, err := svc.ReplayForDisplay(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	oracle := func(beforeID int64, limit int) ([]llm.ChatMessage, int) {
		msgs := full.Messages
		if beforeID > 0 {
			cut := len(msgs)
			for i, m := range msgs {
				if m.ID >= beforeID {
					cut = i
					break
				}
			}
			msgs = msgs[:cut]
		}
		total := len(msgs)
		if len(msgs) > limit {
			msgs = msgs[len(msgs)-limit:]
		}
		return msgs, total
	}

	// Message ids present in the fold (skip markers whose ID equals the
	// compress record id — markers ARE part of the fold and can serve as
	// beforeID boundaries too, but pick real message ids plus boundaries
	// around them for a thorough sweep).
	var ids []int64
	for _, m := range full.Messages {
		ids = append(ids, m.ID)
	}

	limitCases := []int{3, 5, 100}
	for _, beforeID := range ids {
		for _, limit := range limitCases {
			wantMsgs, wantTotal := oracle(beforeID, limit)
			gotMsgs, gotTotal, err := svc.GetHistoryBeforeForDisplay(tenantID, beforeID, limit)
			if err != nil {
				t.Fatalf("beforeID=%d limit=%d: %v", beforeID, limit, err)
			}
			if gotTotal != wantTotal {
				t.Fatalf("beforeID=%d limit=%d: total want %d, got %d", beforeID, limit, wantTotal, gotTotal)
			}
			if len(gotMsgs) != len(wantMsgs) {
				t.Fatalf("beforeID=%d limit=%d: len want %d, got %d", beforeID, limit, len(wantMsgs), len(gotMsgs))
			}
			for i := range wantMsgs {
				if gotMsgs[i].ID != wantMsgs[i].ID || gotMsgs[i].Content != wantMsgs[i].Content {
					t.Fatalf("beforeID=%d limit=%d: row %d want (id=%d %q), got (id=%d %q)",
						beforeID, limit, i, wantMsgs[i].ID, wantMsgs[i].Content, gotMsgs[i].ID, gotMsgs[i].Content)
				}
			}
		}
	}
}

// TestGetHistoryBeforeForDisplayBelowWindowMarker verifies the strict window
// semantics (2026-09-03 user directive, chat_F64D4096DA6F): a compress record
// BELOW the display window does NOT surface its marker — the marker renders
// only when the user pages to its compression point. The earlier below-window
// prepend was reverted: it rendered the marker at the TOP of the window (a
// position unrelated to its actual compression point — "为什么压缩 turn 还没
// 加载你就渲染 [Compacted context] 了"), and across loadMore batches the
// prepend + fold double-sourcing of the SAME compress record caused markers
// to stack at the top and dedup-disappear.
func TestGetHistoryBeforeForDisplayBelowWindowMarker(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)

	// Pre-compression messages.
	svc.AppendMessage(tenantID, llm.NewUserMessage("old user 1"))
	svc.AppendMessage(tenantID, llm.NewAssistantMessage("old answer 1"))

	// Compress record.
	if _, err := svc.AppendContextSnapshot(tenantID, HistoryRecordCompress, []llm.ChatMessage{
		{Role: "user", Content: "[Compacted context]\n\nSummary"},
		{Role: "user", Content: "This conversation was compacted."},
	}); err != nil {
		t.Fatal(err)
	}

	// Post-compression messages: enough that a small window excludes the
	// compress record entirely.
	for i := 0; i < 6; i++ {
		svc.AppendMessage(tenantID, llm.NewUserMessage("post user"))
		svc.AppendMessage(tenantID, llm.NewAssistantMessage("post answer"))
	}

	// Window of 6 (compress record below the boundary): NO marker — strict
	// window semantics. The marker appears only when the user pages to the
	// compression point.
	msgs, _, err := svc.GetHistoryBeforeForDisplay(tenantID, 0, 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 6 {
		t.Fatalf("strict window: exactly 6 rows (no fabricated marker), got %d: %+v", len(msgs), msgs)
	}
	for i, m := range msgs {
		if strings.HasPrefix(m.Content, "[Compacted context]") {
			t.Fatalf("below-window marker must NOT render (row %d) — the compression point is outside the loaded window", i)
		}
	}

	// Wide window (compress record inside): the fold emits the marker at its
	// stream position — exactly one.
	wide, _, err := svc.GetHistoryBeforeForDisplay(tenantID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	markerCount := 0
	for _, m := range wide {
		if strings.HasPrefix(m.Content, "[Compacted context]") {
			markerCount++
		}
	}
	if markerCount != 1 {
		t.Fatalf("in-window fold must emit exactly 1 marker, got %d", markerCount)
	}

	// loadMore (beforeID = oldest id of the narrow window): the window now
	// covers the compress record — the fold emits the marker (single source,
	// no double-sourcing). This is the ONLY path that surfaces it.
	var oldestID int64
	for _, m := range msgs {
		oldestID = m.ID
		break
	}
	page2, _, err := svc.GetHistoryBeforeForDisplay(tenantID, oldestID, 100)
	if err != nil {
		t.Fatal(err)
	}
	page2Markers := 0
	for _, m := range page2 {
		if strings.HasPrefix(m.Content, "[Compacted context]") {
			page2Markers++
		}
	}
	if page2Markers != 1 {
		t.Fatalf("page 2 must contain exactly 1 marker (in-window fold, single source), got %d", page2Markers)
	}
}
