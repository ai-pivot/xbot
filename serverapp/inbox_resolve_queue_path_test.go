package serverapp

// inbox_resolve_queue_path_test.go — 2026-11-12 P0（用户报「子代理发 report，我在收件箱里
// 回复后，回复消息直接发给了主代理而不是对应子代理」）的入口级回归。
//
// ⛔ 为什么必须单独写这个文件：既有的 user_notices_test.go 用 userBoardCtx()，它把
// `CallerSessionKey` **人为设成** "web:web-4/court.shoufu:main" —— 而真实线上子代理的
// 队列 / 通知注入 / 看板回流路径（buildMainRunConfig → buildBaseRunConfig）
// **根本不设 CallerSessionKey**。也就是说旧测试把 bug 的唯一触发条件给绕过去了：
// 测试全绿，线上全红。
//
// 本文件用 queuePathSubAgentCtx() 复刻真实形态：
//   - Channel    = "agent"
//   - ChatID     = "web:web-4/court.shoufu:main"      （interactive key，无 agent: 前缀）
//   - SessionKey = "agent:web:web-4/court.shoufu:main"
//   - RootSessionKey = 同一个 canonical key
//   - CallerSessionKey = ""                            ← 关键：线上就是空的
//
// 覆盖全链路：SendMessage(to=user, report) 落卡 → 卡片 origin.session 必须是**子代理**
// 会话 → 收件箱 approve → 回流必须投到 (agent, web:web-4/court.shoufu:main)，
// **绝不能**是 (web, web-4)（= 主代理 / 御前）。

import (
	"testing"

	"xbot/channel/web"
	"xbot/config"
	"xbot/protocol"
	"xbot/tools"
)

func webChatSel(chatID string) web.SessionSelector {
	return web.SessionSelector{Channel: "web", ChatID: chatID}
}

// userNoticeOriginFromProtocol 从协议化卡片（= 前端看到的形态）里取 origin，
// 走的是 toProtocolStructured 的 Envelope，而不是直接读 DB —— 断言用户视角。
func userNoticeOriginFromProtocol(t *testing.T, m protocol.StructuredMessage) (string, string) {
	t.Helper()
	raw, ok := m.Envelope["origin"]
	if !ok {
		return "", ""
	}
	o, ok := raw.(map[string]any)
	if !ok {
		return "", ""
	}
	s, _ := o["session"].(string)
	a, _ := o["agent"].(string)
	return s, a
}

// queuePathSubAgentCtx 复刻「子代理走通用主会话路径发卡」时的工具上下文。
//
// CallerSessionKey 留空是 core：这是线上真实值，也是 2026-11-04 / 2026-11-12 两次 P0
// 的共同触发条件。谁把它填上，这个测试就失去判别力了。
func queuePathSubAgentCtx() *tools.ToolContext {
	return &tools.ToolContext{
		Channel:          "agent",
		ChatID:           "web:web-4/court.shoufu:main",
		SessionKey:       "agent:web:web-4/court.shoufu:main",
		RootSessionKey:   "agent:web:web-4/court.shoufu:main",
		AgentDepth:       1,
		AgentID:          "agent:court.shoufu/main",
		CallerSessionKey: "",
		OriginUserID:     "web-4",
		SenderID:         "main",
	}
}

// TestInboxResolve_QueuePathSubAgentOrigin 是本次 P0 的主回归。
func TestInboxResolve_QueuePathSubAgentOrigin(t *testing.T) {
	ag, bs := newNoticeAgent(t)
	cb := buildWebCallbacks(&config.Config{}, ag, nil)
	sel := webChatSel("web-4")

	tool := &tools.SendMessageTool{}
	if _, err := tool.Execute(queuePathSubAgentCtx(),
		`{"to":"user","kind":"report","message":"队列路径 P0：批复必须回到子代理","payload":"{\"summary\":\"待批\",\"needs_approval\":true}","idempotency_key":"qp-report-1"}`); err != nil {
		t.Fatalf("Execute(to=user) from queue path: %v", err)
	}

	items, _, _, err := cb.StructuredInbox(sel, 0, 20)
	if err != nil || len(items) != 1 {
		t.Fatalf("inbox: items=%d err=%v, want exactly 1", len(items), err)
	}
	card := items[0]

	// ① 卡片的 origin.session 必须记成子代理会话（不是主代理）。
	originSession, originAgent := userNoticeOriginFromProtocol(t, card)
	if originSession != "web:web-4/court.shoufu:main" {
		t.Fatalf("card origin.session = %q, want the sub-agent session "+
			"\"web:web-4/court.shoufu:main\" (got the main session if it is \"web:web-4\")",
			originSession)
	}
	if originAgent == "" {
		t.Fatalf("card origin.agent must name the originating agent, got empty")
	}

	// ② 用户在收件箱 approve → 回流必须进子代理会话。
	res, err := cb.StructuredResolve("web-4", sel, resolveInput(card.MessageID, "approve", "同意"))
	if err != nil {
		t.Fatalf("StructuredResolve(approve): %v", err)
	}
	if res.Origin != "web:web-4/court.shoufu:main" {
		t.Fatalf("resolved origin = %q, want sub-agent session", res.Origin)
	}

	got := drainInbound(bs)
	if len(got) != 1 {
		t.Fatalf("feedback count = %d, want exactly 1", len(got))
	}
	fb := got[0]
	if fb.Channel != "agent" || fb.ChatID != "web:web-4/court.shoufu:main" {
		t.Fatalf("feedback went to %s:%s — want agent:web:web-4/court.shoufu:main.\n"+
			"  web:web-4 = 主代理（御前）会话 = 本 P0 的症状", fb.Channel, fb.ChatID)
	}
	if fb.ChatID == "web-4" {
		t.Fatalf("feedback landed in the MAIN session (web:web-4) instead of the sub-agent")
	}
}

// TestInboxResolve_QueuePathWithoutSessionKeyStillWorks 覆盖「连 SessionKey 都没有」的
// 极端形态（例如某个调用方只带 Channel/ChatID）。此时应从 Channel=="agent" + ChatID
// 里的 "/" 推断出子代理会话，仍然不许回落到主代理。
func TestInboxResolve_QueuePathWithoutSessionKeyStillWorks(t *testing.T) {
	ag, bs := newNoticeAgent(t)
	cb := buildWebCallbacks(&config.Config{}, ag, nil)
	sel := webChatSel("web-4")

	ctx := queuePathSubAgentCtx()
	ctx.SessionKey = ""
	ctx.RootSessionKey = ""

	tool := &tools.SendMessageTool{}
	if _, err := tool.Execute(ctx,
		`{"to":"user","kind":"report","message":"只剩 Channel+ChatID 也要回子代理","payload":"{\"summary\":\"待批\"}","idempotency_key":"qp-report-2"}`); err != nil {
		t.Fatalf("Execute(to=user): %v", err)
	}
	items, _, _, err := cb.StructuredInbox(sel, 0, 20)
	if err != nil || len(items) != 1 {
		t.Fatalf("inbox: items=%d err=%v", len(items), err)
	}
	originSession, _ := userNoticeOriginFromProtocol(t, items[0])
	if originSession != "web:web-4/court.shoufu:main" {
		t.Fatalf("card origin.session = %q, want sub-agent session (ChatID carries it)", originSession)
	}

	if _, err := cb.StructuredResolve("web-4", sel, resolveInput(items[0].MessageID, "approve", "ok")); err != nil {
		t.Fatalf("StructuredResolve: %v", err)
	}
	got := drainInbound(bs)
	if len(got) != 1 || got[0].ChatID != "web:web-4/court.shoufu:main" {
		t.Fatalf("feedback = %+v, want exactly one to web:web-4/court.shoufu:main", got)
	}
}

// TestInboxResolve_MainAgentCardStaysInMainSession 是反向判别力：主代理（御前）自己
// 发的卡，批复必须回流到主会话 —— 修复不能把所有回流都推到 agent 通道。
func TestInboxResolve_MainAgentCardStaysInMainSession(t *testing.T) {
	ag, bs := newNoticeAgent(t)
	cb := buildWebCallbacks(&config.Config{}, ag, nil)
	sel := webChatSel("web-4")

	mainCtx := &tools.ToolContext{
		Channel:      "web",
		ChatID:       "web-4",
		SessionKey:   "web:web-4",
		AgentDepth:   0,
		OriginUserID: "web-4",
		SenderID:     "web-4",
	}
	tool := &tools.SendMessageTool{}
	if _, err := tool.Execute(mainCtx,
		`{"to":"user","kind":"report","message":"主代理自发的卡","payload":"{\"summary\":\"待批\"}","idempotency_key":"main-report-1"}`); err != nil {
		t.Fatalf("Execute(to=user) from main: %v", err)
	}
	items, _, _, err := cb.StructuredInbox(sel, 0, 20)
	if err != nil || len(items) != 1 {
		t.Fatalf("inbox: items=%d err=%v", len(items), err)
	}
	if _, err := cb.StructuredResolve("web-4", sel, resolveInput(items[0].MessageID, "approve", "ok")); err != nil {
		t.Fatalf("StructuredResolve: %v", err)
	}
	got := drainInbound(bs)
	if len(got) != 1 {
		t.Fatalf("feedback count = %d, want 1", len(got))
	}
	if got[0].Channel != "web" || got[0].ChatID != "web-4" {
		t.Fatalf("main-agent card feedback went to %s:%s, want web:web-4",
			got[0].Channel, got[0].ChatID)
	}
}
