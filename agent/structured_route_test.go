package agent

import "testing"

// structured_route_test.go — 回归 2026-10-22 的"批红打进空壳会话"事故。
//
// 逻辑地址 agent:role/instance 必须映射到 interactiveKey 生成的物理会话 key；
// 解析失败要**报错**，绝不能退回逻辑地址（那会新建一个野会话，消息凭空消失）。

func TestResolveStructuredTarget(t *testing.T) {
	a := &Agent{}

	// 非 agent 通道：原样返回。
	if got, err := a.ResolveStructuredTarget("web", "web-4"); err != nil || got != "web-4" {
		t.Fatalf("web target = (%q,%v), want web-4", got, err)
	}

	// 已经是物理 key（含冒号）：原样放行，不做二次解析。
	physical := "web_web-4/tester:court-verify"
	if got, err := a.ResolveStructuredTarget("agent", physical); err != nil || got != physical {
		t.Fatalf("physical key = (%q,%v), want unchanged", got, err)
	}

	// 逻辑地址但未注册：必须报错，且不能返回原值。
	got, err := a.ResolveStructuredTarget("agent", "tester/ghost")
	if err == nil {
		t.Fatal("unregistered sub-agent must be rejected")
	}
	if got != "" {
		t.Fatalf("must not fall back to the logical address, got %q", got)
	}
}

func TestResolveInteractiveChatID_MemoryRegistry(t *testing.T) {
	a := &Agent{}
	key := "web_web-4/tester:live"
	a.interactiveSubAgents.Store(key, &interactiveAgent{roleName: "tester", instance: "live"})

	if got := a.ResolveInteractiveChatID("tester/live"); got != key {
		t.Fatalf("resolve = %q, want %q", got, key)
	}
	if got := a.ResolveInteractiveChatID("tester/other"); got != "" {
		t.Fatalf("unknown instance must resolve to empty, got %q", got)
	}
}
