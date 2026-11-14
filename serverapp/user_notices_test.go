package serverapp

// user_notices_test.go — SendMessage(to="user") 用户看板的最小闭环（后端）。
//
// 走**真实链路**：tools.SendMessageTool.Execute → tools 注入的 userNotice 落地实现
// → session_messages 结构化行 → StructuredInbox/StructuredCount（/api/inbox 的数据源）
// → StructuredResolve（/api/inbox/resolve 的数据源）→ agent.DeliverStructuredMessage。
//
// 覆盖判据：
//   a) to="user" 解析到用户会话并落库（pending 可查）
//   c) 收件箱能查到该条、unanswered=1，且**没有**唤醒用户会话（Silent）
//   d) approve 后发起 agent 收到回流（计数=1，断言不是"日志里有"）
//   e) 二次 approve 回流计数仍=1（CAS 幂等）

import (
	"path/filepath"
	"strings"
	"testing"

	"xbot/agent"
	"xbot/bus"
	"xbot/channel/web"
	"xbot/config"
	"xbot/protocol"
	"xbot/storage/sqlite"
	"xbot/tools"
)

func newNoticeAgent(t *testing.T) (*agent.Agent, *bus.MessageBus) {
	t.Helper()
	dir := t.TempDir()
	bs := bus.NewMessageBus()
	ag, err := agent.New(agent.Config{
		WorkDir:        dir,
		DBPath:         filepath.Join(dir, "xbot.db"),
		XbotHome:       dir,
		SandboxMode:    "none",
		MemoryProvider: "flat",
		Bus:            bs,
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}
	t.Cleanup(func() { _ = ag.Close() })
	return ag, bs
}

// userBoardCtx 是最上级 agent（深度 1）的工具上下文。
func userBoardCtx() *tools.ToolContext {
	return &tools.ToolContext{
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

func drainInbound(bs *bus.MessageBus) []bus.InboundMessage {
	var out []bus.InboundMessage
	for {
		select {
		case m := <-bs.Inbound:
			out = append(out, m)
		default:
			return out
		}
	}
}

func resolveInput(id int64, action, comment string) protocol.StructuredResolveInput {
	return protocol.StructuredResolveInput{MessageID: id, Action: action, Comment: comment}
}

func TestSendMessageUser_StoresCardAndInboxCountsIt(t *testing.T) {
	ag, bs := newNoticeAgent(t)
	cb := buildWebCallbacks(&config.Config{}, ag, nil)
	sel := web.SessionSelector{Channel: "web", ChatID: "web-4"}

	tool := &tools.SendMessageTool{}
	res, err := tool.Execute(userBoardCtx(),
		`{"to":"user","kind":"report","message":"N1 后端闭环：卡片已入库","payload":"{\"summary\":\"一句话结论\",\"needs_approval\":false}","idempotency_key":"n1-report-1"}`)
	if err != nil {
		t.Fatalf("Execute(to=user): %v", err)
	}
	if !strings.Contains(res.Summary, "web:web-4") {
		t.Fatalf("result = %q", res.Summary)
	}

	// 卡片落库但**不唤醒**用户会话（Silent）。
	if got := drainInbound(bs); len(got) != 0 {
		t.Fatalf("card append must not wake the user session, got %d inbound message(s)", len(got))
	}

	// 收件箱：/api/inbox 的数据源。
	items, hasMore, _, err := cb.StructuredInbox(sel, 0, 20)
	if err != nil {
		t.Fatalf("StructuredInbox: %v", err)
	}
	if len(items) != 1 || hasMore {
		t.Fatalf("inbox items = %d (hasMore=%v), want exactly 1", len(items), hasMore)
	}
	card := items[0]
	if card.Kind != "report" || card.Text != "N1 后端闭环：卡片已入库" {
		t.Fatalf("card = %+v", card)
	}
	if card.Answered || card.Cancelled {
		t.Fatalf("card must be pending: %+v", card)
	}
	if card.Sender.Type != "agent" || card.Sender.ID != "agent:main/court.shoufu" {
		t.Fatalf("card sender = %+v, want the originating agent", card.Sender)
	}
	n, err := cb.StructuredCount(sel)
	if err != nil {
		t.Fatalf("StructuredCount: %v", err)
	}
	if n != 1 {
		t.Fatalf("unanswered = %d, want 1", n)
	}
	// origin 必须随卡片一起持久化（回流目标不能靠内存）。
	origin := ""
	if o, ok := card.Envelope["origin"].(map[string]any); ok {
		origin, _ = o["session"].(string)
	}
	if origin != "web:web-4/court.shoufu:main" {
		t.Fatalf("envelope origin session = %q, want web:web-4/court.shoufu:main (envelope=%+v)", origin, card.Envelope)
	}

	// 幂等：同 key 再发一次不产生第二张卡。
	if _, err := tool.Execute(userBoardCtx(),
		`{"to":"user","kind":"report","message":"N1 后端闭环：卡片已入库","idempotency_key":"n1-report-1"}`); err != nil {
		t.Fatalf("duplicate Execute: %v", err)
	}
	if items, _, _, _ := cb.StructuredInbox(sel, 0, 20); len(items) != 1 {
		t.Fatalf("duplicate idempotency key created %d cards, want 1", len(items))
	}
}

// TestUserNoticeCard_StaysOutOfStream 是"出流"判据：
// 卡片不能出现在聊天流/历史回放/LLM 上下文里（用户要看的是看板，不是被消息淹没）。
//
// 三条断言：展示回放 0 条、LLM 回放 0 条、存储里这行的 record_type='notice'
// （仍是同一张 session_messages，不是第二套存储）。
func TestUserNoticeCard_StaysOutOfStream(t *testing.T) {
	ag, _ := newNoticeAgent(t)
	cb := buildWebCallbacks(&config.Config{}, ag, nil)
	sel := web.SessionSelector{Channel: "web", ChatID: "web-4"}

	const cardText = "出流卡片：这条不该出现在聊天流里"
	tool := &tools.SendMessageTool{}
	if _, err := tool.Execute(userBoardCtx(),
		`{"to":"user","kind":"report","message":"`+cardText+`","idempotency_key":"n1-stream-1"}`); err != nil {
		t.Fatalf("Execute(to=user): %v", err)
	}
	items, _, _, ierr := cb.StructuredInbox(sel, 0, 20)
	if ierr != nil || len(items) != 1 {
		t.Fatalf("board must still see the card: items=%d err=%v", len(items), ierr)
	}

	sess, err := ag.MultiSession().GetOrCreateSession(sel.Channel, sel.ChatID)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	svc, ok := sess.SessionService().(*sqlite.SessionService)
	if !ok {
		t.Fatal("unexpected session backend")
	}
	tenantID := sess.TenantID()

	// ① 存储里确实有一行，且 record_type 是 notice（复用同一张表）。
	records, err := svc.GetFullHistory(tenantID)
	if err != nil {
		t.Fatalf("GetFullHistory: %v", err)
	}
	noticeRows := 0
	for _, r := range records {
		if r.Type == sqlite.HistoryRecordNotice {
			noticeRows++
		}
	}
	if noticeRows != 1 {
		t.Fatalf("notice rows = %d, want 1 (record_type=notice in session_messages)", noticeRows)
	}
	// 同一行的正文就是卡片内容（GetFullHistory 只给 message/command 填 Message，
	// 所以正文从结构化行读）。
	cardRow, err := svc.GetStructuredMessageByID(tenantID, items[0].MessageID)
	if err != nil {
		t.Fatalf("GetStructuredMessageByID: %v", err)
	}
	if cardRow.Content != cardText {
		t.Fatalf("notice row content = %q, want %q", cardRow.Content, cardText)
	}

	// ② 展示回放（聊天流）里 0 条。
	display, err := svc.ReplayForDisplay(tenantID)
	if err != nil {
		t.Fatalf("ReplayForDisplay: %v", err)
	}
	for _, m := range display.Messages {
		if strings.Contains(m.Content, cardText) {
			t.Fatalf("card leaked into the chat stream: %+v", m)
		}
	}

	// ③ LLM 上下文里 0 条。
	llmReplay, err := svc.Replay(tenantID)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	for _, m := range llmReplay.Messages {
		if strings.Contains(m.Content, cardText) {
			t.Fatalf("card leaked into the LLM context: %+v", m)
		}
	}
}

// TestUserNoticeResolve_FeedbackOnce 覆盖判据 d + e：
// approve 后发起 agent 收到 1 条回流；再次 approve 仍只有 1 条（CAS 幂等）。
func TestUserNoticeResolve_FeedbackOnce(t *testing.T) {
	ag, bs := newNoticeAgent(t)
	cb := buildWebCallbacks(&config.Config{}, ag, nil)
	sel := web.SessionSelector{Channel: "web", ChatID: "web-4"}

	tool := &tools.SendMessageTool{}
	if _, err := tool.Execute(userBoardCtx(),
		`{"to":"user","kind":"report","message":"请批复：N1 后端最小闭环","payload":"{\"summary\":\"待批\",\"needs_approval\":true}","idempotency_key":"n1-approve-1"}`); err != nil {
		t.Fatalf("Execute(to=user): %v", err)
	}
	items, _, _, err := cb.StructuredInbox(sel, 0, 20)
	if err != nil || len(items) != 1 {
		t.Fatalf("inbox before resolve: items=%d err=%v", len(items), err)
	}
	cardID := items[0].MessageID

	// ① 用户 approve → 状态 resolved + 回流 1 条
	first, err := cb.StructuredResolve("web-4", sel, resolveInput(cardID, "approve", "同意，继续"))
	if err != nil {
		t.Fatalf("StructuredResolve(approve): %v", err)
	}
	if first.Status != "approved" || first.Duplicate {
		t.Fatalf("first resolve = %+v, want approved & not duplicate", first)
	}
	if first.Origin != "web:web-4/court.shoufu:main" {
		t.Fatalf("origin = %q, want the card's origin session", first.Origin)
	}
	got := drainInbound(bs)
	if len(got) != 1 {
		t.Fatalf("feedback count = %d, want exactly 1", len(got))
	}
	fb := got[0]
	if fb.Channel != "agent" || fb.ChatID != "web:web-4/court.shoufu:main" {
		t.Fatalf("feedback went to %s:%s, want agent:web:web-4/court.shoufu:main", fb.Channel, fb.ChatID)
	}
	if fb.Metadata["structured_kind"] != "decision" {
		t.Fatalf("feedback metadata = %+v, want structured_kind=decision", fb.Metadata)
	}
	if fb.Metadata["structured_delivery"] != "true" {
		t.Fatalf("feedback metadata = %+v, want structured_delivery=true", fb.Metadata)
	}

	// 收件箱收敛：unanswered 归零。
	if n, _ := cb.StructuredCount(sel); n != 0 {
		t.Fatalf("unanswered after approve = %d, want 0", n)
	}

	// ② 二次 approve（同卡、同动作）→ 幂等命中，不再回流
	second, err := cb.StructuredResolve("web-4", sel, resolveInput(cardID, "approve", "同意，继续"))
	if err != nil {
		t.Fatalf("second StructuredResolve(approve) must be idempotent, got error: %v", err)
	}
	if !second.Duplicate {
		t.Fatalf("second resolve must report duplicate: %+v", second)
	}
	if again := drainInbound(bs); len(again) != 0 {
		t.Fatalf("second approve produced %d extra feedback(s), want 0", len(again))
	}

	// ③ 换一个动作也不行（状态已 resolved）：必须报 409 语义错误，且不再回流。
	if _, err := cb.StructuredResolve("web-4", sel, resolveInput(cardID, "reject", "反对")); err == nil {
		t.Fatal("resolving an already resolved card with another action must fail")
	} else if !strings.Contains(err.Error(), "already answered") {
		t.Fatalf("error = %v, want already answered", err)
	}
	if again := drainInbound(bs); len(again) != 0 {
		t.Fatalf("rejected re-resolve produced %d extra feedback(s), want 0", len(again))
	}
}

// TestUserNoticeResolve_AllActionsFeedbackOnce 是判据 d 的全动作覆盖：
// approve / reject / answer / dismiss 四种交互都必须让发起 agent 收到**恰好一条**回流。
func TestUserNoticeResolve_AllActionsFeedbackOnce(t *testing.T) {
	ag, bs := newNoticeAgent(t)
	cb := buildWebCallbacks(&config.Config{}, ag, nil)
	sel := web.SessionSelector{Channel: "web", ChatID: "web-4"}
	tool := &tools.SendMessageTool{}

	cases := []struct {
		action string
		status string
	}{
		{"approve", "approved"},
		{"reject", "rejected"},
		{"answer", "answered"},
		{"dismiss", "dismissed"},
	}
	for _, c := range cases {
		cardJSON := `{"to":"user","kind":"report","message":"待处理卡片 ` + c.action + `","idempotency_key":"n1-all-` + c.action + `"}`
		if _, err := tool.Execute(userBoardCtx(), cardJSON); err != nil {
			t.Fatalf("%s: Execute: %v", c.action, err)
		}
		items, _, _, err := cb.StructuredInbox(sel, 0, 20)
		if err != nil {
			t.Fatalf("%s: inbox: %v", c.action, err)
		}
		// 前一张卡已被处理（出收件箱），所以任何时刻 pending 都只有刚发的这一张。
		if len(items) != 1 {
			t.Fatalf("%s: pending cards = %d, want 1", c.action, len(items))
		}
		// 收件箱按 id DESC，取最新的一张（就是刚发的）。
		cardID := items[0].MessageID
		res, err := cb.StructuredResolve("web-4", sel, resolveInput(cardID, c.action, "comment-"+c.action))
		if err != nil {
			t.Fatalf("%s: resolve: %v", c.action, err)
		}
		if res.Status != c.status || res.Duplicate {
			t.Fatalf("%s: result = %+v, want status=%s not duplicate", c.action, res, c.status)
		}
		got := drainInbound(bs)
		if len(got) != 1 {
			t.Fatalf("%s: feedback count = %d, want 1", c.action, len(got))
		}
		// 回流行必须落在**发起方会话**里（agent 醒来从历史里读）；
		// DeliverStructuredMessage 对空闲会话投的是空 content，所以内容靠这一行。
		if texts := feedbackTexts(t, ag, "web:web-4/court.shoufu:main"); !containsLine(texts, "comment-"+c.action) {
			t.Fatalf("%s: feedback row missing the user's comment; rows=%v", c.action, texts)
		}
	}
	if n, _ := cb.StructuredCount(sel); n != 0 {
		t.Fatalf("unanswered = %d, want 0 after resolving all cards", n)
	}
	// 每张卡只留一条回流记录（幂等键 notice-feedback:<id>）。
	texts := feedbackTexts(t, ag, "web:web-4/court.shoufu:main")
	if len(texts) != len(cases) {
		t.Fatalf("feedback rows = %d, want %d: %v", len(texts), len(cases), texts)
	}
}

// feedbackTexts 列出某个发起方会话里所有回流记录（decision/answer）的正文。
func feedbackTexts(t *testing.T, ag *agent.Agent, originSession string) []string {
	t.Helper()
	sess, err := ag.MultiSession().GetOrCreateSession("agent", originSession)
	if err != nil {
		t.Fatalf("origin session: %v", err)
	}
	svc, ok := sess.SessionService().(*sqlite.SessionService)
	if !ok {
		t.Fatal("unexpected session backend")
	}
	var out []string
	for _, kind := range []string{"decision", "answer"} {
		rows, _, _, err := svc.ListStructuredMessages(sess.TenantID(), kind, 0, 100)
		if err != nil {
			t.Fatalf("list %s: %v", kind, err)
		}
		for _, r := range rows {
			out = append(out, r.Content)
		}
	}
	return out
}

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}

// TestUserNoticeResolve_Dismiss 覆盖 dismiss 分支：状态 dismissed、卡片出收件箱、
// 回流仍然只发一次。
func TestUserNoticeResolve_Dismiss(t *testing.T) {
	ag, bs := newNoticeAgent(t)
	cb := buildWebCallbacks(&config.Config{}, ag, nil)
	sel := web.SessionSelector{Channel: "web", ChatID: "web-4"}

	tool := &tools.SendMessageTool{}
	if _, err := tool.Execute(userBoardCtx(),
		`{"to":"user","kind":"ask_user","message":"要不要现在做？","payload":"{\"questions\":[{\"id\":\"q1\",\"question\":\"现在做吗\"}]}","idempotency_key":"n1-dismiss-1"}`); err != nil {
		t.Fatalf("Execute(to=user): %v", err)
	}
	items, _, _, err := cb.StructuredInbox(sel, 0, 20)
	if err != nil || len(items) != 1 {
		t.Fatalf("inbox: items=%d err=%v", len(items), err)
	}
	cardID := items[0].MessageID

	res, err := cb.StructuredResolve("web-4", sel, resolveInput(cardID, "dismiss", "先不做"))
	if err != nil {
		t.Fatalf("StructuredResolve(dismiss): %v", err)
	}
	if res.Status != "dismissed" || res.Duplicate {
		t.Fatalf("resolve = %+v, want dismissed", res)
	}
	if n, _ := cb.StructuredCount(sel); n != 0 {
		t.Fatalf("unanswered after dismiss = %d, want 0", n)
	}
	if got := drainInbound(bs); len(got) != 1 {
		t.Fatalf("dismiss feedback count = %d, want 1", len(got))
	}
	if res2, err := cb.StructuredResolve("web-4", sel, resolveInput(cardID, "dismiss", "先不做")); err != nil || !res2.Duplicate {
		t.Fatalf("second dismiss = %+v, %v (want duplicate)", res2, err)
	}
	if got := drainInbound(bs); len(got) != 0 {
		t.Fatalf("second dismiss produced %d extra feedback(s)", len(got))
	}
}

// TestUserNoticeResolve_RejectsNonCard：非看板行（event）与不存在的 id 必须拒绝，
// 非法动作同样拒绝 —— 避免把任意结构化行当卡片"批复"。
func TestUserNoticeResolve_RejectsNonCard(t *testing.T) {
	ag, bs := newNoticeAgent(t)
	cb := buildWebCallbacks(&config.Config{}, ag, nil)
	sel := web.SessionSelector{Channel: "web", ChatID: "web-4"}

	msg, _, err := cb.StructuredAppend("web-4", sel, protocol.StructuredMessageInput{
		Kind: "event", Payload: map[string]any{"summary": "巡检完成"}, IdempotencyKey: "evt-1",
	})
	if err != nil {
		t.Fatalf("StructuredAppend(event): %v", err)
	}
	if _, err := cb.StructuredResolve("web-4", sel, resolveInput(msg.MessageID, "approve", "")); err == nil {
		t.Fatal("event rows must not be resolvable from the board")
	}
	if _, err := cb.StructuredResolve("web-4", sel, resolveInput(999999, "approve", "")); err == nil {
		t.Fatal("missing card must not resolve")
	}
	if _, err := cb.StructuredResolve("web-4", sel, resolveInput(msg.MessageID, "bogus", "")); err == nil {
		t.Fatal("invalid action must be rejected")
	}
	if got := drainInbound(bs); len(got) != 0 {
		t.Fatalf("rejected resolves produced %d feedback(s), want 0", len(got))
	}
}
