package agent

import "testing"

// TestListInteractiveSessions_ReportsParentSessionAddress 锁定子代理行的
// 父会话标注语义：ParentChannel/ParentChatID 必须是**父会话的身份**
// （channel + 该会话的 chatID/map key），而不是一律 "agent" + 裸 parentKey。
//
// 现场（2026-11-04）：「后端 ia.running=true，前端侧边栏却没有子代理在跑」。
// 根因就在这里 —— 旧实现 ParentChannel 硬编码 "agent"，一级子代理的父
// （"web:web-4"）被记成 (agent, "web:web-4")，serverapp 建树时 lookupAgent
// ("agent:web:web-4") 必然 miss，合成父也 parse 失败 ⇒ 行掉进
// OrphanSubAgents ⇒ 前端 session-tree 只渲染 children ⇒ 侧边栏没有这条行。
func TestListInteractiveSessions_ReportsParentSessionAddress(t *testing.T) {
	a := &Agent{}

	// 一级子代理：父是主会话，parentKey = "web:chat-1"（sessionAddress 的结果）。
	defer a.RegisterInteractiveSessionForTest(InteractiveSessionSeed{
		Key: "web:chat-1/court.gongbu:inst-1", ParentKey: "web:chat-1",
		Role: "court.gongbu", Instance: "inst-1", Running: true,
	})()
	// 嵌套子代理：父是 agent 会话，parentKey = "agent:web:chat-1/court.shoufu:main"
	// （sessionAddress 对含 "/" 的 chatID 不剥前缀，所以带一层 "agent:"）。
	defer a.RegisterInteractiveSessionForTest(InteractiveSessionSeed{
		Key:       "agent:web:chat-1/court.shoufu:main/court.gongbu:inst-2",
		ParentKey: "agent:web:chat-1/court.shoufu:main",
		Role:      "court.gongbu", Instance: "inst-2", Running: true,
	})()

	cases := []struct {
		channel  string
		key      string
		wantChan string
		wantChat string
	}{
		{"web", "web:chat-1/court.gongbu:inst-1", "web", "chat-1"},
		{"agent", "agent:web:chat-1/court.shoufu:main/court.gongbu:inst-2", "agent", "web:chat-1/court.shoufu:main"},
	}
	for _, tc := range cases {
		sessions := a.ListInteractiveSessions(tc.channel, "")
		var got *InteractiveSessionInfo
		for i := range sessions {
			if sessions[i].Key == tc.key {
				got = &sessions[i]
				break
			}
		}
		if got == nil {
			t.Fatalf("session %q not listed for channel %q: %#v", tc.key, tc.channel, sessions)
		}
		if got.ParentChannel != tc.wantChan || got.ParentChatID != tc.wantChat {
			t.Fatalf("session %q parent = (%q,%q), want (%q,%q)",
				tc.key, got.ParentChannel, got.ParentChatID, tc.wantChan, tc.wantChat)
		}
		if !got.Running {
			t.Fatalf("session %q must report Running=true", tc.key)
		}
	}
}

// TestIsProcessingByChannel_AgentKeyForms 是 running 查询链的回归护栏：
// DB 的 tenants.chat_id 与 interactiveSubAgents 的 map key 同形，所以两种真实
// 形态都必须命中，且互不串台。
//
// 说明：这条测试在旧代码上**是绿的** —— 它证明「FullKey 带 agent: 前缀 ⇒ Load
// 必然 miss」这个假设不成立（现场 DB 里 chat_id 就是 map key）。留作回归护栏。
func TestIsProcessingByChannel_AgentKeyForms(t *testing.T) {
	a := &Agent{}
	const topKey = "web:chat-1/court.gongbu:inst-1"
	const nestedKey = "agent:web:chat-1/court.shoufu:main/court.gongbu:inst-2"

	defer a.RegisterInteractiveSessionForTest(InteractiveSessionSeed{
		Key: topKey, ParentKey: "web:chat-1", Role: "court.gongbu", Instance: "inst-1", Running: true,
	})()

	if !a.IsProcessingByChannel("agent", topKey) {
		t.Fatalf("top-level agent key %q must be running", topKey)
	}
	if a.IsProcessingByChannel("agent", nestedKey) {
		t.Fatalf("nested key %q must not report running (no such session)", nestedKey)
	}

	defer a.RegisterInteractiveSessionForTest(InteractiveSessionSeed{
		Key: nestedKey, ParentKey: "agent:web:chat-1/court.shoufu:main", Role: "court.gongbu", Instance: "inst-2", Running: true,
	})()
	if !a.IsProcessingByChannel("agent", nestedKey) {
		t.Fatalf("nested agent key %q must be running", nestedKey)
	}

	// 主会话路径不受影响。
	if a.IsProcessingByChannel("web", "chat-1") {
		t.Fatal("main session must not be running")
	}
	a.RegisterChatCancelForTest("web", "chat-1")
	defer a.UnregisterChatCancelForTest("web", "chat-1")
	if !a.IsProcessingByChannel("web", "chat-1") {
		t.Fatal("main session with cancel channel must be running")
	}
}
