package xbot

import (
	"database/sql"
	"strings"
	"testing"

	"xbot/llm"
	"xbot/memory"
)

// openRawTestDB opens an in-memory SQLite DB WITHOUT running the provider's
// initSchema/migration — for testing migrations against pre-existing schemas.
func openRawTestDB(t *testing.T) (*sql.DB, error) {
	t.Helper()
	return sql.Open("sqlite", ":memory:")
}

// ---------------------------------------------------------------------------
// PR-3: session isolation — the xbot memory provider (single shared instance,
// single operator) injected OTHER sessions' content into unrelated
// conversations:
//
//   1. "## Recent Sessions" (Recall's query-anchored searchShortTerm) matched
//      ANY session's short-term summary — another session's [Compacted
//      context] summary got injected into conversations that never saw it
//      (live evidence: the GLM-5.2 GPU-tuning session's compaction summary
//      appeared in an unrelated design conversation).
//   2. Auto-extracted long-term memories (ConsolidateTurn/PreCompress) went
//      straight into the GLOBAL pool — session-local task state polluted
//      every other session's injection.
// ---------------------------------------------------------------------------

// TestRecallNoCrossSessionInjection — Recall must never inject other sessions'
// content: the "## Recent Sessions" section is REMOVED (cross-session recall
// goes through memory_search on demand), and long-term injection is
// global-scope only.
//
// RED (pre-PR-3): "## Recent Sessions" injects the other session's summary
// when the query matches (query-anchored searchShortTerm has no session
// filter).
func TestRecallNoCrossSessionInjection(t *testing.T) {
	m, _ := newTestMemory(t)

	// Another session's short-term summary — the cross-session pollution
	// source (a different session's compaction/task summary).
	if err := m.addShortTermMemory("另一个会话的任务：GLM-5.2 MoL TPOT 调优，decode radix L1 命中 87-99%", "GLM,TPOT,调优", "session-other"); err != nil {
		t.Fatal(err)
	}
	// A GLOBAL long-term memory — must still be injected (user-level fact).
	if _, err := m.AddMemory(t.Context(), LongTermMemory{
		Type:       "fact",
		Content:    "user prefers dark theme",
		Keywords:   "theme,preference",
		Importance: 0.9,
	}); err != nil {
		t.Fatal(err)
	}

	out, err := m.Recall(t.Context(), "GLM TPOT 调优 theme preference")
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(out, "## Recent Sessions") {
		t.Errorf("BUG REPRODUCED: cross-session short-term injection still present — \"## Recent Sessions\" matched another session's content (the live evidence: an unrelated session's [Compacted context] summary injected into a design conversation)\n%q", truncateForLog(out, 400))
	}
	if strings.Contains(out, "另一个会话的任务") {
		t.Errorf("BUG REPRODUCED: other session's short-term summary injected into this session's Recall output:\n%q", truncateForLog(out, 400))
	}
	if !strings.Contains(out, "user prefers dark theme") {
		t.Error("global long-term memory must still be injected")
	}
}

// TestConsolidateTurnWritesSessionScope — auto-extracted memories
// (ConsolidateTurn) must be stored with scope='session': session-local task
// state never pollutes the global pool. The global pool is written ONLY by
// explicit memory_add (default scope='global').
func TestConsolidateTurnWritesSessionScope(t *testing.T) {
	m, db := newTestMemory(t)

	toolArgs := `{"memories":[{"type":"fact","content":"gpu cluster is at 8.222.11.182","keywords":"gpu,cluster","importance":0.8}]}`
	mock := &streamModeLLM{streamEvents: []llm.StreamEvent{
		{Type: llm.EventToolCall, ToolCall: &llm.ToolCallDelta{Index: 0, ID: "tc1", Name: "extract_memories", Arguments: toolArgs}},
		{Type: llm.EventDone, FinishReason: llm.FinishReasonToolCalls},
	}}
	if _, err := m.ConsolidateTurn(t.Context(), memory.MemorizeInput{
		Messages:         []llm.ChatMessage{llm.NewUserMessage("we deployed the gpu cluster at 8.222.11.182")},
		LastConsolidated: 0,
		LLMClient:        mock,
		Model:            "test",
	}); err != nil {
		t.Fatal(err)
	}

	var scope string
	if err := db.QueryRow(`SELECT scope FROM xbot_long_term_memories WHERE content LIKE '%gpu cluster%'`).Scan(&scope); err != nil {
		t.Fatalf("auto-extracted memory not found: %v", err)
	}
	if scope != "session" {
		t.Errorf("auto-extracted memory scope = %q, want \"session\" (ConsolidateTurn extraction must be session-scoped — global pool is explicit-add only)", scope)
	}

	// And the auto-injection path (Recall) must NOT inject it — global only.
	out, err := m.Recall(t.Context(), "gpu cluster")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "8.222.11.182") {
		t.Errorf("BUG REPRODUCED: session-scoped auto-extracted memory leaked into Recall injection:\n%q", truncateForLog(out, 400))
	}
}

// TestRecallTimestampsInjected — global long-term injection carries created_at
// so the model can judge staleness itself (superseded/old entries are visible
// as old, not silently presented as current).
func TestRecallTimestampsInjected(t *testing.T) {
	m, _ := newTestMemory(t)
	if _, err := m.AddMemory(t.Context(), LongTermMemory{
		Type:       "fact",
		Content:    "user runs the b300 gpu cluster",
		Keywords:   "gpu,b300",
		Importance: 0.9,
	}); err != nil {
		t.Fatal(err)
	}
	out, err := m.Recall(t.Context(), "gpu b300 cluster")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "user runs the b300 gpu cluster") {
		t.Fatal("global long-term memory not injected — test setup broken")
	}
	// The injection line must carry a date marker (e.g. "2026-09-02" or the
	// created_at column value) — model-side staleness judgment needs it.
	if !strings.Contains(out, "20") {
		t.Errorf("injected memory line must carry created_at for staleness judgment, got:\n%q", truncateForLog(out, 300))
	}
}

// TestScopeMigrationAddsColumns — an existing DB (pre-PR-3 schema, no scope
// column) is migrated in place: scope lands with DEFAULT 'global' (existing
// rows keep their behavior — they were global) and superseded_by lands NULL.
func TestScopeMigrationAddsColumns(t *testing.T) {
	db, err := openRawTestDB(t)
	if err != nil {
		t.Fatal(err)
	}
	// Create the OLD schema (no scope / superseded_by columns).
	if _, err := db.Exec(`CREATE TABLE xbot_long_term_memories (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL DEFAULT 0,
		tenant_id INTEGER NOT NULL DEFAULT 0,
		type TEXT NOT NULL,
		content TEXT NOT NULL,
		keywords TEXT,
		tags TEXT,
		source_session TEXT,
		importance REAL DEFAULT 0.5,
		heat_score REAL DEFAULT 1.0,
		access_count INTEGER DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		last_accessed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		file_path TEXT,
		search_text TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO xbot_long_term_memories (user_id, tenant_id, type, content, keywords) VALUES (42, 7, 'fact', 'legacy memory', 'legacy')`); err != nil {
		t.Fatal(err)
	}

	// New() runs initSchema + migrateLegacyTenantData — the ALTER must add
	// the columns idempotently.
	m := New(42, 7, t.TempDir(), db)
	t.Cleanup(func() { _ = m.Close() })

	// Existing row keeps DEFAULT 'global'.
	var scope string
	var supersededBy any
	if err := db.QueryRow(`SELECT scope, superseded_by FROM xbot_long_term_memories WHERE content = 'legacy memory'`).Scan(&scope, &supersededBy); err != nil {
		t.Fatalf("scope/superseded_by columns not migrated: %v", err)
	}
	if scope != "global" {
		t.Errorf("legacy row scope = %q, want \"global\" (existing rows must keep their behavior)", scope)
	}
	if supersededBy != nil {
		t.Errorf("legacy row superseded_by = %v, want NULL", supersededBy)
	}
}
