package serverapp

// user_notice_reflow_correlation_test.go — N1 断言③：回流消息的 correlation_id
// 必须与**卡片创建时**的值逐字相等。
//
// 为什么不能只断言"非空"：回流是"用户处理了哪张卡"的唯一凭据。若回流自己造一把
// 钥匙（旧实现是 notice-<message_id>），发起 agent 拿到的钥匙与卡片不是同一把，
// 前端/审计就无法把「我提的这条待办」和「用户对它的处理」对上——两把钥匙各自
// 合法、却永远配不上对。
//
// 覆盖两条路径：
//   a) 正常 approve → 恰好 1 条回流，correlation_id 逐字等于卡片的值：
//      存储行（发起方会话里的 decision 行）+ 投递元数据（唤醒消息 metadata）两处都查；
//   b) 重复处理（同动作幂等 / 换动作 409）→ 不产生第二条回流，键值不变。
//
// 走真实链路：SendMessageTool.Execute(to="user") → serverapp appendFn 落库
// → StructuredResolve 回调（CAS + 回流）→ sqlite / bus。

import (
	"strings"
	"testing"

	"xbot/agent"
	"xbot/bus"
	"xbot/channel/web"
	"xbot/config"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// noticeDecisionRows 读出发起方会话里的全部回流行（decision/answer），按 id 降序。
func noticeDecisionRows(t *testing.T, ag *agent.Agent, originSession string) []sqlite.StructuredMessage {
	t.Helper()
	sess, err := ag.MultiSession().GetOrCreateSession("agent", originSession)
	if err != nil {
		t.Fatalf("origin session %q: %v", originSession, err)
	}
	svc, ok := sess.SessionService().(*sqlite.SessionService)
	if !ok {
		t.Fatal("unexpected session backend")
	}
	var out []sqlite.StructuredMessage
	for _, kind := range []string{"decision", "answer"} {
		rows, _, _, err := svc.ListStructuredMessages(sess.TenantID(), kind, 0, 100)
		if err != nil {
			t.Fatalf("list %s: %v", kind, err)
		}
		out = append(out, rows...)
	}
	return out
}

// drainStructuredReflow 只取「结构化消息投递」（structured_delivery=true），即回流本身。
//
// 为什么不数全部 inbound：bus.Inbound 是**全进程共享**的入站队列，同一次处理里可能
// 夹着与本判据无关的注入（2026-10-30T00:55:40Z 机器高负载下实测偶发多出 1 条，
// 让「重复 approve 不得再投递」假红）。判据本身**不放松**：若重复 approve 真的又投递了
// 一条结构化回流，它仍会被数到并判红。被过滤的消息一律 t.Logf 留痕，便于事后归因。
func drainStructuredReflow(t *testing.T, bs *bus.MessageBus, step string) []bus.InboundMessage {
	t.Helper()
	var out []bus.InboundMessage
	for {
		select {
		case m := <-bs.Inbound:
			if m.Metadata["structured_delivery"] == "true" {
				out = append(out, m)
				continue
			}
			t.Logf("%s: 过滤非结构化 inbound（channel=%q chat=%q sender=%q kind=%q content=%q）",
				step, m.Channel, m.ChatID, m.SenderID, m.Metadata["structured_kind"], m.Content)
		default:
			return out
		}
	}
}

func TestUserNoticeReflow_CarriesCardCorrelationID(t *testing.T) {
	ag, bs := newNoticeAgent(t)
	cb := buildWebCallbacks(&config.Config{}, ag, nil)
	sel := web.SessionSelector{Channel: "web", ChatID: "web-4"}

	tool := &tools.SendMessageTool{}
	const cardText = "N1 断言③：回流必须带同一把 correlation_id"
	if _, err := tool.Execute(userBoardCtx(),
		`{"to":"user","kind":"report","message":"`+cardText+`","idempotency_key":"n1-corr-1"}`); err != nil {
		t.Fatalf("Execute(to=user): %v", err)
	}

	items, _, _, err := cb.StructuredInbox(sel, 0, 20)
	if err != nil || len(items) != 1 {
		t.Fatalf("inbox before resolve: items=%d err=%v", len(items), err)
	}
	card := items[0]
	cardCorr := strings.TrimSpace(card.CorrelationID)
	// 卡片自身必须带钥匙：否则"回流对齐"无源可对（空 == 空 是假绿）。
	if cardCorr == "" {
		t.Fatalf("卡片创建时没有 correlation_id（card.Envelope=%+v）：回流无从对齐", card.Envelope)
	}

	// ── a) 正常 approve：回流 1 条，钥匙逐字相等 ─────────────────────────────
	res, err := cb.StructuredResolve("web-4", sel, resolveInput(card.MessageID, "approve", "同意，继续"))
	if err != nil {
		t.Fatalf("StructuredResolve(approve): %v", err)
	}
	if res.Duplicate {
		t.Fatalf("first resolve must not be a duplicate: %+v", res)
	}
	rows := noticeDecisionRows(t, ag, "web:web-4/court.shoufu:main")
	if len(rows) != 1 {
		t.Fatalf("回流存储行 = %d, want exactly 1: %+v", len(rows), rows)
	}
	if rows[0].CorrelationID != cardCorr {
		t.Fatalf("回流存储行 correlation_id = %q, want %q（必须与卡片逐字相等）", rows[0].CorrelationID, cardCorr)
	}
	got := drainStructuredReflow(t, bs, "首次 approve")
	if len(got) != 1 {
		t.Fatalf("回流投递 = %d, want exactly 1", len(got))
	}
	if mdCorr := got[0].Metadata["correlation_id"]; mdCorr != cardCorr {
		t.Fatalf("回流投递 metadata correlation_id = %q, want %q（投递层也必须对齐）", mdCorr, cardCorr)
	}

	// ── b) 重复处理不产生第二条回流，键值不变 ───────────────────────────────
	dup, err := cb.StructuredResolve("web-4", sel, resolveInput(card.MessageID, "approve", "同意，继续"))
	if err != nil {
		t.Fatalf("重复 approve 必须幂等（CAS），got error: %v", err)
	}
	if !dup.Duplicate {
		t.Fatalf("重复 approve 必须标记 duplicate: %+v", dup)
	}
	if extra := drainStructuredReflow(t, bs, "重复 approve"); len(extra) != 0 {
		t.Fatalf("重复 approve 产生了 %d 条额外回流（want 0）", len(extra))
	}
	if _, err := cb.StructuredResolve("web-4", sel, resolveInput(card.MessageID, "reject", "反对")); err == nil {
		t.Fatal("已处理卡片换动作必须报 409 语义错误")
	} else if !strings.Contains(err.Error(), "already answered") {
		t.Fatalf("换动作错误 = %v, want already answered", err)
	}
	if extra := drainStructuredReflow(t, bs, "换动作被拒"); len(extra) != 0 {
		t.Fatalf("换动作重投产生了 %d 条额外回流（want 0）", len(extra))
	}
	rowsAfter := noticeDecisionRows(t, ag, "web:web-4/court.shoufu:main")
	if len(rowsAfter) != 1 {
		t.Fatalf("重复处理后回流行 = %d, want 1（不得二次回流）", len(rowsAfter))
	}
	if rowsAfter[0].CorrelationID != cardCorr {
		t.Fatalf("重复处理后回流行 correlation_id = %q, want %q（不得被重写）", rowsAfter[0].CorrelationID, cardCorr)
	}
}

// ── N1 断言①：回流幂等（计数 == 1 且卡片状态不二次推进）────────────────────
//
// 判据两条，都用整数/状态值断言，不看日志：
//   a) 同一张卡 approve 两次 → 发起 agent 收到的回流**恰好 1 条**（投递计数 == 1，
//      回流行 == 1）；第二次是幂等命中，不得再投递；
//   b) 第二次 approve 之后卡片状态**不变**：仍 approved（answered=1 / cancelled=0），
//      且处理记录行 id 与首次相同（不是"又 resolved 了一次"）。

// noticeCardRow 读出用户会话里的卡片行（CAS 之后的真实状态）。
func noticeCardRow(t *testing.T, ag *agent.Agent, id int64) *sqlite.StructuredMessage {
	t.Helper()
	sess, err := ag.MultiSession().GetOrCreateSession("web", "web-4")
	if err != nil {
		t.Fatalf("user session: %v", err)
	}
	svc, ok := sess.SessionService().(*sqlite.SessionService)
	if !ok {
		t.Fatal("unexpected session backend")
	}
	row, err := svc.GetStructuredMessageByID(sess.TenantID(), id)
	if err != nil {
		t.Fatalf("GetStructuredMessageByID(%d): %v", id, err)
	}
	return row
}

func TestUserNoticeResolve_ReflowCountExactlyOnce(t *testing.T) {
	ag, bs := newNoticeAgent(t)
	cb := buildWebCallbacks(&config.Config{}, ag, nil)
	sel := web.SessionSelector{Channel: "web", ChatID: "web-4"}
	originSession := "web:web-4/court.shoufu:main"

	tool := &tools.SendMessageTool{}
	if _, err := tool.Execute(userBoardCtx(),
		`{"to":"user","kind":"report","message":"请批复：N1 断言① 幂等计数","idempotency_key":"n1-idem-1"}`); err != nil {
		t.Fatalf("Execute(to=user): %v", err)
	}
	items, _, _, err := cb.StructuredInbox(sel, 0, 20)
	if err != nil || len(items) != 1 {
		t.Fatalf("inbox: items=%d err=%v", len(items), err)
	}
	cardID := items[0].MessageID

	// ① 首次 approve：回流计数 == 1（整数断言）
	first, err := cb.StructuredResolve("web-4", sel, resolveInput(cardID, "approve", "同意"))
	if err != nil {
		t.Fatalf("first approve: %v", err)
	}
	if first.Duplicate {
		t.Fatalf("首次处理不得是 duplicate: %+v", first)
	}
	delivered := drainStructuredReflow(t, bs, "首次 approve")
	if len(delivered) != 1 {
		t.Fatalf("首次 approve 回流投递 = %d, want exactly 1", len(delivered))
	}
	if rows := noticeDecisionRows(t, ag, originSession); len(rows) != 1 {
		t.Fatalf("首次 approve 回流行 = %d, want exactly 1", len(rows))
	}
	before := noticeCardRow(t, ag, cardID)
	if !before.Answered || before.Cancelled {
		t.Fatalf("首次 approve 后卡片状态 = answered:%v cancelled:%v, want answered:true cancelled:false",
			before.Answered, before.Cancelled)
	}

	// ② 二次 approve：幂等命中，不再投递，也不产生第二行
	second, err := cb.StructuredResolve("web-4", sel, resolveInput(cardID, "approve", "同意"))
	if err != nil {
		t.Fatalf("二次 approve 必须幂等，got error: %v", err)
	}
	if !second.Duplicate {
		t.Fatalf("二次 approve 必须标记 duplicate: %+v", second)
	}
	if second.ResolutionID != first.ResolutionID {
		t.Fatalf("二次 approve 处理记录 id = %d, want %d（不得二次 resolved）",
			second.ResolutionID, first.ResolutionID)
	}
	if extra := drainStructuredReflow(t, bs, "二次 approve"); len(extra) != 0 {
		t.Fatalf("二次 approve 回流投递 = %d, want 0（总计数必须仍为 1）", len(extra))
	}
	if rows := noticeDecisionRows(t, ag, originSession); len(rows) != 1 {
		t.Fatalf("二次 approve 回流行 = %d, want exactly 1", len(rows))
	}
	after := noticeCardRow(t, ag, cardID)
	if !after.Answered || after.Cancelled {
		t.Fatalf("二次 approve 后卡片状态被改动 = answered:%v cancelled:%v, want answered:true cancelled:false",
			after.Answered, after.Cancelled)
	}
	if after.ID != before.ID || after.Content != before.Content || !after.CreatedAt.Equal(before.CreatedAt) {
		t.Fatalf("二次 approve 重写了卡片行: before=%+v after=%+v", before, after)
	}
}
