package sqlite

import (
	"database/sql"
	"testing"
)

func TestMigrateV65ToV66RepairsMissingTokenUsageTables(t *testing.T) {
	dbPath := t.TempDir() + "/v65-missing-token-usage.db"
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	if _, err := conn.Exec(`
		CREATE TABLE tenants (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			channel TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			UNIQUE(channel, chat_id)
		);
		CREATE TABLE schema_version (version INTEGER PRIMARY KEY);
		INSERT INTO schema_version(version) VALUES (65);
	`); err != nil {
		conn.Close()
		t.Fatalf("create v65 fixture: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("migrate fixture: %v", err)
	}
	defer db.Close()

	var version int
	if err := db.Conn().QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %d, want %d", version, schemaVersion)
	}

	for _, table := range []string{"user_token_usage", "daily_token_usage"} {
		var name string
		if err := db.Conn().QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name); err != nil {
			t.Fatalf("table %s missing after migration: %v", table, err)
		}
	}

	for _, index := range []string{"idx_daily_token_usage_sender", "idx_daily_token_usage_date"} {
		var name string
		if err := db.Conn().QueryRow(
			"SELECT name FROM sqlite_master WHERE type='index' AND name=?", index,
		).Scan(&name); err != nil {
			t.Fatalf("index %s missing after migration: %v", index, err)
		}
	}
}
