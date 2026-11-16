package agent

// run_config_reminder_policy_test —— C2（2026-10-28 门下省 M3 审计）：
// **表驱动钉死三类 Run 的 reminder 策略期望值**：{main:false, subAgent:false, compress:true}。
//
// 为什么现有静态穷举守卫不够：`run_config_literal_guard_test.go` 只校验字面量
// **写了** `DisableSystemReminder`，不校验值。把 `buildBaseRunConfig` 的字段改成
// `true`（用户可见主 turn 被永久关掉 reminder）→ 静态守卫依然全绿。这是真洞。
//
// 本用例直接**调用构造点**拿返回值断言字段，不扫源码字符串：
//   - main     : (*Agent).buildBaseRunConfig(...)       → 期望 false（对话轮要 reminder）
//   - subAgent : (*Agent).buildSubAgentRunConfig(...)   → 期望 false（子代理是对话轮）
//   - compress : compressRunConfig(...)                 → 期望 true （内部一次性摘要，不是对话轮）
//
// 变异自证（证据 guard-mutation-m3.txt / -m3b.txt）：
//   - 把 buildBaseRunConfig 的字段改成 true  → 本表 FAIL 并指名 main
//   - 把 compressRunConfig 的字段改成 false  → 本表 FAIL 并指名 compress

import (
	"context"
	"testing"

	"xbot/llm"
	"xbot/tools"
)

func TestRunConfigReminderPolicyTable(t *testing.T) {
	// --- main：用户可见主 turn 的构造点 ---
	mt, _ := newAgentHistorySession(t)
	a := &Agent{
		multiSession: mt,
		tools:        tools.NewRegistry(),
		skills:       NewSkillStore(t.TempDir(), nil, nil),
		workDir:      t.TempDir(),
		xbotHome:     t.TempDir(),
		agentsDir:    t.TempDir(),
	}
	ctx := WithUserContext(context.Background(), &UserContext{Model: "test-model"})
	// 第二个返回值不是 error（是调用方用的模式/标志位），本用例只用 RunConfig。
	mainCfg, _ := a.buildBaseRunConfig(
		ctx, "web", "chat-reminder-policy", "u1",
		[]llm.ChatMessage{llm.NewUserMessage("hi")}, "Smith", "1")

	// --- subAgent：子代理对话轮的构造点 ---
	subCfg, subErr := a.buildSubAgentRunConfig(
		ctx,
		&tools.ToolContext{
			AgentID: "main", Channel: "web", ChatID: "chat-reminder-policy",
			SenderID: "u1", UserID: 42, Role: "admin", SenderName: "Smith",
		},
		"task", "", nil, tools.SubAgentCapabilities{}, "reviewer", false, "inst-1", "")
	if subErr != nil {
		t.Fatalf("buildSubAgentRunConfig: %v", subErr)
	}

	// --- compress：内部一次性摘要 Run 的构造点 ---
	compressCfg := compressRunConfig(
		&mockLLM{}, "test-model",
		[]llm.ChatMessage{llm.NewSystemMessage("sys"), llm.NewUserMessage("summarize")},
		1024)

	cases := []struct {
		name string
		got  bool
		want bool
	}{
		{"main", mainCfg.DisableSystemReminder, false},
		{"subAgent", subCfg.DisableSystemReminder, false},
		{"compress", compressCfg.DisableSystemReminder, true},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: DisableSystemReminder = %v, want %v —— 策略被改动（对话轮=false/内部一次性 Run=true），"+
				"关掉对话轮会静默丢失首轮 reminder；放开内部 Run 会破坏压缩请求「末条=指令」契约",
				c.name, c.got, c.want)
		}
	}
	t.Logf("reminder 策略表通过: main=%v subAgent=%v compress=%v",
		mainCfg.DisableSystemReminder, subCfg.DisableSystemReminder, compressCfg.DisableSystemReminder)
}
