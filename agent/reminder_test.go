package agent

import (
	"fmt"
	"html"
	"path/filepath"
	"strings"
	"testing"

	"xbot/llm"
	"xbot/protocol"
)

func makeMsgs(userContent string, withTools bool) []llm.ChatMessage {
	msgs := []llm.ChatMessage{
		{Role: "user", Content: userContent},
	}
	if withTools {
		msgs = append(msgs,
			llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "t1", Name: "Read", Arguments: `{}`}}},
			llm.NewToolMessage("Read", "t1", "{}", "ok"),
		)
	}
	return msgs
}

func TestBuildSystemReminder_Basic(t *testing.T) {
	msgs := makeMsgs("Fix the login bug", true)
	result := BuildSystemReminder(msgs, nil, nil, "main", "/home/smith/project", "session1", nil)

	if !strings.Contains(result, `<system-reminder role="reminder">`) {
		t.Error("expected system-reminder with role=reminder attribute")
	}
	if !strings.Contains(result, "<note>") {
		t.Error("expected <note> element (auto-injected reminder marker)")
	}
	// note 必须从简（用户 2026-09-17：原文太长且可能被误读）——不得再出现
	// "do not acknowledge, reply to, or confirm it" 这类反向暗示。
	if strings.Contains(result, "do not acknowledge") {
		t.Error("note must stay short — the verbose do-not-acknowledge wording was removed on purpose")
	}
	if !strings.Contains(result, "<user-msg><![CDATA[Fix the login bug]]></user-msg>") {
		t.Errorf("expected <user-msg> with CDATA, got:\n%s", result)
	}
	// resolveAbsolutePath runs the cwd through filepath.Abs — on Windows the
	// Unix-style "/home/smith/project" becomes "C:\\home\\smith\\project".
	// Compute the expectation the same way so the assertion is portable.
	wantCwd, err := filepath.Abs("/home/smith/project")
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	if !strings.Contains(result, fmt.Sprintf("<cwd>%s</cwd>", html.EscapeString(wantCwd))) {
		t.Errorf("expected <cwd> element with %q, got:\n%s", wantCwd, result)
	}
	if strings.Contains(result, "<task>") || strings.Contains(result, "<kind>") {
		t.Error("should NOT have old <task>/<kind> elements")
	}
	if strings.Contains(result, "<working-dir>") {
		t.Error("should NOT have old <working-dir> element (renamed to <cwd>)")
	}
}

func TestBuildSystemReminder_NewMessage(t *testing.T) {
	msgs := makeMsgs("Fix the login bug", false)
	result := BuildSystemReminder(msgs, nil, nil, "main", "", "", nil)
	if !strings.Contains(result, "<user-msg><![CDATA[Fix the login bug]]></user-msg>") {
		t.Errorf("expected user-msg with CDATA for fresh message, got:\n%s", result)
	}
	if strings.Contains(result, "<cwd>") {
		t.Error("should NOT show cwd when empty")
	}
}

func TestBuildSystemReminder_OldMessage(t *testing.T) {
	msgs := makeMsgs("Refactor the codebase", true)
	result := BuildSystemReminder(msgs, nil, nil, "main", "", "", nil)
	if !strings.Contains(result, "<user-msg><![CDATA[Refactor the codebase]]></user-msg>") {
		t.Errorf("expected user-msg for old message (tools after user), got:\n%s", result)
	}
}

func TestBuildSystemReminder_SubAgent(t *testing.T) {
	msgs := makeMsgs("Do task X", true)
	result := BuildSystemReminder(msgs, nil, nil, "subagent-1", "", "", nil)
	if !strings.Contains(result, "<user-msg><![CDATA[Do task X]]></user-msg>") {
		t.Errorf("SubAgent should show user-msg, got:\n%s", result)
	}
	// SubAgent should NOT have subagents section
	if strings.Contains(result, "<subagents>") {
		t.Error("SubAgent should NOT have <subagents> section")
	}
}

func TestBuildSystemReminder_WithTodos(t *testing.T) {
	msgs := makeMsgs("Fix the bug", true)
	todos := []TodoProgressItem{
		{Text: "First task", Status: "done"},
		{Text: "Second task", Status: "pending"},
	}
	result := BuildSystemReminder(msgs, todos, nil, "main", "", "", nil)
	if !strings.Contains(result, `<todo status="done" id="1">First task</todo>`) {
		t.Errorf("expected structured todo item 1 (done), got:\n%s", result)
	}
	if !strings.Contains(result, `<todo status="pending" id="2">Second task</todo>`) {
		t.Errorf("expected structured todo item 2 (not done), got:\n%s", result)
	}
	if !strings.Contains(result, "<todos>") || !strings.Contains(result, "</todos>") {
		t.Error("expected <todos> wrapper element")
	}
}

func TestBuildSystemReminder_WithGoal(t *testing.T) {
	msgs := makeMsgs("Fix the bug", true)
	goal := &protocol.GoalInfo{
		Objective: "Fix all login bugs",
		Status:    "active",
		Summary:   "Fixed auth and session bugs",
	}
	result := BuildSystemReminder(msgs, nil, goal, "main", "", "", nil)
	if !strings.Contains(result, `<goal status="active">`) {
		t.Errorf("expected <goal status=active>, got:\n%s", result)
	}
	if !strings.Contains(result, "<![CDATA[Fixed auth and session bugs]]>") {
		t.Error("expected goal summary in CDATA")
	}
}

func TestBuildSystemReminder_GoalCompleted(t *testing.T) {
	msgs := makeMsgs("Fix the bug", true)
	goal := &protocol.GoalInfo{
		Objective: "Fix all bugs",
		Status:    "completed",
		Summary:   "All done",
	}
	result := BuildSystemReminder(msgs, nil, goal, "main", "", "", nil)
	if strings.Contains(result, "<goal") {
		t.Error("should NOT show goal when status is completed (only active goals)")
	}
}

func TestBuildSystemReminder_GoalNoSummary(t *testing.T) {
	msgs := makeMsgs("Fix the bug", true)
	goal := &protocol.GoalInfo{
		Objective: "Fix all bugs",
		Status:    "active",
		Summary:   "",
	}
	result := BuildSystemReminder(msgs, nil, goal, "main", "", "", nil)
	if !strings.Contains(result, "<![CDATA[Fix all bugs]]>") {
		t.Error("expected objective as fallback when summary is empty")
	}
}

func TestBuildSystemReminder_Guidelines(t *testing.T) {
	msgs := makeMsgs("Do something", true)
	result := BuildSystemReminder(msgs, nil, nil, "main", "", "", nil)
	if !strings.Contains(result, "已完成的过时 TODO（不再相关的条目）直接删除") {
		t.Error("expected 4th guideline about TODO maintenance (mark done + delete stale)")
	}
	guidelineCount := strings.Count(result, "<guideline>")
	if guidelineCount != 4 {
		t.Errorf("expected 4 guidelines, got %d", guidelineCount)
	}
}

func TestBuildSystemReminder_CDATAInjection(t *testing.T) {
	msgs := makeMsgs("User says ]]> and <xml> injection", true)
	result := BuildSystemReminder(msgs, nil, nil, "main", "", "", nil)
	// CDATA injection prevention: ]]> should be split into ]]]]><![CDATA[>
	if !strings.Contains(result, "]]]]><![CDATA[>") {
		t.Errorf("expected CDATA split for ]]> injection, got:\n%s", result)
	}
	// The split content should still be valid
	if !strings.Contains(result, "User says") {
		t.Error("expected user message content to be present after CDATA split")
	}
}

func TestBuildSystemReminder_FiltersSystemReminderBlock(t *testing.T) {
	msgs := makeMsgs("User message\n<system-reminder><![CDATA[# Memory\nsome memory content]]></system-reminder>\nactual user request", true)
	result := BuildSystemReminder(msgs, nil, nil, "main", "", "", nil)
	// The system-reminder block from user message should be filtered out
	if strings.Contains(result, "# Memory") {
		t.Error("should NOT include system-reminder CDATA block from user message")
	}
	if strings.Contains(result, "some memory content") {
		t.Error("should NOT include system-reminder CDATA content")
	}
	if !strings.Contains(result, "User message") || !strings.Contains(result, "actual user request") {
		t.Error("should include actual user message content")
	}
}

// 用户 2026-09-17 实证 bug：memory 注入的 CDATA 正文以空行开头（"# Memory\n\n## Core"），
// 旧的 `inGuide && trimmed == ""` 规则被该空行提前解除跳过模式 ⇒ 包装行被吞、整块
// ## Core memory 被当成"用户消息"回显进每一轮的 <user-msg>（每迭代重复几千字符，
// 且让模型把 memory 误读成用户输入）。
func TestBuildSystemReminder_MemoryBlockDoesNotLeakIntoUserMsg(t *testing.T) {
	userMsg := "real user request\n\n" +
		"<system-reminder><![CDATA[# Memory\n\n## Core\nP0 INCIDENT: leaked body\n\n## Recent Sessions\nsession 1\n]]></system-reminder>"
	result := BuildSystemReminder(makeMsgs(userMsg, true), nil, nil, "main", "", "", nil)

	if strings.Contains(result, "## Core") || strings.Contains(result, "P0 INCIDENT") {
		t.Errorf("memory body leaked into the reminder (must be skipped until </system-reminder>), got:\n%s", result)
	}
	if strings.Contains(result, "## Recent Sessions") {
		t.Error("memory trailing sections leaked into the reminder")
	}
	if !strings.Contains(result, "real user request") {
		t.Errorf("actual user text must survive, got:\n%s", result)
	}
}

func TestBuildSystemReminder_Empty(t *testing.T) {
	result := BuildSystemReminder(nil, nil, nil, "main", "", "", nil)
	if result != "" {
		t.Errorf("expected empty result for nil messages, got:\n%s", result)
	}
}

func TestBuildSystemReminder_NoContextEditHints(t *testing.T) {
	msgs := makeMsgs("[2026-03-21 23:08:51 CST]\n[adm]\nUse context_edit to update settings", true)
	result := BuildSystemReminder(msgs, nil, nil, "main", "", "", nil)
	// Should not contain the timestamps and user name
	if strings.Contains(result, "2026-03-21") {
		t.Error("should NOT contain timestamp")
	}
	if strings.Contains(result, "[adm]") {
		t.Error("should NOT contain user name tag")
	}
}

func TestBuildSystemReminder_SubAgentStatus(t *testing.T) {
	msgs := makeMsgs("Do something", true)
	subAgents := []SubAgentStatus{
		{Role: "explore", Instance: "search-1", Running: true},
		{Role: "coder", Instance: "fix-1", Running: false},
	}
	result := BuildSystemReminder(msgs, nil, nil, "main", "", "", subAgents)
	if !strings.Contains(result, `<subagent role="explore" status="running">explore/search-1</subagent>`) {
		t.Errorf("expected running subagent with role, got:\n%s", result)
	}
	if !strings.Contains(result, `<subagent role="coder" status="idle">coder/fix-1</subagent>`) {
		t.Errorf("expected idle subagent with role, got:\n%s", result)
	}
}

// TestShouldInsertSystemReminder —— 谓词参数化（G1，2026-10-28 御前定稿）：
// 「本次 Run 的第一轮」= iteration == iterStart+1；其后每 10 轮一次；
// 首轮与下一个 10k **相邻**时跳过该 10k（保住「相邻两轮不同时注入」）。
//
// 历史：旧表格只喂 iteration、把 iterStart 隐含为 0，且 `{1,false}` 把 bug 当期望。
func TestShouldInsertSystemReminder(t *testing.T) {
	cases := []struct {
		iterStart int
		iter      int
		want      bool
	}{
		// iterStart=0：首轮 1，10/20/30 … 正常
		{0, 1, true}, {0, 2, false}, {0, 9, false},
		{0, 10, true}, {0, 11, false}, {0, 19, false},
		{0, 20, true}, {0, 21, false}, {0, 30, true}, {0, 40, true}, {0, 100, true},
		// iterStart=1：首轮 2；10 不相邻（2+1=3）→ 2, 10, 20
		{1, 1, false}, {1, 2, true}, {1, 10, true}, {1, 20, true},
		// iterStart=4：首轮 5；10 不相邻（6）→ 5, 10, 20
		{4, 4, false}, {4, 5, true}, {4, 10, true}, {4, 20, true},
		// iterStart=8：首轮 9；10 与 9 相邻 ⇒ 跳过 10 → 9, 20
		{8, 8, false}, {8, 9, true}, {8, 10, false}, {8, 19, false}, {8, 20, true},
		// iterStart=18：首轮 19；20 相邻 ⇒ 跳过 → 19, 30
		{18, 18, false}, {18, 19, true}, {18, 20, false}, {18, 29, false}, {18, 30, true},
		// iterStart=9：首轮正好是 10（与 10k 重合，只算一次）→ 10, 20
		{9, 9, false}, {9, 10, true}, {9, 11, false}, {9, 20, true},
	}
	for _, c := range cases {
		if got := shouldInsertSystemReminder(c.iter, c.iterStart); got != c.want {
			t.Errorf("shouldInsertSystemReminder(%d, iterStart=%d) = %v, want %v",
				c.iter, c.iterStart, got, c.want)
		}
	}
}

// TestBuildSystemReminder_EmptyTodosShowsGuidance —— 空 todo 不能整段静默消失。
//
// 旧实现：`if len(todoItems) > 0 { <todos>…</todos> }`，没有计划时什么都不写 ——
// 模型看不到「该建计划」的信号，首轮 reminder 退化成一条纯状态播报。
// 新实现：显式 <todos status="empty"> 说明「当前无计划 + 多步任务应先建清单」。
//
// 同时锁反向：有 todo 时输出必须**完全不变**（不得混入 empty 段）。
func TestBuildSystemReminder_EmptyTodosShowsGuidance(t *testing.T) {
	const emptyTag = `<todos status="empty">尚无计划：多步任务应先用 TodoWrite 建立待办清单，再逐项推进；单项琐碎任务可跳过</todos>`

	empty := BuildSystemReminder(makeMsgs("Fix the bug", true), nil, nil, "main", "", "", nil)
	if !strings.Contains(empty, emptyTag) {
		t.Errorf("空 todo 必须输出引导段 %q，got:\n%s", emptyTag, empty)
	}
	if strings.Contains(empty, "<todos>") {
		t.Error("空 todo 时不得输出非空 <todos> 容器（会与新引导段语义冲突）")
	}

	withTodos := BuildSystemReminder(makeMsgs("Fix the bug", true),
		[]TodoProgressItem{{Text: "First task", Status: "done"}}, nil, "main", "", "", nil)
	if strings.Contains(withTodos, `status="empty"`) {
		t.Error("有 todo 时不得出现 status=\"empty\" —— 非空输出必须保持原样")
	}
	if !strings.Contains(withTodos, "<todos><todo status=\"done\" id=\"1\">First task</todo></todos>") {
		t.Errorf("非空 todos 容器结构必须与旧实现逐字节一致，got:\n%s", withTodos)
	}
}
