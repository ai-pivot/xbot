package tools

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// ==================== FormatBgTaskCompletion ====================

func TestFormatBgTaskCompletion_Basic(t *testing.T) {
	now := time.Now()
	finished := now.Add(30 * time.Second)
	task := &BackgroundTask{
		ID:         "abc123",
		Command:    "echo hello",
		Status:     BgTaskDone,
		StartedAt:  now,
		FinishedAt: &finished,
		ExitCode:   0,
		Output:     "hello\n",
	}

	result := FormatBgTaskCompletion(task, "")

	if !strings.Contains(result, "abc123") {
		t.Error("missing task ID")
	}
	if !strings.Contains(result, "echo hello") {
		t.Error("missing command")
	}
	if !strings.Contains(result, "done") {
		t.Error("missing status")
	}
	if !strings.Contains(result, "Exit Code: 0") {
		t.Error("missing exit code")
	}
	if !strings.Contains(result, "hello") {
		t.Error("missing output")
	}
}

func TestFormatBgTaskCompletion_WithError(t *testing.T) {
	now := time.Now()
	finished := now.Add(5 * time.Second)
	task := &BackgroundTask{
		ID:         "err1",
		Command:    "false",
		Status:     BgTaskError,
		StartedAt:  now,
		FinishedAt: &finished,
		ExitCode:   1,
		Error:      "exit status 1",
		Output:     "",
	}

	result := FormatBgTaskCompletion(task, "")

	if !strings.Contains(result, "failed") {
		t.Error("should say 'failed' for error status")
	}
	if !strings.Contains(result, "Error: exit status 1") {
		t.Error("missing error")
	}
	if !strings.Contains(result, "Exit Code: 1") {
		t.Error("missing exit code")
	}
	if !strings.Contains(result, "(no output)") {
		t.Error("should show no output hint")
	}
}

func TestFormatBgTaskCompletion_LargeOutputTruncated(t *testing.T) {
	now := time.Now()
	finished := now.Add(1 * time.Second)
	largeOutput := strings.Repeat("x", 3000) // > 2000 threshold
	task := &BackgroundTask{
		ID:         "big1",
		Command:    "cat large.log",
		Status:     BgTaskDone,
		StartedAt:  now,
		FinishedAt: &finished,
		ExitCode:   0,
		Output:     largeOutput,
	}

	result := FormatBgTaskCompletion(task, "")

	if !strings.Contains(result, "truncated") {
		t.Error("should indicate truncation for large output")
	}
	if !strings.Contains(result, "3000") {
		t.Error("should show total size")
	}
	if !strings.Contains(result, "2000") {
		t.Error("should show truncated size")
	}
	// Result should be significantly shorter than original
	if len(result) > len(largeOutput) {
		t.Errorf("truncated result (%d) should be shorter than original (%d)", len(result), len(largeOutput))
	}
}

func TestFormatBgTaskCompletion_SmallOutputNotTruncated(t *testing.T) {
	now := time.Now()
	finished := now.Add(1 * time.Second)
	output := strings.Repeat("x", 500) // < 2000 threshold
	task := &BackgroundTask{
		ID:         "small1",
		Command:    "cat small.log",
		Status:     BgTaskDone,
		StartedAt:  now,
		FinishedAt: &finished,
		ExitCode:   0,
		Output:     output,
	}

	result := FormatBgTaskCompletion(task, "")

	if strings.Contains(result, "truncated") {
		t.Error("should NOT truncate small output")
	}
	if !strings.Contains(result, output) {
		t.Error("should contain full output")
	}
}

func TestFormatBgTaskCompletion_Killed(t *testing.T) {
	now := time.Now()
	finished := now.Add(1 * time.Second)
	task := &BackgroundTask{
		ID:         "kill1",
		Command:    "sleep 999",
		Status:     BgTaskKilled,
		StartedAt:  now,
		FinishedAt: &finished,
		ExitCode:   -1,
		Error:      "killed by user",
		Output:     "",
	}

	result := FormatBgTaskCompletion(task, "")

	if !strings.Contains(result, "killed by user") {
		t.Error("should say 'killed by user'")
	}
	if !strings.Contains(result, "Exit Code: -1") {
		t.Error("should show negative exit code for killed tasks")
	}
	if !strings.Contains(result, "Status: killed") {
		t.Error("should show killed status")
	}
}

func TestFormatBgTaskCompletion_AlwaysShowsExitCode(t *testing.T) {
	now := time.Now()
	finished := now.Add(1 * time.Second)
	task := &BackgroundTask{
		ID:         "ok1",
		Command:    "echo hi",
		Status:     BgTaskDone,
		StartedAt:  now,
		FinishedAt: &finished,
		ExitCode:   0,
		Output:     "hi\n",
	}

	result := FormatBgTaskCompletion(task, "")

	if !strings.Contains(result, "Exit Code: 0") {
		t.Error("should always show exit code, even for success")
	}
}

func TestFormatBgTaskCompletion_NilFinishedAt(t *testing.T) {
	task := &BackgroundTask{
		ID:        "still",
		Command:   "sleep 999",
		Status:    BgTaskRunning,
		StartedAt: time.Now(),
	}
	result := FormatBgTaskCompletion(task, "")
	if result != "" {
		t.Errorf("should return empty string for task without FinishedAt, got: %q", result)
	}
}

// ==================== task_wait × background sub-agent boundaries ====================

// TestTaskWaitSubAgentAlreadyDone — boundary: task_wait on an ALREADY-FINISHED
// background sub-agent must return immediately via the fast path (no blocking).
func TestTaskWaitSubAgentAlreadyDone(t *testing.T) {
	mgr := NewBackgroundTaskManager()
	defer mgr.UnregisterSubAgentTask("sub-done1")

	sub := mgr.RegisterSubAgentTask("sub-done1", "web:chat", "user1", "explore", "inst-1", func() {})
	mgr.CloseSubAgentTask(sub.ID, BgTaskDone, "finished ok")

	start := time.Now()
	toolCtx := &ToolContext{BgTaskManager: mgr, Ctx: context.Background()}
	result, err := (&TaskWaitTool{}).Execute(toolCtx, `{"task_id":"sub-done1","timeout":1}`)
	if err != nil {
		t.Fatalf("task_wait on done sub-agent returned error: %v", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Errorf("task_wait on already-done sub-agent blocked; elapsed=%v", time.Since(start))
	}
	if !strings.Contains(result.Summary, "Sub-Agent Task: sub-done1") || !strings.Contains(result.Summary, "done") {
		t.Errorf("unexpected result for done sub-agent: %q", result.Summary)
	}
}

// TestTaskWaitSubAgentRunningTimesOut — boundary: task_wait on a RUNNING
// background sub-agent blocks until the timeout then reports "still running"
// (never blocks forever).
func TestTaskWaitSubAgentRunningTimesOut(t *testing.T) {
	mgr := NewBackgroundTaskManager()
	defer mgr.UnregisterSubAgentTask("sub-run1")

	mgr.RegisterSubAgentTask("sub-run1", "web:chat", "user1", "explore", "inst-1", func() {})

	start := time.Now()
	toolCtx := &ToolContext{BgTaskManager: mgr, Ctx: context.Background()}
	result, err := (&TaskWaitTool{}).Execute(toolCtx, `{"task_id":"sub-run1","timeout":1}`)
	if err != nil {
		t.Fatalf("task_wait on running sub-agent returned error: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 900*time.Millisecond {
		t.Errorf("expected task_wait to block ~1s, elapsed=%v", elapsed)
	}
	if !strings.Contains(result.Summary, "still running") {
		t.Errorf("expected 'still running' after timeout, got: %q", result.Summary)
	}
}

// TestSubAgentTaskIDPrefix — the generated task_id must carry the "sub-" prefix
// so the spawn message can hand it to the parent agent for task_wait and it is
// distinguishable from Shell task ids.
func TestSubAgentTaskIDPrefix(t *testing.T) {
	mgr := NewBackgroundTaskManager()
	sub := mgr.RegisterSubAgentTask("", "web:chat", "user1", "explore", "inst-1", func() {})
	if !strings.HasPrefix(sub.ID, "sub-") {
		t.Errorf("sub-agent task id should start with sub-, got %q", sub.ID)
	}
	mgr.UnregisterSubAgentTask(sub.ID)
}

// TestTaskWaitSubAgentUnloadedUnblocks — boundary: task_wait on a sub-agent that
// was unloaded/killed (done channel closed by the unload path) must return
// immediately instead of blocking until timeout.
func TestTaskWaitSubAgentUnloadedUnblocks(t *testing.T) {
	mgr := NewBackgroundTaskManager()
	defer mgr.UnregisterSubAgentTask("sub-killed1")

	sub := mgr.RegisterSubAgentTask("sub-killed1", "web:chat", "user1", "explore", "inst-1", func() {})
	// Simulate the unload path closing the task (BgTaskKilled).
	mgr.CloseSubAgentTask(sub.ID, BgTaskKilled, "interactive session unloaded")

	start := time.Now()
	toolCtx := &ToolContext{BgTaskManager: mgr, Ctx: context.Background()}
	result, err := (&TaskWaitTool{}).Execute(toolCtx, `{"task_id":"sub-killed1","timeout":5}`)
	if err != nil {
		t.Fatalf("task_wait on killed sub-agent returned error: %v", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Errorf("task_wait on killed sub-agent blocked; elapsed=%v", time.Since(start))
	}
	if !strings.Contains(result.Summary, "killed") {
		t.Errorf("expected killed status, got: %q", result.Summary)
	}
}

// TestTruncateTailPreviewUTF8 — byte slicing at a non-rune boundary would cut
// a CJK/multibyte character mid-sequence producing invalid UTF-8. The helper
// must always land on a rune boundary.
func TestTruncateTailPreviewUTF8(t *testing.T) {
	// Short content: unchanged.
	if got := truncateTailPreview("short", 500); got != "short" {
		t.Errorf("short content changed: %q", got)
	}
	// Long CJK content: the result must be valid UTF-8 (no replacement runes)
	// and end with the tail of the original.
	long := strings.Repeat("中文内容测试", 100) // 600 bytes > 500
	got := truncateTailPreview(long, 500)
	if !utf8.ValidString(got) {
		t.Errorf("truncateTailPreview produced invalid UTF-8: %q", got)
	}
	if !strings.HasPrefix(got, "... ") {
		t.Errorf("expected '... ' prefix, got %q", got)
	}
	// The tail must be a suffix of the original (after stripping the prefix).
	tail := strings.TrimPrefix(got, "... ")
	if !strings.HasSuffix(long, tail) {
		t.Errorf("tail %q is not a suffix of the original", tail)
	}
	// Mixed ASCII + CJK cut exactly at the boundary.
	mixed := strings.Repeat("a", 490) + "中文"
	if got := truncateTailPreview(mixed, 500); !utf8.ValidString(got) {
		t.Errorf("mixed content produced invalid UTF-8: %q", got)
	}
}
