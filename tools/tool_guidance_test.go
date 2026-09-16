package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

// fakeInteractiveManager implements InteractiveSubAgentManager so task_read's
// sub-agent path can be asserted without an Agent.
type fakeInteractiveManager struct {
	inspectOut  string
	inspectErr  error
	gotRole     string
	gotInstance string
	gotTail     int
}

func (f *fakeInteractiveManager) RunSubAgent(*ToolContext, string, string, []string, SubAgentCapabilities, string, string, string) (string, error) {
	return "", nil
}

func (f *fakeInteractiveManager) SpawnInteractive(*ToolContext, string, string, string, []string, SubAgentCapabilities, string, string) (string, error) {
	return "", nil
}

func (f *fakeInteractiveManager) SendInteractive(*ToolContext, string, string, string, []string, SubAgentCapabilities, string, string) (string, error) {
	return "", nil
}

func (f *fakeInteractiveManager) UnloadInteractive(*ToolContext, string, string) error { return nil }

func (f *fakeInteractiveManager) InterruptInteractive(*ToolContext, string, string) error { return nil }

func (f *fakeInteractiveManager) InspectInteractive(_ *ToolContext, role, instance string, tail int) (string, error) {
	f.gotRole, f.gotInstance, f.gotTail = role, instance, tail
	return f.inspectOut, f.inspectErr
}

// TestTaskRead_SubAgentUsesInspectLogic — task_read on a sub-agent ID must return
// the SAME recent-progress dump as SubAgent(action="inspect") instead of the old
// "no streaming output" text (sub-agent runs have no output buffer; their progress
// lives in the interactive session).
func TestTaskRead_SubAgentUsesInspectLogic(t *testing.T) {
	mgr := NewBackgroundTaskManager()
	defer mgr.UnregisterSubAgentTask("sub-inspect1")
	mgr.RegisterSubAgentTask("sub-inspect1", "web:chat", "user1", "explore", "inst-1", func() {})

	const dump = "## explore/inst-1  (running, 3 messages)\n### Last Reply:\nINSPECT_MARKER"
	fake := &fakeInteractiveManager{inspectOut: dump}
	toolCtx := &ToolContext{BgTaskManager: mgr, Manager: fake, Ctx: context.Background()}

	res, err := (&TaskReadTool{}).Execute(toolCtx, `{"task_id":"sub-inspect1"}`)
	if err != nil {
		t.Fatalf("task_read on sub-agent returned error: %v", err)
	}
	if !strings.Contains(res.Summary, "INSPECT_MARKER") {
		t.Errorf("expected the inspect dump, got: %q", res.Summary)
	}
	if strings.Contains(res.Summary, "no streaming output") {
		t.Errorf("sub-agent read must not fall back to the placeholder text: %q", res.Summary)
	}
	if fake.gotRole != "explore" || fake.gotInstance != "inst-1" {
		t.Errorf("inspect called with wrong target: role=%q instance=%q", fake.gotRole, fake.gotInstance)
	}
	if fake.gotTail != 5 {
		t.Errorf("default tail should be 5, got %d", fake.gotTail)
	}

	// Explicit tail is forwarded (same parameter as SubAgent action="inspect").
	if _, err := (&TaskReadTool{}).Execute(toolCtx, `{"task_id":"sub-inspect1","tail":12}`); err != nil {
		t.Fatalf("task_read with tail returned error: %v", err)
	}
	if fake.gotTail != 12 {
		t.Errorf("tail should be forwarded to inspect, got %d", fake.gotTail)
	}
}

// TestTaskRead_SubAgentDeadSessionIsReported — a sub-agent whose session is gone
// (unloaded/consolidated) must report WHAT the ID refers to plus the fallback,
// never a bare error.
func TestTaskRead_SubAgentDeadSessionIsReported(t *testing.T) {
	mgr := NewBackgroundTaskManager()
	defer mgr.UnregisterSubAgentTask("sub-gone1")
	sub := mgr.RegisterSubAgentTask("sub-gone1", "web:chat", "user1", "explore", "inst-9", func() {})
	mgr.CloseSubAgentTask(sub.ID, BgTaskDone, "done")

	fake := &fakeInteractiveManager{inspectErr: context.DeadlineExceeded}
	toolCtx := &ToolContext{BgTaskManager: mgr, Manager: fake, Ctx: context.Background()}

	res, err := (&TaskReadTool{}).Execute(toolCtx, `{"task_id":"sub-gone1"}`)
	if err != nil {
		t.Fatalf("expected a graceful message, got error: %v", err)
	}
	for _, want := range []string{"explore", "inst-9", "task_status"} {
		if !strings.Contains(res.Summary, want) {
			t.Errorf("message should mention %q, got: %q", want, res.Summary)
		}
	}
}

// TestTaskRead_SubAgentWithoutInspector — when no interactive manager is wired,
// point the model at the equivalent entry point instead of failing.
func TestTaskRead_SubAgentWithoutInspector(t *testing.T) {
	mgr := NewBackgroundTaskManager()
	defer mgr.UnregisterSubAgentTask("sub-noins1")
	mgr.RegisterSubAgentTask("sub-noins1", "web:chat", "user1", "explore", "inst-2", func() {})

	toolCtx := &ToolContext{BgTaskManager: mgr, Ctx: context.Background()}
	res, err := (&TaskReadTool{}).Execute(toolCtx, `{"task_id":"sub-noins1"}`)
	if err != nil {
		t.Fatalf("expected a graceful message, got error: %v", err)
	}
	if !strings.Contains(res.Summary, "inspect") || !strings.Contains(res.Summary, "explore") {
		t.Errorf("message should point at SubAgent inspect, got: %q", res.Summary)
	}
}

// TestTaskWaitDescription_DiscouragesBlocking — the guidance lives in the tool
// description (the model reads it), so assert it there: AVOID by default, and a
// 1-minute default timeout.
func TestTaskWaitDescription_DiscouragesBlocking(t *testing.T) {
	desc := (&TaskWaitTool{}).Description()
	for _, want := range []string{"AVOID THIS TOOL", "ONLY when", "nothing else useful to do", "explicitly asks"} {
		if !strings.Contains(desc, want) {
			t.Errorf("task_wait description must contain %q (got: %s)", want, desc)
		}
	}
	// 反向断言：不能残留旧的鼓励阻塞文案（同一段里既禁止又鼓励 = 行为契约自相矛盾）。
	for _, forbidden := range []string{
		"Use this instead of running",
		"no wasted iterations on sleep polling",
		"The current iteration blocks until the task(s) are done",
	} {
		if strings.Contains(desc, forbidden) {
			t.Errorf("task_wait description must NOT contain the legacy encouraging wording %q (got: %s)", forbidden, desc)
		}
	}
	params := (&TaskWaitTool{}).Parameters()
	var timeoutDesc string
	for _, p := range params {
		if p.Name == "timeout" {
			timeoutDesc = p.Description
		}
	}
	if !strings.Contains(timeoutDesc, "60") {
		t.Errorf("task_wait default timeout must be 1 min (60s), param description: %q", timeoutDesc)
	}
}

// TestShellGuidance_NoSleepAndOneMinutePromote — shell must tell the model NOT to
// sleep, and its default (auto-promote-to-background) timeout is 1 minute.
func TestShellGuidance_NoSleepAndOneMinutePromote(t *testing.T) {
	if DefaultShellTimeout != 60*time.Second {
		t.Errorf("shell default timeout (auto-promote) must be 60s, got %v", DefaultShellTimeout)
	}
	desc := (&ShellTool{}).Description()
	for _, want := range []string{"DO NOT SLEEP", "background", "delivered to you"} {
		if !strings.Contains(desc, want) {
			t.Errorf("shell description must contain %q", want)
		}
	}
	if strings.Contains(desc, "use task_wait to wait for completion") {
		t.Error("shell description must not push the model towards task_wait")
	}
	for _, p := range (&ShellTool{}).Parameters() {
		if p.Name == "timeout" && !strings.Contains(p.Description, "60") {
			t.Errorf("shell timeout param must advertise the 1 min default, got %q", p.Description)
		}
	}
}

// TestTaskReadDescription_MentionsSubAgent — the description must advertise the
// sub-agent path, otherwise the model never tries it.
func TestTaskReadDescription_MentionsSubAgent(t *testing.T) {
	desc := (&TaskReadTool{}).Description()
	for _, want := range []string{"sub-agent", "inspect", "tail"} {
		if !strings.Contains(strings.ToLower(desc), want) {
			t.Errorf("task_read description must mention %q, got: %s", want, desc)
		}
	}
}

// TestEmbeddedToolGuidanceArtifacts — the packaged explore agent / skill-creator
// skill carry the new guidance (they ship to every install, so guard them).
func TestEmbeddedToolGuidanceArtifacts(t *testing.T) {
	creator, err := ReadEmbeddedSkillFile("skill-creator", "SKILL.md")
	if err != nil {
		t.Fatalf("read embedded skill-creator: %v", err)
	}
	// explore 是可写的内置 agent（用户明确：唯一的内置 agent，不让它写不太好）——
	// 撤回只读约束后，description/rules 不得再出现禁止编辑的表述。
	explore, err := ReadEmbeddedAgentFile("explore")
	if err != nil {
		t.Fatalf("read embedded explore agent: %v", err)
	}
	for _, banned := range []string{"Do NOT use this agent for any editing", "禁止编辑"} {
		if strings.Contains(string(explore), banned) {
			t.Errorf("explore agent must stay writable; unexpected %q", banned)
		}
	}
	if !strings.Contains(string(explore), "FileReplace") {
		t.Error("explore agent keeps its write tools (FileCreate/FileReplace)")
	}

	for _, want := range []string{"enumerate EVERY activation condition", "sole"} {
		if !strings.Contains(string(creator), want) {
			t.Errorf("skill-creator must require enumerating activation conditions (%q)", want)
		}
	}
}

// TestBgTaskTips_NotifyNotWait — the guidance attached to every background task
// (shell background start + timeout auto-promote) must tell the model that the
// result arrives as a notification on completion, point at task_status for a
// non-blocking check, and steer AWAY from task_wait.
//
// Regression guard (user report 2026-09-13): the tips used to advertise
// `Use task_wait (task_id=...)`, which made the model block on a whole idle turn.
func TestBgTaskTips_NotifyNotWait(t *testing.T) {
	tips := bgTaskTips("3f8f492a")
	for _, want := range []string{
		"automatically delivered to you as a notification",
		"Keep working",
		`task_status (task_id=["3f8f492a"])`,
		"Avoid task_wait",
	} {
		if !strings.Contains(tips, want) {
			t.Errorf("bgTaskTips must contain %q (got: %s)", want, tips)
		}
	}
	if strings.Contains(tips, "Use task_wait (task_id=") {
		t.Errorf("bgTaskTips must not push the model towards task_wait (got: %s)", tips)
	}
}

// TestRunningInspectionBegsOffPolling — task_status / task_read 读到"仍在运行"
// 的目标时必须直接劝退轮询（用户 2026-09-13）：不要一直调这个工具、结束会自动
// 通知、把时间用在别的事上。
func TestRunningInspectionBegsOffPolling(t *testing.T) {
	for _, want := range []string{
		"Do NOT keep calling",
		"AUTOMATICALLY as a notification",
		"other useful work",
	} {
		if !strings.Contains(runningTaskGuidanceBody, want) {
			t.Errorf("runningTaskGuidanceBody must contain %q (got: %s)", want, runningTaskGuidanceBody)
		}
	}
	if !strings.Contains(runningTaskGuidanceBody, "task_status (task_id=[...])") {
		t.Error("guidance must point at the non-blocking status check")
	}
	// task_status 的运行中输出必须带上它
	out := formatTask(&BackgroundTask{ID: "3f8f492a", Command: "sleep 100", Status: BgTaskRunning, StartedAt: time.Now()})
	if !strings.Contains(out, runningTaskGuidanceBody) {
		t.Errorf("task_status output for a running task must carry the anti-poll guidance:\n%s", out)
	}
}

// TestTaskFormats_CarryPollingHint — 用户 2026-09-15：「改一下 task_status / task_read /
// SubAgent(inspect)，返回中提示模型不要一直调用这些工具轮询，做有意义的事情」。
// 这三个工具的**返回体**必须携带 PollingHint（后台完成会自动通知，不必轮询）。
func TestTaskFormats_CarryPollingHint(t *testing.T) {
	now := time.Now()
	cases := map[string]string{
		"task_status(running)": formatTask(&BackgroundTask{
			ID: "3f8f492a", Command: "sleep 100", Status: BgTaskRunning, StartedAt: now,
		}),
		"task_status(done)": formatTask(&BackgroundTask{
			ID: "3f8f492a", Command: "ls", Status: BgTaskDone, StartedAt: now, FinishedAt: &now, ExitCode: 0,
		}),
		"task_read(subagent)": formatSubAgentTask(&SubAgentTask{
			ID: "sub-1", Role: "explore", Instance: "i1", Status: BgTaskRunning, StartedAt: now,
		}),
	}
	for name, out := range cases {
		if !strings.Contains(out, PollingHint) {
			t.Errorf("%s 的返回必须包含 PollingHint（别轮询提示），got:\n%s", name, out)
		}
	}
	// 提示本身必须点明两件事：不要反复轮询 + 完成会自动通知。
	for _, want := range []string{"不要反复轮询", "自动以通知送达"} {
		if !strings.Contains(PollingHint, want) {
			t.Errorf("PollingHint 必须包含 %q，got: %q", want, PollingHint)
		}
	}
}
