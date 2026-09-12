package agent

import (
	"context"
	"testing"

	"xbot/tools"
)

// TestSubAgentRunConfig_WiresBgTaskManagerAndIdentity —— SubAgent 的 RunConfig
// 必须把主 Agent 的后台任务管理器与身份三件套接出去。
//
// 用户实测 bug：SubAgent 里跑 `cargo check` 被 auto-promote 到后台时直接失败
// "background tasks not supported (BgTaskManager not configured)" ——
// buildToolContext 只从 cfg.BgTaskManager 取 tc.BgTaskManager，而
// buildSubAgentRunConfig 漏了这个字段（只有 buildMainRunConfig 设了）。
func TestSubAgentRunConfig_WiresBgTaskManagerAndIdentity(t *testing.T) {
	mt, _ := newAgentHistorySession(t)
	bgMgr := tools.NewBackgroundTaskManager()
	a := &Agent{
		multiSession: mt,
		tools:        tools.NewRegistry(),
		skills:       NewSkillStore(t.TempDir(), nil, nil),
	}
	a.SetBgTaskManager(bgMgr)

	ctx := WithUserContext(context.Background(), &UserContext{})
	parentCtx := &tools.ToolContext{
		AgentID:    "main",
		Channel:    "web",
		ChatID:     "chat-bg",
		SenderID:   "u1",
		UserID:     42,
		Role:       "admin",
		SenderName: "Smith",
	}

	cfg := a.buildSubAgentRunConfig(ctx, parentCtx, "task", "", nil, tools.SubAgentCapabilities{}, "reviewer", false, "inst-1", "")
	if cfg.BgTaskManager == nil {
		t.Fatal("SubAgent RunConfig must carry BgTaskManager (Shell background tasks / auto-promote depend on it)")
	}

	tc := buildToolContext(ctx, &cfg)
	if tc.BgTaskManager == nil {
		t.Fatal("ToolContext.BgTaskManager must be non-nil inside a SubAgent")
	}
	if tc.BgTaskManager != bgMgr {
		t.Error("ToolContext.BgTaskManager must be the same manager instance as the Agent's")
	}
	// SubAgent 的后台任务归属必须隔离到自己的 sessionKey（不污染父会话）。
	if tc.BgSessionKey == cfg.RootSessionKey {
		t.Errorf("SubAgent BgSessionKey must be isolated from the root session (got %q)", tc.BgSessionKey)
	}
	if tc.BgSessionKey == "" {
		t.Error("SubAgent BgSessionKey must not be empty")
	}

	// 身份三件套（用户作用域工具行为依赖）
	if tc.UserID != parentCtx.UserID {
		t.Errorf("UserID not inherited: got %d want %d", tc.UserID, parentCtx.UserID)
	}
	if tc.Role != parentCtx.Role {
		t.Errorf("Role not inherited: got %q want %q", tc.Role, parentCtx.Role)
	}
	if tc.SenderName != parentCtx.SenderName {
		t.Errorf("SenderName not inherited: got %q want %q", tc.SenderName, parentCtx.SenderName)
	}
}
