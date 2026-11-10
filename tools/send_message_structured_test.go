package tools

import (
	"strings"
	"testing"

	"xbot/protocol"
)

// send_message_structured_test.go — SendMessage 的结构化分支（批复/回答）。
//
// 设计约束（2026-10-21 用户定）：批复不是新工具，而是"一次特殊的 send"。
// 这里只验证**分发与防护**，CAS 本身由 storage 层测试覆盖（web 与工具共用同一
// 份 appendFn，不在这里重复实现）。

type capturedStructured struct {
	sender  string
	channel string
	chatID  string
	in      protocol.StructuredMessageInput
	calls   int
}

func stubStructured(t *testing.T, id int64) *capturedStructured {
	t.Helper()
	got := &capturedStructured{}
	SetStructuredAppend(func(senderID string, caller CallerScope, channel, chatID string, in protocol.StructuredMessageInput) (int64, bool, error) {
		got.calls++
		got.sender, got.channel, got.chatID, got.in = senderID, channel, chatID, in
		return id, false, nil
	})
	t.Cleanup(func() { SetStructuredAppend(nil) })
	return got
}

func TestSendMessage_KindDecision_RoutesToStructuredAppend(t *testing.T) {
	got := stubStructured(t, 77)

	tool := &SendMessageTool{}
	ctx := &ToolContext{Channel: "web", ChatID: "web-4", OriginUserID: "web-4", SenderID: "web-4"}
	res, err := tool.Execute(ctx, `{"to":"agent:backend/main","message":"准","kind":"decision","reply_to":"12","payload":"{\"verdict\":\"approve\"}","idempotency_key":"k1"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.calls != 1 {
		t.Fatalf("structured append calls = %d, want 1", got.calls)
	}
	if got.channel != "agent" || got.chatID != "backend/main" {
		t.Fatalf("target = %s:%s, want agent:backend/main", got.channel, got.chatID)
	}
	if got.in.Kind != "decision" || got.in.ReplyTo != "12" {
		t.Fatalf("input = %+v", got.in)
	}
	if v, _ := got.in.Payload["verdict"].(string); v != "approve" {
		t.Fatalf("payload.verdict = %q, want approve", v)
	}
	if got.in.IdempotencyKey != "k1" {
		t.Fatalf("idempotency = %q", got.in.IdempotencyKey)
	}
	if got.sender != "web-4" {
		t.Fatalf("sender = %q, want the originating user (web-4) for audit", got.sender)
	}
	if !strings.Contains(res.Summary+res.Detail, "批复已提交") {
		t.Fatalf("result does not look like an approval: %+v", res)
	}
}

func TestSendMessage_KindText_DoesNotTouchStructured(t *testing.T) {
	got := stubStructured(t, 1)

	tool := &SendMessageTool{}
	ctx := &ToolContext{
		Channel: "web", ChatID: "web-4", SenderID: "web-4",
		PeerMessageFn: func(targetSessionKey, message string) string { return "ok" },
	}
	if _, err := tool.Execute(ctx, `{"to":"session:cli:/tmp/x","message":"hello"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.calls != 0 {
		t.Fatalf("plain text must not go through structured append (calls=%d)", got.calls)
	}
}

// TestSendMessage_Structured_PassesCallerSession 取代原来的"自激就地拦截"测试。
//
// 审计 D-07：自激判定以前在 tools 层比**逻辑地址**（agent:team.data/main）
// 与 ctx 的**物理 key**，永不相等，等于没拦。修复方式是把判定下沉到落地点：
// tools 只负责把调用方 SessionKey 原样透传（callerSession），由 serverapp 在
// resolve 出物理 key 之后比较。所以这里断言的是"透传"，不是"拦截"。
func TestSendMessage_Structured_PassesCallerSession(t *testing.T) {
	var gotCaller CallerScope
	SetStructuredAppend(func(senderID string, caller CallerScope, channel, chatID string, in protocol.StructuredMessageInput) (int64, bool, error) {
		gotCaller = caller
		return 1, false, nil
	})
	t.Cleanup(func() { SetStructuredAppend(nil) })

	tool := &SendMessageTool{}
	ctx := &ToolContext{
		Channel: "agent", ChatID: "web:web-4/team.data:main",
		CallerSessionKey: "web:web-4/team.data:main",
		OriginUserID:     "web-4",
	}
	if _, err := tool.Execute(ctx, `{"to":"agent:team.ops/main","message":"汇总","kind":"report","payload":"{\"summary\":\"s\"}"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotCaller.SessionKey != "web:web-4/team.data:main" || gotCaller.Channel != "agent" {
		t.Fatalf("caller scope = %+v, want the caller's physical session key + channel", gotCaller)
	}
}

func TestSendMessage_Structured_DecisionRequiresReplyTo(t *testing.T) {
	stubStructured(t, 1)

	tool := &SendMessageTool{}
	ctx := &ToolContext{Channel: "web", ChatID: "web-4"}
	_, err := tool.Execute(ctx, `{"to":"agent:backend/main","message":"准","kind":"decision","payload":"{\"verdict\":\"approve\"}"}`)
	if err == nil || !strings.Contains(err.Error(), "reply_to") {
		t.Fatalf("want reply_to requirement, got %v", err)
	}
}

func TestSendMessage_Structured_RejectsBroadcastTargets(t *testing.T) {
	stubStructured(t, 1)

	tool := &SendMessageTool{}
	ctx := &ToolContext{Channel: "web", ChatID: "web-4"}
	for _, addr := range []string{"group:g1", "peer:p1"} {
		if _, err := tool.Execute(ctx, `{"to":"`+addr+`","message":"x","kind":"report","payload":"{\"summary\":\"s\"}"}`); err == nil {
			t.Fatalf("%s must be rejected for structured sends", addr)
		}
	}
}

func TestSendMessage_Structured_RejectsBadPayload(t *testing.T) {
	stubStructured(t, 1)

	tool := &SendMessageTool{}
	ctx := &ToolContext{Channel: "web", ChatID: "web-4"}
	if _, err := tool.Execute(ctx, `{"to":"agent:backend/main","message":"x","kind":"report","payload":"[1,2,3]"}`); err == nil {
		t.Fatal("non-object payload must be rejected")
	}
}

func TestSendMessage_Structured_UnavailableWithoutInjection(t *testing.T) {
	SetStructuredAppend(nil)
	tool := &SendMessageTool{}
	ctx := &ToolContext{Channel: "web", ChatID: "web-4"}
	_, err := tool.Execute(ctx, `{"to":"agent:backend/main","message":"x","kind":"report","payload":"{\"summary\":\"s\"}"}`)
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("want unavailable error, got %v", err)
	}
}

func TestStructuredTarget(t *testing.T) {
	cases := []struct {
		addr, channel, chatID string
		wantErr               bool
	}{
		{"agent:backend/main", "agent", "backend/main", false},
		{"session:web:chat_ABC", "web", "chat_ABC", false},
		{"session:cli:/home/smith/x", "cli", "/home/smith/x", false},
		{"feishu:oc_123", "feishu", "oc_123", false},
		{"group:g1", "", "", true},
		{"peer:p1", "", "", true},
		{"backend", "", "", true},
	}
	for _, c := range cases {
		ch, chat, err := structuredTarget(c.addr)
		if c.wantErr {
			if err == nil {
				t.Fatalf("%s: want error", c.addr)
			}
			continue
		}
		if err != nil || ch != c.channel || chat != c.chatID {
			t.Fatalf("%s: got (%q,%q,%v), want (%q,%q,nil)", c.addr, ch, chat, err, c.channel, c.chatID)
		}
	}
}
