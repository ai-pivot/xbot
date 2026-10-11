package tools

import (
	"bytes"
	"context"
	"os"
	"runtime/pprof"
	"strings"
	"testing"
	"time"
)

// goroutineProfileCount counts goroutines whose stack (including the
// "created by" line) contains the given substring. debug=2 includes
// "created by" frames so leaked watcher goroutines are attributable.
func goroutineProfileCount(substr string) int {
	p := pprof.Lookup("goroutine")
	var buf bytes.Buffer
	//nolint:errcheck // WriteTo to bytes.Buffer cannot fail
	p.WriteTo(&buf, 2)
	return strings.Count(buf.String(), substr)
}

// waitForGoroutineCount polls until the goroutine-profile count for substr
// drops to want (goroutine exit is asynchronous; allow a short grace period),
// then reports the final count.
func waitForGoroutineCount(substr string, want int, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	count := goroutineProfileCount(substr)
	for count > want && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		count = goroutineProfileCount(substr)
	}
	return count
}

// TestNoneSandboxExecDoesNotLeakWatchCtx reproduces the watchCtx goroutine
// leak (production dump 2026-10-03: 4085 leaked goroutines blocked on
// "os/exec.(*Cmd).watchCtx" chan send). Root cause: Exec builds the command
// with managedCtx=true (exec.CommandContext), which starts the internal
// watchCtx goroutine. watchCtx's ONLY exit path is a successful send on the
// result channel, which happens exclusively inside cmd.Wait(). But the
// none-sandbox paths intentionally call cmd.Process.Wait() to avoid the
// io.Copy EOF hang — so cmd.Wait() is NEVER called and watchCtx blocks
// forever on the send. Every Shell tool call in none-sandbox mode leaked one
// goroutine (plus the os pipe FDs held by the exec.Cmd). The fix builds with
// managedCtx=false (plain exec.Command — no watcher goroutine at all) and
// kills the process group on context cancellation explicitly.
func TestNoneSandboxExecDoesNotLeakWatchCtx(t *testing.T) {
	if os.Geteuid() == 0 && os.Getenv("XBOT_TEST_ALLOW_ROOT") == "" {
		t.Skip("running as root: shell wrappers behave differently")
	}
	s := &NoneSandbox{}

	// A fast, successful command: completes before any ctx cancellation,
	// then Exec returns and the WithTimeout-derived ctx is cancelled via
	// defer cancel(). Pre-fix, the leaked watchCtx sits on chan send forever.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := s.Exec(ctx, ExecSpec{Shell: true, Command: "true"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}

	// The watcher goroutine (if any) must be gone shortly after Exec returns.
	if n := waitForGoroutineCount("watchCtx", 0, 2*time.Second); n != 0 {
		t.Fatalf("os/exec watchCtx goroutine leaked: %d still alive after Exec returned", n)
	}
}

// TestNoneSandboxExecTimeoutStillKills is the semantic guard for the leak fix:
// with managedCtx=false, Exec must still kill the process group on timeout
// (previously this relied on exec.CommandContext's internal watcher).
func TestNoneSandboxExecTimeoutStillKills(t *testing.T) {
	if os.Geteuid() == 0 && os.Getenv("XBOT_TEST_ALLOW_ROOT") == "" {
		t.Skip("running as root: shell wrappers behave differently")
	}
	s := &NoneSandbox{}

	start := time.Now()
	res, err := s.Exec(context.Background(), ExecSpec{
		Shell:   true,
		Command: "sleep 30",
		Timeout: 300 * time.Millisecond,
	})
	elapsed := time.Since(start)

	if err == nil && res.ExitCode == 0 {
		t.Fatalf("sleep 30 exited 0 with 300ms timeout — timeout kill broken (elapsed=%v)", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("timeout not enforced: elapsed=%v, want <5s", elapsed)
	}
	if !res.TimedOut {
		t.Fatalf("TimedOut = false, want true (timeout kill must report TimedOut)")
	}
	if n := waitForGoroutineCount("watchCtx", 0, 2*time.Second); n != 0 {
		t.Fatalf("os/exec watchCtx goroutine leaked after timeout path: %d", n)
	}
}

// TestNoneSandboxExecAsyncKillerExits reproduces the killer-goroutine leak
// (production dump: 30 goroutines blocked on <-ctx.Done() inside
// noneSandboxExecAsync). The killer only waits on ctx.Done(); when the
// command completes normally and the caller's ctx is never cancelled, the
// killer never exits. The fix adds a process-exited channel so the killer
// exits when the command exits, regardless of ctx lifetime.
func TestNoneSandboxExecAsyncKillerExits(t *testing.T) {
	if os.Geteuid() == 0 && os.Getenv("XBOT_TEST_ALLOW_ROOT") == "" {
		t.Skip("running as root: shell wrappers behave differently")
	}
	ctx := context.Background() // never cancelled — the worst case for the killer
	code, err := noneSandboxExecAsync(ctx, ExecSpec{Shell: true, Command: "true"}, nil)
	if err != nil {
		t.Fatalf("noneSandboxExecAsync: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	// Both the async watcher (watchCtx) and the killer goroutine must exit
	// once the command finishes. The killer's frame shows the enclosing
	// function name, so it is attributable via "noneSandboxExecAsync".
	if n := waitForGoroutineCount("noneSandboxExecAsync", 0, 2*time.Second); n != 0 {
		t.Fatalf("noneSandboxExecAsync goroutines still alive after command exit: %d", n)
	}
}

// TestNoneSandboxExecAsyncStillKillsOnCancel is the semantic guard for the
// async leak fix: cancellation must still kill the process group.
func TestNoneSandboxExecAsyncStillKillsOnCancel(t *testing.T) {
	if os.Geteuid() == 0 && os.Getenv("XBOT_TEST_ALLOW_ROOT") == "" {
		t.Skip("running as root: shell wrappers behave differently")
	}
	ctx, cancel := context.WithCancel(context.Background())
	var output strings.Builder
	done := make(chan struct{})
	var code int
	go func() {
		defer close(done)
		var err error
		code, err = noneSandboxExecAsync(ctx, ExecSpec{Shell: true, Command: "sleep 30"}, func(string) {})
		if err != nil {
			code = -1
		}
	}()

	time.Sleep(300 * time.Millisecond) // let the sleep start
	start := time.Now()
	cancel()
	<-done
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("cancel did not kill the process group: took %v", elapsed)
	}
	if code == 0 {
		t.Fatalf("cancelled sleep 30 exited 0 — process group kill broken")
	}
	_ = output
}
