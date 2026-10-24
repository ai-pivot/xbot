package sqlite

import (
	"testing"
)

func TestInteractiveAgentRegistry(t *testing.T) {
	db := openTestDB(t)
	svc := NewInteractiveAgentService(db.Conn())

	parent := InteractiveAgentRecord{
		AgentKey:      "agent:gongbu/main",
		ParentChannel: "web",
		ParentChatID:  "chat_1",
		RoleName:      "gongbu",
		Instance:      "main",
		Task:          "部署 PPU",
		Background:    true,
		Status:        InteractiveAgentIdle,
		ModelName:     "balance",
		OriginUserID:  "web-1",
		WorkspaceRoot: "/work",
		ParentAgentID: "main",
	}
	if err := svc.Upsert(parent); err != nil {
		t.Fatalf("upsert parent: %v", err)
	}

	// 幂等：同 key 再写一次不应产生第二行，且 created_at 保留。
	first, err := svc.Get("agent:gongbu/main")
	if err != nil {
		t.Fatalf("get parent: %v", err)
	}
	parent.Status = InteractiveAgentRunning
	if err := svc.Upsert(parent); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	second, err := svc.Get("agent:gongbu/main")
	if err != nil {
		t.Fatalf("get parent again: %v", err)
	}
	if second.CreatedAt != first.CreatedAt {
		t.Errorf("upsert must preserve created_at: %q -> %q", first.CreatedAt, second.CreatedAt)
	}
	if second.Status != InteractiveAgentRunning {
		t.Errorf("status = %q, want running", second.Status)
	}

	// 孙代理：parent 是 (agent, "gongbu/main")
	grand := InteractiveAgentRecord{
		AgentKey:      "agent:coder/1",
		ParentChannel: "agent",
		ParentChatID:  "gongbu/main",
		RoleName:      "coder",
		Instance:      "1",
		Status:        InteractiveAgentIdle,
	}
	if err := svc.Upsert(grand); err != nil {
		t.Fatalf("upsert grandchild: %v", err)
	}

	// ListChildAgents 只返回直属子节点，不跨层。
	children, err := svc.ListChildAgents("web", "chat_1")
	if err != nil {
		t.Fatalf("list children: %v", err)
	}
	if len(children) != 1 || children[0].AgentKey != "agent:gongbu/main" {
		t.Fatalf("children = %+v, want exactly agent:gongbu/main", children)
	}
	grandChildren, err := svc.ListChildAgents("agent", "gongbu/main")
	if err != nil {
		t.Fatalf("list grandchildren: %v", err)
	}
	if len(grandChildren) != 1 || grandChildren[0].AgentKey != "agent:coder/1" {
		t.Fatalf("grandchildren = %+v, want exactly agent:coder/1", grandChildren)
	}

	// MarkAllRunningAsInterrupted：重启后把所有 running 改成 interrupted。
	n, err := svc.MarkAllRunningAsInterrupted()
	if err != nil {
		t.Fatalf("mark running interrupted: %v", err)
	}
	if n != 1 {
		t.Errorf("marked %d rows, want 1", n)
	}
	after, _ := svc.Get("agent:gongbu/main")
	if after.Status != InteractiveAgentInterrupted {
		t.Errorf("status = %q, want interrupted", after.Status)
	}

	// MarkStatus 只改状态与 turn_id。
	if err := svc.MarkStatus("agent:gongbu/main", InteractiveAgentRunning, 42); err != nil {
		t.Fatalf("mark status: %v", err)
	}
	after, _ = svc.Get("agent:gongbu/main")
	if after.Status != InteractiveAgentRunning || after.TurnID != 42 {
		t.Errorf("status=%q turn=%d, want running/42", after.Status, after.TurnID)
	}

	// 级联 unload：父 + 孙一起变 unloaded（递归 CTE）。
	if err := svc.MarkUnloadedCascade("agent:gongbu/main"); err != nil {
		t.Fatalf("cascade unload: %v", err)
	}
	for _, key := range []string{"agent:gongbu/main", "agent:coder/1"} {
		rec, err := svc.Get(key)
		if err != nil {
			t.Fatalf("get %s: %v", key, err)
		}
		if rec.Status != InteractiveAgentUnloaded {
			t.Errorf("%s status = %q, want unloaded", key, rec.Status)
		}
	}
}

// TestMarkOrphanedIdleUnloaded 覆盖僵尸行清理的判据：
//   - idle + 会话租户已不在        → unloaded（真空壳，destroy 只删了会话库）
//   - idle + 会话租户还在          → 保持 idle（重启后还能 restore，不能判死）
//   - idle + 会话租户不在但在 liveKeys 里 → 保持 idle（运行时还活着）
//   - running（任意情况）          → 不动（本函数只清 idle）
//   - instance 为空的角色          → 会话 key 是 "<parent>/<role>"，不能多拼冒号
//
// 并且是幂等的：第二次执行影响 0 行。
func TestMarkOrphanedIdleUnloaded(t *testing.T) {
	db := openTestDB(t)
	svc := NewInteractiveAgentService(db.Conn())
	tenants := NewTenantService(db)

	rec := func(key, parentChat, role, instance, status string) InteractiveAgentRecord {
		return InteractiveAgentRecord{
			AgentKey:      key,
			ParentChannel: "web",
			ParentChatID:  parentChat,
			RoleName:      role,
			Instance:      instance,
			Status:        status,
			Background:    true,
		}
	}

	// A：僵尸 —— 有注册行，会话租户早就被 DestroySession 删掉了。
	if err := svc.Upsert(rec("agent:explore/zombie-1", "chat_1", "explore", "zombie-1", InteractiveAgentIdle)); err != nil {
		t.Fatalf("upsert zombie: %v", err)
	}
	// B：可恢复 —— 会话租户还在，重启后 restoreInteractiveTree 会重建它。
	if err := svc.Upsert(rec("agent:team.sili/main", "chat_1", "team.sili", "main", InteractiveAgentIdle)); err != nil {
		t.Fatalf("upsert restorable: %v", err)
	}
	if _, err := tenants.GetOrCreateTenantID("agent", "web:chat_1/team.sili:main"); err != nil {
		t.Fatalf("create tenant for restorable: %v", err)
	}
	// C：running 行（上一个进程遗留）—— 不归本函数管。
	if err := svc.Upsert(rec("agent:explore/legacy-run", "chat_1", "explore", "legacy-run", InteractiveAgentRunning)); err != nil {
		t.Fatalf("upsert running: %v", err)
	}
	// D：instance 为空 —— 会话 key 拼法是 "web:chat_1/simple"（不带冒号）。
	if err := svc.Upsert(rec("agent:simple/", "chat_1", "simple", "", InteractiveAgentIdle)); err != nil {
		t.Fatalf("upsert empty-instance: %v", err)
	}
	if _, err := tenants.GetOrCreateTenantID("agent", "web:chat_1/simple"); err != nil {
		t.Fatalf("create tenant for empty-instance: %v", err)
	}
	// E：运行时还活着（liveKeys 里）—— 即使租户不在也不许判死。
	if err := svc.Upsert(rec("agent:explore/live-1", "chat_1", "explore", "live-1", InteractiveAgentIdle)); err != nil {
		t.Fatalf("upsert live: %v", err)
	}

	// 启动瞬间：运行时 map 为空，E 还没起 —— 应当只有 A 被清掉。
	n, err := svc.MarkOrphanedIdleUnloaded(nil)
	if err != nil {
		t.Fatalf("mark orphaned idle unloaded: %v", err)
	}
	if n != 2 { // A + E（E 此时不在 liveKeys 里，租户也不在 → 也是空壳）
		t.Errorf("first pass marked %d rows, want 2 (zombie + not-yet-live)", n)
	}

	want := map[string]string{
		"agent:explore/zombie-1":   InteractiveAgentUnloaded,
		"agent:team.sili/main":     InteractiveAgentIdle, // 会话还在：必须保留
		"agent:explore/legacy-run": InteractiveAgentRunning,
		"agent:simple/":            InteractiveAgentIdle, // 会话还在：必须保留
		"agent:explore/live-1":     InteractiveAgentUnloaded,
	}
	for key, status := range want {
		got, gerr := svc.Get(key)
		if gerr != nil {
			t.Fatalf("get %s: %v", key, gerr)
		}
		if got.Status != status {
			t.Errorf("%s status = %q, want %q", key, got.Status, status)
		}
	}

	// 幂等：再跑一次影响 0 行。
	n, err = svc.MarkOrphanedIdleUnloaded(nil)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if n != 0 {
		t.Errorf("second pass marked %d rows, want 0 (idempotent)", n)
	}

	// liveKeys 保护：E 重新变 idle 后，出现在 liveKeys 里就不再被清。
	if err := svc.MarkStatus("agent:explore/live-1", InteractiveAgentIdle, 0); err != nil {
		t.Fatalf("mark live idle: %v", err)
	}
	n, err = svc.MarkOrphanedIdleUnloaded([]string{"agent:explore/live-1"})
	if err != nil {
		t.Fatalf("third pass: %v", err)
	}
	if n != 0 {
		t.Errorf("third pass marked %d rows, want 0 (live key protected)", n)
	}
	if got, _ := svc.Get("agent:explore/live-1"); got.Status != InteractiveAgentIdle {
		t.Errorf("live agent status = %q, want idle (must not be unloaded)", got.Status)
	}
}

func TestInteractiveAgentNotFound(t *testing.T) {
	db := openTestDB(t)
	svc := NewInteractiveAgentService(db.Conn())
	if _, err := svc.Get("agent:nope/x"); err == nil {
		t.Fatal("expected ErrInteractiveAgentNotFound, got nil")
	}
}
