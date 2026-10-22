package sqlite

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpenSameProcessIsReferenceCounted 守住「同进程多次 Open 同一个库必须成功」：
// serverapp 的 sharedDB、tokenDB、MigrateMemoryToFiles、MultiTenant 都会各自
// Open 一次主库。Linux 的 flock 是 open-file-description 级，不做引用计数就会
// 让第二次 Open 失败 → 服务起不来。
func TestOpenSameProcessIsReferenceCounted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xbot.db")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	second, err := Open(path)
	if err != nil {
		t.Fatalf("second open in the same process must succeed (reference counted): %v", err)
	}

	// 关闭其中一个，另一个必须仍然可用（引用计数未归零，锁不释放）。
	if err := second.Close(); err != nil {
		t.Fatalf("close second: %v", err)
	}
	if _, qerr := first.conn.Exec(`SELECT 1`); qerr != nil {
		t.Fatalf("first must stay usable until the last close: %v", qerr)
	}

	// 最后一个 Close 之后，锁必须释放，可以重新打开。
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}
	third, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after all closes must succeed: %v", err)
	}
	_ = third.Close()
}

// TestOpenRejectsSecondProcess 守住 2026-10-21 主库损坏事故的根因：
// **两个进程**打开同一个库会互相 checkpoint/unlink WAL，最终写坏结构。
// 子进程必须在这里失败，且错误信息要能定位。
func TestOpenRejectsSecondProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xbot.db")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	defer func() { _ = first.Close() }()

	cmd := exec.Command(os.Args[0], "-test.run=TestHelperChildOpen", "-test.v")
	cmd.Env = append(os.Environ(), "XBOT_LOCK_TEST_DB="+path)
	out, runErr := cmd.CombinedOutput()
	if runErr == nil {
		t.Fatalf("child process must fail to open a locked database; output:\n%s", out)
	}
	if !strings.Contains(string(out), "already in use") {
		t.Fatalf("child error must mention 'already in use'; output:\n%s", out)
	}

	// 生产实例（本进程）必须完全不受影响。
	if _, qerr := first.conn.Exec(`SELECT 1`); qerr != nil {
		t.Fatalf("first database must stay usable after the child was rejected: %v", qerr)
	}
}

// TestHelperChildOpen 只被子进程使用：它期望 Open 失败。
func TestHelperChildOpen(t *testing.T) {
	path := os.Getenv("XBOT_LOCK_TEST_DB")
	if path == "" {
		t.Skip("helper: only runs as a child process")
	}
	db, err := Open(path)
	if err != nil {
		t.Fatalf("expected failure: %v", err)
	}
	_ = db.Close()
	t.Fatal("expected Open to fail in the second process, but it succeeded")
}

// TestMemoryDBDoesNotLock 内存库不参与 flock（每个内存库本来就是独立的）。
func TestMemoryDBDoesNotLock(t *testing.T) {
	a, err := Open(":memory:")
	if err != nil {
		t.Fatalf("first memory open: %v", err)
	}
	b, err := Open(":memory:")
	if err != nil {
		t.Fatalf("second memory open must succeed: %v", err)
	}
	_ = a.Close()
	_ = b.Close()
}

// TestDoubleCloseKeepsLockForOtherHandles — D-01 回归（2026-10-22 门下省实证）。
//
// 判据必须看**子进程的错误内容**，只看 exit code 没有判别力：
// 子进程可能因别的原因失败，那样"删掉 CAS 测试仍绿"。
//   - 子进程报 "already in use" → 锁仍在 → PASS
//   - 子进程报 "expected Open to fail" → 锁被提前放掉 → D-01 复现 → FAIL
func TestDoubleCloseKeepsLockForOtherHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xbot.db")

	a, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	b, err := Open(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}

	_ = a.Close()
	_ = a.Close() // 重复 Close 不得再减引用计数

	cmd := exec.Command(os.Args[0], "-test.run=TestHelperChildOpen", "-test.v")
	cmd.Env = append(os.Environ(), "XBOT_LOCK_TEST_DB="+path)
	out, _ := cmd.CombinedOutput()
	s := string(out)
	t.Logf("child output:\n%s", s)

	if strings.Contains(s, "expected Open to fail in the second process") {
		t.Fatalf("D-01 REPRODUCED: after double Close the lock was released while handle b is still open")
	}
	if !strings.Contains(s, "already in use") {
		t.Fatalf("child was not rejected with 'already in use'; output:\n%s", s)
	}
	_ = b.Close()
}
