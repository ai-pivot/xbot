package agent

// interactive_registry_reconcile_test.go —— interactive_agents 僵尸行根治的两条
// 回归守卫（2026-10-23 P3）：
//
//  1. 销毁必须落库：destroyInteractiveSession 以前只删会话库（DestroySession），
//     主库那一行永远是 idle ⇒ ListActive / ListChildAgents / 组织树把死会话当活的
//     列出来（生产实测：81 行里 75 行 idle 全是僵尸，其中 explore 61 条）。
//  2. 启动必须对账：重启后运行时 map 是空的，历史僵尸只能在这个时机清；
//     但「会话还在的 idle 行」不能一起判死 —— restoreInteractiveTree 跳过 unloaded，
//     判错就等于把重启恢复功能关掉。

import (
	"testing"
	"time"

	"xbot/storage/sqlite"
)

// registryRec 造一条注册表行（agent_key 形态 "agent:<role>/<instance>"）。
func registryRec(key, parentChannel, parentChatID, role, instance, status string) sqlite.InteractiveAgentRecord {
	return sqlite.InteractiveAgentRecord{
		AgentKey:      key,
		ParentChannel: parentChannel,
		ParentChatID:  parentChatID,
		RoleName:      role,
		Instance:      instance,
		Status:        status,
		Background:    true,
	}
}

// TestDestroyInteractiveSessionMarksRegistryUnloaded：销毁路径必须把该 agent（含级联
// 子代理）在注册表里标成 unloaded，且重复销毁幂等。
func TestDestroyInteractiveSessionMarksRegistryUnloaded(t *testing.T) {
	mt, _ := newAgentHistorySession(t)
	a := &Agent{multiSession: mt}
	svc := sqlite.NewInteractiveAgentService(mt.DB().Conn())

	parent := registryRec("agent:reviewer/z1", "cli", "/w", "reviewer", "z1", sqlite.InteractiveAgentIdle)
	if err := svc.Upsert(parent); err != nil {
		t.Fatalf("upsert parent: %v", err)
	}
	// 孙代理：parent 是 (agent, "reviewer/z1") —— 级联 CTE 靠这对值展开。
	grand := registryRec("agent:helper/g1", "agent", "reviewer/z1", "helper", "g1", sqlite.InteractiveAgentIdle)
	if err := svc.Upsert(grand); err != nil {
		t.Fatalf("upsert grandchild: %v", err)
	}

	key := interactiveKey("cli", "/w", "reviewer", "z1")
	a.interactiveSubAgents.Store(key, &interactiveAgent{
		roleName: "reviewer",
		instance: "z1",
		lastUsed: time.Now(),
	})

	a.destroyInteractiveSession(key)

	got, err := svc.Get("agent:reviewer/z1")
	if err != nil {
		t.Fatalf("get parent after destroy: %v", err)
	}
	if got.Status != sqlite.InteractiveAgentUnloaded {
		t.Errorf("parent status after destroy = %q, want %q", got.Status, sqlite.InteractiveAgentUnloaded)
	}
	gotGrand, err := svc.Get("agent:helper/g1")
	if err != nil {
		t.Fatalf("get grandchild after destroy: %v", err)
	}
	if gotGrand.Status != sqlite.InteractiveAgentUnloaded {
		t.Errorf("grandchild status after destroy = %q, want %q (cascade)", gotGrand.Status, sqlite.InteractiveAgentUnloaded)
	}
	if _, ok := a.interactiveSubAgents.Load(key); ok {
		t.Fatal("session still registered after destroy")
	}

	// 幂等：重复销毁是 no-op，不能 panic，状态仍是 unloaded。
	a.destroyInteractiveSession(key)
	if again, _ := svc.Get("agent:reviewer/z1"); again == nil || again.Status != sqlite.InteractiveAgentUnloaded {
		t.Fatalf("repeat destroy changed the row: %+v", again)
	}
}

// TestReconcileInteractiveAgentsOnStartup：启动对账只清 zombi，不误伤可恢复会话。
func TestReconcileInteractiveAgentsOnStartup(t *testing.T) {
	mt, _ := newAgentHistorySession(t)
	a := &Agent{multiSession: mt}
	svc := sqlite.NewInteractiveAgentService(mt.DB().Conn())

	// 1) 僵尸：idle 行 + 会话租户已被销毁（destroy 只删了会话库的典型残留）。
	if err := svc.Upsert(registryRec("agent:explore/zombie-1", "web", "chat_1", "explore", "zombie-1", sqlite.InteractiveAgentIdle)); err != nil {
		t.Fatalf("upsert zombie: %v", err)
	}
	// 2) 可恢复：idle 行 + 会话租户还在 → 不许判死（否则重启恢复失效）。
	if err := svc.Upsert(registryRec("agent:team.sili/main", "web", "chat_1", "team.sili", "main", sqlite.InteractiveAgentIdle)); err != nil {
		t.Fatalf("upsert restorable: %v", err)
	}
	if _, err := mt.Store().Tenant().GetOrCreateTenantID("agent", "web:chat_1/team.sili:main"); err != nil {
		t.Fatalf("create tenant for restorable: %v", err)
	}
	// 3) 上一个进程遗留的 running 行 → interrupted（不是 unloaded）。
	if err := svc.Upsert(registryRec("agent:explore/legacy-run", "web", "chat_1", "explore", "legacy-run", sqlite.InteractiveAgentRunning)); err != nil {
		t.Fatalf("upsert running: %v", err)
	}
	// 4) 运行时还活着的会话（map 里有实例）：即使租户还没建也不许清。
	if err := svc.Upsert(registryRec("agent:reviewer/live-1", "web", "chat_1", "reviewer", "live-1", sqlite.InteractiveAgentIdle)); err != nil {
		t.Fatalf("upsert live: %v", err)
	}
	a.interactiveSubAgents.Store(interactiveKey("web", "chat_1", "reviewer", "live-1"), &interactiveAgent{
		roleName: "reviewer",
		instance: "live-1",
		lastUsed: time.Now(),
	})

	if err := a.ReconcileInteractiveAgentsOnStartup(); err != nil {
		t.Fatalf("startup reconcile: %v", err)
	}

	cases := []struct{ key, want string }{
		{"agent:explore/zombie-1", sqlite.InteractiveAgentUnloaded},
		{"agent:team.sili/main", sqlite.InteractiveAgentIdle},
		{"agent:explore/legacy-run", sqlite.InteractiveAgentInterrupted},
		{"agent:reviewer/live-1", sqlite.InteractiveAgentIdle},
	}
	for _, c := range cases {
		rec, err := svc.Get(c.key)
		if err != nil {
			t.Fatalf("get %s: %v", c.key, err)
		}
		if rec.Status != c.want {
			t.Errorf("%s status = %q, want %q", c.key, rec.Status, c.want)
		}
	}

	// 幂等：再对一次账不改任何东西。
	if err := a.ReconcileInteractiveAgentsOnStartup(); err != nil {
		t.Fatalf("second startup reconcile: %v", err)
	}
	for _, c := range cases {
		rec, err := svc.Get(c.key)
		if err != nil {
			t.Fatalf("get %s after second reconcile: %v", c.key, err)
		}
		if rec.Status != c.want {
			t.Errorf("second reconcile changed %s: %q, want %q", c.key, rec.Status, c.want)
		}
	}
}

// TestReconcileInteractiveAgentsOnStartupNilSafe：启动对账挂在 boot 路径上，缺依赖
// （nil Agent / 没有主库的 Agent）时必须是 no-op，不能 panic、不能返回错误 ——
// 否则一次对账就能把启动打挂。serverapp 的接线只做「调它 + WARN」，没有额外逻辑，
// 所以这里覆盖的就是那条接线路径上唯一可能出错的部分。
func TestReconcileInteractiveAgentsOnStartupNilSafe(t *testing.T) {
	var nilAgent *Agent
	if err := nilAgent.ReconcileInteractiveAgentsOnStartup(); err != nil {
		t.Fatalf("nil agent: %v", err)
	}
	if err := (&Agent{}).ReconcileInteractiveAgentsOnStartup(); err != nil {
		t.Fatalf("agent without multiSession: %v", err)
	}
}
