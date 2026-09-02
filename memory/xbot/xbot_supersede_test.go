package xbot

import (
	"context"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// PR-4: supersede chain — stale-memory fix.
//
// The old behavior: adding an updated fact ("cluster moved to Y") when an old
// one exists ("cluster at X") DOUBLE-STORED both — AddMemory has no dedup and
// no invalidation. Recall then injected BOTH (the stale one first — BM25
// ranks it higher on exact keyword overlap), and the model saw contradictory
// facts with no way to tell which was current ("注入错误或者过期的记忆").
//
// New behavior: an explicit AddMemory with strong keyword overlap marks the
// old ACTIVE entries superseded_by=<new id> (rollback preserved in DB, rows
// never deleted). Recall / SearchMemories / ListMemories filter superseded
// entries — only current facts are visible.
// ---------------------------------------------------------------------------

// TestAddMemorySupersedesStaleMatch — explicit re-add of an updated fact must
// supersede (not double-store) the strong-matching old entry.
func TestAddMemorySupersedesStaleMatch(t *testing.T) {
	m, db := newTestMemory(t)
	// Filler corpus for realistic BM25 IDF.
	for i := 0; i < 8; i++ {
		if err := m.addLongTermMemory(LongTermMemory{
			Type:     "fact",
			Content:  strings.Repeat("filler unrelated content ", i+1),
			Keywords: "filleruniquetokens" + strings.Repeat("z", i+1),
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Old fact (global, active). 6 shared keywords — mirrors the dedup test's
	// calibration (6-token overlap reaches bm25 < -6.0 on a 9-row corpus;
	// 3 tokens only reach ≈ -4.2 and would NOT fire the supersede threshold).
	oldKeywords := "gpu cluster address location b300 deploy"
	if _, err := m.AddMemory(context.Background(), LongTermMemory{
		Type:     "fact",
		Content:  "user's gpu cluster is at 8.222.11.182",
		Keywords: oldKeywords,
	}); err != nil {
		t.Fatal(err)
	}

	// Updated fact (same domain — strong keyword overlap).
	newID, err := m.AddMemory(context.Background(), LongTermMemory{
		Type:     "fact",
		Content:  "user's gpu cluster moved to 38.255.28.6",
		Keywords: oldKeywords,
	})
	if err != nil {
		t.Fatal(err)
	}

	// The old entry must be marked superseded by the new one.
	var oldSupersededBy any
	if err := db.QueryRow(`SELECT superseded_by FROM xbot_long_term_memories WHERE content LIKE '%8.222.11.182%'`).Scan(&oldSupersededBy); err != nil {
		t.Fatalf("old entry not found: %v", err)
	}
	if oldSupersededBy == nil {
		t.Errorf("BUG REPRODUCED: stale entry not superseded (superseded_by IS NULL) — double-store: both the old and the new fact are active, Recall injects contradictory facts")
	}
	if got, ok := oldSupersededBy.(int64); ok && got != newID {
		t.Errorf("superseded_by = %d, want the new entry id %d", got, newID)
	}

	// Recall injects ONLY the current fact (superseded filtered at query level).
	out, err := m.Recall(context.Background(), "gpu cluster", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "8.222.11.182") {
		t.Errorf("BUG REPRODUCED: superseded (stale) fact still injected by Recall:\n%s", out)
	}
	if !strings.Contains(out, "moved to 38.255.28.6") {
		t.Errorf("current fact not injected by Recall:\n%s", out)
	}

	// SearchMemories (tool path) also filters superseded.
	entries, err := m.SearchMemories(context.Background(), "gpu cluster", "fact", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Content, "8.222.11.182") {
			t.Errorf("BUG REPRODUCED: superseded fact returned by SearchMemories: %+v", e)
		}
	}
}

// TestSessionScopedAutoExtractionDoesNotSupersede — the supersede chain only
// applies to EXPLICIT global adds (the model deliberately updating a fact).
// Session-scoped auto-extraction (ConsolidateTurn) keeps its dedup-skip
// behavior and NEVER supersedes global entries (task-local state must not
// invalidate user-level facts).
func TestSessionScopedAutoExtractionDoesNotSupersede(t *testing.T) {
	m, db := newTestMemory(t)
	for i := 0; i < 8; i++ {
		if err := m.addLongTermMemory(LongTermMemory{
			Type:     "fact",
			Content:  strings.Repeat("filler unrelated content ", i+1),
			Keywords: "filleruniquetokens" + strings.Repeat("z", i+1),
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Active global fact (6 shared keywords — strong-match territory).
	sharedKeywords := "gpu cluster address location b300 deploy"
	if _, err := m.AddMemory(context.Background(), LongTermMemory{
		Type:     "fact",
		Content:  "user's gpu cluster is at 8.222.11.182",
		Keywords: sharedKeywords,
	}); err != nil {
		t.Fatal(err)
	}

	// Session-scoped auto write with the SAME keywords (auto-extraction path
	// via addLongTermMemory, scope='session') — must NOT supersede the global
	// (even at strong-match territory: task-local state must not invalidate
	// user-level facts).
	if err := m.addLongTermMemory(LongTermMemory{
		Type:     "fact",
		Content:  "session says cluster moved to 38.255.28.6",
		Keywords: sharedKeywords,
		Scope:    "session",
	}); err != nil {
		t.Fatal(err)
	}

	var supersededBy any
	if err := db.QueryRow(`SELECT superseded_by FROM xbot_long_term_memories WHERE content LIKE '%8.222.11.182%'`).Scan(&supersededBy); err != nil {
		t.Fatal(err)
	}
	if supersededBy != nil {
		t.Errorf("session-scoped auto-extraction must NOT supersede global entries (task-local state invalidating user-level facts):\n%v", supersededBy)
	}
}
