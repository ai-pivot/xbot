package sqlite

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureStartupSchema_CreatesTurnIndex pins the index that
// GetSyntheticPairRowsByTurn depends on.
//
// Without idx_sm_tenant_turn, SQLite falls back to
// idx_session_messages_tenant_history (tenant_id, id) and filters every message
// of the tenant — /api/iteration_detail measured p50 1676ms / max 5057ms in
// production on 2026-10-27.
func TestEnsureStartupSchema_CreatesTurnIndex(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "turn-index.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	var n int
	err = db.Conn().QueryRow(
		"SELECT count(*) FROM sqlite_master WHERE type='index' AND name='idx_sm_tenant_turn'",
	).Scan(&n)
	if err != nil {
		t.Fatalf("query index: %v", err)
	}
	if n != 1 {
		t.Fatal("idx_sm_tenant_turn was not created by ensureStartupSchema")
	}

	// The index must actually be used. A "SCAN session_messages" here would mean
	// the index is unusable and the endpoint stays slow.
	rows, err := db.Conn().Query(`
		EXPLAIN QUERY PLAN
		SELECT id, role FROM session_messages
		WHERE tenant_id = 1 AND turn_id = 2 AND record_type = 'message'
		ORDER BY id ASC`)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan.WriteString(detail)
		plan.WriteString("\n")
	}
	t.Logf("query plan:\n%s", plan.String())

	if strings.Contains(plan.String(), "SCAN session_messages") {
		t.Error("query still scans session_messages — idx_sm_tenant_turn is not being used")
	}
	if !strings.Contains(plan.String(), "idx_sm_tenant_turn") {
		t.Errorf("expected the plan to use idx_sm_tenant_turn, got:\n%s", plan.String())
	}
}
