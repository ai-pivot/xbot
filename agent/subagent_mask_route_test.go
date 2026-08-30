package agent

import (
	"context"
	"testing"

	"xbot/tools"
)

// TestBuildSubAgentRunConfig_MaskRecallRouteConsistency 断言 SubAgent 的 mask
// 写入与读取路由同源：
//   - 写入侧：cfg.MaskStore 按 derived tenantID 路由（maybeMaskObservations）。
//   - 读取侧：recall_masked 工具按 ctx.TenantID → tenantMaskRouter 反查 store，
//     而 ctx.TenantID 的唯一注入点是 cfg.ToolContextExtras.TenantID
//     （buildToolContext 只在 extras != nil 时覆盖）。
//
// caps.Memory=false 时 extras 曾缺失 → tc.TenantID=0 → recall_masked 路由到
// maskStoreFor(0)（空 store）而非写入侧的 derived store，被遮蔽内容静默
// not found（Loop 3 审查 1c）。caps.Memory=true 时 buildSubAgentMemory 返回的
// extras.TenantID 与 derived 同值（同一纯函数同参数），覆盖赋值等价。
func TestBuildSubAgentRunConfig_MaskRecallRouteConsistency(t *testing.T) {
	mt, _ := newAgentHistorySession(t)
	a := &Agent{
		multiSession: mt,
		tools:        tools.NewRegistry(),
		skills:       NewSkillStore(t.TempDir(), nil, nil),
	}
	ctx := WithUserContext(context.Background(), &UserContext{})
	parentCtx := &tools.ToolContext{
		AgentID:  "main",
		Channel:  "test",
		ChatID:   "chat",
		SenderID: "u1",
	}

	// 断裂条件：无 memory capability 的 SubAgent（大多数 review/code 类角色）。
	caps := tools.SubAgentCapabilities{Memory: false}
	cfg := a.buildSubAgentRunConfig(ctx, parentCtx, "task", "", nil, caps, "reviewer", false, "inst-1", "")

	// parentExtras 与 buildSubAgentRunConfig 内部重建同源（channel/chatID 一致）。
	parentExtras := a.buildToolContextExtras("test", "chat")
	derived := deriveSubAgentTenantID(parentExtras.TenantID, "main", "reviewer")

	if cfg.ToolContextExtras == nil {
		t.Fatalf("caps.Memory=false: cfg.ToolContextExtras=nil → recall_masked 的 ctx.TenantID=0，"+
			"路由到空 store（写入侧 MaskStore key=%d）", derived)
	}
	if cfg.ToolContextExtras.TenantID != derived {
		t.Fatalf("caps.Memory=false: extras.TenantID=%d, want derived=%d（与 cfg.MaskStore 写入 key 一致）",
			cfg.ToolContextExtras.TenantID, derived)
	}
	if cfg.MaskStore != a.maskStoreFor(derived) {
		t.Fatalf("caps.Memory=false: cfg.MaskStore 不是 derived store (key=%d)", derived)
	}
}
