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
	result := BuildSystemReminder(msgs, nil, true, nil, "main", "/home/smith/project", "session1", nil)

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
	if strings.Contains(result, "<user-msg") {
		t.Errorf("reminder 不得再回吐用户原话（N2a-B 已删除该段），got:\n%s", result)
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
	result := BuildSystemReminder(msgs, nil, true, nil, "main", "", "", nil)
	// N2a-B：用户原话不再回吐（无论新消息还是老消息）—— 反向断言。
	if strings.Contains(result, "<user-msg") {
		t.Errorf("reminder 不得再回吐用户原话, got:\n%s", result)
	}
	if strings.Contains(result, "<cwd>") {
		t.Error("should NOT show cwd when empty")
	}
}

func TestBuildSystemReminder_OldMessage(t *testing.T) {
	msgs := makeMsgs("Refactor the codebase", true)
	result := BuildSystemReminder(msgs, nil, true, nil, "main", "", "", nil)
	if strings.Contains(result, "<user-msg") {
		t.Errorf("reminder 不得再回吐用户原话（旧消息场景同样）, got:\n%s", result)
	}
}

func TestBuildSystemReminder_SubAgent(t *testing.T) {
	msgs := makeMsgs("Do task X", true)
	result := BuildSystemReminder(msgs, nil, true, nil, "subagent-1", "", "", nil)
	if strings.Contains(result, "<user-msg") {
		t.Errorf("SubAgent 也不得回吐用户原话, got:\n%s", result)
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
	result := BuildSystemReminder(msgs, todos, true, nil, "main", "", "", nil)
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
	result := BuildSystemReminder(msgs, nil, true, goal, "main", "", "", nil)
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
	result := BuildSystemReminder(msgs, nil, true, goal, "main", "", "", nil)
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
	result := BuildSystemReminder(msgs, nil, true, goal, "main", "", "", nil)
	if !strings.Contains(result, "<![CDATA[Fix all bugs]]>") {
		t.Error("expected objective as fallback when summary is empty")
	}
}

func TestBuildSystemReminder_Guidelines(t *testing.T) {
	msgs := makeMsgs("Do something", true)
	result := BuildSystemReminder(msgs, nil, true, nil, "main", "", "", nil)
	if !strings.Contains(result, "已完成的过时 TODO（不再相关的条目）直接删除") {
		t.Error("expected 4th guideline about TODO maintenance (mark done + delete stale)")
	}
	guidelineCount := strings.Count(result, "<guideline>")
	if guidelineCount != 4 {
		t.Errorf("expected 4 guidelines, got %d", guidelineCount)
	}
}

// TestBuildSystemReminder_CDATAInjection —— CDATA 注入防护（wrapCDATA）仍然有效。
//
// N2a-B 之后 reminder 里唯一走 wrapCDATA 的内容是 goal summary（用户原话段已删），
// 所以防护断言改从 goal 走：`]]>` 必须被拆成 `]]]]><![CDATA[>`，不得提前闭合 CDATA。
func TestBuildSystemReminder_CDATAInjection(t *testing.T) {
	msgs := makeMsgs("User says ]]> and <xml> injection", true)
	goal := &protocol.GoalInfo{
		Objective: "obj",
		Status:    "active",
		Summary:   "close ]]> and inject <xml>",
	}
	result := BuildSystemReminder(msgs, nil, true, goal, "main", "", "", nil)
	// CDATA injection prevention: ]]> should be split into ]]]]><![CDATA[>
	if !strings.Contains(result, "]]]]><![CDATA[>") {
		t.Errorf("expected CDATA split for ]]> injection, got:\n%s", result)
	}
	if strings.Contains(result, "close ]]> and") {
		t.Errorf("raw ]]> must not survive inside CDATA (would close it early), got:\n%s", result)
	}
	if !strings.Contains(result, "close") || !strings.Contains(result, "inject <xml>") {
		t.Error("expected goal summary content to survive the CDATA split")
	}
}

// TestBuildSystemReminder_DoesNotEchoUserMessage —— 用户消息内容（含被 memory
// 注入块污染过的形态）**一个字都不该进 reminder**。
//
// 历史：本条曾是「从用户消息里提取目标」的过滤器测试（要把 memory CDATA 块从
// 回吐内容里剔除）。N2a-B 把整个回吐段删掉之后，「剔除得干不干净」这个命题自动成立 ——
// 这里保留成一条**回归守卫**：任何人都别把用户原文重新塞回 reminder。
func TestBuildSystemReminder_DoesNotEchoUserMessage(t *testing.T) {
	msgs := makeMsgs("User message\n<system-reminder><![CDATA[# Memory\nsome memory content]]></system-reminder>\nactual user request", true)
	result := BuildSystemReminder(msgs, nil, true, nil, "main", "", "", nil)
	if strings.Contains(result, "<user-msg") {
		t.Errorf("reminder 不得包含任何回吐段, got:\n%s", result)
	}
	if strings.Contains(result, "# Memory") {
		t.Error("should NOT include system-reminder CDATA block from user message")
	}
	if strings.Contains(result, "some memory content") {
		t.Error("should NOT include system-reminder CDATA content")
	}
	if strings.Contains(result, "actual user request") || strings.Contains(result, "User message") {
		t.Error("用户消息正文不得出现在 reminder 里（N2a-B：回吐段已删）")
	}
	// 骨架必须还在（别把整段删干净）
	for _, want := range []string{`<system-reminder role="reminder">`, "<note>", "<guidelines>"} {
		if !strings.Contains(result, want) {
			t.Errorf("reminder 骨架缺失 %q, got:\n%s", want, result)
		}
	}
}

// 用户 2026-09-17 实证 bug：memory 注入的 CDATA 正文以空行开头（"# Memory\n\n## Core"），
// 旧的 `inGuide && trimmed == ""` 规则被该空行提前解除跳过模式 ⇒ 包装行被吞、整块
// ## Core memory 被当成"用户消息"回显进每一轮的回吐段（每迭代重复几千字符，
// 且让模型把 memory 误读成用户输入）。
//
// N2a-B 之后回吐段整体删除，本条退化为「memory 正文绝不出现在 reminder 里」的守卫 ——
// 保留它是因为 memory 的 CDATA 内容每轮都在消息历史里，是最容易被顺手再抄一遍的东西。
func TestBuildSystemReminder_MemoryBlockDoesNotLeakIntoUserMsg(t *testing.T) {
	userMsg := "real user request\n\n" +
		"<system-reminder><![CDATA[# Memory\n\n## Core\nP0 INCIDENT: leaked body\n\n## Recent Sessions\nsession 1\n]]></system-reminder>"
	result := BuildSystemReminder(makeMsgs(userMsg, true), nil, true, nil, "main", "", "", nil)

	if strings.Contains(result, "## Core") || strings.Contains(result, "P0 INCIDENT") {
		t.Errorf("memory body leaked into the reminder (must never be echoed), got:\n%s", result)
	}
	if strings.Contains(result, "## Recent Sessions") {
		t.Error("memory trailing sections leaked into the reminder")
	}
	if strings.Contains(result, "real user request") {
		t.Errorf("用户原文不得回吐（N2a-B 删除了回吐段）, got:\n%s", result)
	}
}

func TestBuildSystemReminder_Empty(t *testing.T) {
	result := BuildSystemReminder(nil, nil, true, nil, "main", "", "", nil)
	if result != "" {
		t.Errorf("expected empty result for nil messages, got:\n%s", result)
	}
}

func TestBuildSystemReminder_NoContextEditHints(t *testing.T) {
	msgs := makeMsgs("[2026-03-21 23:08:51 CST]\n[adm]\nUse context_edit to update settings", true)
	result := BuildSystemReminder(msgs, nil, true, nil, "main", "", "", nil)
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
	result := BuildSystemReminder(msgs, nil, true, nil, "main", "", "", subAgents)
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
// N2a（2026-10-30）：本谓词与 systemReminderInsertEvery=10 **原样保留** ——
// 这次只改落点（fake tool pair → 尾部内容拼接），频率改动属 N2b。
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

	empty := BuildSystemReminder(makeMsgs("Fix the bug", true), nil, true, nil, "main", "", "", nil)
	if !strings.Contains(empty, emptyTag) {
		t.Errorf("空 todo（且可知）必须输出引导段 %q，got:\n%s", emptyTag, empty)
	}
	if strings.Contains(empty, "<todos>") {
		t.Error("空 todo 时不得输出非空 <todos> 容器（会与新引导段语义冲突）")
	}

	withTodos := BuildSystemReminder(makeMsgs("Fix the bug", true),
		[]TodoProgressItem{{Text: "First task", Status: "done"}}, true, nil, "main", "", "", nil)
	if strings.Contains(withTodos, `status="empty"`) {
		t.Error("有 todo 时不得出现 status=\"empty\" —— 非空输出必须保持原样")
	}
	if !strings.Contains(withTodos, "<todos><todo status=\"done\" id=\"1\">First task</todo></todos>") {
		t.Errorf("非空 todos 容器结构必须与旧实现逐字节一致，got:\n%s", withTodos)
	}
}

// TestBuildSystemReminder_TodosUnknownOmitsBlock —— N2a-B 的核心语义：
// 「todo 是否可知」是**显式入参**，不可知时整段不输出。
//
// 为什么必须这样：不可知（没接 TodoManager / key 为空）时输出 status="empty"
// 等于**编造状态** —— 实测 SubAgent 明明有自己的 todo 列表，注入块却每轮都写
// 「尚无计划」，模型据此重复建清单。过期/错误状态比没有状态更坏。
func TestBuildSystemReminder_TodosUnknownOmitsBlock(t *testing.T) {
	msgs := makeMsgs("Fix the bug", true)

	for _, items := range [][]TodoProgressItem{
		nil,
		{{Text: "First task", Status: "done"}},
	} {
		// todosKnown=false 时即便有 items 也不可信（调用方说不知道 ⇒ 一律不输出）
		got := BuildSystemReminder(msgs, items, false, nil, "main", "", "", nil)
		if strings.Contains(got, "<todos") {
			t.Errorf("todosKnown=false 时不得输出任何 <todos 段（items=%v），got:\n%s", items, got)
		}
		if strings.Contains(got, "尚无计划") {
			t.Errorf("todosKnown=false 时不得出现「尚无计划」（那是编造的假状态），got:\n%s", got)
		}
		// 骨架必须一律保留
		for _, want := range []string{
			`<system-reminder role="reminder">`, "<note>", "<guidelines>", "</system-reminder>",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("todosKnown=false 时骨架 %q 必须保留，got:\n%s", want, got)
			}
		}
	}

	// 反面对照：可知 + 空 ⇒ 必须给引导段（与上面形成判别对，防止「永远不输出」假绿）
	known := BuildSystemReminder(msgs, nil, true, nil, "main", "", "", nil)
	if !strings.Contains(known, `<todos status="empty">`) {
		t.Errorf("todosKnown=true 且空时必须输出 status=\"empty\" 引导段，got:\n%s", known)
	}
}
