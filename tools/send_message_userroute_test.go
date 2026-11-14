package tools

// send_message_userroute_test.go — SendMessage(to="user") 用户看板路由的分发与防护。
//
// 设计口径（2026-10-29 用户原话 + 派发单 G-20261029-N1 §2）：
//   - 落点是**用户可见的根会话**，不是发起的子代理会话；
//   - 只有最上级 agent（AgentDepth ≤ 1）可用，更深的子代理必须**明确报错**，
//     不许静默降级成普通 IM 发送；
//   - 卡片落库但不唤醒目标会话（Silent=true），并带回流目标（OriginSession）。
//
// 存储/回流由 serverapp 的集成测试覆盖（见 serverapp/user_notices_test.go），
// 这里只验证工具层的解析与守卫 —— 与既有 send_message_structured_test.go 同构。

import (
	"strings"
	"testing"

	"xbot/protocol"
)

type capturedUserNotice struct {
	sender  string
	channel string
	chatID  string
	caller  CallerScope
	in      protocol.StructuredMessageInput
	calls   int
}

func stubUserNotice(t *testing.T, id int64) *capturedUserNotice {
	t.Helper()
	got := &capturedUserNotice{}
	SetUserNoticeAppend(func(senderID string, caller CallerScope, channel, chatID string, in protocol.StructuredMessageInput) (int64, bool, error) {
		got.calls++
		got.sender, got.caller, got.channel, got.chatID, got.in = senderID, caller, channel, chatID, in
		return id, false, nil
	})
	t.Cleanup(func() { SetUserNoticeAppend(nil) })
	return got
}

// topLevelAgentCtx 是最上级 agent（深度 1）的工具上下文：根会话 web:web-4，
// 自己的物理会话 key 是 interactive key（存储层属 channel="agent"）。
func topLevelAgentCtx() *ToolContext {
	return &ToolContext{
		Channel:          "web",
		ChatID:           "web-4",
		RootSessionKey:   "web:web-4",
		AgentDepth:       1,
		AgentID:          "main/court.shoufu",
		SessionKey:       "main/court.shoufu",
		CallerSessionKey: "web:web-4/court.shoufu:main",
		OriginUserID:     "web-4",
		SenderID:         "main",
	}
}

func TestSendMessageUserRoute_ResolvesToUserSession(t *testing.T) {
	got := stubUserNotice(t, 88)

	tool := &SendMessageTool{}
	res, err := tool.Execute(topLevelAgentCtx(),
		`{"to":"user","kind":"report","message":"N1 后端闭环完成","payload":"{\"summary\":\"一句话结论\"}","idempotency_key":"n1-card-1"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.calls != 1 {
		t.Fatalf("user notice calls = %d, want 1", got.calls)
	}
	// 落点必须是用户可见的根会话，而不是调用方自己的子代理会话。
	if got.channel != "web" || got.chatID != "web-4" {
		t.Fatalf("target = %s:%s, want web:web-4 (the user's session)", got.channel, got.chatID)
	}
	if !got.in.Silent {
		t.Fatal("card must be persisted silently (no wake-up of the user session)")
	}
	if got.in.OriginSession != "web:web-4/court.shoufu:main" {
		t.Fatalf("origin session = %q, want the caller's physical session (web:web-4/court.shoufu:main)", got.in.OriginSession)
	}
	if got.in.OriginAgent != "agent:main/court.shoufu" {
		t.Fatalf("origin agent = %q, want agent:main/court.shoufu", got.in.OriginAgent)
	}
	if got.in.Kind != "report" {
		t.Fatalf("kind = %q, want report", got.in.Kind)
	}
	if got.in.IdempotencyKey != "n1-card-1" {
		t.Fatalf("idempotency = %q", got.in.IdempotencyKey)
	}
	if s, _ := got.in.Payload["summary"].(string); s != "一句话结论" {
		t.Fatalf("payload.summary = %q", s)
	}
	if got.caller.SessionKey != "web:web-4/court.shoufu:main" {
		t.Fatalf("caller scope session = %q", got.caller.SessionKey)
	}
	if !strings.Contains(res.Summary, "卡片已提交到用户看板") || !strings.Contains(res.Summary, "web:web-4") {
		t.Fatalf("result = %+v", res)
	}
}

// TestSendMessageUserRoute_MainSessionDepthZero：主会话自己（深度 0）也能用；
// 回流目标就是它自己（用户在主会话里提问/汇报，处理后回到这里）。
func TestSendMessageUserRoute_MainSessionDepthZero(t *testing.T) {
	got := stubUserNotice(t, 91)

	ctx := &ToolContext{
		Channel: "web", ChatID: "web-4", AgentDepth: 0,
		AgentID: "main", OriginUserID: "web-4",
	}
	tool := &SendMessageTool{}
	if _, err := tool.Execute(ctx, `{"to":"user","kind":"ask_user","message":"选 A 还是 B？","payload":"{\"questions\":[{\"id\":\"q1\",\"question\":\"选哪个\"}]}"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.channel != "web" || got.chatID != "web-4" {
		t.Fatalf("target = %s:%s, want web:web-4", got.channel, got.chatID)
	}
	if got.in.OriginSession != "web:web-4" {
		t.Fatalf("origin session = %q, want web:web-4", got.in.OriginSession)
	}
	if got.in.OriginAgent != "" {
		t.Fatalf("main session must not claim an agent address, got %q", got.in.OriginAgent)
	}
	if got.in.Kind != "ask_user" {
		t.Fatalf("kind = %q, want ask_user", got.in.Kind)
	}
}

// TestSendMessageUserRoute_RejectsNestedAgent 是判据 b：深度 ≥2 的子代理调用
// to="user" 必须拿到**明确可判**的错误，且不得触达落地实现（不静默降级）。
func TestSendMessageUserRoute_RejectsNestedAgent(t *testing.T) {
	got := stubUserNotice(t, 1)

	ctx := topLevelAgentCtx()
	ctx.AgentDepth = 2 // 孙级子代理
	ctx.AgentID = "main/court.gongbu/proj.xbot.dev"
	ctx.SessionKey = "main/court.gongbu/proj.xbot.dev"
	ctx.CallerSessionKey = "web:web-4/proj.xbot.dev:main"

	tool := &SendMessageTool{}
	_, err := tool.Execute(ctx, `{"to":"user","kind":"report","message":"我不该打扰用户"}`)
	if err == nil {
		t.Fatal("nested sub-agent must NOT be allowed to target user")
	}
	if !strings.Contains(err.Error(), "only top-level agents may target") {
		t.Fatalf("error must be mechanically identifiable, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "agent depth 2") {
		t.Fatalf("error must report the caller's depth, got %q", err.Error())
	}
	if got.calls != 0 {
		t.Fatalf("rejected call must not reach the user-notice sink (calls=%d)", got.calls)
	}
}

func TestSendMessageUserRoute_RejectsBadAddress(t *testing.T) {
	got := stubUserNotice(t, 1)
	tool := &SendMessageTool{}
	for _, addr := range []string{"user:", "user:someone-else"} {
		_, err := tool.Execute(topLevelAgentCtx(), `{"to":"`+addr+`","kind":"report","message":"x"}`)
		if err == nil {
			t.Fatalf("address %q must be rejected", addr)
		}
		if !strings.Contains(err.Error(), "invalid address") && !strings.Contains(err.Error(), "unknown user") {
			t.Fatalf("address %q: unexpected error %q", addr, err.Error())
		}
	}
	if got.calls != 0 {
		t.Fatalf("rejected addresses must not reach the sink (calls=%d)", got.calls)
	}
}

// TestSendMessageUserRoute_KindRestriction：看板只承载 report/ask_user；
// 缺省 kind 视为 report（"简报别被淹没"就是这个场景）。
func TestSendMessageUserRoute_KindRestriction(t *testing.T) {
	got := stubUserNotice(t, 5)
	tool := &SendMessageTool{}

	if _, err := tool.Execute(topLevelAgentCtx(), `{"to":"user","message":"默认当成 report"}`); err != nil {
		t.Fatalf("default kind must be accepted: %v", err)
	}
	if got.in.Kind != "report" {
		t.Fatalf("default kind = %q, want report", got.in.Kind)
	}

	_, err := tool.Execute(topLevelAgentCtx(), `{"to":"user","kind":"decision","message":"批","reply_to":"3"}`)
	if err == nil || !strings.Contains(err.Error(), "board items") {
		t.Fatalf("kind=decision must be rejected with an explanatory error, got %v", err)
	}
}
