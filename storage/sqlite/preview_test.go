package sqlite

import "testing"

// The final reply text lives in iteration_history; session_messages assistant
// rows are often empty placeholders (tool-only iterations). A session_messages
// based preview returned "" and the push body fell back to "本轮回复已完成".
func TestGetLastAssistantPreviewUsesIterationContent(t *testing.T) {
	db := openTestDB(t)
	conn := db.Conn()

	if _, err := conn.Exec(`INSERT INTO tenants (channel, chat_id) VALUES ('cli', '/preview-test')`); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	var tenantID int64
	if err := conn.QueryRow(`SELECT id FROM tenants WHERE channel='cli' AND chat_id='/preview-test'`).Scan(&tenantID); err != nil {
		t.Fatalf("read tenant: %v", err)
	}

	// The shape that broke the old query: a final assistant row with empty content.
	if _, err := conn.Exec(
		`INSERT INTO session_messages (tenant_id, role, content, tool_calls) VALUES (?, 'assistant', '', '[]')`,
		tenantID); err != nil {
		t.Fatalf("insert assistant row: %v", err)
	}

	// Turn 1 has old text; turn 2's tool-only iteration is empty, its final
	// iteration has the actual reply. Only the latter must be returned.
	for _, row := range []struct {
		turn, iter int
		content    string
	}{
		{1, 1, "old reply"},
		{2, 1, ""},
		{2, 2, "final reply text"},
	} {
		if _, err := conn.Exec(
			`INSERT INTO iteration_history (tenant_id, turn_id, iteration, content) VALUES (?, ?, ?, ?)`,
			tenantID, row.turn, row.iter, row.content); err != nil {
			t.Fatalf("insert iteration: %v", err)
		}
	}

	svc := NewSessionService(db)
	got, err := svc.GetLastAssistantPreview(tenantID, 120)
	if err != nil {
		t.Fatalf("GetLastAssistantPreview: %v", err)
	}
	if got != "final reply text" {
		t.Fatalf("preview = %q, want the latest turn's final iteration", got)
	}

	// A newest turn with no text must NOT fall back to the previous turn.
	if _, err := conn.Exec(
		`INSERT INTO iteration_history (tenant_id, turn_id, iteration, content) VALUES (?, 3, 1, '')`,
		tenantID); err != nil {
		t.Fatalf("insert empty turn: %v", err)
	}
	if got, err := svc.GetLastAssistantPreview(tenantID, 120); err != nil || got != "" {
		t.Fatalf("preview after empty newest turn = %q, %v; want empty, nil", got, err)
	}
}
