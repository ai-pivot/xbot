package tools

import (
	"strings"
	"testing"
)

// 用户报的 P0（2026-11-14）：
//
//	SendMessage → to="parent" → "session messaging not available in this context"
//
// 根因：顶层子代理的父是**用户会话**，to="parent" 解析成 `session:<channel>:<chatID>`，
// 而 sendToSession 只认 PeerMessageFn（只有 CLI 会话才装），子代理没有 ⇒ 直接报错。
// 同时它用 ctx.Channel/ChatID 判"是否发给自己"，而子代理的这两个字段指向父会话，
// 即使补上 dispatcher 也会被误判成 self。
type stubSender struct {
	calls [][3]string
}

func (s *stubSender) SendMessage(channelName, chatID, content string) (string, error) {
	s.calls = append(s.calls, [3]string{channelName, chatID, content})
	return "ok", nil
}

func subAgentParentCtx(sender *stubSender) *ToolContext {
	return &ToolContext{
		Channel:          "web", // 子代理的物理字段指向父会话
		ChatID:           "chat_9C218CD94A75",
		RootSessionKey:   "web:chat_9C218CD94A75",
		AgentDepth:       1,
		AgentID:          "main/taizi",
		SessionKey:       "main/taizi",
		CallerSessionKey: "web:chat_9C218CD94A75/taizi:fedtail-x3",
		OriginUserID:     "web-4",
		MessageSender:    sender,
		// PeerMessageFn 故意为 nil —— 生产里子代理就是这样
	}
}

func TestSendMessageParentRouteWorksInSubAgentContext(t *testing.T) {
	sender := &stubSender{}
	ctx := subAgentParentCtx(sender)
	res, err := (&SendMessageTool{}).Execute(ctx, `{"to":"parent","message":"卡在权限上，需要你拍板"}`)
	if err != nil {
		t.Fatalf("to=parent in a sub-agent context must not error: %v", err)
	}
	if len(sender.calls) != 1 {
		t.Fatalf("dispatcher calls = %d, want 1", len(sender.calls))
	}
	got := sender.calls[0]
	if got[0] != "web" || got[1] != "chat_9C218CD94A75" {
		t.Fatalf("delivered to %s:%s, want the parent session web:chat_9C218CD94A75", got[0], got[1])
	}
	if !strings.Contains(got[2], "需要你拍板") {
		t.Fatalf("payload lost: %q", got[2])
	}
	if !strings.Contains(res.Summary, "delivered") {
		t.Fatalf("result = %+v", res)
	}
}

// 子代理自己的 key 是 CallerSessionKey；把它当成 self 才对，不能拿父会话当 self。
func TestSendMessageSessionSelfCheckUsesOwnKey(t *testing.T) {
	sender := &stubSender{}
	ctx := subAgentParentCtx(sender)

	// 发给父会话：允许
	if _, err := (&SendMessageTool{}).Execute(ctx, `{"to":"session:web:chat_9C218CD94A75","message":"hi"}`); err != nil {
		t.Fatalf("parent session must be addressable: %v", err)
	}
	// 发给自己（交互 key）：拒绝
	_, err := (&SendMessageTool{}).Execute(ctx, `{"to":"session:web:chat_9C218CD94A75/taizi:fedtail-x3","message":"hi"}`)
	if err == nil || !strings.Contains(err.Error(), "yourself") {
		t.Fatalf("self target must be rejected, got %v", err)
	}
}

// 没有 dispatcher 时仍然是明确错误（而不是静默丢失）。
func TestSendMessageSessionWithoutDispatcherStillErrors(t *testing.T) {
	ctx := &ToolContext{Channel: "web", ChatID: "chat_x", CallerSessionKey: "web:chat_x/taizi:x"}
	_, err := (&SendMessageTool{}).Execute(ctx, `{"to":"session:web:chat_x","message":"hi"}`)
	if err == nil || !strings.Contains(err.Error(), "no dispatcher") {
		t.Fatalf("want a clear no-dispatcher error, got %v", err)
	}
}
