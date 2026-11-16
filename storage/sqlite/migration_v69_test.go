package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestMigrateV69ToV70RepairsMissingReasoningItems reproduces the production
// schema created by the v66 migration-version collision: the database has
// already advanced to v68, but session_messages never received reasoning_items.
func TestMigrateV69ToV70RepairsMissingReasoningItems(t *testing.T) {
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
	// v71（每会话一个 DB）之后迁移链继续推进——断言当前 schemaVersion 而非
	// 硬编码 70，后续 bump 不需要再改这里。
	if version != schemaVersion {
		t.Fatalf("schema version = %d, want %d (current schemaVersion)", version, schemaVersion)
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
	// 手工构造的 DB 不在共享连接表里，Close() 走 releaseSharedConn 会直接返回、
	// 不会真正关闭 raw。Windows 上残留的文件句柄会让 t.TempDir 的 RemoveAll 报
	// "The process cannot access the file because it is being used by another
	// process" —— 所以这里必须显式释放。
	defer func() {
		_ = db.Close()
		_ = raw.Close()
	}()

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
