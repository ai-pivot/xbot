package agent

import (
	"strings"
	"testing"
)

// TestBgSpawnMessage_NotifyNotWait — the sub-agent background spawn message
// (interactive + one-shot, both via bgSpawnMessage) hands out a task_id; it must
// present task_status as the non-blocking way to check and steer the model AWAY
// from task_wait.
//
// Regression guard (user report 2026-09-13): the message used to advertise
// `Use task_wait (task_id=...) to wait for completion`, which made the main
// agent block on a whole idle turn instead of continuing to work.
func TestBgSpawnMessage_NotifyNotWait(t *testing.T) {
	msg := bgSpawnMessage("sub-1eefac7a")
	for _, want := range []string{
		"Background task ID: sub-1eefac7a",
		"automatically as a notification",
		"keep working",
		`task_status (task_id=["sub-1eefac7a"])`,
		"avoid task_wait",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("bgSpawnMessage must contain %q (got: %s)", want, msg)
		}
	}
	if strings.Contains(msg, "Use task_wait (task_id=") {
		t.Errorf("bgSpawnMessage must not push the model towards task_wait (got: %s)", msg)
	}
}
