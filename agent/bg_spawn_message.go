package agent

import "fmt"

// bgSpawnMessage is the SINGLE source of the sub-agent background spawn message.
//
// Both spawn paths (interactive — interactive.go — and one-shot — engine_wire.go)
// append it to their reply. Keeping one implementation is deliberate: a second
// copy drifts (the shell background tips had exactly that failure mode), and the
// wording is a behavioural contract — the model reads it.
//
// Contract (user requirement 2026-09-13): the result is delivered as a
// notification when the sub-agent finishes → use task_status for a non-blocking
// check → avoid task_wait (it blocks a whole turn doing nothing).
//
// Guarded by TestBgSpawnMessage_NotifyNotWait.
func bgSpawnMessage(taskID string) string {
	return fmt.Sprintf(
		"\n\nBackground task ID: %s. Its result is delivered to you automatically as a notification when it finishes — keep working; check progress with task_status (task_id=[%q]); avoid task_wait (it blocks a whole turn doing nothing).",
		taskID, taskID,
	)
}
