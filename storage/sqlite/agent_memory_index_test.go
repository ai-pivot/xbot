package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureAgentMemorySchema_TenantIndexesExist pins the 2026-10-27 schema
// drift finding: production had idx_xbot_stm_tenant / idx_xbot_ltm_tenant
// (added by hand during some incident) but the code's agentMemorySchema never
// declared them. Any fresh deployment was therefore measurably slower than
// production, and nothing would ever report the difference.
//
// The check is EXPLAIN-based, not name-based: an index that exists but is not
// usable is just as broken as one that is missing.
func TestEnsureAgentMemorySchema_TenantIndexesExist(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "agent-mem-idx.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := NewAgentMemoryService(db).EnsureAgentMemorySchema(ctx); err != nil {
		t.Fatalf("EnsureAgentMemorySchema: %v", err)
	}

	// Both indexes must be declared.
	for _, want := range []string{"idx_xbot_stm_tenant", "idx_xbot_ltm_tenant"} {
		var n int
		if err := db.Conn().QueryRowContext(ctx,
			"SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?", want).Scan(&n); err != nil {
			t.Fatalf("query %s: %v", want, err)
		}
		if n != 1 {
			t.Fatalf("%s is missing from a freshly created database — this is exactly the "+
				"schema drift that made new deployments slower than production", want)
		}
	}

	// And they must actually be used for tenant_id lookups.
	for _, tc := range []struct{ table, idx string }{
		{"xbot_short_term_memories", "idx_xbot_stm_tenant"},
		{"xbot_long_term_memories", "idx_xbot_ltm_tenant"},
	} {
		rows, err := db.Conn().QueryContext(ctx,
			"EXPLAIN QUERY PLAN SELECT count(*) FROM "+tc.table+" WHERE tenant_id = 1")
		if err != nil {
			t.Fatalf("explain %s: %v", tc.table, err)
		}
		var plan strings.Builder
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				rows.Close()
				t.Fatalf("scan plan: %v", err)
			}
			plan.WriteString(detail)
			plan.WriteString("\n")
		}
		rows.Close()
		t.Logf("%s plan:\n%s", tc.table, plan.String())
		if !strings.Contains(plan.String(), tc.idx) {
			t.Errorf("%s: query on tenant_id does not use %s:\n%s", tc.table, tc.idx, plan.String())
		}
	}
}
