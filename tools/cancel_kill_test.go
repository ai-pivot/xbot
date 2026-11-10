package tools

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// cancelKillBudget is the hard bound for "cancel → process group gone".
// User requirement (2026-10-22): ≤100ms, so a cancel never leaves the user
// staring at a shell that is still running.
const cancelKillBudget = 100 * time.Millisecond

func readPid(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pid file %s never appeared", path)
	return 0
}

// killTestPid SIGKILLs a stray process left behind by a failing assertion.
// Portable on purpose (os.Process.Kill → SIGKILL on Unix, TerminateProcess on
// Windows) so this test file needs no per-OS companion.
func killTestPid(pid int) {
	if pid <= 0 {
		return
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}

func pidAlive(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 > len(s) {
		return true
	}
	f := strings.Fields(s[i+1:])
	return len(f) > 0 && f[0] != "Z"
}

func waitPidGone(pid int, budget time.Duration) time.Duration {
	start := time.Now()
	for time.Since(start) < budget {
		if !pidAlive(pid) {
			return time.Since(start)
		}
		time.Sleep(time.Millisecond)
	}
	return -1
}

// TestExecCancelKillsEscapedProcessTreeWithin100ms 覆盖「shell 必须 100ms 内被杀」
// （用户 2026-10-22）最恶劣的形态：命令里有一个**脱离了进程组**的孙进程
// （`setsid`），并且它继承了 stdout/stderr 管道。
//
// 修复前（实测）：kill(-pgid) 打不到它 ⇒ 它继续握着管道写端 ⇒ 采集 goroutine
// 永远等不到 EOF ⇒ noneSandboxExecAsync 的 wg.Wait() 永不返回 ⇒ ShellTool 的
// `<-execDone` 永不返回 ⇒ **cancel 之后 turn 永远结束不了**
// （goroutine dump: none_sandbox.go:532 wg.Wait ← sandboxExecAsync ←
// ShellTool.executeForeground；生产实例实测 turn 30s+ 不结束）。
//
// 变异判别：
//   - 去掉 killProcessTree 里的后代收集（只留 kill(-pid)）⇒ 逃逸孙进程存活、
//     管道不断 ⇒ 本测试在 exec 返回超时处变红；
//   - 去掉 waitForPipeCapture 的强关管道/上限 ⇒ 同上，变红。
func TestExecCancelKillsEscapedProcessTreeWithin100ms(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("linux-only test (reads /proc)")
	}
	dir := t.TempDir()
	mainPidFile := filepath.Join(dir, "main.pid")
	escapedPidFile := filepath.Join(dir, "escaped.pid")

	// The escaped branch is `setsid sh -c …`: it gets its OWN process group and
	// inherits our pipes — exactly the shape that used to wedge the turn.
	script := "setsid sh -c 'echo $$ > " + escapedPidFile + "; exec sleep 9401' & " +
		"echo $$ > " + mainPidFile + "; exec sleep 9402"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	returned := make(chan error, 1)
	go func() {
		_, err := noneSandboxExecAsync(ctx, ExecSpec{Shell: true, Command: script}, nil)
		returned <- err
	}()

	mainPid := readPid(t, mainPidFile)
	escapedPid := readPid(t, escapedPidFile)
	t.Cleanup(func() {
		// Never leave strays behind if the assertions fail.
		killTestPid(escapedPid)
		killTestPid(mainPid)
	})
	if !pidAlive(escapedPid) {
		t.Fatal("escaped grandchild is not running — test setup is wrong")
	}

	cancelledAt := time.Now()
	cancel()

	select {
	case <-returned:
	case <-time.After(3 * time.Second):
		t.Fatalf("exec did not return within 3s of cancel — pipe capture is wedged by the escaped grandchild (pid %d)", escapedPid)
	}
	execReturned := time.Since(cancelledAt)

	mainGone := waitPidGone(mainPid, cancelKillBudget)
	escapedGone := waitPidGone(escapedPid, cancelKillBudget)

	if mainGone < 0 {
		t.Fatalf("shell pid %d still alive %v after cancel (budget %v)", mainPid, cancelKillBudget, cancelKillBudget)
	}
	if escapedGone < 0 {
		t.Fatalf("escaped (setsid) grandchild pid %d survived the cancel — killProcessTree must take down the whole tree, not just the process group", escapedPid)
	}
	if execReturned > cancelKillBudget {
		t.Fatalf("exec returned %v after cancel (budget %v) — the tool call must not outlive the cancel", execReturned, cancelKillBudget)
	}
	t.Logf("cancel→exec-return=%v main-gone=%v escaped-gone=%v", execReturned, mainGone, escapedGone)
}

// TestNoneSandboxExecCancelKillsGroupWithin100ms — 普通形态（bash exec 成 sleep，
// 单进程）：cancel 后进程必须 100ms 内消失、Exec 100ms 内返回。
func TestNoneSandboxExecCancelKillsGroupWithin100ms(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("linux-only test (reads /proc)")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "shell.pid")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := &NoneSandbox{}
	returned := make(chan error, 1)
	go func() {
		_, err := s.Exec(ctx, ExecSpec{Shell: true, Command: "echo $$ > " + pidFile + "; exec sleep 9403"})
		returned <- err
	}()

	pid := readPid(t, pidFile)
	t.Cleanup(func() { killTestPid(pid) })

	cancelledAt := time.Now()
	cancel()

	select {
	case <-returned:
	case <-time.After(3 * time.Second):
		t.Fatalf("Exec did not return within 3s of cancel (pid %d)", pid)
	}
	execReturned := time.Since(cancelledAt)
	gone := waitPidGone(pid, cancelKillBudget)
	if gone < 0 {
		t.Fatalf("shell pid %d still alive %v after cancel", pid, cancelKillBudget)
	}
	if execReturned > cancelKillBudget {
		t.Fatalf("Exec returned %v after cancel (budget %v)", execReturned, cancelKillBudget)
	}
	t.Logf("cancel→exec-return=%v proc-gone=%v", execReturned, gone)
}

// TestWaitForPipeCaptureIsBounded — 采集 goroutine 的等待**必须有上界**：
// 管道写端被一个我们杀不到的外部进程握着时（这里用测试自己持有写端模拟），
// 读端永远不会 EOF。waitForPipeCapture 必须强关管道并在硬上限内返回，
// 否则 shell 工具会永久挂住（这正是「cancel 之后 turn 停不下来」的第二道成因）。
//
// 变异判别：去掉 force-close / 硬上限（退回 `<-done` 无限等）⇒ 本测试在 2s
// 上限处变红。
func TestWaitForPipeCaptureIsBounded(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pw.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 32)
		for {
			if _, err := pr.Read(buf); err != nil {
				return // "file already closed" once the helper force-closes it
			}
		}
	}()
	// Give the reader a moment to block on Read.
	time.Sleep(20 * time.Millisecond)

	done := make(chan struct{})
	start := time.Now()
	go func() {
		waitForPipeCapture(&wg, pr, io.NopCloser(strings.NewReader("")))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("waitForPipeCapture blocked on a pipe that never reaches EOF — it must force-close and bound the wait")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("waitForPipeCapture took %v — bound it (50ms drain + 200ms hard cap)", elapsed)
	}
}
