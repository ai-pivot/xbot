package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"xbot/llm"
	log "xbot/logger"
)

// TaskStatusTool returns the current status of one or more background tasks.
type TaskStatusTool struct{}

func (t *TaskStatusTool) Name() string   { return "task_status" }
func (t *TaskStatusTool) Required() bool { return false }
func (t *TaskStatusTool) Description() string {
	return `Check the status of background task(s). Shows task ID, command, status (running/done/error/killed), elapsed time, and a preview of the output.

task_id is an ARRAY of task ID strings — check multiple tasks in ONE call:
  - Single task:  task_id: ["bg-abc123"]
  - Multiple:     task_id: ["bg-abc123", "sub-def456"]
  - Each ID's status is reported independently; unknown IDs are listed as errors without aborting the rest.

If status is "running", use task_wait to block until completion, or continue with other work — the result will be injected automatically when the task finishes.

Parameters (JSON):
  - task_id: array of strings — the task ID(s) to check`
}

func (t *TaskStatusTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		// Pure array type (NOT a string/array union) — see task_wait.go for rationale.
		// Execute tolerates a bare string for backward compatibility.
		{Name: "task_id", Type: "array", Items: &llm.ToolParamItems{Type: "string"}, Description: "Array of background task IDs to check. Single task: [\"abc123\"]. Pass IDs EXACTLY as shown in the tool result (no 'bg:' prefix).", Required: true},
	}
}

func (t *TaskStatusTool) Execute(toolCtx *ToolContext, input string) (*ToolResult, error) {
	if toolCtx == nil || toolCtx.BgTaskManager == nil {
		return nil, fmt.Errorf("background tasks not supported")
	}

	params, err := parseToolArgs[struct {
		TaskID json.RawMessage `json:"task_id"`
	}](input)
	if err != nil {
		return nil, err
	}

	taskIDs, err := parseTaskIDs(params.TaskID)
	if err != nil {
		return nil, err
	}
	if len(taskIDs) == 0 {
		return nil, fmt.Errorf("task_id is required (string or array of strings)")
	}

	// Single task — exact same output as before (backward compatible).
	if len(taskIDs) == 1 {
		id := taskIDs[0]
		// Shell background task first, then background sub-agent task.
		if task, err := toolCtx.BgTaskManager.Status(id); err == nil {
			return NewResult(formatTask(task)), nil
		}
		if subTask, err := toolCtx.BgTaskManager.SubAgentStatus(id); err == nil {
			return NewResult(formatSubAgentTask(subTask)), nil
		}
		return nil, fmt.Errorf("task %s not found", id)
	}

	// Multiple tasks — per-ID tolerant aggregation: an unknown ID is reported
	// in the output instead of aborting the whole query.
	var sb strings.Builder
	fmt.Fprintf(&sb, "Status of %d tasks:\n", len(taskIDs))
	for _, id := range taskIDs {
		if task, err := toolCtx.BgTaskManager.Status(id); err == nil {
			fmt.Fprintf(&sb, "\n%s\n", formatTask(task))
			continue
		}
		if subTask, err := toolCtx.BgTaskManager.SubAgentStatus(id); err == nil {
			fmt.Fprintf(&sb, "\n%s\n", formatSubAgentTask(subTask))
			continue
		}
		fmt.Fprintf(&sb, "\nTask: %s\nStatus: not found (no background task or sub-agent with this ID)\n", id)
	}
	return NewResult(sb.String()), nil
}

// TaskKillTool terminates one or more running background tasks.
type TaskKillTool struct{}

func (t *TaskKillTool) Name() string   { return "task_kill" }
func (t *TaskKillTool) Required() bool { return false }
func (t *TaskKillTool) Description() string {
	return `Terminate running background task(s). All child processes of each task will be killed.

task_id is an ARRAY of task ID strings — kill multiple tasks in ONE call:
  - Single task:  task_id: ["bg-abc123"]
  - Multiple:     task_id: ["bg-abc123", "sub-def456"]
  - Each ID's result is reported independently (killed / cancelled / not found); failures do not abort the rest
  - Use with care in batch mode — verify the IDs before killing

Parameters (JSON):
  - task_id: array of strings — the task ID(s) to kill`
}

func (t *TaskKillTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		// Pure array type (NOT a string/array union) — see task_wait.go for rationale.
		// Execute tolerates a bare string for backward compatibility.
		{Name: "task_id", Type: "array", Items: &llm.ToolParamItems{Type: "string"}, Description: "Array of background task IDs to kill. Single task: [\"abc123\"]. Pass IDs EXACTLY as shown in the tool result (no 'bg:' prefix).", Required: true},
	}
}

func (t *TaskKillTool) Execute(toolCtx *ToolContext, input string) (*ToolResult, error) {
	if toolCtx == nil || toolCtx.BgTaskManager == nil {
		return nil, fmt.Errorf("background tasks not supported")
	}

	params, err := parseToolArgs[struct {
		TaskID json.RawMessage `json:"task_id"`
	}](input)
	if err != nil {
		return nil, err
	}

	taskIDs, err := parseTaskIDs(params.TaskID)
	if err != nil {
		return nil, err
	}
	if len(taskIDs) == 0 {
		return nil, fmt.Errorf("task_id is required (string or array of strings)")
	}

	// Single task — exact same output as before (backward compatible).
	if len(taskIDs) == 1 {
		id := taskIDs[0]
		// Shell background task first.
		if err := toolCtx.BgTaskManager.Kill(id); err == nil {
			log.WithField("task_id", id).Info("Background task killed by user")
			return NewResult(fmt.Sprintf("Task %s killed successfully.", id)), nil
		}
		// Background sub-agent task: cancel its context (interrupt).
		if subTask, serr := toolCtx.BgTaskManager.SubAgentStatus(id); serr == nil {
			if subTask.cancel != nil {
				subTask.cancel()
			}
			log.WithFields(log.Fields{"task_id": id, "role": subTask.Role}).Info("Background sub-agent task cancelled by user")
			return NewResult(fmt.Sprintf("Sub-agent task %s cancelled (interrupting role %q).", id, subTask.Role)), nil
		}
		return NewErrorResult(fmt.Sprintf("Failed to kill task %s: task not found", id)), nil
	}

	// Multiple tasks — per-ID aggregation; each result is reported explicitly
	// so the model can see exactly which IDs were killed and which failed.
	var sb strings.Builder
	fmt.Fprintf(&sb, "Kill results for %d tasks:", len(taskIDs))
	for _, id := range taskIDs {
		if err := toolCtx.BgTaskManager.Kill(id); err == nil {
			log.WithField("task_id", id).Info("Background task killed by user")
			fmt.Fprintf(&sb, "\n- Task %s: killed successfully", id)
			continue
		}
		if subTask, serr := toolCtx.BgTaskManager.SubAgentStatus(id); serr == nil {
			if subTask.cancel != nil {
				subTask.cancel()
			}
			log.WithFields(log.Fields{"task_id": id, "role": subTask.Role}).Info("Background sub-agent task cancelled by user")
			fmt.Fprintf(&sb, "\n- Sub-agent task %s: cancelled (interrupting role %q)", id, subTask.Role)
			continue
		}
		fmt.Fprintf(&sb, "\n- Task %s: FAILED — task not found", id)
	}
	return NewResult(sb.String()), nil
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
		// Sub-agent tasks exist but have no streaming output — distinguish
		// "wrong kind of task" from "not found" (task_wait/task_status/
		// task_kill all resolve sub-agent IDs; task_read must too, or a
		// valid sub-xxx ID misleadingly reports "task not found").
		if subTask, serr := toolCtx.BgTaskManager.SubAgentStatus(params.TaskID); serr == nil {
			return NewResult(fmt.Sprintf(
				"Sub-agent task %s: no streaming output — sub-agent results are delivered via the completion notification, not an output buffer.\nUse task_status %q for its current status (role %q, status: %s).",
				params.TaskID, params.TaskID, subTask.Role, subTask.Status)), nil
		}
		return nil, err
	}

	// CurrentOutput: locked read — the Adopt ticker and Start-path outputBuf
	// mutate Output under t.mu; a plain field read races them.
	output := task.CurrentOutput()
	if params.Tail > 0 && len(output) > params.Tail {
		output = "... (truncated) ...\n" + output[len(output)-params.Tail:]
	}

	if output == "" {
		return NewResult(fmt.Sprintf("Task %s has no output yet.", task.ID)), nil
	}

	return NewResult(fmt.Sprintf("[Task %s output (%s, %d bytes)]\n%s",
		task.ID, task.Status, len(output), output)), nil
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

	// Show last 500 chars of output as preview (UTF-8 safe — byte slicing can
	// cut mid-rune for CJK/multibyte content, producing invalid UTF-8).
	preview := task.CurrentOutput()
	if len(preview) > 500 {
		preview = truncateTailPreview(preview, 500)
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
			preview = truncateTailPreview(preview, 500)
		}
		fmt.Fprintf(&sb, "Result Preview:\n%s\n", preview)
	}

	return sb.String()
}

// truncateTailPreview keeps the TAIL of s (up to maxBytes bytes) with a
// "... " prefix, adjusting the cut to a UTF-8 rune boundary so CJK/multibyte
// characters are never sliced mid-rune (invalid UTF-8). Inputs shorter than
// maxBytes are returned unchanged.
func truncateTailPreview(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	tail := s[len(s)-(maxBytes-4):] // reserve 4 bytes for the "... " prefix
	// Drop leading bytes until the slice starts on a UTF-8 rune boundary.
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	return "... " + tail
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
	} else {
		// CurrentOutput: locked read (Adopt ticker / Start outputBuf write under mu).
		taskOut := task.CurrentOutput()
		if taskOut != "" {
			// Sanitize \r overwrites and ANSI escape sequences so that progress
			// bar output (tqdm, curl, etc.) renders cleanly in the TUI.
			output := SanitizeOutput(taskOut)
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
