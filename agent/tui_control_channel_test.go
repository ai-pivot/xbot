package agent

import (
	"context"
	"path/filepath"
	"testing"

	"xbot/bus"
	"xbot/tools"
)

// hasToolDef 报告某 sessionKey 的工具定义里是否含指定工具（走与运行时同一条
// AsDefinitionsForSession 链路）。
func hasToolDef(reg *tools.Registry, sessionKey, name string) bool {
	for _, d := range reg.AsDefinitionsForSession(sessionKey, 0) {
		if d.Name() == name {
			return true
		}
	}
	return false
}

// TestTuiControlIsCLIChannelOnly —— 用户 2026-09-20 要求：tui_control 是 CLI
// 渠道专属工具，**只在 cli 注册**。
//
// 历史 bug：它被 `RegisterCore` 全局注册 ⇒ web/feishu 会话既能看到它（提示词还会
// 引导模型去调用），也能真的调用它 —— 而 web 没有 TUI、也没有 TUIControl 回调，
// 调用必然报 "tui_control: TUI session control is only available in local CLI mode"
// （实测：web 会话里让它改会话名 → 报这个错）。本地/远程 CLI 的 sessionKey 都是
// "cli:..." ⇒ 注册到 cli 渠道对两者同时生效。
func TestTuiControlIsCLIChannelOnly(t *testing.T) {
	dir := t.TempDir()
	ag, err := New(Config{
		WorkDir:        dir,
		DBPath:         filepath.Join(dir, "xbot.db"),
		XbotHome:       dir,
		SandboxMode:    "none",
		MemoryProvider: "flat",
		Bus:            bus.NewMessageBus(),
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}
	t.Cleanup(func() { _ = ag.Close() })

	// ① 可见性：只有 cli 前缀的会话能看到它。
	if !hasToolDef(ag.tools, "cli:/home/smith/src/xbot", "tui_control") {
		t.Error("cli 会话必须能看到 tui_control")
	}
	for _, key := range []string{"web:chat_1", "feishu:chat_1", ""} {
		if hasToolDef(ag.tools, key, "tui_control") {
			t.Errorf("sessionKey %q 不得看到 tui_control（CLI 专属）", key)
		}
	}

	// ② 可执行性：与可见性一致（web 会话拿到名字也执行不了）。
	if _, ok := ag.tools.GetForSession("tui_control", 0, "web:chat_1"); ok {
		t.Error("web 会话不得能执行 tui_control")
	}
	if _, ok := ag.tools.GetForSession("tui_control", 0, "cli:/home/smith/src/xbot"); !ok {
		t.Error("cli 会话必须能执行 tui_control")
	}

	// ③ 不再有全局注册（防止任何绕过会话上下文的调用点把它复活）。
	if _, ok := ag.tools.Get("tui_control"); ok {
		t.Error("tui_control 不得再全局注册（Get 全局查找必须 miss）")
	}
}

// TestSubAgentRunConfig_DropsTuiControl —— 子代理**绝不能操作父会话的 TUI**
// （切换/关闭用户正在看的会话是用户级动作）。
//
// 仅靠"注册到 cli 渠道"不足以挡住它：channel 专属工具会随 `Registry.Clone()`
// 复制进子代理注册表，而 `filterSubAgentTools` 只遍历**全局**工具、删不掉 channel
// 工具。buildSubAgentRunConfig 必须显式移除 —— 这样无论子代理的 sessionKey 长什么
// 样（one-shot 是 "main/<role>"，interactive 形式是带渠道前缀的
// "cli:/repo/<role>:<inst>"）都不会再命中它。
func TestSubAgentRunConfig_DropsTuiControl(t *testing.T) {
	mt, _ := newAgentHistorySession(t)
	a := &Agent{
		multiSession: mt,
		tools:        tools.NewRegistry(),
		skills:       NewSkillStore(t.TempDir(), nil, nil),
	}
	a.tools.RegisterForChannel("cli", &tools.TuiControlTool{})

	ctx := WithUserContext(context.Background(), &UserContext{})
	parentCtx := &tools.ToolContext{
		AgentID:  "main",
		Channel:  "cli",
		ChatID:   "/repo",
		SenderID: "cli_user",
	}
	cfg := a.buildSubAgentRunConfig(ctx, parentCtx, "task", "", nil, tools.SubAgentCapabilities{}, "reviewer", false, "inst-1", "")

	// 前提：父会话（cli）确实看得到 —— 否则这条测试证明不了"移除"生效。
	if !hasToolDef(a.tools, "cli:/repo", "tui_control") {
		t.Fatal("前提不成立：cli 会话本身必须能看到 tui_control")
	}
	// ① 子代理注册表里连这个 channel 工具都不该存在（Clone 后被显式移除）。
	if tool, ok := cfg.Tools.GetChannelTool("cli", "tui_control"); ok {
		t.Fatalf("SubAgent 注册表不得保留 tui_control channel 工具（got %T）", tool)
	}
	// ② 无论用子代理自己的 key 还是带渠道前缀的 interactive 形式 key，都查不到。
	for _, key := range []string{cfg.SessionKey, "cli:/repo/reviewer:inst-1"} {
		if hasToolDef(cfg.Tools, key, "tui_control") {
			t.Fatalf("SubAgent（%s）不得继承 tui_control —— 子代理不能操作父会话 TUI", key)
		}
		if _, ok := cfg.Tools.GetForSession("tui_control", 0, key); ok {
			t.Fatalf("SubAgent（%s）不得能执行 tui_control", key)
		}
	}
}
