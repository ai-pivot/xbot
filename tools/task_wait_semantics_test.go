package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestTaskWait_MultiID_AllVsAnySemantics is the decisive behavioral test for
// the all/any modes (the coverage gap task_multi_id_test.go left):
//
//   - mode=all  blocks until EVERY task finishes (a slow task delays the
//     return past the fast task's completion);
//   - mode=any  returns as soon as the FIRST task finishes (the slow task is
//     still reported as running).
func TestTaskWait_MultiID_AllVsAnySemantics(t *testing.T) {
	newMgr := func() (*BackgroundTaskManager, string, string, *ToolContext) {
		mgr := NewBackgroundTaskManager()
		// Fast task: completes immediately.
		fast := mgr.RegisterSubAgentTask("sub-tfast", "web:chat", "user1", "explore", "inst-fast", func() {})
		mgr.CloseSubAgentTask(fast.ID, BgTaskDone, "fast done")
		// Slow task: completes when startSlow closes.
		slow := mgr.RegisterSubAgentTask("sub-tslow", "web:chat", "user1", "explore", "inst-slow", func() {})
		return mgr, fast.ID, slow.ID, &ToolContext{BgTaskManager: mgr, Ctx: context.Background()}
	}

	// --- mode=all: must block until the SLOW task also completes ---
	mgr, fastID, slowID, toolCtx := newMgr()
	go func() {
		// Hold the slow task for 400ms after the fast task is already done.
		time.Sleep(400 * time.Millisecond)
		mgr.CloseSubAgentTask(slowID, BgTaskDone, "slow done")
	}()
	start := time.Now()
	res, err := (&TaskWaitTool{}).Execute(toolCtx, `{"task_id":["`+fastID+`","`+slowID+`"],"mode":"all","timeout":10}`)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("mode=all returned error: %v", err)
	}
	if elapsed < 350*time.Millisecond {
		t.Errorf("mode=all returned after %v — but the slow task takes 400ms: all must wait for EVERY task (looked like any)", elapsed)
	}
	if !strings.Contains(res.Summary, "All tasks completed (2/2") {
		t.Errorf("mode=all output must report All tasks completed (2/2), got:\n%s", res.Summary)
	}
	if strings.Contains(res.Summary, "⏳ still running") {
		t.Errorf("mode=all (all finished) must not report any task as still running:\n%s", res.Summary)
	}

	// --- mode=any: must return as soon as the FAST task completes ---
	mgr2, fastID2, slowID2, toolCtx2 := newMgr()
	defer mgr2.UnregisterSubAgentTask(slowID2)
	go func() {
		// The slow task would complete at 400ms — any must return BEFORE it.
		time.Sleep(400 * time.Millisecond)
		mgr2.CloseSubAgentTask(slowID2, BgTaskDone, "slow done")
	}()
	start2 := time.Now()
	res2, err2 := (&TaskWaitTool{}).Execute(toolCtx2, `{"task_id":["`+fastID2+`","`+slowID2+`"],"mode":"any","timeout":10}`)
	elapsed2 := time.Since(start2)
	if err2 != nil {
		t.Fatalf("mode=any returned error: %v", err2)
	}
	if elapsed2 >= 400*time.Millisecond {
		t.Errorf("mode=any returned after %v — it must return as soon as the first task finishes (< 400ms)", elapsed2)
	}
	if !strings.Contains(res2.Summary, "mode: any") {
		t.Errorf("mode=any output must be labeled (mode: any), got:\n%s", res2.Summary)
	}
	if !strings.Contains(res2.Summary, "⏳ still running") {
		t.Errorf("mode=any with a pending slow task must report it as still running:\n%s", res2.Summary)
	}
}

// TestTaskRead_SubAgentID_NotMisleadingNotFound verifies task_read resolves
// sub-agent task IDs (consistency with task_wait/task_status/task_kill): a
// valid sub-xxx ID must NOT report "task not found" — sub-agent tasks have no
// streaming output, and the tool must say so (pointing at task_status).
func TestTaskRead_SubAgentID_NotMisleadingNotFound(t *testing.T) {
	mgr := NewBackgroundTaskManager()
	defer mgr.UnregisterSubAgentTask("sub-read1")

	sub := mgr.RegisterSubAgentTask("sub-read1", "web:chat", "user1", "explore", "inst-1", func() {})
	mgr.CloseSubAgentTask(sub.ID, BgTaskDone, "finished output preview")

	toolCtx := &ToolContext{BgTaskManager: mgr, Ctx: context.Background()}
	res, err := (&TaskReadTool{}).Execute(toolCtx, `{"task_id":"sub-read1"}`)
	if err != nil {
		t.Fatalf("task_read on a sub-agent ID must not return an error, got: %v", err)
	}
	for _, want := range []string{"Sub-agent task sub-read1", "no streaming output", "task_status"} {
		if !strings.Contains(res.Summary, want) {
			t.Errorf("task_read(sub-agent) output missing %q, got:\n%s", want, res.Summary)
		}
	}

	// A genuinely unknown ID still errors (not-found is correct there).
	if _, err := (&TaskReadTool{}).Execute(toolCtx, `{"task_id":"ghost-id"}`); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("unknown ID must keep the not-found error, got: %v", err)
	}
}
