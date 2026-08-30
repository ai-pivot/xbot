package tools

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"xbot/llm"
)

// TaskWaitTool blocks until one or more background tasks complete (or the
// timeout expires). Supports waiting on multiple task IDs simultaneously with
// "all" (default — wait for every task) or "any" (return as soon as the first
// task finishes) modes.
//
// This replaces the old "sleep N + task_status" polling pattern — the agent
// calls task_wait once instead of wasting iterations on sleep.
//
// If all tasks are already completed, returns immediately.
type TaskWaitTool struct{}

func (t *TaskWaitTool) Name() string   { return "task_wait" }
func (t *TaskWaitTool) Required() bool { return false }
func (t *TaskWaitTool) Description() string {
	return `Block until background task(s) finish, or the timeout expires. Returns the final status and output preview for each task.

	task_id is an ARRAY of task ID strings — pass all IDs you are waiting on in ONE call:
  - Single task:  task_id: ["bg-abc123"]
  - Multiple:     task_id: ["bg-abc123", "sub-def456"]  (mode "any" returns on the first completion, "all" waits for every task)

	NOTE: pass the IDs EXACTLY as shown in the tool result, without any prefix —
	e.g. "3f8f492a" for Shell background tasks (raw hex), "sub-1eefac7a" for
	sub-agents. Never add a "bg:" or "bg-" prefix.

Use this instead of running "sleep N" in a foreground Shell to wait for a
background task. The current iteration blocks until the task(s) are done — no
wasted iterations on sleep polling.

If the task is already completed, returns immediately.

Parameters (JSON):
  - task_id: array of strings — the background task ID(s) to wait for
  - mode: string (optional) — "all" (default, wait for all) or "any" (return on first completion; for multiple IDs)
  - timeout: number (optional), max seconds to wait (default: 60, max: 300)`
}

func (t *TaskWaitTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		// Pure array type (NOT a string/array union): some providers validate
		// arguments against the schema and unions get shaky support; a bare
		// "string" schema also made models pass single strings. The schema is
		// now the single source of truth — ALWAYS an array. The Execute side
		// (parseTaskIDs) still tolerates a bare string for backward
		// compatibility with older tool-call history.
		{Name: "task_id", Type: "array", Items: &llm.ToolParamItems{Type: "string"}, Description: "Array of background task IDs to wait for. Single task: [\"3f8f492a\"]. Pass IDs EXACTLY as shown in the tool result — Shell background task IDs are raw hex like \"3f8f492a\" (no prefix), sub-agent IDs start with 'sub-'. Do NOT add any 'bg:' or 'bg-' prefix.", Required: true},
		{Name: "mode", Type: "string", Description: "Wait mode: 'all' (default — wait for every task to finish) or 'any' (return as soon as the first task finishes). For multiple IDs.", Required: false},
		{Name: "timeout", Type: "number", Description: "Max seconds to wait (default: 60, max: 300)", Required: false},
	}
}

func (t *TaskWaitTool) Execute(toolCtx *ToolContext, input string) (*ToolResult, error) {
	if toolCtx == nil || toolCtx.BgTaskManager == nil {
		return nil, fmt.Errorf("background tasks not supported")
	}

	params, err := parseToolArgs[struct {
		TaskID  json.RawMessage `json:"task_id"`
		Mode    string          `json:"mode"`
		Timeout int             `json:"timeout"`
	}](input)
	if err != nil {
		return nil, err
	}

	// Parse task_id: accept string (single) or array (multiple).
	taskIDs, err := parseTaskIDs(params.TaskID)
	if err != nil {
		return nil, err
	}
	if len(taskIDs) == 0 {
		return nil, fmt.Errorf("task_id is required (string or array of strings)")
	}

	// Determine mode (default: all).
	mode := strings.ToLower(params.Mode)
	if mode == "" {
		mode = "all"
	}
	if mode != "all" && mode != "any" {
		return nil, fmt.Errorf("mode must be 'all' or 'any', got %q", mode)
	}

	// Determine timeout (default 60s, max 300s).
	timeoutSec := params.Timeout
	if timeoutSec <= 0 {
		timeoutSec = 60
	}
	if timeoutSec > 300 {
		timeoutSec = 300
	}

	// Single task — fast path (no fan-out overhead).
	if len(taskIDs) == 1 {
		return waitSingleTask(toolCtx, taskIDs[0], timeoutSec)
	}

	// Multiple tasks — fan out.
	return waitMultipleTasks(toolCtx, taskIDs, mode, timeoutSec)
}

// parseTaskIDs accepts a JSON string or a JSON array of strings.
func parseTaskIDs(raw json.RawMessage) ([]string, error) {
	// Try string first.
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}, nil
	}
	// Try array.
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr, nil
	}
	return nil, fmt.Errorf("task_id must be a string or array of strings")
}

// waitSingleTask waits for one task (Shell bg or sub-agent), returning its
// formatted status.
func waitSingleTask(toolCtx *ToolContext, taskID string, timeoutSec int) (*ToolResult, error) {
	// Fast path 1: Shell background task.
	task, err := toolCtx.BgTaskManager.Status(taskID)
	if err == nil {
		if task.Status != BgTaskRunning {
			return NewResult(formatTask(task)), nil
		}
		return waitBgTaskDone(toolCtx, taskID, timeoutSec, func() (string, error) {
			t, err := toolCtx.BgTaskManager.Status(taskID)
			if err != nil {
				return "", err
			}
			return formatTask(t), nil
		}, func() (<-chan struct{}, error) {
			return toolCtx.BgTaskManager.WaitDone(taskID)
		})
	}

	// Fast path 2: background sub-agent task.
	subTask, serr := toolCtx.BgTaskManager.SubAgentStatus(taskID)
	if serr != nil {
		return nil, err // "task not found" (shell lookup error is the canonical one)
	}
	if subTask.Status != BgTaskRunning {
		return NewResult(formatSubAgentTask(subTask)), nil
	}
	return waitBgTaskDone(toolCtx, taskID, timeoutSec, func() (string, error) {
		t, err := toolCtx.BgTaskManager.SubAgentStatus(taskID)
		if err != nil {
			return "", err
		}
		return formatSubAgentTask(t), nil
	}, func() (<-chan struct{}, error) {
		return toolCtx.BgTaskManager.SubAgentWaitDone(taskID)
	})
}

// taskWaitResult holds the formatted output for one task.
type taskWaitResult struct {
	id   string
	text string
	done bool
}

// waitMultipleTasks waits for multiple tasks concurrently. In "all" mode it
// blocks until every task finishes (or timeout). In "any" mode it returns as
// soon as the first task finishes.
func waitMultipleTasks(toolCtx *ToolContext, taskIDs []string, mode string, timeoutSec int) (*ToolResult, error) {
	ctx := toolCtx.Ctx
	timer := time.NewTimer(time.Duration(timeoutSec) * time.Second)
	defer timer.Stop()

	// Collect done channels for all tasks.
	type taskChan struct {
		id     string
		doneCh <-chan struct{}
		isSub  bool
	}
	var chans []taskChan
	var alreadyDone []taskWaitResult

	for _, id := range taskIDs {
		// Try Shell bg task first.
		task, err := toolCtx.BgTaskManager.Status(id)
		if err == nil {
			if task.Status != BgTaskRunning {
				alreadyDone = append(alreadyDone, taskWaitResult{id: id, text: formatTask(task), done: true})
				continue
			}
			ch, err := toolCtx.BgTaskManager.WaitDone(id)
			if err != nil {
				alreadyDone = append(alreadyDone, taskWaitResult{id: id, text: fmt.Sprintf("Error: %v", err), done: true})
			} else {
				chans = append(chans, taskChan{id: id, doneCh: ch, isSub: false})
			}
			continue
		}
		// Try sub-agent task.
		subTask, serr := toolCtx.BgTaskManager.SubAgentStatus(id)
		if serr != nil {
			alreadyDone = append(alreadyDone, taskWaitResult{id: id, text: "Error: task not found", done: true})
			continue
		}
		if subTask.Status != BgTaskRunning {
			alreadyDone = append(alreadyDone, taskWaitResult{id: id, text: formatSubAgentTask(subTask), done: true})
			continue
		}
		ch, err := toolCtx.BgTaskManager.SubAgentWaitDone(id)
		if err != nil {
			alreadyDone = append(alreadyDone, taskWaitResult{id: id, text: fmt.Sprintf("Error: %v", err), done: true})
		} else {
			chans = append(chans, taskChan{id: id, doneCh: ch, isSub: true})
		}
	}

	// "any" mode: if any task is already done, return immediately.
	if mode == "any" && len(alreadyDone) > 0 {
		return NewResult(formatMultiResults(alreadyDone, mode, timeoutSec, false)), nil
	}

	// "all" mode: if all tasks are already done, return immediately.
	if mode == "all" && len(chans) == 0 {
		return NewResult(formatMultiResults(alreadyDone, mode, timeoutSec, false)), nil
	}

	// Wait for tasks to complete.
	results := make([]taskWaitResult, 0, len(taskIDs))
	results = append(results, alreadyDone...)
	timedOut := false

	for len(chans) > 0 {
		// Build select cases dynamically.
		cases := make([]reflect.SelectCase, 0, len(chans)+2)
		caseIdx := make([]taskChan, 0, len(chans))

		for _, tc := range chans {
			cases = append(cases, reflect.SelectCase{
				Dir:  reflect.SelectRecv,
				Chan: reflect.ValueOf(tc.doneCh),
			})
			caseIdx = append(caseIdx, tc)
		}
		// Add timer and ctx.
		cases = append(cases, reflect.SelectCase{
			Dir:  reflect.SelectRecv,
			Chan: reflect.ValueOf(timer.C),
		})
		cases = append(cases, reflect.SelectCase{
			Dir:  reflect.SelectRecv,
			Chan: reflect.ValueOf(ctx.Done()),
		})

		chosen, _, _ := reflect.Select(cases)
		if chosen == len(cases)-2 {
			// Timer fired.
			timedOut = true
			break
		}
		if chosen == len(cases)-1 {
			// Context cancelled.
			break
		}

		// A task completed.
		tc := caseIdx[chosen]
		var text string
		if tc.isSub {
			t, _ := toolCtx.BgTaskManager.SubAgentStatus(tc.id)
			text = formatSubAgentTask(t)
		} else {
			t, _ := toolCtx.BgTaskManager.Status(tc.id)
			text = formatTask(t)
		}
		results = append(results, taskWaitResult{id: tc.id, text: text, done: true})

		// Remove from pending.
		chans = append(chans[:chosen], chans[chosen+1:]...)

		if mode == "any" {
			// "any" mode: first completion is enough.
			break
		}
	}

	// For tasks still running (timeout or "any" mode), fetch current status.
	for _, tc := range chans {
		var text string
		if tc.isSub {
			t, _ := toolCtx.BgTaskManager.SubAgentStatus(tc.id)
			text = formatSubAgentTask(t)
		} else {
			t, _ := toolCtx.BgTaskManager.Status(tc.id)
			text = formatTask(t)
		}
		results = append(results, taskWaitResult{id: tc.id, text: text, done: false})
	}

	return NewResult(formatMultiResults(results, mode, timeoutSec, timedOut)), nil
}

// formatMultiResults formats the results of waiting on multiple tasks.
func formatMultiResults(results []taskWaitResult, mode string, timeoutSec int, timedOut bool) string {
	var b strings.Builder
	doneCount := 0
	for _, r := range results {
		if r.done {
			doneCount++
		}
	}

	if timedOut {
		fmt.Fprintf(&b, "Timed out after %ds — %d/%d tasks completed (mode: %s).\n\n", timeoutSec, doneCount, len(results), mode)
	} else if mode == "any" {
		fmt.Fprintf(&b, "First task completed (mode: any). %d/%d tasks done.\n\n", doneCount, len(results))
	} else {
		fmt.Fprintf(&b, "All tasks completed (%d/%d, mode: all).\n\n", doneCount, len(results))
	}

	for _, r := range results {
		status := "✅ done"
		if !r.done {
			status = "⏳ still running"
		}
		fmt.Fprintf(&b, "── %s [%s] ──\n%s\n\n", r.id, status, r.text)
	}

	return b.String()
}

// waitBgTaskDone blocks on a task's done channel until it closes, the timeout
// expires, or the agent context is cancelled. format returns the current status
// text; waitDone returns the done channel.
func waitBgTaskDone(toolCtx *ToolContext, taskID string, timeoutSec int, format func() (string, error), waitDone func() (<-chan struct{}, error)) (*ToolResult, error) {
	doneCh, err := waitDone()
	if err != nil {
		return nil, err
	}

	timer := time.NewTimer(time.Duration(timeoutSec) * time.Second)
	defer timer.Stop()

	select {
	case <-toolCtx.Ctx.Done():
		// Agent cancelled (Ctrl+C). Return current status.
		text, _ := format()
		return NewResult(fmt.Sprintf("Wait interrupted.\n\n%s", text)), nil

	case <-timer.C:
		// Timeout — task still running.
		text, _ := format()
		return NewResult(fmt.Sprintf("Timed out after %ds — task is still running.\n\n%s", timeoutSec, text)), nil

	case <-doneCh:
		// Task finished — return final status.
		text, err := format()
		if err != nil {
			return nil, err
		}
		return NewResult(text), nil
	}
}

var _ Tool = (*TaskWaitTool)(nil)
