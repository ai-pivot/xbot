package sqlite

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
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

// TestSecondOpenDoesNotDropPOSIXLockOrAllowExternalWALUnlink 回归 2026-11-18 主库脑裂损坏事故：
//
// 根因链：
//  1. 进程内第一次 sqlite.Open(path) 打开连接池并建立 openPinnedGuard（在 path 与 path-shm 上持有
//     POSIX fcntl 共享读锁，使外部 C-sqlite 关闭时看到 nRef>0 而不 unlink -wal）；
//  2. 同一进程内第二次 sqlite.Open(path)（如启动时的 MigrateMemoryToFiles）在入口处调了
//     verifySQLiteHeader(path) -> os.Open(path) + f.Close()；
//  3. Linux 的 POSIX fcntl 锁是进程级（process-wide）而非 fd 级：关闭同一 inode 上的任意一个 fd
//     会立即清空本进程在该 inode 上持有的**全部** POSIX 锁！
//  4. 随后外部进程（如 python3 sqlite3）打开再关闭该库时，发现无锁，触发 last-close unlink
//     删掉活体 -wal，导致常驻连接池陷入「孤儿 WAL + 新建 WAL」双 WAL 脑裂并最终写坏主库。
func TestSecondOpenDoesNotDropPOSIXLockOrAllowExternalWALUnlink(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	db1, err := Open(path)
	if err != nil {
		t.Fatalf("Open 1: %v", err)
	}
	defer func() { _ = db1.Close() }()

	// 同进程第二次 Open + Close（模拟启动期辅助调用）
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("Open 2: %v", err)
	}
	if err := db2.Close(); err != nil {
		t.Fatalf("Close 2: %v", err)
	}

	// 外部 C-sqlite（python3）以默认读写模式打开并关闭该库：
	// 由于本进程的 POSIX 锁未被第二次 Open 的 verifySQLiteHeader 清掉，
	// 外部进程绝不能 unlink 掉活体 -wal/-shm。
	pyScript := "import sqlite3, sys; c=sqlite3.connect(sys.argv[1]); c.execute('SELECT count(*) FROM tenants').fetchall(); c.close()"
	if out, err := exec.Command("python3", "-c", pyScript, path).CombinedOutput(); err != nil {
		t.Fatalf("python3 connect failed: %v (out=%s)", err, string(out))
	}

	if got := deletedSidecarFDs(t, path); len(got) != 0 {
		t.Fatalf("external sqlite3 close unlinked live sidecar(s) after second Open: %v", got)
	}
	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Fatalf("-wal missing on disk after external close: %v", err)
	}
}

// TestConnectionHookSetsPersistWALOnExpandedPoolConns 验证当并发连接数超过初始预热的
// 4 条（以及超过 MaxIdleConns=8）并在高峰后被连接池回收关闭时，驱动级 ConnectionHook
// 确保每一条动态扩容的物理连接都带有 SQLITE_FCNTL_PERSIST_WAL，回收时绝不 unlink -wal。
func TestConnectionHookSetsPersistWALOnExpandedPoolConns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pool.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	const nConns = 14 // > MaxIdleConns(8) 且 > 旧 enablePersistWAL 的 4
	conns := make([]*sql.Conn, 0, nConns)
	for i := 0; i < nConns; i++ {
		c, err := db.Conn().Conn(ctx)
		if err != nil {
			t.Fatalf("borrow conn %d: %v", i, err)
		}
		var one int
		if err := c.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
			t.Fatalf("query on conn %d: %v", i, err)
		}
		conns = append(conns, c)
	}
	for _, c := range conns {
		_ = c.Close()
	}

	if got := deletedSidecarFDs(t, path); len(got) != 0 {
		t.Fatalf("pool shrink unlinked live sidecar(s): %v", got)
	}
	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Fatalf("-wal missing after pool shrink: %v", err)
	}
}
