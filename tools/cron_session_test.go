package tools

import (
	"path/filepath"
	"strings"
	"testing"

	"xbot/storage/sqlite"
)

func newCronTestTool(t *testing.T) *CronTool {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "cron.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewCronTool(sqlite.NewCronService(db))
}

// jobIDFromResult extracts the job ID from an add result ("Job created: job_xxx\n...").
func jobIDFromResult(t *testing.T, res *ToolResult) string {
	t.Helper()
	const marker = "Job created: "
	idx := strings.Index(res.Summary, marker)
	if idx < 0 {
		t.Fatalf("add result must contain %q, got: %s", marker, res.Summary)
	}
	rest := res.Summary[idx+len(marker):]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[:nl]
	}
	return strings.TrimSpace(rest)
}

// TestCronToolSessionIsolation_ListAndRemove verifies the Cron tool's session
// scoping: each session (channel+chatID) sees and can remove ONLY its own
// jobs. A foreign session's job is invisible in list and unremovable (the
// ownership mismatch reports "job not found", no existence leak). This mirrors
// the web task panel's CronTasks callback (ListJobsByChannelChatID) — the
// tool layer and the panel must agree on the same boundary.
func TestCronToolSessionIsolation_ListAndRemove(t *testing.T) {
	tool := newCronTestTool(t)
	sessA := &ToolContext{Channel: "web", ChatID: "chat-a", SenderID: "user-1"}
	sessB := &ToolContext{Channel: "web", ChatID: "chat-b", SenderID: "user-1"}

	// Add one job from each session.
	resA, err := tool.Execute(sessA, `{"action":"add","message":"session A job","every_seconds":60}`)
	if err != nil {
		t.Fatalf("add from session A: %v", err)
	}
	jobA := jobIDFromResult(t, resA)

	resB, err := tool.Execute(sessB, `{"action":"add","message":"session B job","every_seconds":60}`)
	if err != nil {
		t.Fatalf("add from session B: %v", err)
	}
	jobB := jobIDFromResult(t, resB)

	// Session A lists → sees ONLY its own job.
	listA, err := tool.Execute(sessA, `{"action":"list"}`)
	if err != nil {
		t.Fatalf("list from session A: %v", err)
	}
	if got := listA.Summary; strings.Count(got, "- **job_") != 1 || !strings.Contains(got, jobA) {
		t.Fatalf("session A list must show exactly its own job %s (session-scoped), got:\n%s", jobA, got)
	}
	if strings.Contains(listA.Summary, jobB) {
		t.Fatalf("session A list must NOT show session B's job %s (session-scoped), got:\n%s", jobB, listA.Summary)
	}

	// Session B lists → sees ONLY its own job.
	listB, err := tool.Execute(sessB, `{"action":"list"}`)
	if err != nil {
		t.Fatalf("list from session B: %v", err)
	}
	if got := listB.Summary; strings.Count(got, "- **job_") != 1 || !strings.Contains(got, jobB) {
		t.Fatalf("session B list must show exactly its own job %s (session-scoped), got:\n%s", jobB, got)
	}

	// Session B removes session A's job → rejected (session ownership).
	removeInput := `{"action":"remove","job_id":"` + jobA + `"}`
	res, err := tool.Execute(sessB, removeInput)
	if err == nil {
		t.Fatalf("session B must NOT be able to remove session A's job %s — got success: %+v", jobA, res)
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("cross-session remove must report 'not found' (no existence leak), got: %v", err)
	}
	// The foreign job must still exist (remove was rejected).
	listA2, err := tool.Execute(sessA, `{"action":"list"}`)
	if err != nil {
		t.Fatalf("list A (after rejected cross-session remove): %v", err)
	}
	if !strings.Contains(listA2.Summary, jobA) {
		t.Fatalf("session A's job %s must survive the rejected cross-session remove", jobA)
	}

	// Session A removes its own job → success.
	if _, err := tool.Execute(sessA, removeInput); err != nil {
		t.Fatalf("session A removing its own job: %v", err)
	}
	listA3, err := tool.Execute(sessA, `{"action":"list"}`)
	if err != nil {
		t.Fatalf("list A (after own remove): %v", err)
	}
	if !strings.Contains(listA3.Summary, "No scheduled jobs") {
		t.Fatalf("session A must see no jobs after removing its own, got:\n%s", listA3.Summary)
	}
}

// TestCronToolSubAgentJobsBelongToParentSession verifies SubAgent-created cron
// jobs belong to the PARENT session: a SubAgent's ToolContext carries the root
// origin (Channel+ChatID of the parent session, set by buildSubAgentRunConfig),
// so the job created inside a SubAgent is created with the parent session's
// channel+chatID — the parent session's list shows it and can remove it, and
// a DIFFERENT session cannot. This is the intended design (SubAgent products
// belong to the parent session, mirroring the bg-task/tenant ownership rules).
func TestCronToolSubAgentJobsBelongToParentSession(t *testing.T) {
	tool := newCronTestTool(t)
	// The parent session (main agent's session).
	parent := &ToolContext{Channel: "web", ChatID: "chat-parent", SenderID: "user-1"}
	// The SubAgent's ToolContext: SAME Channel+ChatID as the parent (root
	// origin — buildSubAgentRunConfig sets Channel/ChatID from the parent's
	// origin), different SenderID (the subagent's own agent id).
	subAgent := &ToolContext{Channel: "web", ChatID: "chat-parent", SenderID: "main/explore:mem-1"}
	// An unrelated session.
	other := &ToolContext{Channel: "web", ChatID: "chat-other", SenderID: "user-1"}

	res, err := tool.Execute(subAgent, `{"action":"add","message":"subagent-created job","every_seconds":120}`)
	if err != nil {
		t.Fatalf("add from subagent context: %v", err)
	}
	jobID := jobIDFromResult(t, res)

	// The PARENT session sees the subagent's job (same channel+chatID).
	parentList, err := tool.Execute(parent, `{"action":"list"}`)
	if err != nil {
		t.Fatalf("parent list: %v", err)
	}
	if !strings.Contains(parentList.Summary, jobID) {
		t.Fatalf("parent session must see the subagent-created job %s (subagent ctx carries the parent origin), got:\n%s", jobID, parentList.Summary)
	}

	// An unrelated session does NOT see it.
	otherList, err := tool.Execute(other, `{"action":"list"}`)
	if err != nil {
		t.Fatalf("other-session list: %v", err)
	}
	if strings.Contains(otherList.Summary, jobID) {
		t.Fatalf("unrelated session must NOT see the subagent-created job %s, got:\n%s", jobID, otherList.Summary)
	}

	// The parent session CAN remove the subagent's job (session ownership —
	// SenderID differs but channel+chatID match; the parent manages the job).
	removeInput := `{"action":"remove","job_id":"` + jobID + `"}`
	if _, err := tool.Execute(parent, removeInput); err != nil {
		t.Fatalf("parent session must be able to remove the subagent-created job: %v", err)
	}

	// The unrelated session still cannot (already removed — same "not found").
	if _, err := tool.Execute(other, removeInput); err == nil {
		t.Fatalf("unrelated session must not remove the (already removed) job")
	}
}
