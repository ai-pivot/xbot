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

func TestInteractiveAgentNotFound(t *testing.T) {
	db := openTestDB(t)
	svc := NewInteractiveAgentService(db.Conn())
	if _, err := svc.Get("agent:nope/x"); err == nil {
		t.Fatal("expected ErrInteractiveAgentNotFound, got nil")
	}
}
