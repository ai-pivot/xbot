package agent

// structured_interactive_revive_test.go — 「子代理只要有 DB 记录就必须复活」的判据。
//
// 用户口径（2026-11-25）：
//   - 结构化回执投递到子代理会话时，**只要有 `interactive_agents` 记录（= 有 DB），
//     就一定要把它恢复出来并投给子代理本人**；
//   - 只有真·没有记录（从未 spawn / 已显式 unload）才「当无事发生」。
//
// 这条不变量曾经被破坏两次：
//   ① 投递栈只查 bgSessionStates ⇒ 判成 idle ⇒ 由主 Agent 执行（身份丢失）；
//   ② 惰恢复按**手拆的**父地址查注册表（拆成 ("agent","web:web-4")）⇒ 永远 0 行 ⇒
//      明明有 DB 也被判死。父身份必须按写侧同一规则规范化：
//      `web:web-4/court.shoufu:main` 的父是 `("web","web-4")`。

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"xbot/bus"
	"xbot/storage/sqlite"
)

// newReviveTestAgent 装配一个**完整** Agent（走 agent.New，才能拿到 userSys ——
// 恢复路径靠 ResolveUserContext 解析等级/模型，缺了它 restoreOneAgent 必然失败，
// 那正是「有 DB 也复活不了」的现场）。
func newReviveTestAgent(t *testing.T) *Agent {
	t.Helper()
	dir := t.TempDir()
	// New() 把 role 目录规范成 <XbotHome>/.xbot/agents（见 agent.New 的 agentsDir 推导）。
	agentsDir := filepath.Join(dir, ".xbot", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	roleWithPrompt(t, agentsDir, "reviewer", "You are the reviewer role.")

	ag, err := New(Config{
		WorkDir:        dir,
		DBPath:         filepath.Join(dir, "xbot.db"),
		XbotHome:       dir,
		SandboxMode:    "none",
		MemoryProvider: "none",
		Bus:            bus.NewMessageBus(),
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}
	t.Cleanup(func() { _ = ag.Close() })
	return ag
}

// 注册表里有记录 ⇒ 必须复活成子代理会话，而不是被判死。
func TestEnsureInteractiveSession_RestoresFromRegistry(t *testing.T) {
	a := newReviveTestAgent(t)
	const (
		parentChannel = "web"
		parentChatID  = "web-4"
		roleName      = "reviewer"
		instance      = "lead-1"
	)
	mapKey := interactiveKey(parentChannel, parentChatID, roleName, instance)

	if _, ok := a.loadInteractiveAgent(mapKey); ok {
		t.Fatal("precondition: session must NOT be in memory (simulating a process restart)")
	}
	if err := a.interactiveAgentSvc().Upsert(sqlite.InteractiveAgentRecord{
		AgentKey:      "agent:" + roleName + "/" + instance,
		ParentChannel: parentChannel,
		ParentChatID:  parentChatID,
		RoleName:      roleName,
		Instance:      instance,
		Background:    true,
		Status:        sqlite.InteractiveAgentIdle,
		OriginUserID:  "u1",
	}); err != nil {
		t.Fatalf("upsert registry row: %v", err)
	}

	if _, ok := a.ensureInteractiveSession(context.Background(), mapKey, "u1"); !ok {
		t.Fatalf("session %q has a registry row (DB) but was not restored — "+
			"父地址必须按写侧规范化推导，手拆字符串会让 ListChildAgents 永远 miss", mapKey)
	}
	if _, ok := a.loadInteractiveAgent(mapKey); !ok {
		t.Fatalf("restored session not registered under %q", mapKey)
	}
}

// 整条投递链：有 DB 记录的子代理会话必须收到回执（以子代理身份），而不是被丢弃。
func TestDeliverStructuredToInteractive_RevivesAndDelivers(t *testing.T) {
	a := newReviveTestAgent(t)
	const (
		parentChannel = "web"
		parentChatID  = "web-4"
		roleName      = "reviewer"
		instance      = "lead-1"
	)
	mapKey := interactiveKey(parentChannel, parentChatID, roleName, instance)
	if err := a.interactiveAgentSvc().Upsert(sqlite.InteractiveAgentRecord{
		AgentKey:      "agent:" + roleName + "/" + instance,
		ParentChannel: parentChannel,
		ParentChatID:  parentChatID,
		RoleName:      roleName,
		Instance:      instance,
		Background:    true,
		Status:        sqlite.InteractiveAgentIdle,
		OriginUserID:  "u1",
	}); err != nil {
		t.Fatalf("upsert registry row: %v", err)
	}

	item := &StructuredQueueItem{
		Channel: "agent", ChatID: mapKey, SenderID: "web-4",
		Content: "[系统] 用户取消了卡片 #7（kind=ask_user，未作答）。",
		Kind:    "answer", Priority: StructuredPriorityHigh, CorrelationID: "ask-7",
	}
	persisted := 0
	if handled := a.DeliverStructuredMessagePersisting(item, func() error { persisted++; return nil }); !handled {
		t.Fatal("delivery must be handled by the sub-agent path")
	}
	if _, ok := a.loadInteractiveAgent(mapKey); !ok {
		t.Fatalf("sub-agent %q (has DB record) was NOT revived by the delivery path", mapKey)
	}
	if persisted != 1 {
		t.Fatalf("idle sub-agent path must persist exactly once: %d", persisted)
	}
	// 绝不能回落主 Agent（通用 inbound）。
	select {
	case m := <-a.bus.Inbound:
		t.Fatalf("receipt leaked to the main agent's inbound: %+v", m)
	default:
	}
}

// 父身份推导必须与注册表写侧逐字对齐（含孙代理）。
func TestInteractiveRegistryParent(t *testing.T) {
	cases := []struct{ key, channel, chatID string }{
		{"web:web-4/court.shoufu:main", "web", "web-4"},
		{"web:web-4/court.shoufu:main/court.bingbu:x", "web", "web-4/court.shoufu:main"},
		{"cli:/w/reviewer:lead-1", "cli", "/w"},
		// 存量镜像形态：parentKey 曾写成 agent:web:web-4，必须再剥一层。
		{"agent:web:web-4/court.shoufu:main", "web", "web-4"},
	}
	for _, c := range cases {
		ch, chat, ok := interactiveRegistryParent(c.key)
		if !ok || ch != c.channel || chat != c.chatID {
			t.Fatalf("interactiveRegistryParent(%q) = (%q,%q,%v), want (%q,%q,true)",
				c.key, ch, chat, ok, c.channel, c.chatID)
		}
	}
	if _, _, ok := interactiveRegistryParent("web-4"); ok {
		t.Fatal("非子会话 key 不得推出父身份")
	}
}
