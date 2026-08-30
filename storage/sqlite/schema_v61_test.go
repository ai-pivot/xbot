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
	if version != 61 {
		t.Fatalf("schema version = %d, want 61", version)
	}
	assertIndex(t, migDB.Conn(), "idx_cron_jobs_user", "")
	assertIndex(t, migDB.Conn(), "idx_sm_tenant_role_id", "role IN ('user','assistant')")

	// Idempotency: re-running the migration body must not error.
	if err := migrateV60ToV61(migDB.Conn()); err != nil {
		t.Fatalf("re-run migrateV60ToV61 must be idempotent: %v", err)
	}
}
