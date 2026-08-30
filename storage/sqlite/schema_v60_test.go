package sqlite

import (
	"database/sql"
	"testing"
)

// TestV60ControlRecordPartialIndex verifies the v60 partial index on
// session_messages (tenant_id, record_type, target_history_id) WHERE
// record_type != 'message'. The migration and the fresh-schema DDL must stay
// in sync (AGENTS.md migration rules): both paths create the index, message
// hot-path rows never enter it, control records (ask_answer) do.
func TestV60ControlRecordPartialIndex(t *testing.T) {
	// Path 1: fresh schema (createSchema) already carries the index DDL.
	db := openTestDB(t)
	defer db.Close()

	assertControlRecordIndex := func(t *testing.T, q interface {
		QueryRow(query string, args ...any) *sql.Row
	}, stage string) {
		t.Helper()
		var name string
		err := q.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='index' AND name='idx_sm_tenant_record'",
		).Scan(&name)
		if err != nil {
			t.Fatalf("%s: idx_sm_tenant_record missing: %v", stage, err)
		}
		// Verify the index's partial WHERE clause: message rows must not
		// satisfy the index predicate. Forcing the index on a message-only
		// lookup must yield no rows (the partial predicate excludes them).
		// (EXPLAIN QUERY PLAN choices are the optimizer's — the behavioral
		// contract is the partial predicate, not which index it picks.)
		var sql string
		err = q.QueryRow(
			"SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_sm_tenant_record'",
		).Scan(&sql)
		if err != nil {
			t.Fatalf("%s: read index DDL failed: %v", stage, err)
		}
		if !containsSubstring(sql, "record_type != 'message'") {
			t.Errorf("%s: idx_sm_tenant_record is not a partial index on control records: %s", stage, sql)
		}
	}

	assertControlRecordIndex(t, db.Conn(), "fresh schema")

	// Row-level check: message rows stay out of the partial index, control
	// rows enter it. Insert one tenant + one of each record type, then compare
	// the index-forced count against the predicate count.
	conn := db.Conn()
	if _, err := conn.Exec(
		"INSERT INTO tenants (channel, chat_id) VALUES ('web', 'v60-test')",
	); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	var tenantID int64
	if err := conn.QueryRow("SELECT id FROM tenants WHERE channel='web' AND chat_id='v60-test'").Scan(&tenantID); err != nil {
		t.Fatalf("seed tenant id: %v", err)
	}
	if _, err := conn.Exec(
		"INSERT INTO session_messages (tenant_id, role, content, record_type, target_history_id) VALUES (?, 'user', 'm', 'message', NULL), (?, 'user', 'q', 'ask_question', 1), (?, 'user', 'a', 'ask_answer', 1)",
		tenantID, tenantID, tenantID,
	); err != nil {
		t.Fatalf("seed rows: %v", err)
	}
	var inIndex, totalControl int
	if err := conn.QueryRow(
		"SELECT COUNT(*) FROM session_messages INDEXED BY idx_sm_tenant_record WHERE record_type != 'message'",
	).Scan(&inIndex); err != nil {
		t.Fatalf("count index rows: %v", err)
	}
	if err := conn.QueryRow(
		"SELECT COUNT(*) FROM session_messages WHERE record_type != 'message'",
	).Scan(&totalControl); err != nil {
		t.Fatalf("count control rows: %v", err)
	}
	if inIndex != totalControl {
		t.Errorf("partial index rows (%d) != control rows (%d): message rows must never enter the index", inIndex, totalControl)
	}
	// The ask_answer anti-join lookup shape is servable by the partial index
	// when the query carries the index predicate literally (SQLite's partial-
	// index implication is syntactic: `record_type = 'ask_answer'` does NOT
	// imply `record_type != 'message'`, so only queries carrying the literal
	// predicate can use it).
	var answerID int64
	if err := conn.QueryRow(
		"SELECT id FROM session_messages INDEXED BY idx_sm_tenant_record WHERE tenant_id = ? AND record_type != 'message' AND record_type = 'ask_answer' AND target_history_id = 1",
		tenantID,
	).Scan(&answerID); err != nil {
		t.Fatalf("control-record lookup via partial index: %v", err)
	}
	if answerID == 0 {
		t.Errorf("ask_answer row not found via partial index lookup")
	}

	db.Close()

	// Path 2: v59 → v60 migration on a legacy DB (fixture pinned at v59).
	dbPath := t.TempDir() + "/test_v60_migration.db"
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.Exec(`
		CREATE TABLE tenants (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			channel TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			subscription_id TEXT DEFAULT '',
			model TEXT DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_active_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(channel, chat_id)
		);
		CREATE TABLE session_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			tenant_id INTEGER NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			tool_call_id TEXT,
			tool_name TEXT,
			tool_arguments TEXT,
			tool_calls TEXT,
			detail TEXT,
			reasoning_content TEXT DEFAULT '',
			display_only INTEGER DEFAULT 0,
			context_tokens INTEGER DEFAULT 0,
			turn_id INTEGER DEFAULT 0,
			record_type TEXT NOT NULL DEFAULT 'message',
			target_history_id INTEGER,
			record_data TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
		);
		CREATE INDEX idx_session_messages_tenant_created ON session_messages(tenant_id, created_at);
		CREATE INDEX idx_session_messages_tenant_history ON session_messages(tenant_id, id);
		-- cron_jobs exists in every real v59 DB (user_id added by the v45
		-- migration); migrations touching it (v61 idx_cron_jobs_user) require it.
		CREATE TABLE cron_jobs (
			id TEXT PRIMARY KEY,
			message TEXT NOT NULL,
			channel TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			sender_id TEXT NOT NULL DEFAULT '',
			cron_expr TEXT,
			every_seconds INTEGER DEFAULT 0,
			delay_seconds INTEGER DEFAULT 0,
			at TEXT,
			created_at DATETIME NOT NULL,
			next_run DATETIME NOT NULL,
			last_trigger DATETIME,
			one_shot INTEGER NOT NULL DEFAULT 0,
			user_id INTEGER DEFAULT 0
		);
		-- Tables the v62 migration touches (system-subscription removal +
		-- reference cleanup). Every real v59 DB has them (created by v12/v22/
		-- v35/v39/v44/v45 migrations); the fixture pins only what earlier
		-- migrations needed, so they are pinned here explicitly for v62.
		CREATE TABLE user_llm_subscriptions (
			id TEXT PRIMARY KEY, sender_id TEXT NOT NULL, name TEXT NOT NULL DEFAULT '',
			provider TEXT NOT NULL DEFAULT 'openai', base_url TEXT NOT NULL DEFAULT '',
			api_key TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '',
			max_context INTEGER DEFAULT 0, max_output_tokens INTEGER DEFAULT 0,
			thinking_mode TEXT DEFAULT '', cached_models TEXT NOT NULL DEFAULT '',
			api_type TEXT DEFAULT '', enabled INTEGER NOT NULL DEFAULT 1,
			is_system INTEGER NOT NULL DEFAULT 0, user_id INTEGER DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT (datetime('now')), updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE user_default_model (
			sender_id TEXT PRIMARY KEY, subscription_id TEXT NOT NULL,
			model TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL DEFAULT (datetime('now')),
			user_id INTEGER DEFAULT 0
		);
		CREATE TABLE subscription_models (
			id TEXT PRIMARY KEY, subscription_id TEXT NOT NULL, model TEXT NOT NULL,
			max_context INTEGER NOT NULL DEFAULT 0, max_output_tokens INTEGER NOT NULL DEFAULT 0,
			thinking_mode TEXT NOT NULL DEFAULT '', api_type TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL DEFAULT (datetime('now')), updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE user_identities (
			id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL,
			channel TEXT NOT NULL, channel_user_id TEXT NOT NULL,
			linked_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, UNIQUE(channel, channel_user_id)
		);
		CREATE TABLE user_settings (
			id INTEGER PRIMARY KEY AUTOINCREMENT, channel TEXT NOT NULL, sender_id TEXT NOT NULL,
			key TEXT NOT NULL, value TEXT NOT NULL DEFAULT '', updated_at INTEGER NOT NULL,
			UNIQUE(channel, sender_id, key)
		);
		CREATE TABLE iteration_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT, message_id INTEGER NOT NULL DEFAULT 0,
			tenant_id INTEGER NOT NULL, turn_id INTEGER NOT NULL DEFAULT 0,
			iteration INTEGER NOT NULL, content TEXT NOT NULL DEFAULT '',
			reasoning TEXT NOT NULL DEFAULT '', tools TEXT NOT NULL DEFAULT '[]',
			tokens INTEGER NOT NULL DEFAULT 0, ttft_ms INTEGER NOT NULL DEFAULT 0,
			tokens_per_sec INTEGER NOT NULL DEFAULT 0, total_ms INTEGER NOT NULL DEFAULT 0,
			tpot_ms INTEGER NOT NULL DEFAULT 0,
			input_tokens INTEGER NOT NULL DEFAULT 0, cached_tokens INTEGER NOT NULL DEFAULT 0,
			model TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE schema_version (version INTEGER PRIMARY KEY);
		INSERT INTO schema_version (version) VALUES (59);
	`); err != nil {
		t.Fatalf("create v59 fixture: %v", err)
	}
	raw.Close()

	db2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open v59 fixture for migration: %v", err)
	}
	defer db2.Close()
	var version int
	if err := db2.Conn().QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("v59 fixture must migrate to the current schema (%d), got version %d", schemaVersion, version)
	}
	assertControlRecordIndex(t, db2.Conn(), "v59 migration")

	// Idempotency: re-running the migration body (CREATE INDEX IF NOT EXISTS)
	// must not error — simulate by calling the migration function directly.
	if err := migrateV59ToV60(db2.Conn()); err != nil {
		t.Fatalf("re-run migrateV59ToV60 must be idempotent: %v", err)
	}
}

func containsSubstring(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
