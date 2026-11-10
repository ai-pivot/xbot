package serverapp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestServerLockRejectsSecondProcess 守住 2026-10-21 主库损坏事故的设计意图：
// 同一份配置（同一个 XBOT_HOME）只允许一个 serve 进程。两个版本不同、语义
// 不同的进程共用同一个 SQLite 库会写坏结构（malformed 11）。
func TestServerLockRejectsSecondProcess(t *testing.T) {
	home := t.TempDir()

	lock, err := AcquireServerLock(home)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer lock.Release()

	// 子进程尝试拿同一把锁，必须失败且错误里带 "already running"。
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperChildServerLock", "-test.v")
	cmd.Env = append(os.Environ(), "XBOT_LOCK_TEST_HOME="+home)
	out, runErr := cmd.CombinedOutput()
	if runErr == nil {
		t.Fatalf("child must fail to acquire the server lock; output:\n%s", out)
	}
	if !strings.Contains(string(out), "already running") {
		t.Fatalf("child error must mention 'already running'; output:\n%s", out)
	}
	// 报错里要带持锁者 pid，便于运维定位。
	if !strings.Contains(string(out), "pid ") {
		t.Fatalf("child error must include the holder pid; output:\n%s", out)
	}
}

// TestHelperChildServerLock 只被子进程使用：它期望拿锁失败。
func TestHelperChildServerLock(t *testing.T) {
	home := os.Getenv("XBOT_LOCK_TEST_HOME")
	if home == "" {
		t.Skip("helper: only runs as a child process")
	}
	l, err := AcquireServerLock(home)
	if err != nil {
		t.Fatalf("expected failure: %v", err)
	}
	l.Release()
	t.Fatal("expected AcquireServerLock to fail in the second process, but it succeeded")
}

// TestServerLockReleaseAllowsRestart 保证 Release 之后能重新拿锁（正常重启路径）。
func TestServerLockReleaseAllowsRestart(t *testing.T) {
	home := t.TempDir()

	first, err := AcquireServerLock(home)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if _, err := os.Stat(first.Path()); err != nil {
		t.Fatalf("lock file must exist: %v", err)
	}
	first.Release()

	second, err := AcquireServerLock(home)
	if err != nil {
		t.Fatalf("re-acquire after Release must succeed: %v", err)
	}
	second.Release()
}

// TestServerLockEmptyHomeRejected 空 home 直接拒绝，避免在 cwd 乱写锁文件。
func TestServerLockEmptyHomeRejected(t *testing.T) {
	if _, err := AcquireServerLock(""); err == nil {
		t.Fatal("empty home must be rejected")
	}
}

// TestServerLockRecordsOwnerPid 锁文件里要记录持锁者 pid。
func TestServerLockRecordsOwnerPid(t *testing.T) {
	home := filepath.Join(t.TempDir(), "xbothome")
	lock, err := AcquireServerLock(home)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer lock.Release()

	b, err := os.ReadFile(lock.Path())
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	want := strings.TrimSpace(string(b))
	if want == "" {
		t.Fatal("lock file must contain the holder pid")
	}
	if want != itoa(os.Getpid()) {
		t.Fatalf("lock holder = %q, want %d", want, os.Getpid())
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
