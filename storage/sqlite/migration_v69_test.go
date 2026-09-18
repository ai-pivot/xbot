package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestMigrateV68ToV69RepairsMissingReasoningItems reproduces the production
// schema created by the v66 migration-version collision: the database has
// already advanced to v68, but session_messages never received reasoning_items.
func TestMigrateV68ToV69RepairsMissingReasoningItems(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "v68-missing-reasoning-items.db")
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	if _, err := raw.Exec(`
		CREATE TABLE tenants (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			channel TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			UNIQUE(channel, chat_id)
		);
		CREATE TABLE schema_version (version INTEGER PRIMARY KEY);
		INSERT INTO schema_version(version) VALUES (68);
		CREATE TABLE session_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			content TEXT NOT NULL
		);
		INSERT INTO session_messages(content) VALUES ('preserve me');
	`); err != nil {
		_ = raw.Close()
		t.Fatalf("create v68 fixture: %v", err)
	}
	if err := raw.Close(); err != nil {
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
	if version != 69 {
		t.Fatalf("schema version = %d, want 69", version)
	}

	var content, reasoningItems string
	if err := db.Conn().QueryRow(
		"SELECT content, reasoning_items FROM session_messages WHERE id = 1",
	).Scan(&content, &reasoningItems); err != nil {
		t.Fatalf("query repaired session_messages: %v", err)
	}
	if content != "preserve me" {
		t.Fatalf("content = %q, want preserved row", content)
	}
	if reasoningItems != "" {
		t.Fatalf("reasoning_items = %q, want empty default", reasoningItems)
	}
}

func TestMigrateV68ToV69IsIdempotentWhenColumnExists(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "v68-with-reasoning-items.db")
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	if _, err := raw.Exec(`
		CREATE TABLE schema_version (version INTEGER PRIMARY KEY);
		INSERT INTO schema_version(version) VALUES (68);
		CREATE TABLE session_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			reasoning_items TEXT DEFAULT ''
		);
	`); err != nil {
		_ = raw.Close()
		t.Fatalf("create v68 fixture: %v", err)
	}
	db := &DB{conn: raw, path: dbPath}
	defer db.Close()

	if err := migrateV68ToV69(db); err != nil {
		t.Fatalf("migrateV68ToV69: %v", err)
	}
	if err := migrateV68ToV69(db); err != nil {
		t.Fatalf("second migrateV68ToV69: %v", err)
	}

	var version int
	if err := db.Conn().QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != 69 {
		t.Fatalf("schema version = %d, want 69", version)
	}
}
