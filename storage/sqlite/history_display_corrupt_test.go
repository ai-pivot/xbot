package sqlite

import (
	"testing"

	"xbot/llm"
)

// TestReplayForDisplaySkipsCorruptedCompressRecord (刑部2b) verifies the
// display fold's handling of a corrupted compress record matches
// countDisplayRowsBefore's: a record whose record_data cannot decode into
// ContextSnapshot is SKIPPED (tolerant), not a hard error — one corrupted
// record must not take down the whole history read. Previously
// replayDisplayRecords returned the error while countDisplayRowsBefore
// continued, so the window path and the fold diverged.
func TestReplayForDisplaySkipsCorruptedCompressRecord(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)

	// Normal messages around the corrupted record.
	svc.AppendMessage(tenantID, llm.NewUserMessage("before corruption"))
	svc.AppendMessage(tenantID, llm.NewAssistantMessage("answer before corruption"))
	svc.AppendMessage(tenantID, llm.NewUserMessage("after corruption"))

	// Insert a corrupted compress record directly (bypasses AppendControl's
	// json.Marshal — record_data is invalid JSON, e.g. a torn write).
	if _, err := svc.db.Conn().Exec(`
		INSERT INTO session_messages
		(tenant_id, role, content, display_only, record_type, record_data, created_at)
		VALUES (?, 'control', '', 1, 'compress', ?, ?)
	`, tenantID, `{"messages": [INVALID-JSON`, "2026-08-29T00:00:00Z"); err != nil {
		t.Fatalf("seed corrupted compress record: %v", err)
	}

	svc.AppendMessage(tenantID, llm.NewAssistantMessage("answer after corruption"))

	// Full-history display replay must tolerate the corrupted record.
	res, err := svc.ReplayForDisplay(tenantID)
	if err != nil {
		t.Fatalf("ReplayForDisplay failed on a single corrupted compress record (must skip, not fail): %v", err)
	}
	if len(res.Messages) != 4 {
		t.Errorf("expected 4 display messages around the skipped corrupt record, got %d", len(res.Messages))
	}
}
