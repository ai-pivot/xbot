package tools

import "encoding/json"

// SyntheticToolHints is the UI-only payload attached to injected (synthetic)
// system notifications: background task completion, sub-agent completion, cron
// fires, interjections, cancel markers, …
//
// It travels in ToolProgress.ToolHints / IterationToolSnapshot.ToolHints and is
// consumed by the web renderer (web/src/components/agent/ToolRender.tsx) to draw
// a rich card — the ORIGINAL task/command, status, duration and an output
// preview — instead of dumping the raw notification text.
//
// It is NEVER sent to the model. The model only sees the formatted tool-result
// text (FormatBgTaskCompletion / FormatSubAgentBgNotify / …), so nothing here
// has to be LLM-friendly; it exists purely for presentation. That also means
// fields can be added freely without touching prompts.
type SyntheticToolHints struct {
	// Kind selects the card layout in the web UI:
	// bg_task | subagent | cron | async | interrupt | cancel | loop | ask_user | delivered
	Kind string `json:"kind"`
	// TaskID identifies a background task (for the "view full output" affordance).
	TaskID string `json:"task_id,omitempty"`
	// Task is the ORIGINAL thing the user asked for: the shell command for a
	// background task, or the sub-agent's task description.
	Task string `json:"task,omitempty"`
	// Role/Instance identify a sub-agent.
	Role     string `json:"role,omitempty"`
	Instance string `json:"instance,omitempty"`
	// Status is the terminal state (done/error/killed/cancelled).
	Status string `json:"status,omitempty"`
	// ExitCode is set for background shell tasks (nil when not applicable).
	ExitCode *int `json:"exit_code,omitempty"`
	// ElapsedMS is the wall-clock duration.
	ElapsedMS int64 `json:"elapsed_ms,omitempty"`
	// Message carries the body of message-like notifications
	// (cron fired / async message / interstitial interjection).
	Message string `json:"message,omitempty"`
	// Output is a rune-safe preview of the produced output.
	Output string `json:"output,omitempty"`
	// Error carries a failure reason when Status is error/killed.
	Error string `json:"error,omitempty"`
}

// maxSyntheticPreviewBytes bounds the UI preview carried in hints. The web card
// collapses it; the full text stays available through task_read /
// offload_recall / the sub-agent session.
const maxSyntheticPreviewBytes = 4000

// EncodeSyntheticToolHints marshals hints for the UI channel. Returns "" on
// failure so callers can leave ToolHints empty (the web then falls back to
// rendering Summary).
func EncodeSyntheticToolHints(h SyntheticToolHints) string {
	b, err := json.Marshal(h)
	if err != nil {
		return ""
	}
	return string(b)
}

// BgTaskHints builds the UI payload for a finished background shell task —
// original command, status, exit code, duration and an output preview.
func BgTaskHints(t *BackgroundTask) SyntheticToolHints {
	if t == nil {
		return SyntheticToolHints{Kind: "bg_task"}
	}
	h := SyntheticToolHints{
		Kind:   "bg_task",
		TaskID: t.ID,
		Task:   t.Command,
		Status: string(t.Status),
		Error:  t.Error,
	}
	exit := t.ExitCode
	h.ExitCode = &exit
	if t.FinishedAt != nil {
		h.ElapsedMS = t.FinishedAt.Sub(t.StartedAt).Milliseconds()
	}
	h.Output = TruncateHeadPreview(t.CurrentOutput(), maxSyntheticPreviewBytes)
	return h
}

// SubAgentHints builds the UI payload for a finished sub-agent: role/instance,
// the ORIGINAL task it was spawned with, duration and a result preview.
func SubAgentHints(n *SubAgentBgNotify) SyntheticToolHints {
	if n == nil {
		return SyntheticToolHints{Kind: "subagent"}
	}
	h := SyntheticToolHints{
		Kind:      "subagent",
		Role:      n.Role,
		Instance:  n.Instance,
		Task:      n.Task,
		Status:    "done",
		ElapsedMS: n.Elapsed.Milliseconds(),
		Output:    TruncateHeadPreview(n.Content, maxSyntheticPreviewBytes),
	}
	return h
}

// MessageHints builds the UI payload for message-like notifications that have
// no task lifecycle (cron fires, peer/webhook messages, interjections, …).
func MessageHints(kind, message string) SyntheticToolHints {
	return SyntheticToolHints{
		Kind:    kind,
		Message: message,
		Output:  TruncateHeadPreview(message, maxSyntheticPreviewBytes),
	}
}
