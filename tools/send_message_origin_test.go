package tools

import "testing"

// 用户报的 P0（2026-11-14）：子代理用 SendMessage(to="user", kind="ask_user") 提问，
// 用户在卡片上处理之后，答复被投给了**主代理**。
//
// 根因：卡片的回流目标 origin.session 由 interactiveOriginSession 决定，而它只认
// SessionKey 的旧形态 `agent:<origin>/<role>:<instance>`。现在子代理的 SessionKey 是
// 调用链 ID（`main/taizi`，不含 origin），Channel 又是 web ⇒ 两个分支都落空 ⇒
// origin.session 退回根会话。修复后必须**优先**用 CallerSessionKey（精确 owner key）。
func TestSendMessageUserRoute_SubAgentOriginUsesOwnerKey(t *testing.T) {
	got := stubUserNotice(t, 301)
	ctx := &ToolContext{
		Channel:          "web", // 物理渠道是父会话
		ChatID:           "chat_9C218CD94A75",
		RootSessionKey:   "web:chat_9C218CD94A75",
		AgentDepth:       1,
		AgentID:          "main/taizi",
		SessionKey:       "main/taizi", // 调用链 ID：没有 origin、也没有 agent: 前缀
		CallerSessionKey: "web:chat_9C218CD94A75/taizi:fedtail-x3",
		OriginUserID:     "web-4",
		SenderID:         "main",
	}
	if _, err := (&SendMessageTool{}).Execute(ctx,
		`{"to":"user","kind":"ask_user","message":"选哪台机器？","payload":"{\"questions\":[{\"id\":\"q1\",\"question\":\"目标主机\"}]}"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.calls != 1 {
		t.Fatalf("user notice calls = %d, want 1", got.calls)
	}
	// 卡片仍落在用户可见的根会话。
	if got.channel != "web" || got.chatID != "chat_9C218CD94A75" {
		t.Fatalf("card target = %s:%s, want the user session web:chat_9C218CD94A75", got.channel, got.chatID)
	}
	// 但回流必须回到发起的子代理，而不是根会话。
	const wantOrigin = "web:chat_9C218CD94A75/taizi:fedtail-x3"
	if got.in.OriginSession != wantOrigin {
		t.Fatalf("origin session = %q, want the asking sub-agent %q (answering must not reach the main agent)", got.in.OriginSession, wantOrigin)
	}
	if got.in.OriginAgent != "agent:main/taizi" {
		t.Fatalf("origin agent = %q, want agent:main/taizi", got.in.OriginAgent)
	}
}

// AgentID 已带 agent: 前缀时不得再叠一层（否则 origin.agent 变成 agent:agent:...）。
func TestSendMessageUserRoute_OriginAgentNeverDoublePrefixed(t *testing.T) {
	got := stubUserNotice(t, 302)
	ctx := &ToolContext{
		Channel: "web", ChatID: "web-4", RootSessionKey: "web:web-4",
		AgentDepth: 1, AgentID: "agent:main/court.shoufu",
		SessionKey:       "agent:main/court.shoufu",
		CallerSessionKey: "web:web-4/court.shoufu:main",
		OriginUserID:     "web-4",
	}
	if _, err := (&SendMessageTool{}).Execute(ctx, `{"to":"user","kind":"report","message":"x"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.in.OriginAgent != "agent:main/court.shoufu" {
		t.Fatalf("origin agent = %q, want a single agent: prefix", got.in.OriginAgent)
	}
	if got.in.OriginSession != "web:web-4/court.shoufu:main" {
		t.Fatalf("origin session = %q", got.in.OriginSession)
	}
}
