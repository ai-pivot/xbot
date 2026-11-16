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

type injectedCall struct{ channel, chatID, sender, content string }

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
	var got []injectedCall
	ctx := subAgentParentCtx(&stubSender{})
	// 生产里 buildToolContext 给所有上下文都接了 InjectInbound；这里按真实形态接上。
	ctx.InjectInbound = func(channel, chatID, sender, content string) {
		got = append(got, injectedCall{channel, chatID, sender, content})
	}
	res, err := (&SendMessageTool{}).Execute(ctx, `{"to":"parent","message":"卡在权限上，需要你拍板"}`)
	if err != nil {
		t.Fatalf("to=parent in a sub-agent context must not error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("InjectInbound calls = %d, want 1", len(got))
	}
	if got[0].channel != "web" || got[0].chatID != "chat_9C218CD94A75" {
		t.Fatalf("delivered to %s:%s, want the parent session web:chat_9C218CD94A75", got[0].channel, got[0].chatID)
	}
	if !strings.Contains(got[0].content, "需要你拍板") {
		t.Fatalf("payload lost: %q", got[0].content)
	}
	if !strings.Contains(res.Summary, "delivered") {
		t.Fatalf("result = %+v", res)
	}
}

// 子代理自己的 key 是 CallerSessionKey；把它当成 self 才对，不能拿父会话当 self。
func TestSendMessageSessionSelfCheckUsesOwnKey(t *testing.T) {
	sender := &stubSender{}
	ctx := subAgentParentCtx(sender)
	ctx.InjectInbound = func(channel, chatID, sender, content string) {}

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
	if err == nil || !strings.Contains(err.Error(), "neither InjectInbound nor a dispatcher") {
		t.Fatalf("want a clear no-dispatcher error, got %v", err)
	}
}

// 2026-11-14 用户实测：子代理 SendMessage(to="web:chat_9C218CD94A75") 全部丢失。
// 裸会话地址曾落进 IM 分支 ⇒ WebChannel.Send 只推 WS，不注入、不落库 ⇒ 静默丢失。
func TestBareSessionAddressInjectsInsteadOfUIOnly(t *testing.T) {
	var got []injectedCall
	ctx := subAgentParentCtx(&stubSender{})
	ctx.InjectInbound = func(channel, chatID, sender, content string) {
		got = append(got, injectedCall{channel, chatID, sender, content})
	}
	res, err := (&SendMessageTool{}).Execute(ctx,
		`{"to":"web:chat_9C218CD94A75","message":"【taizi 只读审计状态】无新增实现"}`)
	if err != nil {
		t.Fatalf("bare session address must not error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("InjectInbound calls = %d, want 1 (dispatcher push would be lost)", len(got))
	}
	if got[0].channel != "web" || got[0].chatID != "chat_9C218CD94A75" {
		t.Fatalf("injected into %s:%s, want web:chat_9C218CD94A75", got[0].channel, got[0].chatID)
	}
	if res.Summary == "" || !strings.Contains(res.Summary, "delivered") {
		t.Fatalf("result = %+v", res)
	}
}

// session: 前缀与裸地址必须等价。
func TestSessionPrefixAndBareAddressAreEquivalent(t *testing.T) {
	for _, to := range []string{"web:chat_9C", "session:web:chat_9C"} {
		var got []injectedCall
		ctx := subAgentParentCtx(&stubSender{})
		ctx.InjectInbound = func(channel, chatID, sender, content string) {
			got = append(got, injectedCall{channel, chatID, sender, content})
		}
		if _, err := (&SendMessageTool{}).Execute(ctx, `{"to":"`+to+`","message":"x"}`); err != nil {
			t.Fatalf("%s: %v", to, err)
		}
		if len(got) != 1 || got[0].channel != "web" || got[0].chatID != "chat_9C" {
			t.Fatalf("%s injected %+v", to, got)
		}
	}
}

// IM 渠道不能被当成会话注入。
func TestIMAddressStillUsesDispatcher(t *testing.T) {
	sender := &stubSender{}
	ctx := subAgentParentCtx(sender)
	var injected []injectedCall
	ctx.InjectInbound = func(channel, chatID, sender, content string) {
		injected = append(injected, injectedCall{channel, chatID, sender, content})
	}
	if _, err := (&SendMessageTool{}).Execute(ctx, `{"to":"feishu:ou_abc","message":"hi"}`); err != nil {
		t.Fatalf("feishu: %v", err)
	}
	if len(injected) != 0 {
		t.Fatalf("IM address must not be injected into the agent loop: %+v", injected)
	}
	if len(sender.calls) != 1 || sender.calls[0][0] != "feishu" {
		t.Fatalf("dispatcher calls = %+v", sender.calls)
	}
}
