package agent

import (
	"testing"

	"xbot/storage/sqlite"
)

// 2026-11-04 御前补正（双判据 + 生产/读侧共用判据 + 逐层剥）：
//
// cli 父活洞（**pre-existing**，45b69ce5 逐字相同，本分支未引入）：
// 旧判据「chatID 不含 / 才剥」让 cli 父（chatID="cli:/home/smith/repo" 含 /）不剥，
// spawn 写出 parentKey="agent:cli:/home/smith/repo"；读侧 isMirrorSessionAddress
// 同样用「rest 含 / 就 false」，于是连归一化也兜不住 ⇒ 建树时被当成 agent 父，
// key 解析出 ("agent","cli:/home/smith/repo")，合成出 "cli:/home/smith" 父行 ——
// 子代理被挂成与真主会话同 chatID 的 agent 行（假祖先），CLI 面板按
// ParentChatID==chatID 过滤看不到自己的子代理。
//
// 修法：生产侧（sessionAddress）与读侧（isMirrorSessionAddress）**共用**
// agentContextMirrorTarget 一个判据：首个 "/" 之前那段是 "<ch>:<非空 chatID>" ⇒
// 真子代理地址（不剥）；是 "<ch>:"（chatID 为空）⇒ 镜像（剥）。

// TestInteractiveParentAddress_CLIParentMirror 是御前指定的表驱动硬断言。
func TestInteractiveParentAddress_CLIParentMirror(t *testing.T) {
	a := &Agent{}
	cases := []struct {
		name        string
		parentKey   string
		wantChannel string
		wantChatID  string
	}{
		{
			name:        "cli 父镜像 ⇒ 真实会话",
			parentKey:   "agent:cli:/home/smith/repo",
			wantChannel: "cli", wantChatID: "/home/smith/repo",
		},
		{
			name:        "套镜像逐层剥",
			parentKey:   "agent:agent:cli:/path",
			wantChannel: "cli", wantChatID: "/path",
		},
		{
			name:        "真子代理地址不误剥（首个 / 前 chatID 非空）",
			parentKey:   "agent:web:web-4/court.x:inst-1",
			wantChannel: "agent", wantChatID: "web:web-4/court.x:inst-1",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ch, chatID := a.interactiveParentAddress(c.parentKey)
			if ch != c.wantChannel || chatID != c.wantChatID {
				t.Fatalf("interactiveParentAddress(%q) = (%q,%q), want (%q,%q)",
					c.parentKey, ch, chatID, c.wantChannel, c.wantChatID)
			}
		})
	}
}

// TestSessionAddress_CLIParentMirrorCollapses 锁生产侧：cli 父不得写成 agent:cli:…。
func TestSessionAddress_CLIParentMirrorCollapses(t *testing.T) {
	cases := []struct{ chatID, wantAddr, wantChannel, wantChatID string }{
		// origin chatID 是**会话地址**形态（qualifyChatID 的产物）："cli:<path>"。
		{"cli:/home/smith/repo", "cli:/home/smith/repo", "cli", "/home/smith/repo"},
		{"cli:/home/smith/repo:Agent-main", "cli:/home/smith/repo:Agent-main", "cli", "/home/smith/repo:Agent-main"},
	}
	for _, c := range cases {
		addr, ok := sessionAddress("agent", c.chatID)
		if !ok {
			t.Fatalf("sessionAddress(agent,%q) rejected a valid origin", c.chatID)
		}
		if addr != c.wantAddr {
			t.Fatalf("sessionAddress(agent,%q) = %q, want %q（cli 父不得写成 agent:cli:… 假祖先）",
				c.chatID, addr, c.wantAddr)
		}
		ch, chatID, ok := splitSessionAddress(addr)
		if !ok || ch != c.wantChannel || chatID != c.wantChatID {
			t.Fatalf("splitSessionAddress(%q) = (%q,%q,%v), want (%q,%q,true)",
				addr, ch, chatID, ok, c.wantChannel, c.wantChatID)
		}
		if isMirrorSessionAddress(addr) {
			t.Fatalf("%q is still a mirror address", addr)
		}
	}
}

// TestSessionAddress_RealSubAgentAddressNotStripped 锁第二条判据：真子代理地址不误剥。
func TestSessionAddress_RealSubAgentAddressNotStripped(t *testing.T) {
	cases := []struct{ chatID, wantAddr, wantChannel, wantChatID string }{
		{"web:web-4/court.x:inst-1", "agent:web:web-4/court.x:inst-1", "agent", "web:web-4/court.x:inst-1"},
		{"web:web-4/court.shoufu:main/court.gongbu:inst-2", "agent:web:web-4/court.shoufu:main/court.gongbu:inst-2",
			"agent", "web:web-4/court.shoufu:main/court.gongbu:inst-2"},
	}
	a := &Agent{}
	for _, c := range cases {
		addr, ok := sessionAddress("agent", c.chatID)
		if !ok || addr != c.wantAddr {
			t.Fatalf("sessionAddress(agent,%q) = %q,%v want %q,true（真子代理地址不得剥）",
				c.chatID, addr, ok, c.wantAddr)
		}
		ch, chatID := a.interactiveParentAddress(addr)
		if ch != c.wantChannel || chatID != c.wantChatID {
			t.Fatalf("interactiveParentAddress(%q) = (%q,%q), want (%q,%q)",
				addr, ch, chatID, c.wantChannel, c.wantChatID)
		}
	}
}

// TestSessionAddress_CLIParentedSubAgentStaysAddress 锁 cli 父的**嵌套**形态。
func TestSessionAddress_CLIParentedSubAgentStaysAddress(t *testing.T) {
	const originChatID = "cli:/repo:Agent-main/review:1"
	addr, ok := sessionAddress("agent", originChatID)
	if !ok || addr != originChatID {
		t.Fatalf("sessionAddress(agent,%q) = %q,%v want %q,true", originChatID, addr, ok, originChatID)
	}
	a := &Agent{}
	defer a.RegisterInteractiveSessionForTest(InteractiveSessionSeed{
		Key: originChatID, Role: "review", Instance: "1", Running: true,
	})()
	ch, chatID := a.interactiveParentAddress(addr)
	if ch != "agent" || chatID != originChatID {
		t.Fatalf("interactiveParentAddress(%q) = (%q,%q), want (agent,%q)", addr, ch, chatID, originChatID)
	}
}

// TestRestoredCLIParentKeyIsNormalized 是御前要求的**持久化 roundtrip**：
// 注册表行 ('agent','cli:/path') 按 interactive_persist.go:258 的
// ParentChannel+":"+ParentChatID 重建后仍必须归一化成 (cli, /path) ——
// 这是「重启一次镜像就复活」的入口，只修生产侧挡不住它。
func TestRestoredCLIParentKeyIsNormalized(t *testing.T) {
	mt, _ := newAgentHistorySession(t)
	a := &Agent{multiSession: mt}
	svc := sqlite.NewInteractiveAgentService(mt.DB().Conn())

	rec := registryRec("agent:court.gongbu:cli-1", "agent", "cli:/path", "court.gongbu", "cli-1",
		sqlite.InteractiveAgentIdle)
	if err := svc.Upsert(rec); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := svc.Get("agent:court.gongbu:cli-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	restoredParentKey := got.ParentChannel + ":" + got.ParentChatID
	if restoredParentKey != "agent:cli:/path" {
		t.Fatalf("restored parentKey = %q, want agent:cli:/path（roundtrip 形态没复现，用例前提失效）", restoredParentKey)
	}
	ch, chatID := a.interactiveParentAddress(restoredParentKey)
	if ch != "cli" || chatID != "/path" {
		t.Fatalf("restored parentKey %q → (%q,%q), want (cli,/path)", restoredParentKey, ch, chatID)
	}
}
