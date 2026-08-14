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
	return `Block until a background task finishes (Shell background command OR background sub-agent), or the timeout expires. Returns the final status and output preview.

Use this instead of running "sleep N" in a foreground Shell to wait for a background task. The current iteration blocks until the task is done — no wasted iterations on sleep polling.

If the task is already completed, returns immediately.

Parameters (JSON):
  - task_id: string, the background task ID to wait for (Shell background task or background sub-agent task ID)
  - timeout: number (optional), max seconds to wait (default: 60, max: 300)`
}

func (t *TaskWaitTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "task_id", Type: "string", Description: "The background task ID to wait for (Shell background task or background sub-agent task ID)", Required: true},
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

	// Determine timeout (default 60s, max 300s).
	timeoutSec := params.Timeout
	if timeoutSec <= 0 {
		timeoutSec = 60
}
	if timeoutSec > 300 {
		timeoutSec = 300
}

	// Fast path 1: Shell background task.
	task, err := toolCtx.BgTaskManager.Status(params.TaskID)
	if err == nil {
	if task.Status != BgTaskRunning {
		return NewResult(formatTask(task)), nil
}
		return waitBgTaskDone(toolCtx, params.TaskID, timeoutSec, func() (string, error) {
			t, err := toolCtx.BgTaskManager.Status(params.TaskID)
	if err != nil {
				return "", err
}
			return formatTask(t), nil
		}, func() (<-chan struct{}, error) {
			return toolCtx.BgTaskManager.WaitDone(params.TaskID)
		})
}

	// Fast path 2: background sub-agent task.
	subTask, serr := toolCtx.BgTaskManager.SubAgentStatus(params.TaskID)
	if serr != nil {
		return nil, err // "task not found" (shell lookup error is the canonical one)
}
	if subTask.Status != BgTaskRunning {
		return NewResult(formatSubAgentTask(subTask)), nil
}
	return waitBgTaskDone(toolCtx, params.TaskID, timeoutSec, func() (string, error) {
		t, err := toolCtx.BgTaskManager.SubAgentStatus(params.TaskID)
	if err != nil {
			return "", err
}
		return formatSubAgentTask(t), nil
	}, func() (<-chan struct{}, error) {
		return toolCtx.BgTaskManager.SubAgentWaitDone(params.TaskID)
	})
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
