package serverapp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Invariant: the server/RPC layer must never move a session's last_active_at.
//
// Regression (2026-09-11, reported by the user): HistorySnapshot (the web
// /api/history endpoint) and the get_history RPC both called
// TenantService.TouchTenantID while serving READS. The web UI loads history for
// every session it renders, so merely opening or refreshing the page re-stamped
// them all. After a laptop slept overnight, every session from yesterday showed
// up as "active today" in the sidebar — the TODAY/YESTERDAY grouping is derived
// from last_active_at.
//
// last_active_at is bumped in exactly ONE place: agent.processMessage's
// eager-save of a real user message (that is the only "the user actually
// interacted with this session" signal). Keeping the write in the agent also
// keeps the semantic in one place. If a future server-side path genuinely needs
// to move it, route it through the agent (or update this test deliberately with
// a comment explaining why) instead of re-adding a call here.
//
// This guard is intentionally a source scan: the defect is "a read handler
// touches the tenant", which no amount of storage-layer unit testing can catch
// (TenantService.GetOrCreateTenantID is already side-effect free — the bug was
// the explicit TouchTenantID call at the call site).
func TestServerappDoesNotTouchTenantLastActive(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	const forbidden = "TouchTenantID"
	var offenders []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		// Scan CODE only. The comments above intentionally name the forbidden
		// call (they explain the regression), and a naive substring scan would
		// flag them.
		for i, line := range strings.Split(string(body), "\n") {
			code := line
			if idx := strings.Index(code, "//"); idx >= 0 {
				code = code[:idx]
			}
			if strings.Contains(code, forbidden) {
				offenders = append(offenders, fmt.Sprintf("%s:%d", name, i+1))
			}
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("serverapp must not call %s (reading history must not move last_active_at); found at: %v\n"+
			"last_active_at is bumped only by agent.processMessage's user-message eager-save.",
			forbidden, offenders)
	}
}
