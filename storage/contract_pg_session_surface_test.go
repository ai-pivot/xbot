package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
	"xbot/storage/internal/pg"

	"xbot/llm"
	"xbot/storage/sqlite"
)

// TestPGStorage_SessionStoreSurface exercises every SessionStore method the PG
// backend implements beyond the original 21-method surface — i.e. the full set
// TenantSession calls.
//
// Why this test exists: the interface is satisfied at compile time (the
// `newPGSessionStore` return type is SessionStore), but compile-time checks
// cannot catch an invalid $N placeholder mix-up, a parameter type-inference
// failure, a column typo or a control-row shape drift. Those only fail against
// a real server — which is exactly what the PG backend is for.
//
// DSN-gated like TestPGStorage_Smoke: set XBOT_PG_TEST_DSN.
func TestPGStorage_SessionStoreSurface(t *testing.T) {
	dsn := os.Getenv("XBOT_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("set XBOT_PG_TEST_DSN to run the PG session-store surface test")
	}
	ctx := context.Background()
	store, err := pg.NewStorage(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPGStorage: %v", err)
	}
	defer store.Close()

	tenant := store.Tenant()
	chatID := fmt.Sprintf("chat_PG_SURFACE_%d", time.Now().UnixNano())
	tid, err := tenant.GetOrCreateTenantID("web", chatID)
	if err != nil {
		t.Fatalf("GetOrCreateTenantID: %v", err)
	}
	defer func() { _ = tenant.DeleteTenant(tid) }()

	sess, err := store.Session(tid)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}

	// ── AppendMessage / AppendMessages ──────────────────────────────────
	firstID, err := sess.AppendMessage(tid, llm.NewUserMessage("hello pg"))
	if err != nil || firstID <= 0 {
		t.Fatalf("AppendMessage: id=%d err=%v", firstID, err)
	}
	ids, err := sess.AppendMessages(tid, []llm.ChatMessage{
		llm.NewAssistantMessage("hi"),
		llm.NewUserMessage("next"),
	})
	if err != nil || len(ids) != 2 {
		t.Fatalf("AppendMessages: ids=%v err=%v", ids, err)
	}
	if ids[0] <= firstID || ids[1] <= ids[0] {
		t.Fatalf("AppendMessages ids not monotonic: %v (first=%d)", ids, firstID)
	}

	// ── AppendCommandMessage (display-only, record_type='command') ──────
	cmdID, err := sess.AppendCommandMessage(tid, "user", "!pwd")
	if err != nil || cmdID <= 0 {
		t.Fatalf("AppendCommandMessage: id=%d err=%v", cmdID, err)
	}

	// ── AppendControl: context_edit travels here, atomic types are rejected ──
	ctrlID, err := sess.AppendControl(tid, sqlite.HistoryRecordContextEdit, firstID,
		sqlite.MessageMutations{Mutations: []sqlite.MessageMutation{{TargetHistoryID: firstID}}})
	if err != nil || ctrlID <= 0 {
		t.Fatalf("AppendControl(context_edit): id=%d err=%v", ctrlID, err)
	}
	if _, err := sess.AppendControl(tid, sqlite.HistoryRecordMask, 0, nil); err == nil {
		t.Error("AppendControl(mask) must be rejected — mask owns its atomic append path")
	}

	// ── AppendContextSnapshot (compress snapshot control record) ────────
	snapID, err := sess.AppendContextSnapshot(tid, sqlite.HistoryRecordCompress,
		[]llm.ChatMessage{{Role: "user", ID: firstID, Content: "hello pg"}})
	if err != nil || snapID <= 0 {
		t.Fatalf("AppendContextSnapshot: id=%d err=%v", snapID, err)
	}
	if _, err := sess.AppendContextSnapshot(tid, sqlite.HistoryRecordMask, nil); err == nil {
		t.Error("AppendContextSnapshot(mask) must be rejected")
	}

	// ── AppendMasks is a documented no-op on PG ─────────────────────────
	if err := sess.AppendMasks(tid, []sqlite.MaskMutation{{TargetHistoryID: firstID, Content: "x"}}); err != nil {
		t.Errorf("AppendMasks: %v", err)
	}

	// ── GetFullHistory: row shapes must match SQLite's ──────────────────
	records, err := sess.GetFullHistory(tid)
	if err != nil {
		t.Fatalf("GetFullHistory: %v", err)
	}
	byID := make(map[int64]sqlite.HistoryRecord, len(records))
	for _, rec := range records {
		byID[rec.HistoryID] = rec
	}
	if m, ok := byID[firstID]; !ok || m.Type != sqlite.HistoryRecordMessage || m.Message.Content != "hello pg" || m.Message.DisplayOnly {
		t.Fatalf("GetFullHistory: message row = %+v (found=%v)", m, ok)
	}
	if c, ok := byID[cmdID]; !ok || c.Type != sqlite.HistoryRecordCommand || !c.Message.DisplayOnly || !c.Message.CommandRow {
		t.Fatalf("GetFullHistory: command row = %+v (found=%v)", c, ok)
	}
	if ctrl, ok := byID[ctrlID]; !ok || ctrl.Type != sqlite.HistoryRecordContextEdit || ctrl.TargetHistoryID != firstID {
		t.Fatalf("GetFullHistory: control row = %+v (found=%v)", ctrl, ok)
	}
	if snap, ok := byID[snapID]; !ok || snap.Type != sqlite.HistoryRecordCompress || len(snap.Data) == 0 {
		t.Fatalf("GetFullHistory: snapshot row = %+v (found=%v)", snap, ok)
	}

	// ── Iteration history ───────────────────────────────────────────────
	if err := sess.AppendIterationHistory(tid, firstID, 7, sqlite.IterationRecord{
		TurnID: 7, Iteration: 1, Content: "answer", InputTokens: 100, Model: "m", SubscriptionID: "s",
	}); err != nil {
		t.Fatalf("AppendIterationHistory: %v", err)
	}
	iters, err := sess.GetAllIterationHistory(tid)
	if err != nil {
		t.Fatalf("GetAllIterationHistory: %v", err)
	}
	if len(iters) != 1 || iters[0].TurnID != 7 || iters[0].Iteration != 1 ||
		iters[0].InputTokens != 100 || iters[0].Model != "m" || iters[0].SubscriptionID != "s" {
		t.Fatalf("GetAllIterationHistory = %+v", iters)
	}

	// ── Replay: control + display-only rows never enter the context ─────
	replay, err := sess.Replay(tid)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(replay.Messages) != 3 {
		t.Fatalf("Replay returned %d messages, want the 3 message rows: %+v", len(replay.Messages), replay.Messages)
	}
	for _, m := range replay.Messages {
		if m.Role == "control" || m.DisplayOnly {
			t.Fatalf("Replay leaked a non-context row: %+v", m)
		}
	}

	// ── GetHistoryBeforeForDisplay ──────────────────────────────────────
	display, total, err := sess.GetHistoryBeforeForDisplay(tid, snapID+1, 10)
	if err != nil {
		t.Fatalf("GetHistoryBeforeForDisplay: %v", err)
	}
	if total != len(records) || len(display) == 0 {
		t.Fatalf("GetHistoryBeforeForDisplay: total=%d len(display)=%d (full history rows=%d)", total, len(display), len(records))
	}

	// ── Counts / token watermark ────────────────────────────────────────
	userCount, err := sess.GetUserMessageCount(tid)
	if err != nil || userCount != 2 {
		t.Fatalf("GetUserMessageCount = %d (err=%v), want 2 — the command row is display-only", userCount, err)
	}
	if err := sess.UpdateUserMessageContextTokens(tid, 12345); err != nil {
		t.Fatalf("UpdateUserMessageContextTokens: %v", err)
	}
	tokens, err := sess.GetLastUserMessageContextTokens(tid)
	if err != nil || tokens != 12345 {
		t.Fatalf("GetLastUserMessageContextTokens = %d (err=%v), want 12345", tokens, err)
	}

	// ── UpdateMessageContentNonDisplayOnly (index over the filtered set) ─
	if err := sess.UpdateMessageContentNonDisplayOnly(tid, 1, "hi (edited)"); err != nil {
		t.Fatalf("UpdateMessageContentNonDisplayOnly: %v", err)
	}
	afterEdit, err := sess.GetFullHistory(tid)
	if err != nil {
		t.Fatalf("GetFullHistory (after edit): %v", err)
	}
	if got := recordContent(afterEdit, ids[0]); got != "hi (edited)" {
		t.Fatalf("UpdateMessageContentNonDisplayOnly: content = %q, want %q", got, "hi (edited)")
	}

	// ── GetTenantUsageStats ─────────────────────────────────────────────
	stats, err := sess.GetTenantUsageStats(tid, 5)
	if err != nil {
		t.Fatalf("GetTenantUsageStats: %v", err)
	}
	if stats.IterationCount != 1 || stats.TurnCount != 1 || stats.InputTokens != 100 {
		t.Fatalf("GetTenantUsageStats = %+v, want 1 iteration / 1 turn / 100 input tokens", stats)
	}

	// ── AskUser question/answer pairing ─────────────────────────────────
	askBatch := []llm.ChatMessage{
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "AskUser", Arguments: "{}"}}},
		llm.NewToolMessage("AskUser", "call-1", "{}", `{"question":"pick one"}`),
	}
	askIDs, qID, err := sess.AppendMessagesAndAskQuestion(tid, askBatch, map[string]string{"q": "pick one"})
	if err != nil || len(askIDs) != 2 || qID <= 0 {
		t.Fatalf("AppendMessagesAndAskQuestion: ids=%v q=%d err=%v", askIDs, qID, err)
	}
	if _, err := sess.AppendAskAnswer(tid, "choice A"); err != nil {
		t.Fatalf("AppendAskAnswer: %v", err)
	}
	qID2, err := sess.AppendAskQuestion(tid, map[string]string{"q": "again"})
	if err != nil || qID2 <= 0 {
		t.Fatalf("AppendAskQuestion: id=%d err=%v", qID2, err)
	}
	if _, err := sess.AppendAskAnswerWithUserMessage(tid, "choice B", llm.NewUserMessage("choice B")); err != nil {
		t.Fatalf("AppendAskAnswerWithUserMessage: %v", err)
	}
	// Both questions are answered now ⇒ there is no pending question left.
	if _, err := sess.AppendAskAnswer(tid, "stale"); err == nil {
		t.Error("AppendAskAnswer with no pending question must fail")
	}

	// ── RewindToHistoryID (destructive — must run last) ─────────────────
	// A turn-scoped row so rewind also exercises the iteration_history
	// truncation branch (by turn_id).
	turnMsg := llm.NewUserMessage("turn msg")
	turnMsg.TurnID = 7
	if _, err := sess.AppendMessage(tid, turnMsg); err != nil {
		t.Fatalf("AppendMessage (turn msg): %v", err)
	}
	target, turnIdx, promptTokens, err := sess.RewindToHistoryID(tid, ids[1])
	if err != nil {
		t.Fatalf("RewindToHistoryID: %v", err)
	}
	if target.Content != "next" || target.Role != "user" || turnIdx != 2 {
		t.Fatalf("RewindToHistoryID target = %+v turnIdx=%d, want the 'next' user row as turn 2", target, turnIdx)
	}
	// The truncated-away newest user row carried the 12345 watermark; the last
	// row that survives is the original "hello pg" (never stamped) ⇒ 0.
	if promptTokens != 0 {
		t.Fatalf("RewindToHistoryID promptTokens = %d, want 0 (the surviving last user row has no watermark)", promptTokens)
	}
	remaining, err := sess.GetFullHistory(tid)
	if err != nil {
		t.Fatalf("GetFullHistory (after rewind): %v", err)
	}
	if len(remaining) != 2 {
		t.Fatalf("after rewind %d rows remain, want 2 (firstID + ids[0]): %+v", len(remaining), remaining)
	}
	remainingIters, err := sess.GetAllIterationHistory(tid)
	if err != nil {
		t.Fatalf("GetAllIterationHistory (after rewind): %v", err)
	}
	if len(remainingIters) != 0 {
		t.Fatalf("rewind must truncate iteration_history for the dropped turn, got %+v", remainingIters)
	}
	// Rewinding to a non-user / non-existent row must fail.
	if _, _, _, err := sess.RewindToHistoryID(tid, ids[0]); err == nil {
		t.Error("RewindToHistoryID(assistant row) must fail")
	}
	if _, _, _, err := sess.RewindToHistoryID(tid, 999999999); err == nil {
		t.Error("RewindToHistoryID(unknown history_id) must fail")
	}
}

func recordContent(records []sqlite.HistoryRecord, historyID int64) string {
	for _, rec := range records {
		if rec.HistoryID == historyID {
			return rec.Message.Content
		}
	}
	return ""
}
