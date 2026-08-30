package sqlite

import (
	"database/sql"
	"testing"
)

// TestV61LookupPathIndexes verifies the v61 indexes: idx_cron_jobs_user
// (ListJobsByUserID lookup, backing the web cron panel) and idx_sm_tenant_role_id
// (ListUserChats preview subquery). Both the fresh-schema DDL and the migration
// path must create them (AGENTS.md "three-way sync" rule: schema.go DDL +
// schema_version + migrations.go).
func TestV61LookupPathIndexes(t *testing.T) {
	// Path 1: fresh schema (createSchema carries both indexes in the v61 DDL).
	db := openTestDB(t)

	assertIndex := func(t *testing.T, q interface {
		QueryRow(query string, args ...any) *sql.Row
	}, name, wantPartial string) {
		t.Helper()
		var ddl string
		err := q.QueryRow("SELECT sql FROM sqlite_master WHERE type='index' AND name=?", name).Scan(&ddl)
		if err != nil {
			t.Fatalf("index %s missing (fresh schema): %v", name, err)
		}
		if wantPartial != "" && !containsSubstring(ddl, wantPartial) {
			t.Errorf("index %s is not the expected partial index: %s", name, ddl)
		}
	}

	assertIndex(t, db.Conn(), "idx_cron_jobs_user", "")
	assertIndex(t, db.Conn(), "idx_sm_tenant_role_id", "role IN ('user','assistant')")

	// Path 2: v60 → v61 migration on a legacy DB (fixture pinned at v60,
	// with the tenants table so initSchema takes the migration path).
	dbPath := t.TempDir() + "/test_v61_migration.db"
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
		CREATE TABLE cron_jobs (
			id TEXT PRIMARY KEY, message TEXT NOT NULL, channel TEXT NOT NULL,
			chat_id TEXT NOT NULL, sender_id TEXT NOT NULL DEFAULT '', cron_expr TEXT,
			every_seconds INTEGER DEFAULT 0, delay_seconds INTEGER DEFAULT 0, at TEXT,
			created_at DATETIME NOT NULL, next_run DATETIME NOT NULL, last_trigger DATETIME,
			one_shot INTEGER NOT NULL DEFAULT 0, user_id INTEGER DEFAULT 0
		);
		CREATE TABLE session_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT, tenant_id INTEGER NOT NULL,
			role TEXT NOT NULL, content TEXT NOT NULL
		);
		-- Tables the v62 migration touches (system-subscription removal +
		-- reference cleanup). Every real v60 DB has them; the fixture pins only
		-- what earlier migrations needed, so they are pinned here explicitly.
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
		INSERT INTO schema_version (version) VALUES (60);
	`); err != nil {
		t.Fatalf("seed v60 schema: %v", err)
	}
	raw.Close()

	migDB, err := Open(dbPath)
	if err != nil {
		t.Fatalf("re-open v60 db (runs migration chain): %v", err)
	}
	defer migDB.Close()

	var version int
	if err := migDB.Conn().QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %d, want %d (current schemaVersion)", version, schemaVersion)
	}
	assertIndex(t, migDB.Conn(), "idx_cron_jobs_user", "")
	assertIndex(t, migDB.Conn(), "idx_sm_tenant_role_id", "role IN ('user','assistant')")

	// Idempotency: re-running the migration body must not error.
	if err := migrateV60ToV61(migDB.Conn()); err != nil {
		t.Fatalf("re-run migrateV60ToV61 must be idempotent: %v", err)
	}
}
