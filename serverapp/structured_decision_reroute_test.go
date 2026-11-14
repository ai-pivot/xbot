package serverapp

// structured_decision_reroute_test.go — 2026-11-04 P0
// 「我在这里点的（结构化消息面板），结果批复回流给了御前」的入口级回归。
//
// 真实链路（前端 useStructuredMessages.decide）：
//   appendStructuredMessage(kind=decision, reply_to=<卡片 id>, channel/chat_id=用户会话)
// 旧实现在 appendFn 里对 decision 也走 `deliver → sel`，于是这条批复被注入
// **用户自己的会话**（御前），发起 report 的子代理永远收不到。线上日志铁证：
//   {"kind":"decision","msg":"Injected structured message at iteration boundary",
//    "session_id":"web:web-4"}   ← 首辅发的卡，批复进了御前
//
// 修复：decision 必须回流到卡片的 `env.origin.session`（发起方），sel 只作审计落行。
// 本测试从 **appendFn 入口**驱动（不是直接调 decisionRerouteTarget），
// 所以删掉 appendFn 里的那段重定向 ⇒ 本测试必红。

import (
	"strconv"
	"strings"
	"testing"

	"xbot/channel/web"
	"xbot/protocol"
	"xbot/storage/sqlite"
)

// TestDecisionReroutesToOriginatingAgentSession 锁住回流落点。
func TestDecisionReroutesToOriginatingAgentSession(t *testing.T) {
	ag := newTestAgentForCWD(t)
	appendFn, _, _, _, _, _, _ := structuredCallbacks(ag)

	sel := web.SessionSelector{Channel: "web", ChatID: "chat-reroute"}
	const agentKey = "web:chat-reroute/court.shoufu:main"

	// ① 子代理在自己的会话里发 report 卡片（origin 指向发起方会话）
	card, _, err := appendFn("agent:court.shoufu/main", sel, protocol.StructuredMessageInput{
		Kind:          "report",
		Text:          "混合 LoRA 方案已就绪",
		CorrelationID: "card-reroute-1",
		OriginSession: agentKey,
		OriginAgent:   "agent:court.shoufu/main",
		Silent:        true,
	})
	if err != nil {
		t.Fatalf("append report card: %v", err)
	}
	if card == nil || card.MessageID == 0 {
		t.Fatal("report card not persisted")
	}

	// ② 用户在**自己的会话**里点「准」（前端真实调用形态）
	if _, _, err := appendFn("web-user", sel, protocol.StructuredMessageInput{
		Kind:           "decision",
		ReplyTo:        strconv.FormatInt(card.MessageID, 10),
		Payload:        map[string]any{"verdict": "approve", "target_message_id": strconv.FormatInt(card.MessageID, 10)},
		IdempotencyKey: "web:decision:" + strconv.FormatInt(card.MessageID, 10),
		Text:           "准",
	}); err != nil {
		t.Fatalf("append decision: %v", err)
	}

	// ③ 回流必须在**发起方 agent 会话**里留下一行（agent 醒来才能读到用户说了什么）
	sess, err := ag.MultiSession().GetOrCreateSession("agent", agentKey)
	if err != nil {
		t.Fatalf("open originating agent session: %v", err)
	}
	svc, ok := sess.SessionService().(*sqlite.SessionService)
	if !ok {
		t.Fatal("session backend does not expose sqlite.SessionService")
	}
	msgs, err := svc.GetAllMessages(sess.TenantID())
	if err != nil {
		t.Fatalf("read originating agent session: %v", err)
	}
	found := false
	for _, m := range msgs {
		if strings.Contains(m.Content, "用户处理了卡片") && strings.Contains(m.Content, "已批准") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("批复没有回流到发起方会话 %q（messages=%d）—— 它被投给了用户自己的会话，"+
			"发起 report 的子代理收不到（2026-11-04 用户实测）", agentKey, len(msgs))
	}
}

// TestDecisionStaysInSessionWhenCardOriginatedHere：御前自己发的卡，批复留在御前会话
// —— 重定向不能把「自己发的卡」也甩到别处（否则御前永远收不到自己的批复）。
func TestDecisionStaysInSessionWhenCardOriginatedHere(t *testing.T) {
	ag := newTestAgentForCWD(t)
	appendFn, _, _, _, _, _, _ := structuredCallbacks(ag)

	sel := web.SessionSelector{Channel: "web", ChatID: "chat-self"}

	card, _, err := appendFn("web-user", sel, protocol.StructuredMessageInput{
		Kind:          "report",
		Text:          "御前自己的汇报",
		OriginSession: "web:chat-self",
		Silent:        true,
	})
	if err != nil {
		t.Fatalf("append report card: %v", err)
	}

	if _, _, err := appendFn("web-user", sel, protocol.StructuredMessageInput{
		Kind:           "decision",
		ReplyTo:        strconv.FormatInt(card.MessageID, 10),
		Payload:        map[string]any{"verdict": "approve"},
		IdempotencyKey: "web:decision:self-1",
		Text:           "准",
	}); err != nil {
		t.Fatalf("append decision: %v", err)
	}

	// 不得凭空造出一个 agent 会话
	sess, err := ag.MultiSession().GetOrCreateSession("agent", "web:chat-self")
	if err != nil {
		t.Fatalf("open agent session: %v", err)
	}
	svc, ok := sess.SessionService().(*sqlite.SessionService)
	if !ok {
		t.Fatal("session backend does not expose sqlite.SessionService")
	}
	msgs, _ := svc.GetAllMessages(sess.TenantID())
	for _, m := range msgs {
		if strings.Contains(m.Content, "用户处理了卡片") {
			t.Fatalf("origin 就是本会话时不应把回流行写到 agent 会话里（%q）", m.Content)
		}
	}
}
