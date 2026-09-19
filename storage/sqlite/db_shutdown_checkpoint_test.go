package sqlite

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 2026-09-17 事故面：已 ack 的写入只活在 WAL 里时，进程被 SIGKILL（不 Close）就丢了。
// CheckpointForShutdown 之后，**主库文件单独**（不含 -wal）必须已经包含这些行 ——
// 这正是"停机时把 WAL 赶进主库"要保证的不变量。
func TestCheckpointForShutdown_MakesCommittedDataSurviveWithoutWAL(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "xbot.db")

	db, err := Open(p)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	for _, ch := range []string{"chat_A", "chat_B"} {
		if err := db.AddPendingResume("web", ch, "web-4"); err != nil {
			t.Fatalf("add pending resume %s: %v", ch, err)
		}
	}

	// 此时这些行（连 schema 页）都还在 WAL 里：主库文件单独读 → 0 行。
	before := countPendingResumesInMainFileOnly(t, p)
	if before != 0 {
		t.Logf("precondition note: main file alone already reads %d rows (early checkpoint)", before)
	}

	if err := db.CheckpointForShutdown(); err != nil {
		t.Fatalf("CheckpointForShutdown: %v", err)
	}

	// 模拟"随后被 SIGKILL、WAL 丢失"：只读主库文件本身，必须看得到那 2 行。
	if got := countPendingResumesInMainFileOnly(t, p); got != 2 {
		t.Errorf("REPRO: 主库文件单独读取只有 %d 行, want 2 — 被 SIGKILL 时会丢这些已提交数据", got)
	}

	// 额外证据：TRUNCATE 之后 WAL 应为空（0 字节）或已不存在。
	if fi, statErr := os.Stat(p + "-wal"); statErr == nil && fi.Size() != 0 {
		t.Errorf("checkpoint(TRUNCATE) 后 WAL 仍有 %d 字节, want 0", fi.Size())
	}
}

// countPendingResumesInMainFileOnly 把主库文件【单独】复制出来读（不带 -wal/-shm）。
// 若连表都还不存在（schema 页仍在 WAL 里），按 0 行处理 —— 那正是 checkpoint 之前的状态。
func countPendingResumesInMainFileOnly(t *testing.T, dbPath string) int {
	t.Helper()
	only := filepath.Join(t.TempDir(), "main-only.db")
	b, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read main db: %v", err)
	}
	if err := os.WriteFile(only, b, 0o600); err != nil {
		t.Fatalf("write main-only copy: %v", err)
	}
	conn, err := sql.Open("sqlite", "file:"+only+"?mode=ro&immutable=1")
	if err != nil {
		t.Fatalf("open main-only copy: %v", err)
	}
	defer conn.Close()
	var n int
	if err := conn.QueryRow("SELECT COUNT(*) FROM pending_resumes").Scan(&n); err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return 0
		}
		t.Fatalf("count in main-only copy: %v", err)
	}
	return n
}

// WAL 被 unlink 必须能被识别（"曾被观察到 → 现在路径不存在"）。这是本轮
// "已 ack 的帧不在路径上"的判据；它不依赖 inode，因此不会被文件系统复用 inode 骗过。
func TestWalAnomaly_DetectsDisappearedWAL(t *testing.T) {
	wal := filepath.Join(t.TempDir(), "xbot.db-wal")
	if err := os.WriteFile(wal, []byte("first"), 0o600); err != nil {
		t.Fatalf("create wal: %v", err)
	}
	first, err := os.Stat(wal)
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}

	if got := walAnomaly(nil, true, first); got != "" {
		t.Errorf("首次观测不应报异常, got %q", got)
	}
	if got := walAnomaly(first, true, first); got != "" {
		t.Errorf("同一文件不应报异常, got %q", got)
	}
	// 本轮丢数据的现形：连接还开着，WAL 文件却在路径上消失了
	if got := walAnomaly(first, false, nil); got != "missing" {
		t.Errorf("REPRO: unlink 后未报 missing, got %q — 这条路径漏掉正是本轮丢数据无告警的原因", got)
	}

	// 重建（新文件）—— 若文件系统复用了 inode 则检测不到，属已知 best-effort，
	// 因此这里只记录不断言（"missing" 才是可靠的判据）。
	if err := os.Remove(wal); err != nil {
		t.Fatalf("remove wal: %v", err)
	}
	if err := os.WriteFile(wal, []byte("second"), 0o600); err != nil {
		t.Fatalf("recreate wal: %v", err)
	}
	second, err := os.Stat(wal)
	if err != nil {
		t.Fatalf("stat recreated wal: %v", err)
	}
	t.Logf("walAnomaly(prev, present, recreated) = %q (overlayfs 可能复用 inode)", walAnomaly(first, true, second))
}
