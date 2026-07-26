package tools

import (
	"fmt"
	"time"

	"xbot/llm"
)

// TaskWaitTool blocks until a background task completes or the timeout expires.
// This replaces the old "sleep N + task_status" polling pattern — the agent
// calls task_wait once instead of wasting iterations on sleep.
type TaskWaitTool struct{}

func (t *TaskWaitTool) Name() string   { return "task_wait" }
func (t *TaskWaitTool) Required() bool { return false }
func (t *TaskWaitTool) Description() string {
	return `Block until a background task finishes, or the timeout expires. Returns the final task status and output preview.

Use this instead of running "sleep N" in a foreground Shell to wait for a background task. The current iteration blocks until the task is done — no wasted iterations on sleep polling.

If the task is already completed, returns immediately.

Parameters (JSON):
  - task_id: string, the background task ID to wait for
  - timeout: number (optional), max seconds to wait (default: 60, max: 300)`
}

func (t *TaskWaitTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "task_id", Type: "string", Description: "The background task ID to wait for", Required: true},
		{Name: "timeout", Type: "number", Description: "Max seconds to wait (default: 60, max: 300)", Required: false},
	}
}

func (t *TaskWaitTool) Execute(toolCtx *ToolContext, input string) (*ToolResult, error) {
	if toolCtx == nil || toolCtx.BgTaskManager == nil {
		return nil, fmt.Errorf("background tasks not supported")
	}

	params, err := parseToolArgs[struct {
		TaskID  string `json:"task_id"`
		Timeout int    `json:"timeout"`
	}](input)
	if err != nil {
		return nil, err
	}

	task, err := toolCtx.BgTaskManager.Status(params.TaskID)
	if err != nil {
		return nil, err
	}

	// Already completed — return immediately.
	if task.Status != BgTaskRunning {
		return NewResult(formatTask(task)), nil
	}

	// Determine timeout (default 60s, max 300s).
	timeoutSec := params.Timeout
	if timeoutSec <= 0 {
		timeoutSec = 60
	}
	if timeoutSec > 300 {
		timeoutSec = 300
	}

	// Poll loop: check every 1s, stop on completion / timeout / cancel.
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	timer := time.NewTimer(time.Duration(timeoutSec) * time.Second)
	defer timer.Stop()

	for {
		select {
		case <-toolCtx.Ctx.Done():
			// Agent cancelled (Ctrl+C). Return current status.
			task, _ = toolCtx.BgTaskManager.Status(params.TaskID)
			return NewResult(fmt.Sprintf("Wait interrupted.\n\n%s", formatTask(task))), nil

		case <-timer.C:
			// Timeout — task still running.
			task, _ = toolCtx.BgTaskManager.Status(params.TaskID)
			return NewResult(fmt.Sprintf("Timed out after %ds — task is still running.\n\n%s", timeoutSec, formatTask(task))), nil

		case <-ticker.C:
			// Check if the task has finished.
			updated, err := toolCtx.BgTaskManager.Status(params.TaskID)
			if err != nil {
				return nil, err
			}
			if updated.Status != BgTaskRunning {
				return NewResult(formatTask(updated)), nil
			}
		}
	}
}

var _ Tool = (*TaskWaitTool)(nil)
