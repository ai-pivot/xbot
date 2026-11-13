package agent

import (
	"strings"
	"testing"

	"xbot/tools"
)

// An automated event belongs to the session that created it. Rendering it as
// "a message from the parent agent" made a sub-agent's own task completion look
// like the parent had forwarded it (user report 2026-11-13).
func TestDeliveredMessageContentLabelsOrigin(t *testing.T) {
	const body = "[System Notification] Background task cfa4ac33 completed. Status: done"

	auto := deliveredMessageContent(triggerSourceBackgroundTask, body)
	if strings.Contains(auto, "来自主 agent") {
		t.Fatalf("background-task completion must not claim to come from the parent agent: %s", auto)
	}
	if !strings.Contains(auto, "后台任务") || !strings.Contains(auto, body) {
		t.Fatalf("background-task label lost the event body: %s", auto)
	}

	cron := deliveredMessageContent(triggerSourceCron, body)
	if strings.Contains(cron, "来自主 agent") || !strings.Contains(cron, "定时任务") {
		t.Fatalf("cron event mislabelled: %s", cron)
	}

	human := deliveredMessageContent("", "please continue")
	if !strings.Contains(human, "来自主 agent") || !strings.Contains(human, "please continue") {
		t.Fatalf("a real parent message must keep its original framing: %s", human)
	}
}

// The origin must survive the queue -> drain -> inject hop, otherwise the
// injector cannot tell an automated event from a human message.
func TestPendingMessageDrainPreservesOrigin(t *testing.T) {
	ia := &interactiveAgent{}
	ia.pendingMessages = []pendingUserMsg{
		{content: "task done", origin: triggerSourceBackgroundTask},
		{content: "please continue"},
	}
	got := ia.wirePendingMessageDrain("agent:web:chat/role:inst")()
	if len(got) != 2 {
		t.Fatalf("drained %d notifications, want 2", len(got))
	}
	first, ok := got[0].(*tools.QueuedUserMessage)
	if !ok || first.Origin != triggerSourceBackgroundTask || first.Content != "task done" {
		t.Fatalf("automated origin lost on drain: %#v", got[0])
	}
	second := got[1].(*tools.QueuedUserMessage)
	if second.Origin != "" {
		t.Fatalf("explicit message gained a fabricated origin: %#v", second)
	}
	// A real message must still fail-open the parent notification.
	if !ia.userInputJoined.Load() {
		t.Fatal("explicit human input no longer marks userInputJoined")
	}
}
