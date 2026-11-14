package agent

// system_reminder_tooldefs_test —— N2a（B 面，2026-10-30）判据：
//
// 历史伪工具名 `system_reminder` **不得出现在工具定义列表里**，
// 而真实调用它必须得到 `unknown tool`（不是整份上下文快照）。
//
// 三条判据（缺一不算完成）：
//   a. TestVisibleToolDefs_NeverExposesSystemReminder —— 构造含该名字 def 的切片，
//      调 visibleToolDefs（perm-control 开 / 关两条路径），断言输出里该名字出现 0 次；
//   b. TestDefaultToolExecutor_SystemReminderIsUnknownTool —— 空注册表 / 真实注册表
//      （不含该名字）走 defaultToolExecutor，调用该名字返回 "unknown tool" 且不带快照；
//   c. TestVisibleToolDefs_DiscriminatingRegistryContainsPseudoTool —— 注册表**确实**
//      含该工具（模拟外部注册源）时，过滤前可见、过滤后不可见。
//      没有这一条，上面的"0 次"可能只是因为注册表本来就没有这个名字而假绿。

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"xbot/llm"
	"xbot/tools"
)

// toolDefNameList 收集工具定义的名字（按定义顺序）。
func toolDefNameList(defs []llm.ToolDefinition) []string {
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.Name())
	}
	return names
}

// countSystemReminderDefs 数输出里 `system_reminder` 出现次数（等价于
// `... | grep -c system_reminder`：这里按名字精确计数，而不是子串匹配）。
func countSystemReminderDefs(defs []llm.ToolDefinition) int {
	n := 0
	for _, name := range toolDefNameList(defs) {
		if name == systemReminderToolName {
			n++
		}
	}
	return n
}

// TestVisibleToolDefs_NeverExposesSystemReminder 覆盖 (a)：
// perm-control 开（early-return 路径）/ 关（fallback 路径）都必须丢弃该 def。
func TestVisibleToolDefs_NeverExposesSystemReminder(t *testing.T) {
	defs := []llm.ToolDefinition{
		&mockTool{name: "Read"},
		&mockTool{name: systemReminderToolName},
		&mockTool{name: "Shell"},
		&mockTool{name: "manage_tools"},
	}

	cases := []struct {
		label     string
		permUsers *PermUsersConfig
	}{
		// perm-control 开启 ⇒ visibleToolDefs 直接 early-return defs
		{"perm-control-on", &PermUsersConfig{DefaultUser: "alice", PrivilegedUser: "root"}},
		// perm-control 关闭 ⇒ 走 fallback（channel 过滤 + hidden args 包装）分支
		{"perm-control-off", nil},
	}

	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			got := visibleToolDefs(defs, c.permUsers, "cli")

			if n := countSystemReminderDefs(got); n != 0 {
				t.Errorf("%s: 工具定义里仍有 %d 个 %q（应为 0）：%v",
					c.label, n, systemReminderToolName, toolDefNameList(got))
			}
			// 判别力反证：不能靠"把 defs 全删掉"通过。
			for _, want := range []string{"Read", "Shell", "manage_tools"} {
				if !containsString(toolDefNameList(got), want) {
					t.Errorf("%s: 正常工具 %q 被一并过滤掉了（got=%v）",
						c.label, want, toolDefNameList(got))
				}
			}
			if len(got) != len(defs)-1 {
				t.Errorf("%s: 期望只少 1 个 def（%d → %d），实际 %d",
					c.label, len(defs), len(defs)-1, len(got))
			}
			// 入参切片不得被原地裁剪（注册表视图可能被复用）。
			if len(defs) != 4 {
				t.Fatalf("%s: visibleToolDefs 修改了入参切片（len=%d，want 4）", c.label, len(defs))
			}
		})
	}
}

// TestDefaultToolExecutor_SystemReminderIsUnknownTool 覆盖 (b)：
// 真实调用 `system_reminder` 必须得到 "unknown tool" —— 不是快照、不是友好提示。
func TestDefaultToolExecutor_SystemReminderIsUnknownTool(t *testing.T) {
	// 该名字既不是合成通知工具（tools/synthetic_hints.go 的 SyntheticToolPrefixes），
	// 也不该被 executor 特判成"注入通道"返回内容 —— 这条断言锁住这个前提。
	if tools.IsSyntheticToolName(systemReminderToolName) {
		t.Fatalf("%q 被归为合成通知工具了：executor 会返回友好结果而不是 unknown tool，"+
			"模型因而无法从错误里学会「这个名字不存在」", systemReminderToolName)
	}

	cases := []struct {
		label string
		reg   *tools.Registry
	}{
		// 空注册表（压缩等内部一次性 Run 的形态）
		{"empty-registry", tools.NewRegistry()},
		// 真实注册表，但没有该名字
		{"real-registry-without-pseudo-tool", newTestRegistry(
			&mockTool{name: "Read"},
			&mockTool{name: "Shell"},
		)},
	}

	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			exec := defaultToolExecutor(&RunConfig{Tools: c.reg})
			res, err := exec(context.Background(), llm.ToolCall{
				ID:        "call_sr_1",
				Name:      systemReminderToolName,
				Arguments: "{}",
			})

			if err == nil {
				t.Fatalf("调用 %q 没有报错（result=%+v）—— 说明它被当成真实工具执行了",
					systemReminderToolName, res)
			}
			if res != nil {
				t.Errorf("调用 %q 返回了 tool result（%+v），应为 nil + error", systemReminderToolName, res)
			}
			if !strings.Contains(err.Error(), "unknown tool") {
				t.Errorf("错误信息 = %q，期望含 \"unknown tool\"", err.Error())
			}
			// 快照泄漏判据：返回内容里不得出现 reminder 的区块标记。
			for _, marker := range []string{"<system-reminder", "<user-msg", "<todos"} {
				if strings.Contains(err.Error(), marker) {
					t.Errorf("错误信息里出现了上下文快照标记 %q：%v", marker, err)
				}
			}
		})
	}
}

// TestVisibleToolDefs_DiscriminatingRegistryContainsPseudoTool 覆盖 (c)：
// 注册表**确实**注册了 `system_reminder`（模拟外部插件 / 会话 MCP server），
// 于是 AsDefinitionsForSession 会把它交给模型 —— 过滤前可见、过滤后不可见。
// 这一条同时证明上面两条测试不是"注册表本来就没这个名字"的假绿。
func TestVisibleToolDefs_DiscriminatingRegistryContainsPseudoTool(t *testing.T) {
	reg := newTestRegistry(&mockTool{name: "Read"})
	// 模拟外部注册源：以历史伪工具名注册一个"真实工具"
	reg.Register(&mockTool{name: systemReminderToolName})

	raw := reg.AsDefinitionsForSession("cli:test", 0)
	if !containsString(toolDefNameList(raw), systemReminderToolName) {
		t.Fatalf("对照失败：注册表里没有 %q —— 这组测试会假绿（不过滤也看不见）",
			systemReminderToolName)
	}
	if n := countSystemReminderDefs(raw); n != 1 {
		t.Fatalf("对照失败：AsDefinitionsForSession 返回 %d 个 %q，期望 1", n, systemReminderToolName)
	}

	cases := []struct {
		label     string
		permUsers *PermUsersConfig
	}{
		{"perm-control-on", &PermUsersConfig{DefaultUser: "alice"}},
		{"perm-control-off", nil},
	}

	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			got := visibleToolDefs(raw, c.permUsers, "cli")
			if n := countSystemReminderDefs(got); n != 0 {
				t.Errorf("%s: 过滤后仍有 %d 个 %q（应为 0）：%v",
					c.label, n, systemReminderToolName, toolDefNameList(got))
			}
			if !containsString(toolDefNameList(got), "Read") {
				t.Errorf("%s: 正常工具 \"Read\" 丢了：%v", c.label, toolDefNameList(got))
			}
		})
	}
}

// TestRun_LLMRequestToolsNeverContainSystemReminder 覆盖 B1：
// 走**真实链路** Run → generateResponse → LLMClient.Generate，抓模型实际收到的
// tools 数组（不是只 grep 源码）。设置 N2A_TOOLS_DUMP=<path> 时把该数组的 name
// 列表落盘，供证据用 grep 复验。
func TestRun_LLMRequestToolsNeverContainSystemReminder(t *testing.T) {
	cases := []struct {
		label     string
		permUsers *PermUsersConfig
	}{
		{"perm-control-on", &PermUsersConfig{DefaultUser: "alice"}},
		{"perm-control-off", nil},
	}

	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			// 模拟外部注册源：注册表里**确实**有一个叫 system_reminder 的真实工具
			reg := newTestRegistry(&mockTool{name: "Read"}, &mockTool{name: "Shell"})
			reg.Register(&mockTool{name: systemReminderToolName})

			mock := &mockLLM{responses: []llm.LLMResponse{{Content: "done"}}}
			out := Run(context.Background(), RunConfig{
				LLMClient: mock,
				Model:     "test",
				Tools:     reg,
				Messages:  baseMessages(),
				AgentID:   "main",
				Channel:   "cli",
				PermUsers: c.permUsers,
			})
			if out.Error != nil {
				t.Fatalf("%s: Run 失败：%v", c.label, out.Error)
			}
			if len(mock.calls) == 0 {
				t.Fatalf("%s: 没有发出任何 LLM 请求，无法验证 tools 数组", c.label)
			}

			// 模型实际收到的最后一个请求的 tools 数组
			last := mock.calls[len(mock.calls)-1]
			names := toolDefNameList(last.Tools)
			joined := strings.Join(names, ",")

			if n := countSystemReminderDefs(last.Tools); n != 0 {
				t.Errorf("%s: 请求 tools 数组里仍有 %d 个 %q：%v",
					c.label, n, systemReminderToolName, names)
			}
			// grep 语义等价断言：整段 name 列表里不得出现该子串
			if strings.Count(joined, systemReminderToolName) != 0 {
				t.Errorf("%s: grep %q 命中：%s", c.label, systemReminderToolName, joined)
			}
			// 反证：正常工具还在（不是空数组假绿）
			if !strings.Contains(joined, "Read") {
				t.Errorf("%s: tools 数组里没有 Read（可能整个数组被清空）：%v", c.label, names)
			}

			// 证据落盘（env-gated，默认不写）
			if dumpPath := os.Getenv("N2A_TOOLS_DUMP"); dumpPath != "" {
				body := fmt.Sprintf("# %s: tools 数组（%d 个）\n%s\n", c.label, len(names), strings.Join(names, "\n"))
				if err := os.WriteFile(dumpPath+"."+c.label, []byte(body), 0o644); err != nil {
					t.Errorf("%s: 写 dump 失败：%v", c.label, err)
				}
			}
		})
	}
}

// TestRun_SystemReminderToolCallIsUnknownTool 覆盖 B2：
// 让 mock LLM **真实发起** tool_call name=system_reminder（注册表里也确实注册了一个
// 会返回上下文快照的同名工具），断言：
//   - 该工具**没有被执行**（快照没有被生成）；
//   - 回喂给模型的 tool 消息是 "unknown tool" 而不是任何上下文内容。
func TestRun_SystemReminderToolCallIsUnknownTool(t *testing.T) {
	const snapshotMarker = "SNAPSHOT-SHOULD-NEVER-REACH-THE-MODEL"

	snapshotCalled := false
	leaky := &mockTool{
		name: systemReminderToolName,
		execFunc: func(_ *tools.ToolContext, _ string) (*tools.ToolResult, error) {
			snapshotCalled = true
			return tools.NewResult("<system-reminder><user-msg>" + snapshotMarker +
				"</user-msg><cwd>/home/smith</cwd><todos/><subagents/><guidelines/></system-reminder>"), nil
		},
	}

	reg := newTestRegistry(&mockTool{name: "Read"})
	reg.Register(leaky)

	mock := &mockLLM{responses: []llm.LLMResponse{
		{ToolCalls: []llm.ToolCall{{ID: "call_sr_real", Name: systemReminderToolName, Arguments: "{}"}}},
		{Content: "done"},
	}}

	out := Run(context.Background(), RunConfig{
		LLMClient: mock,
		Model:     "test",
		Tools:     reg,
		Messages:  baseMessages(),
		AgentID:   "main",
		Channel:   "cli",
	})
	if out.Error != nil {
		t.Fatalf("Run 失败：%v", out.Error)
	}
	if snapshotCalled {
		t.Fatalf("同名工具被真实执行了 —— 上下文快照会流回模型（prompt injection 面）")
	}
	if len(mock.calls) < 2 {
		t.Fatalf("只发生了 %d 次 LLM 调用，工具结果没有回喂（无法验证）", len(mock.calls))
	}

	second := mock.calls[1]
	var toolMsg *llm.ChatMessage
	for i := range second.Messages {
		if second.Messages[i].Role == "tool" && second.Messages[i].ToolCallID == "call_sr_real" {
			toolMsg = &second.Messages[i]
			break
		}
	}
	if toolMsg == nil {
		t.Fatalf("第二次请求里没有 tool_call_id=call_sr_real 的 tool 消息：%+v", second.Messages)
	}
	if !strings.Contains(toolMsg.Content, "unknown tool") {
		t.Errorf("回喂内容 = %q，期望含 \"unknown tool\"", toolMsg.Content)
	}
	if strings.Contains(toolMsg.Content, snapshotMarker) || strings.Contains(toolMsg.Content, "<system-reminder") {
		t.Errorf("回喂内容里出现了上下文快照：%q", toolMsg.Content)
	}
	// 原始输出（证据用）：B2 要求"unknown tool 类错误"的原文可复验。
	t.Logf("B2 回喂给模型的 tool 消息原文 = %q", toolMsg.Content)
}

// TestExecutorsGuardBeforeLookup 覆盖 (d)：两个执行器（SubAgent 的
// defaultToolExecutor、主 Agent 的 buildToolExecutor）都必须在**查注册表之前**
// 就判死 neverExposed 名字。
//
// 为什么需要源码级断言：主 Agent 走的是 buildToolExecutor（engine_wire.go），
// 它先查 session MCP 再查全局注册表；Run 级用例只覆盖得到 defaultToolExecutor。
// 任何一处漏了守卫，插件 / 会话 MCP 注册同名工具都能把快照送回模型，
// 而"另一处有守卫"会让普通测试仍然是绿的 —— 所以必须逐个执行器断言顺序。
func TestExecutorsGuardBeforeLookup(t *testing.T) {
	cases := []struct {
		file       string
		lookupMark string
	}{
		{"engine.go", "cfg.Tools.GetForSession("},
		{"engine_wire.go", "GetSessionMCPManager("},
	}
	const guardMark = "isNeverExposedToolName(tc.Name)"
	for _, c := range cases {
		src, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatalf("读 %s: %v", c.file, err)
		}
		masked := string(maskGoSource(src))
		guard := strings.Index(masked, guardMark)
		if guard < 0 {
			t.Errorf("%s: 找不到守卫 %q —— 执行器没有拦 neverExposed 名字", c.file, guardMark)
			continue
		}
		lookup := strings.Index(masked, c.lookupMark)
		if lookup < 0 {
			t.Errorf("%s: 找不到查找点 %q（断言前提失效，请同步更新本用例）", c.file, c.lookupMark)
			continue
		}
		if guard > lookup {
			t.Errorf("%s: 守卫在查找点之后（guard@%d > lookup@%d）—— 同名工具会先被执行",
				c.file, guard, lookup)
		}
	}
}
