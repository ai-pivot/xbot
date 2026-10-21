package sqlite

import (
	"os"
	"path/filepath"
	"testing"
)

// persist_wal_test.go — 回归 2026-10-21 的"孤儿 WAL"事故。
//
// 事故链：serve 持有 xbot.db-wal 的 fd 期间，另一个短命进程打开同一库并正常
// 关闭 → SQLite 认为自己是最后一个连接 → checkpoint + unlink -wal → serve 之后
// 一直往已删除的 inode 追加（实测 4.3MB），任何新连接都读不到这批数据，且两边
// checkpoint 争抢同一批页会复现 2026-10-21 17:17 的页级损坏。
//
// 修复：openSQLite 对池内每个物理连接打开 SQLITE_FCNTL_PERSIST_WAL，关闭时保留
// -wal。本测试断言"关闭后 -wal 仍在"。

func TestPersistWALKeepsWALAfterClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.db")

	db, err := OpenSessionDB(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Conn().Exec("CREATE TABLE x(a INTEGER)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Conn().Exec("INSERT INTO x VALUES (1)"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Fatalf("-wal disappeared after close → PERSIST_WAL not in effect: %v", err)
	}
}

// TestPersistWALMemoryDBAllowed 确保 :memory: 路径不会因为 file control 不支持而失败。
func TestPersistWALMemoryDBAllowed(t *testing.T) {
	db, err := OpenSessionDB(":memory:")
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	if _, err := db.Conn().Exec("CREATE TABLE y(a INTEGER)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
