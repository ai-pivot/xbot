package serverapp

// inbox_interaction_receipt_test.go — 交互回执的「上下文」与「寻址」判据。
//
// 用户报告（2026-11-25）的两个缺陷，本文件各锁一条：
//
//  1. 交互回执不能是裸词：用户在看板上「取消」一张 ask_user 卡片时，发起 agent
//     只收到 「已取消」 —— 它不知道是哪张卡、问的是什么、选项有哪些。旧实现
//     `answerText()` 的 cancelled 分支就 return "已取消"（历史实测行见
//     session_messages 的 content='已取消'）。回执必须是「系统口吻的交互记录」，
//     带卡片原文/问题/选项。
//
//  2. `POST /api/answer` 必须按卡片的 env.origin.session 回流：子代理发的
//     ask_user 卡落在用户会话里，旧实现把答复投给 sel（用户会话 ⇒ 主代理），
//     发起的子代理永远收不到。判据 = 回流行写在**子代理 tenant**，且用户会话
//     收不到任何投递。

import (
	"strings"
	"testing"

	"xbot/agent"
	"xbot/channel/web"
	"xbot/config"
	"xbot/protocol"
	"xbot/storage/common"
	"xbot/tools"
)

// latestAnswerContent 读回 answer 行的正文（回执的唯一可读副本）。
//
// 同会话（origin == sel）投递的内容是**空**的 —— 契约是「先落库 + 空注入」，
// 正文在库里（agent 醒来读历史），所以断言必须读行，不能读 inbound.Content。
func latestAnswerContent(t *testing.T, ag *agent.Agent, channel, chatID, corr string) string {
	t.Helper()
	sess, err := ag.MultiSession().GetOrCreateSession(channel, chatID)
	if err != nil {
		t.Fatalf("open tenant %s:%s: %v", channel, chatID, err)
	}
	svc, ok := sess.SessionService().(interface {
		ListStructuredMessages(tenantID int64, kind string, cursor int64, limit int) ([]common.StructuredMessage, bool, int64, error)
	})
	if !ok {
		t.Fatal("session backend has no structured store")
	}
	rows, _, _, err := svc.ListStructuredMessages(sess.TenantID(), "answer", 0, 50)
	if err != nil {
		t.Fatalf("list answers: %v", err)
	}
	for _, r := range rows {
		if r.CorrelationID == corr {
			return r.Content
		}
	}
	t.Fatalf("no answer row for correlation %q", corr)
	return ""
}

// askUserCard 发一张 to="user" 的 ask_user 卡片，返回 correlation_id 与卡片 id。
func askUserCard(t *testing.T, ag *agent.Agent, payload, message string) (corr string, cardID int64) {
	t.Helper()
	cb := buildWebCallbacks(&config.Config{}, ag, nil)
	sel := web.SessionSelector{Channel: "web", ChatID: "web-4"}
	tool := &tools.SendMessageTool{}
	arg := `{"to":"user","kind":"ask_user","message":"` + message + `","payload":"` +
		strings.ReplaceAll(payload, `"`, `\"`) + `"}`
	if _, err := tool.Execute(userBoardCtx(), arg); err != nil {
		t.Fatalf("Execute(to=user, kind=ask_user): %v", err)
	}
	items, _, _, err := cb.StructuredInbox(sel, 0, 20)
	if err != nil {
		t.Fatalf("StructuredInbox: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("inbox items = %d, want 1", len(items))
	}
	if items[0].CorrelationID == "" {
		t.Fatal("ask_user card must carry a correlation_id (回答钥匙)")
	}
	return items[0].CorrelationID, items[0].MessageID
}

const askPayload = `{"questions":[{"id":"q1","question":"目标主机选哪台？","options":[{"id":"a","label":"1101"},{"id":"b","label":"1102"}]}]}`

// 取消：回执必须是「系统口吻的交互记录 + 卡片上下文」，不是裸词。
func TestStructuredAnswer_CancelReceiptCarriesCardContext(t *testing.T) {
	ag, bs := newNoticeAgent(t)
	cb := buildWebCallbacks(&config.Config{}, ag, nil)
	sel := web.SessionSelector{Channel: "web", ChatID: "web-4"}
	corr, cardID := askUserCard(t, ag, askPayload, "需要你拍板目标主机")
	drainInbound(bs)

	if _, _, err := cb.StructuredAnswer("web-4", sel, protocol.StructuredAnswerInput{
		CorrelationID: corr, Cancelled: true,
	}); err != nil {
		t.Fatalf("StructuredAnswer(cancelled): %v", err)
	}
	// 回执正文的权威副本：answer 行（AnswerAskUser 写的、agent 醒来读的那一行）。
	// 投递层不再断言 —— 这张卡由子代理发起，子代理不在内存时按用户口径「当无事发生」。
	body := latestAnswerContent(t, ag, "web", "web-4", corr)

	// 裸词判据：整条正文不能只有「已取消」「取消」这类词。
	if strings.TrimSpace(body) == "已取消" || strings.TrimSpace(body) == "取消" {
		t.Fatalf("cancel receipt is a bare word %q — the agent cannot tell which card it refers to", body)
	}
	// 必须能定位到卡片：卡片 id + 卡片正文。
	if !strings.Contains(body, "卡片") || !strings.Contains(body, "需要你拍板目标主机") {
		t.Fatalf("cancel receipt lost the card reference/body: %q", body)
	}
	// 必须说清是「用户取消」，且带上问题文本（否则模型不知道取消的是什么提问）。
	if !strings.Contains(body, "取消") {
		t.Fatalf("cancel receipt must state the cancellation: %q", body)
	}
	if !strings.Contains(body, "目标主机选哪台") {
		t.Fatalf("cancel receipt lost the question text: %q", body)
	}
	_ = cardID
}

// 回答：回执必须带问题正文与用户所选 option 的 **label**（不是内部 id）。
func TestStructuredAnswer_AnswerReceiptCarriesQuestionAndOptionLabel(t *testing.T) {
	ag, bs := newNoticeAgent(t)
	cb := buildWebCallbacks(&config.Config{}, ag, nil)
	sel := web.SessionSelector{Channel: "web", ChatID: "web-4"}
	corr, _ := askUserCard(t, ag, askPayload, "需要你拍板目标主机")
	drainInbound(bs)

	if _, _, err := cb.StructuredAnswer("web-4", sel, protocol.StructuredAnswerInput{
		CorrelationID: corr,
		Answers:       []protocol.StructuredAnswer{{QuestionID: "q1", OptionIDs: []string{"b"}}},
	}); err != nil {
		t.Fatalf("StructuredAnswer(answered): %v", err)
	}
	body := latestAnswerContent(t, ag, "web", "web-4", corr)
	if !strings.Contains(body, "1102") {
		t.Fatalf("answer receipt must carry the chosen option label: %q", body)
	}
	if strings.Contains(body, "q1") && !strings.Contains(body, "目标主机选哪台") {
		t.Fatalf("answer receipt leaked the internal question id without its text: %q", body)
	}
	if !strings.Contains(body, "目标主机选哪台") {
		t.Fatalf("answer receipt lost the question text: %q", body)
	}
}

// 子代理发的卡片：回答必须回流到**发起方个子代理会话**，不能进用户会话。
func TestStructuredAnswer_ReroutesToOriginatingSubAgent(t *testing.T) {
	ag, bs := newNoticeAgent(t)
	cb := buildWebCallbacks(&config.Config{}, ag, nil)
	sel := web.SessionSelector{Channel: "web", ChatID: "web-4"}
	corr, _ := askUserCard(t, ag, askPayload, "需要你拍板目标主机")
	drainInbound(bs)

	// userBoardCtx 的子代理 key；卡片落库时 origin.session 就是它。
	const childKey = "web:web-4/court.shoufu:main"
	if _, _, err := cb.StructuredAnswer("web-4", sel, protocol.StructuredAnswerInput{
		CorrelationID: corr,
		Answers:       []protocol.StructuredAnswer{{QuestionID: "q1", OptionIDs: []string{"a"}}},
	}); err != nil {
		t.Fatalf("StructuredAnswer: %v", err)
	}

	// 用户会话（主代理）绝不能收到这条回执。
	for _, m := range drainInbound(bs) {
		if m.Channel == "web" && m.ChatID == "web-4" {
			t.Fatalf("answer feedback was delivered to the USER session (main agent) instead of %q: %+v", childKey, m)
		}
	}

	// 回流必须在子代理 tenant 里留下可读副本（agent 醒来后从历史读）。
	sess, err := ag.MultiSession().GetOrCreateSession("agent", childKey)
	if err != nil {
		t.Fatalf("open sub-agent tenant: %v", err)
	}
	msgs, err := sess.GetMessages()
	if err != nil {
		t.Fatalf("sub-agent history: %v", err)
	}
	found := false
	for _, m := range msgs {
		if strings.Contains(m.Content, "卡片") && strings.Contains(m.Content, "1101") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("sub-agent session has no feedback row mentioning the card and the chosen option (msgs=%d)", len(msgs))
	}
}
