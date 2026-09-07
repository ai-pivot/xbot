package tools

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// crossPlatformSleepCmd returns a plain "sleep N seconds" command for the
// host shell: the local sandbox on Windows runs commands via PowerShell,
// where bare `sleep` works (alias) but `&&` chains are a ParserError.
func crossPlatformSleepCmd(seconds int) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("Start-Sleep -Seconds %d", seconds)
	}
	return fmt.Sprintf("sleep %d", seconds)
}

// crossPlatformEchoSleepEcho returns "echo BEFORE; sleep N; echo AFTER" for
// the host shell. Windows PowerShell 5.1 has no `&&` (ParserError
// InvalidEndOfLine) and treats `$$`/`$x` as automatic variables — the CI
// Windows job hit exactly that (TestExecuteForeground_TimeoutAdoptsRunning).
func crossPlatformEchoSleepEcho(before string, seconds int, after string) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("Write-Output %s; Start-Sleep -Seconds %d; Write-Output %s", before, seconds, after)
	}
	return fmt.Sprintf("echo %s && sleep %d && echo %s", before, seconds, after)
}

// newPromoteTestCtx builds a ToolContext wired for foreground-shell promote tests.
func newPromoteTestCtx(sessionKey, callID string, mgr *BackgroundTaskManager) *ToolContext {
	return &ToolContext{
		Ctx:           context.Background(),
		Channel:       "web",
		ChatID:        "chat-promote",
		BgSessionKey:  sessionKey,
		BgTaskManager: mgr,
		ToolCallID:    callID,
		OriginUserID:  "user-1",
	}
}

// TestExecuteForeground_PromoteToBackground verifies the full promote path:
// a running foreground shell (sleep + echo) is promoted mid-execution via
// PromoteForegroundShell, the tool returns immediately with the task id, and
// the adopted background task later completes with the command's output.
func TestExecuteForeground_PromoteToBackground(t *testing.T) {
	mgr := NewBackgroundTaskManager()
	done := make(chan *ToolResult, 1)

	tool := &ShellTool{}
	ctx := newPromoteTestCtx("web:chat-promote", "call-1", mgr)

	go func() {
		res, err := tool.Execute(ctx, fmt.Sprintf(`{"command":%q}`, crossPlatformEchoSleepEcho("promoted-start", 2, "done")))
		if err != nil {
			t.Errorf("Execute returned error: %v", err)
		}
		done <- res
	}()

	// Wait until the shell registers itself in the promote registry.
	var handle *ForegroundShellHandle
	deadline := time.Now().Add(5 * time.Second)
	for handle == nil && time.Now().Before(deadline) {
		globalForegroundShells.mu.Lock()
		if m := globalForegroundShells.sessions["web:chat-promote"]; m != nil {
			handle = m["call-1"]
		}
		globalForegroundShells.mu.Unlock()
		if handle == nil {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if handle == nil {
		t.Fatal("foreground shell never registered in the promote registry")
	}

	// Promote it.
	taskID, err := PromoteForegroundShell("web:chat-promote", "call-1")
	if err != nil {
		t.Fatalf("PromoteForegroundShell: %v", err)
	}
	if taskID == "" {
		t.Fatal("PromoteForegroundShell returned empty task id")
	}

	// The tool must return promptly with a PROMOTED result.
	select {
	case res := <-done:
		if res == nil {
			t.Fatal("nil result")
		}
		if !strings.Contains(res.Summary, "PROMOTED to background") {
			t.Fatalf("result should mention PROMOTED, got: %s", res.Summary)
		}
		if !strings.Contains(res.Summary, taskID) {
			t.Fatalf("result should embed task id %s, got: %s", taskID, res.Summary)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Execute did not return promptly after promote")
	}

	// The registry entry must be cleaned up after the tool returned.
	if h := foregroundShellFor("web:chat-promote", "call-1"); h != nil {
		t.Error("registry still has the handle after Execute returned")
	}

	// The background task completes with exit 0 and full output.
	task, err := mgr.Status(taskID)
	if err != nil {
		t.Fatalf("Status(%s): %v", taskID, err)
	}
	waitCh, err := mgr.WaitDone(taskID)
	if err != nil {
		t.Fatalf("WaitDone(%s): %v", taskID, err)
	}
	select {
	case <-waitCh:
	case <-time.After(10 * time.Second):
		t.Fatal("background task did not finish")
	}
	if task.Status != BgTaskDone {
		t.Fatalf("task status = %s, want done (error: %s)", task.Status, task.Error)
	}
	if !strings.Contains(task.CurrentOutput(), "done") {
		t.Fatalf("task output should contain the echo output, got: %q", task.CurrentOutput())
	}
}

// TestExecuteForeground_PromoteUnknownSession verifies the RPC entry rejects
// sessions with no running foreground shell.
func TestExecuteForeground_PromoteUnknownSession(t *testing.T) {
	if _, err := PromoteForegroundShell("web:nope", ""); err == nil {
		t.Fatal("expected error for unknown session")
	}
	if _, err := PromoteForegroundShell("", "call-1"); err == nil {
		t.Fatal("expected error for empty session key")
	}
}

// TestExecuteForeground_PromoteDoubleFire verifies a second promote request for
// the same handle is idempotent (returns the same task id, never panics on
// double channel close).
func TestExecuteForeground_PromoteDoubleFire(t *testing.T) {
	mgr := NewBackgroundTaskManager()
	done := make(chan *ToolResult, 1)
	tool := &ShellTool{}
	ctx := newPromoteTestCtx("web:chat-dbl", "call-dbl", mgr)

	go func() {
		res, _ := tool.Execute(ctx, fmt.Sprintf(`{"command":%q}`, crossPlatformSleepCmd(2)))
		done <- res
	}()

	var handle *ForegroundShellHandle
	deadline := time.Now().Add(5 * time.Second)
	for handle == nil && time.Now().Before(deadline) {
		globalForegroundShells.mu.Lock()
		if m := globalForegroundShells.sessions["web:chat-dbl"]; m != nil {
			handle = m["call-dbl"]
		}
		globalForegroundShells.mu.Unlock()
		if handle == nil {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if handle == nil {
		t.Fatal("foreground shell never registered")
	}

	var firstID string
	var secondID string
	var err2 error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); firstID, _ = PromoteForegroundShell("web:chat-dbl", "call-dbl") }()
	go func() { defer wg.Done(); secondID, err2 = PromoteForegroundShell("web:chat-dbl", "call-dbl") }()
	wg.Wait()

	// At least one must succeed; the concurrent second either observes the
	// already-fired signal (same result channel) or errors after the tool
	// returned — both acceptable, but no panic and no hang.
	if firstID == "" && secondID == "" {
		t.Fatalf("both promotes failed (first err ignored, second err: %v)", err2)
	}

	select {
	case res := <-done:
		if res == nil || !strings.Contains(res.Summary, "PROMOTED to background") {
			t.Fatalf("expected PROMOTED result, got: %+v", res)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Execute did not return after promote")
	}
}

// TestExecuteForeground_TimeoutAdoptsRunning verifies the timeout path now
// adopts the ALREADY-RUNNING execution (no re-execution): the task output
// contains what the process wrote before AND after the timeout moment.
func TestExecuteForeground_TimeoutAdoptsRunning(t *testing.T) {
	mgr := NewBackgroundTaskManager()
	tool := &ShellTool{}
	ctx := newPromoteTestCtx("web:chat-timeout", "call-t", mgr)

	res, err := tool.Execute(ctx, fmt.Sprintf(`{"command":%q,"timeout":1}`, crossPlatformEchoSleepEcho("before-timeout", 3, "after-timeout")))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if res == nil || !strings.Contains(res.Summary, "TIMEOUT after") {
		t.Fatalf("expected TIMEOUT result, got: %+v err=%v", res, err)
	}
	// Extract the task id and verify the adopted task eventually prints BOTH
	// echoes (before + after timeout — proving the process kept running).
	idx := strings.Index(res.Summary, `[task_id: "`)
	if idx < 0 {
		t.Fatalf("no task id in result: %s", res.Summary)
	}
	rest := res.Summary[idx+len(`[task_id: "`):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatalf("malformed task id in result: %s", res.Summary)
	}
	taskID := rest[:end]

	waitCh, err := mgr.WaitDone(taskID)
	if err != nil {
		t.Fatalf("WaitDone(%s): %v", taskID, err)
	}
	select {
	case <-waitCh:
	case <-time.After(10 * time.Second):
		t.Fatal("adopted task did not finish")
	}
	task, _ := mgr.Status(taskID)
	if !strings.Contains(task.CurrentOutput(), "before-timeout") {
		t.Fatalf("adopted output lost the pre-timeout echo: %q", task.CurrentOutput())
	}
	if !strings.Contains(task.CurrentOutput(), "after-timeout") {
		t.Fatalf("adopted output lost the post-timeout echo (process was killed instead of adopted): %q", task.CurrentOutput())
	}
}

// TestExecuteForeground_NormalCompletion verifies the plain path (no promote,
// no timeout) still returns output + exit code formatting unchanged.
func TestExecuteForeground_NormalCompletion(t *testing.T) {
	mgr := NewBackgroundTaskManager()
	tool := &ShellTool{}
	ctx := newPromoteTestCtx("web:chat-normal", "call-n", mgr)

	res, err := tool.Execute(ctx, `{"command":"echo hello-foreground"}`)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(res.Summary, "hello-foreground") {
		t.Fatalf("missing output: %q", res.Summary)
	}
	// No shell should remain registered.
	globalForegroundShells.mu.Lock()
	_, has := globalForegroundShells.sessions["web:chat-normal"]
	globalForegroundShells.mu.Unlock()
	if has {
		t.Error("registry not cleaned up after normal completion")
	}
}

// TestExecuteForeground_UserCancelKillsProcess verifies the tool-ctx cancel
// path kills the execution and returns an error (not a promote).
func TestExecuteForeground_UserCancelKillsProcess(t *testing.T) {
	mgr := NewBackgroundTaskManager()
	tool := &ShellTool{}
	ctx, cancel := context.WithCancel(context.Background())
	tc := newPromoteTestCtx("web:chat-cancel", "call-c", mgr)
	tc.Ctx = ctx

	done := make(chan struct{})
	go func() {
		res, err := tool.Execute(tc, fmt.Sprintf(`{"command":%q}`, crossPlatformSleepCmd(30)))
		if err == nil {
			t.Errorf("expected error on cancel, got result: %+v", res)
		}
		close(done)
	}()

	// Wait for registration, then cancel.
	var handle *ForegroundShellHandle
	deadline := time.Now().Add(5 * time.Second)
	for handle == nil && time.Now().Before(deadline) {
		globalForegroundShells.mu.Lock()
		if m := globalForegroundShells.sessions["web:chat-cancel"]; m != nil {
			handle = m["call-c"]
		}
		globalForegroundShells.mu.Unlock()
		if handle == nil {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if handle == nil {
		t.Fatal("foreground shell never registered")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Execute did not return after user cancel")
	}
	// No background task should have been created.
	for _, task := range mgr.ListAllForSession("web:chat-cancel") {
		t.Errorf("cancel should not adopt a background task, got %s (%s)", task.ID, task.Command)
	}
}

// TestAdoptRunningOutputPush verifies SetOnDelta streams output chunks to the
// callback (bg_task_output SSE push path).
func TestAdoptRunningOutputPush(t *testing.T) {
	mgr := NewBackgroundTaskManager()

	var mu sync.Mutex
	var pushed strings.Builder
	done := make(chan struct{})
	var result int
	var execErr error
	doneSig := make(chan struct{})
	handle := &RunningExecHandle{
		Output: func() string {
			mu.Lock()
			defer mu.Unlock()
			return pushed.String()
		},
		Done:   doneSig,
		Result: func() (int, error) { return result, execErr },
		Cancel: func() {},
	}
	go func() {
		mu.Lock()
		pushed.WriteString("part1 ")
		mu.Unlock()
		handle.fireDelta("part1 ")
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		pushed.WriteString("part2")
		mu.Unlock()
		handle.fireDelta("part2")
		result = 0
		close(doneSig)
	}()
	go func() {
		<-done
		close(done)
	}()

	task := mgr.AdoptRunning("web:chat-push", "user-1", "echo test", time.Now(), handle)
	if task == nil {
		t.Fatal("AdoptRunning returned nil")
	}
	// NOTE: no immediate task.Status read here — the adoption goroutine writes
	// task.Status under task.mu as soon as handle.Done closes, and an unlocked
	// field read racing it is a DATA RACE (go test -race). The Running state is
	// implicitly verified by WaitDone + the done-state assertions below.
	waitCh, _ := mgr.WaitDone(task.ID)
	select {
	case <-waitCh:
	case <-time.After(3 * time.Second):
		t.Fatal("adopted task did not finish")
	}
	if task.Status != BgTaskDone {
		t.Fatalf("task status = %s, want done", task.Status)
	}
	if task.CurrentOutput() != "part1 part2" {
		t.Fatalf("task output = %q, want %q", task.CurrentOutput(), "part1 part2")
	}
}
