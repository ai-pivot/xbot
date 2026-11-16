package sqlite

import (
	"path/filepath"
	"testing"
)

// TestOpen_SharedConnectionPool pins the 2026-10-27 root-cause fix.
//
// 修复前：Open() 每次 openSQLite() 都新建一套连接池。于是
//   - fd 随打开的库数膨胀（生产 2440 个 .db fd vs 磁盘 1358 个库）
//   - 某个短连接 Close 时，SQLite 认为「这一连接集的最后一条连接关掉了」，
//     checkpoint 并 unlink 掉 -wal，而常驻池还持有那个 fd ⇒ 后续写入落进没有
//     目录项的 inode（生产实证 4MB / 1015 帧，2 池共 8 个 fd 指向它）
//   - 关停要逐个关这些多余连接 → 52s
//
// 判据：同一路径的所有 *DB 必须共享同一个 *sql.DB。
func TestOpen_SharedConnectionPool(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")

	a, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	b, err := Open(path)
	if err != nil {
		a.Close()
		t.Fatalf("second Open: %v", err)
	}

	if a.Conn() != b.Conn() {
		t.Error("两次 Open 拿到了不同的 *sql.DB —— 连接池没有共享，fd 会随调用次数膨胀")
	}
	if refs := sharedConnRefs(path); refs != 2 {
		t.Errorf("expected 2 references, got %d", refs)
	}

	// 关掉第一个：池必须还活着（第二个持有者仍在用）。
	if err := a.Close(); err != nil {
		t.Fatalf("Close a: %v", err)
	}
	if refs := sharedConnRefs(path); refs != 1 {
		t.Fatalf("after closing one holder, refs = %d, want 1 — the pool was closed too early, "+
			"which is exactly the unlink-while-in-use bug", refs)
	}
	// 第一个 handle 已关闭，但第二个仍必须可用。
	var n int
	if err := b.Conn().QueryRow("SELECT 1").Scan(&n); err != nil {
		t.Fatalf("the surviving holder can no longer use the shared pool: %v", err)
	}

	// 关掉最后一个：池真正关闭，注册表清空。
	if err := b.Close(); err != nil {
		t.Fatalf("Close b: %v", err)
	}
	if refs := sharedConnRefs(path); refs != 0 {
		t.Errorf("after the last Close, refs = %d, want 0", refs)
	}
}

// TestOpen_CloseIsIdempotent guards the panic-fix prerequisite: calling Close
// twice on the same handle must not double-decrement the shared refcount
// (which would close the pool while another holder is still alive).
func TestOpen_CloseIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idem.db")

	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(path)
	if err != nil {
		a.Close()
		t.Fatal(err)
	}

	if err := a.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if refs := sharedConnRefs(path); refs != 1 {
		t.Fatalf("double Close decremented twice: refs = %d, want 1 "+
			"(b would then be talking to a closed pool)", refs)
	}
	var n int
	if err := b.Conn().QueryRow("SELECT 1").Scan(&n); err != nil {
		t.Fatalf("surviving holder broke after a double Close elsewhere: %v", err)
	}
	_ = b.Close()
}

// TestOpen_MemoryIsNotShared ensures :memory: databases stay independent —
// sharing them would leak rows between tests/sessions.
func TestOpen_MemoryIsNotShared(t *testing.T) {
	a, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if a.Conn() == b.Conn() {
		t.Fatal(":memory: databases must NOT share a pool")
	}
}
