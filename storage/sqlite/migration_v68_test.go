package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// openRaw builds a *DB around a hand-crafted SQLite file WITHOUT running
// createSchema/migrations — the fixture already represents a v67 database, and
// Open() would try to (re)create the schema on it.
func openRaw(t *testing.T, dbPath string) (*DB, func()) {
	t.Helper()
	raw, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	return &DB{conn: raw, path: dbPath}, func() { _ = raw.Close() }
}

// createV67ConcurrencyFixture builds a v67 DB whose user_settings holds the SAME
// setting under three different channels — the "one datum running around"
// layout the v68 migration must collapse into a single canonical row.
//
// Real production data before the fix (2026-09-17):
//
//	(web, cli_user, max_concurrency, 125)  ← Web LLM console wrote here
//	(cli, cli_user, max_concurrency, 125)  ← CLI settings panel wrote here
//	('',  cli_user, max_concurrency, 100)  ← legacy writer
//
// plus config.json agent.max_concurrency=7 and AGENT_MAX_CONCURRENCY, each read
// by a different code path — so the panel showed one number while the LLM gate
// silently ran at llm.DefaultLLMConcurrency.
func createV67ConcurrencyFixture(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "v67.db")
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`
		CREATE TABLE schema_version (version INTEGER PRIMARY KEY);
		INSERT INTO schema_version (version) VALUES (67);

		CREATE TABLE user_settings (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			channel    TEXT NOT NULL,
			sender_id  TEXT NOT NULL,
			key        TEXT NOT NULL,
			value      TEXT NOT NULL DEFAULT '',
			updated_at INTEGER NOT NULL,
			UNIQUE(channel, sender_id, key)
		);

		INSERT INTO user_settings (channel, sender_id, key, value, updated_at) VALUES
			('cli', 'cli_user', 'max_concurrency', '125', 3000),
			('web', 'cli_user', 'max_concurrency', '125', 4000),
			('',    'cli_user', 'max_concurrency', '100', 1000),
			('cli', 'cli_user', 'thinking_mode',   'think-max', 1500);
	`); err != nil {
		t.Fatalf("build v67 fixture: %v", err)
	}
	return dbPath
}

// TestMigrateV67ToV68_ConsolidatesConcurrencyRows is the regression test for the
// user report "set 100 concurrency, still stalls at 4-5 subagents": old data must
// be unified so exactly ONE row carries the knob.
func TestMigrateV67ToV68_ConsolidatesConcurrencyRows(t *testing.T) {
	db, closeDB := openRaw(t, createV67ConcurrencyFixture(t))
	defer closeDB()
	if err := migrateV67ToV68(db); err != nil {
		t.Fatalf("migrateV67ToV68: %v", err)
	}

	rows, err := db.Conn().Query(
		`SELECT channel, sender_id, value FROM user_settings WHERE key = 'max_concurrency' ORDER BY channel`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	type row struct{ channel, sender, value string }
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.channel, &r.sender, &r.value); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	if len(got) != 1 {
		t.Fatalf("max_concurrency rows = %d (%v), want exactly 1 (canonical channel)", len(got), got)
	}
	if got[0].channel != "cli" || got[0].value != "125" {
		t.Errorf("canonical row = %+v, want {channel:cli value:125}", got[0])
	}

	// Unrelated settings must be untouched.
	var thinking string
	if err := db.Conn().QueryRow(
		`SELECT value FROM user_settings WHERE key = 'thinking_mode'`).Scan(&thinking); err != nil {
		t.Fatalf("thinking_mode: %v", err)
	}
	if thinking != "think-max" {
		t.Errorf("thinking_mode = %q, want think-max (migration must not touch other keys)", thinking)
	}

	// Idempotent: a second run leaves exactly one row.
	if err := migrateV67ToV68(db); err != nil {
		t.Fatalf("second migrateV67ToV68: %v", err)
	}
	var n int
	if err := db.Conn().QueryRow(
		`SELECT COUNT(*) FROM user_settings WHERE key = 'max_concurrency'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("rows after re-run = %d, want 1", n)
	}
}

// TestMigrateV67ToV68_PromotesNewestWhenCanonicalMissing covers the case where
// the canonical row was never written (e.g. only the Web console set it): the
// newest value must be promoted to the canonical channel, never dropped.
func TestMigrateV67ToV68_PromotesNewestWhenCanonicalMissing(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "v67-only-web.db")
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.Exec(`
		CREATE TABLE schema_version (version INTEGER PRIMARY KEY);
		INSERT INTO schema_version (version) VALUES (67);
		CREATE TABLE user_settings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			channel TEXT NOT NULL, sender_id TEXT NOT NULL, key TEXT NOT NULL,
			value TEXT NOT NULL DEFAULT '', updated_at INTEGER NOT NULL,
			UNIQUE(channel, sender_id, key)
		);
		INSERT INTO user_settings (channel, sender_id, key, value, updated_at) VALUES
			('web', 'cli_user', 'max_concurrency', '88', 5000),
			('',    'cli_user', 'max_concurrency', '42', 1000);
	`); err != nil {
		_ = raw.Close()
		t.Fatalf("build fixture: %v", err)
	}
	_ = raw.Close()

	db, closeDB := openRaw(t, dbPath)
	defer closeDB()
	if err := migrateV67ToV68(db); err != nil {
		t.Fatalf("migrateV67ToV68: %v", err)
	}

	var channel, value string
	if err := db.Conn().QueryRow(
		`SELECT channel, value FROM user_settings WHERE key = 'max_concurrency'`).Scan(&channel, &value); err != nil {
		t.Fatalf("query: %v", err)
	}
	if channel != "cli" || value != "88" {
		t.Errorf("promoted row = {channel:%q value:%q}, want {cli 88} (newest value wins)", channel, value)
	}
}
