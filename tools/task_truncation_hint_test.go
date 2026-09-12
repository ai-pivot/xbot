package tools

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// These tests pin the "truncation must be recoverable" contract: whenever a
// tool result or task preview reaches the model in truncated form, the same
// text must tell the model HOW to get the rest. Regression: sub-agent results
// were cut at 2000 bytes with a vague "use inspect for details", and the tail
// never reached the offload store — so `offload_recall` could not recover it
// even if the model had thought to call it.

// ==================== TruncateHeadPreview ====================

func TestTruncateHeadPreview_ShortInputUnchanged(t *testing.T) {
	in := "hello world"
	if got := TruncateHeadPreview(in, 100); got != in {
		t.Errorf("short input must be returned unchanged, got %q", got)
	}
}

func TestTruncateHeadPreview_CJKNeverSlicedMidRune(t *testing.T) {
	// 200 CJK runes = 600 bytes; cut at 100 bytes lands mid-rune without the
	// rune-boundary fixup.
	in := strings.Repeat("中文测试内容", 40)
	got := TruncateHeadPreview(in, 100)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated string is invalid UTF-8: %q", got)
	}
	if !strings.HasPrefix(got, "中文测试内容") {
		t.Errorf("expected the head to be preserved, got %q", got)
	}
	if !strings.HasSuffix(got, " ...") {
		t.Errorf("expected the ' ...' suffix, got %q", got)
	}
	if len(got) > 100 {
		t.Errorf("result exceeds the byte budget: %d > 100", len(got))
	}
}

// ==================== formatTask (shell background task) ====================

func TestFormatTask_TruncatedPreviewPointsAtTaskRead(t *testing.T) {
	task := &BackgroundTask{
		ID:        "3f8f492a",
		Command:   "make build",
		Status:    BgTaskDone,
		StartedAt: time.Now(),
		ExitCode:  0,
		Output:    strings.Repeat("line of output\n", 100), // > 500 bytes
	}

	got := formatTask(task)
	if !strings.Contains(got, "Output Preview:") {
		t.Fatalf("expected a preview section, got: %q", got)
	}
	if !strings.Contains(got, "task_read") || !strings.Contains(got, "3f8f492a") {
		t.Errorf("truncated preview must tell the model how to get the full output "+
			"(task_read + task_id); got: %q", got)
	}
}

func TestFormatTask_ShortOutputHasNoTruncationHint(t *testing.T) {
	task := &BackgroundTask{
		ID:        "abc",
		Command:   "echo hi",
		Status:    BgTaskDone,
		StartedAt: time.Now(),
		ExitCode:  0,
		Output:    "hi\n",
	}

	if got := formatTask(task); strings.Contains(got, "task_read") {
		t.Errorf("short output must not advertise a truncation hint, got: %q", got)
	}
}

// ==================== formatSubAgentTask ====================

func TestFormatSubAgentTask_TruncatedResultPointsAtOffloadRecall(t *testing.T) {
	fin := time.Now()
	task := &SubAgentTask{
		ID:         "sub-abc123",
		Role:       "explore",
		Instance:   "mem-1",
		Status:     BgTaskDone,
		StartedAt:  fin.Add(-time.Minute),
		FinishedAt: &fin,
		Content:    strings.Repeat("sub-agent findings; ", 200), // > 500 bytes
	}

	got := formatSubAgentTask(task)
	if !strings.Contains(got, "Result Preview:") {
		t.Fatalf("expected a result preview, got: %q", got)
	}
	// The whole point of the fix: the model must be told about offload_recall.
	if !strings.Contains(got, "offload_recall") {
		t.Errorf("truncated sub-agent result must mention offload_recall, got: %q", got)
	}
	// …and about the inspect fallback, with the exact addressing params.
	if !strings.Contains(got, `SubAgent(action="inspect"`) ||
		!strings.Contains(got, `role="explore"`) ||
		!strings.Contains(got, `instance="mem-1"`) {
		t.Errorf("truncated sub-agent result must give an actionable inspect hint, got: %q", got)
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("expected an explicit truncation notice, got: %q", got)
	}
}

func TestFormatSubAgentTask_CJKPreviewRuneSafe(t *testing.T) {
	fin := time.Now()
	task := &SubAgentTask{
		ID:         "sub-cjk",
		Role:       "explore",
		Instance:   "i1",
		Status:     BgTaskDone,
		StartedAt:  fin.Add(-time.Second),
		FinishedAt: &fin,
		Content:    strings.Repeat("这是一段很长的中文子代理结论内容。", 60),
	}

	if got := formatSubAgentTask(task); !utf8.ValidString(got) {
		t.Fatalf("sub-agent preview produced invalid UTF-8: %q", got)
	}
}

// ==================== FormatBgTaskCompletion ====================

func TestFormatBgTaskCompletion_CJKTruncationRuneSafe(t *testing.T) {
	fin := time.Now()
	task := &BackgroundTask{
		ID:         "bg-cjk",
		Command:    "cat big-cn.txt",
		Status:     BgTaskDone,
		StartedAt:  fin.Add(-time.Second),
		FinishedAt: &fin,
		ExitCode:   0,
		Output:     strings.Repeat("中文输出内容", 400), // > 2000 bytes
	}

	got := FormatBgTaskCompletion(task, "")
	if !utf8.ValidString(got) {
		t.Fatalf("bg task completion produced invalid UTF-8 (raw byte slicing?): %q", got)
	}
	if !strings.Contains(got, "task_read") {
		t.Errorf("truncated completion must point at task_read, got: %q", got)
	}
}

// ==================== CloseSubAgentTask keeps the full result ====================

func TestCloseSubAgentTask_KeepsFullContent(t *testing.T) {
	mgr := NewBackgroundTaskManager()
	sub := mgr.RegisterSubAgentTask("sub-full", "web:chat", "user1", "explore", "inst-1", func() {})
	defer mgr.UnregisterSubAgentTask(sub.ID)

	full := strings.Repeat("x", 5000)
	mgr.CloseSubAgentTask(sub.ID, BgTaskDone, full)

	got, err := mgr.SubAgentStatus(sub.ID)
	if err != nil {
		t.Fatalf("SubAgentStatus: %v", err)
	}
	// Regression: this used to come back capped at 2000 bytes with the tail
	// gone, making the rest unrecoverable (the notification path is the only
	// copy, and it offloads rather than stores).
	if len(got.Content) != len(full) {
		t.Errorf("stored content was truncated: got %d bytes, want %d", len(got.Content), len(full))
	}
}
