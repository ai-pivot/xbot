package tools

import (
	"fmt"
	"strings"
	"time"

	"xbot/llm"
	log "xbot/logger"
)

// TaskStatusTool returns the current status of a background task.
type TaskStatusTool struct{}

func (t *TaskStatusTool) Name() string   { return "task_status" }
func (t *TaskStatusTool) Required() bool { return false }
func (t *TaskStatusTool) Description() string {
	return `Check the status of a background task. Shows task ID, command, status (running/done/error/killed), elapsed time, and a preview of the output.

If status is "running", use task_wait to block until completion, or continue with other work — the result will be injected automatically when the task finishes.

Parameters (JSON):
  - task_id: string, the task ID to check`
}

func (t *TaskStatusTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "task_id", Type: "string", Description: "The background task ID to check", Required: true},
	}
}

func (t *TaskStatusTool) Execute(toolCtx *ToolContext, input string) (*ToolResult, error) {
	if toolCtx == nil || toolCtx.BgTaskManager == nil {
		return nil, fmt.Errorf("background tasks not supported")
	}

	params, err := parseToolArgs[struct {
		TaskID string `json:"task_id"`
	}](input)
	if err != nil {
		return nil, err
	}

	// Shell background task first, then background sub-agent task.
	if task, err := toolCtx.BgTaskManager.Status(params.TaskID); err == nil {
		return NewResult(formatTask(task)), nil
	}
	if subTask, err := toolCtx.BgTaskManager.SubAgentStatus(params.TaskID); err == nil {
		return NewResult(formatSubAgentTask(subTask)), nil
	}
	return nil, fmt.Errorf("task %s not found", params.TaskID)
}

// TaskKillTool terminates a running background task.
type TaskKillTool struct{}

func (t *TaskKillTool) Name() string   { return "task_kill" }
func (t *TaskKillTool) Required() bool { return false }
func (t *TaskKillTool) Description() string {
	return `Terminate a running background task. All child processes of the task will be killed.

Parameters (JSON):
  - task_id: string, the task ID to kill`
}

func (t *TaskKillTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "task_id", Type: "string", Description: "The background task ID to kill", Required: true},
	}
}

func (t *TaskKillTool) Execute(toolCtx *ToolContext, input string) (*ToolResult, error) {
	if toolCtx == nil || toolCtx.BgTaskManager == nil {
		return nil, fmt.Errorf("background tasks not supported")
	}

	params, err := parseToolArgs[struct {
		TaskID string `json:"task_id"`
	}](input)
	if err != nil {
		return nil, err
	}

	// Shell background task first.
	if err := toolCtx.BgTaskManager.Kill(params.TaskID); err == nil {
		log.WithField("task_id", params.TaskID).Info("Background task killed by user")
		return NewResult(fmt.Sprintf("Task %s killed successfully.", params.TaskID)), nil
	}
	// Background sub-agent task: cancel its context (interrupt).
	if subTask, serr := toolCtx.BgTaskManager.SubAgentStatus(params.TaskID); serr == nil {
		if subTask.cancel != nil {
			subTask.cancel()
		}
		log.WithFields(log.Fields{"task_id": params.TaskID, "role": subTask.Role}).Info("Background sub-agent task cancelled by user")
		return NewResult(fmt.Sprintf("Sub-agent task %s cancelled (interrupting role %q).", params.TaskID, subTask.Role)), nil
	}
	return NewErrorResult(fmt.Sprintf("Failed to kill task %s: task not found", params.TaskID)), nil
}

// TaskReadTool reads the full output of a completed (or running) background task.
type TaskReadTool struct{}

func (t *TaskReadTool) Name() string   { return "task_read" }
func (t *TaskReadTool) Required() bool { return false }
func (t *TaskReadTool) Description() string {
	return `Read the full output of a background task. Useful for reviewing the complete output of a completed task.

Parameters (JSON):
  - task_id: string, the task ID to read
  - tail: number (optional), only return the last N characters (default: all)`
}

func (t *TaskReadTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "task_id", Type: "string", Description: "The background task ID to read", Required: true},
		{Name: "tail", Type: "number", Description: "Only return the last N characters of output (default: all)", Required: false},
	}
}

func (t *TaskReadTool) Execute(toolCtx *ToolContext, input string) (*ToolResult, error) {
	if toolCtx == nil || toolCtx.BgTaskManager == nil {
		return nil, fmt.Errorf("background tasks not supported")
	}

	params, err := parseToolArgs[struct {
		TaskID string `json:"task_id"`
		Tail   int    `json:"tail"`
	}](input)
	if err != nil {
		return nil, err
	}

	task, err := toolCtx.BgTaskManager.Status(params.TaskID)
	if err != nil {
		return nil, err
	}

	output := task.Output
	if params.Tail > 0 && len(output) > params.Tail {
		output = "... (truncated) ...\n" + output[len(output)-params.Tail:]
	}

	if output == "" {
		return NewResult(fmt.Sprintf("Task %s has no output yet.", task.ID)), nil
	}

	return NewResult(fmt.Sprintf("[Task %s output (%s, %d bytes)]\n%s",
		task.ID, task.Status, len(task.Output), output)), nil
}

// formatTask formats a task for display.
func formatTask(task *BackgroundTask) string {
	elapsed := time.Since(task.StartedAt).Round(time.Second)
	if task.FinishedAt != nil {
		elapsed = task.FinishedAt.Sub(task.StartedAt).Round(time.Second)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Task: %s\n", task.ID)
	fmt.Fprintf(&sb, "Command: %s\n", task.Command)
	fmt.Fprintf(&sb, "Status: %s\n", task.Status)
	fmt.Fprintf(&sb, "Elapsed: %s\n", elapsed)

	if task.Status == BgTaskRunning {
		fmt.Fprintf(&sb, "\n⏳ Task is still running. Use task_wait to wait for completion, or continue with other work.\n")
	}

	if task.ExitCode >= 0 {
		fmt.Fprintf(&sb, "Exit Code: %d\n", task.ExitCode)
	}
	if task.Error != "" {
		fmt.Fprintf(&sb, "Error: %s\n", task.Error)
	}

	// Show last 500 chars of output as preview
	preview := task.Output
	if len(preview) > 500 {
		preview = "... " + preview[len(preview)-497:]
	}
	if preview != "" {
		fmt.Fprintf(&sb, "Output Preview:\n%s\n", preview)
	}

	return sb.String()
}

// formatSubAgentTask formats a background sub-agent task for display.
func formatSubAgentTask(task *SubAgentTask) string {
	elapsed := time.Since(task.StartedAt).Round(time.Second)
	if task.FinishedAt != nil {
		elapsed = task.FinishedAt.Sub(task.StartedAt).Round(time.Second)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Sub-Agent Task: %s\n", task.ID)
	label := task.Role
	if task.Instance != "" {
		label = fmt.Sprintf("%s (instance=%s)", task.Role, task.Instance)
	}
	fmt.Fprintf(&sb, "Sub-Agent: %s\n", label)
	fmt.Fprintf(&sb, "Status: %s\n", task.Status)
	fmt.Fprintf(&sb, "Elapsed: %s\n", elapsed)

	if task.Status == BgTaskRunning {
		fmt.Fprintf(&sb, "\n⏳ Sub-agent is still running. Use task_wait to wait for completion, or continue with other work.\n")
	}

	if task.Content != "" {
		preview := task.Content
		if len(preview) > 500 {
			preview = "... " + preview[len(preview)-497:]
		}
		fmt.Fprintf(&sb, "Result Preview:\n%s\n", preview)
	}

	return sb.String()
}

// This is used by the engine to inject the task result into the conversation as a tool message.
func FormatBgTaskCompletion(task *BackgroundTask, outputOverride string) string {
	if task.FinishedAt == nil {
		return ""
	}
	elapsed := task.FinishedAt.Sub(task.StartedAt).Round(time.Second)

	var sb strings.Builder
	switch task.Status {
	case BgTaskKilled:
		fmt.Fprintf(&sb, "[System Notification] Background task %s killed by user.\n", task.ID)
	case BgTaskError:
		fmt.Fprintf(&sb, "[System Notification] Background task %s failed.\n", task.ID)
	default:
		fmt.Fprintf(&sb, "[System Notification] Background task %s completed.\n", task.ID)
	}
	fmt.Fprintf(&sb, "Command: %s\n", task.Command)
	fmt.Fprintf(&sb, "Status: %s | Elapsed: %s\n", task.Status, elapsed)

	// Always show exit code (including -1 for killed, non-zero for errors)
	fmt.Fprintf(&sb, "Exit Code: %d\n", task.ExitCode)

	if task.Error != "" {
		fmt.Fprintf(&sb, "Error: %s\n", task.Error)
	}

	// When outputOverride is provided (e.g. offload placeholder), use it directly.
	// Otherwise, show the raw output (truncated if too large).
	if outputOverride != "" {
		fmt.Fprintf(&sb, "\n%s", outputOverride)
	} else if task.Output != "" {
		// Sanitize \r overwrites and ANSI escape sequences so that progress
		// bar output (tqdm, curl, etc.) renders cleanly in the TUI.
		output := SanitizeOutput(task.Output)
		// Truncate large output to avoid bloating context
		const maxOutputLen = 2000
		if len(output) > maxOutputLen {
			fmt.Fprintf(&sb, "\nOutput (truncated, %d/%d chars):\n%s\n... [use task_read with task_id=%q for full output]", maxOutputLen, len(output), output[:maxOutputLen], task.ID)
		} else {
			fmt.Fprintf(&sb, "\nOutput:\n%s", output)
		}
	} else {
		sb.WriteString("\n(no output)")
	}

	return sb.String()
}

// ListBgTasks returns a summary of all background tasks for a session.
func ListBgTasks(mgr *BackgroundTaskManager, sessionKey string) string {
	if mgr == nil {
		return "No background task support."
	}

	tasks := mgr.List(sessionKey)
	if len(tasks) == 0 {
		return "No background tasks."
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Background tasks (%d):\n", len(tasks))
	for _, task := range tasks {
		elapsed := time.Since(task.StartedAt).Round(time.Second)
		if task.FinishedAt != nil {
			elapsed = task.FinishedAt.Sub(task.StartedAt).Round(time.Second)
		}
		fmt.Fprintf(&sb, "  %s  %s  %s  %s  (exit %d)\n",
			task.ID, task.Status, elapsed, truncateStr(task.Command, 50), task.ExitCode)
	}
	return sb.String()
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// ensure TaskStatusTool implements Tool
var _ Tool = (*TaskStatusTool)(nil)
var _ Tool = (*TaskKillTool)(nil)
var _ Tool = (*TaskReadTool)(nil)
