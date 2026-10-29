package agent

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"xbot/llm"
	"xbot/protocol"
	"xbot/storage/sqlite"
)

// TestAskUserTTLThirtyMinutesReal 是 D-03 TTL 的**真实时长**实测（默认 skip）。
//
// 为什么要它：短 TTL / 倒拨时间只能证明分支可达，证不了「30 分钟后真的发生」。
// 御前 2026-10-29 令：必须另有一次真实 ≥31 分钟的实测。
//
// 跑法（约 31 分钟）：
//
//	XBOT_TTL_REAL=1 go test ./agent/ -run TestAskUserTTLThirtyMinutesReal -timeout 40m -v
//
// 断言五段（御前收尾判据，任一不满足即红）：
//
//	T+0    ：新鲜提问保留（插话不作废），且不广播、不落取消记录；
//	T+29m  ：仍在 TTL 内 ⇒ 仍保留，且仍不广播、不落取消记录；
//	T+31m  ：超过 TTL ⇒
//	         a) 内存 pending 清空；
//	         b) 广播 ask_user_resolved(reason=cancelled)（含 channel/chat_id/request_id；
//	            侧栏角标据此消失）；
//	         c) 历史里留下**用户可见**的一句话：
//	            "[cancelled] 该提问因超时未回答已关闭（ttl_expired）"；
//	         d) 收件箱 ask_user 行 cancelled=true，且取消记录 reason=ttl_expired。
func TestAskUserTTLThirtyMinutesReal(t *testing.T) {
	if os.Getenv("XBOT_TTL_REAL") != "1" {
		t.Skip("real-time TTL test skipped; set XBOT_TTL_REAL=1 to run (~31 min)")
	}
	const (
		chatID        = "chat-ttl-real"
		requestID     = "req-ttl-real"
		correlationID = "ask-ttl-real"
		wantHistory   = "[cancelled] 该提问因超时未回答已关闭（ttl_expired）"
	)
	mt, _ := newAgentHistorySession(t)
	sess, err := mt.GetOrCreateSession("web", chatID)
	if err != nil {
		t.Fatal(err)
	}
	svc, ok := sess.SessionService().(*sqlite.SessionService)
	if !ok {
		t.Fatal("session service is not *sqlite.SessionService")
	}
	tenantID := sess.TenantID()

	// 历史里的 ask_question 控制记录：AppendAskAnswer 的前置条件。没有它，
	// 「超时收回」只能清内存/置 cancelled，写不出用户可见的取消原因——
	// 那正是御前指出的缺口，所以真跑必须先把提问按真实链路落进历史。
	if _, err := sess.AppendMessage(llm.ChatMessage{
		Role:      "assistant",
		ToolCalls: []llm.ToolCall{{ID: "ask", Name: "AskUser", Arguments: `{}`}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendMessage(llm.NewToolMessage("AskUser", "ask", `{}`, "waiting")); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendAskQuestion(map[string]string{"request_id": requestID}); err != nil {
		t.Fatal(err)
	}

	// 收件箱里的结构化提问（/api/message kind=ask_user 的落库形态）。
	if _, _, err := svc.AppendStructuredMessage(tenantID, sqlite.StructuredMessage{
		ChatID: chatID, Kind: sqlite.StructuredKindAskUser, CorrelationID: correlationID,
		Direction: "in", Role: "assistant", Content: "真实 31 分钟实测",
		SenderType: "user", SenderID: "web-1", SenderName: "web-1", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	a := &Agent{multiSession: mt}
	fake := &fakeResolvedSender{}
	wireResolvedSender(a, fake)

	a.setPendingAskUser("web", chatID, &protocol.ProgressEvent{RequestID: requestID})

	// T+0：新鲜提问必须原样保留，且不得有任何「已收尾」的副作用。
	a.autoResolveStalePendingAskUser("web", chatID)
	if a.GetPendingAskUser("web", chatID) == nil {
		t.Fatal("T+0：新鲜提问被收回（D-03 回归）")
	}
	assertNoAskUserCloseArtifacts(t, svc, tenantID, correlationID, "T+0")
	if ev := fake.snapshot(); len(ev) != 0 {
		t.Fatalf("T+0：新鲜提问不该广播 resolved：%+v", ev)
	}
	t.Logf("T+0 保留 OK（ttl=%s）", pendingAskUserTTL)

	// T+29m：仍在 TTL 内 ⇒ 保留。
	time.Sleep(29 * time.Minute)
	a.autoResolveStalePendingAskUser("web", chatID)
	if a.GetPendingAskUser("web", chatID) == nil {
		t.Fatal("T+29m：还没到 TTL 就被收回")
	}
	assertNoAskUserCloseArtifacts(t, svc, tenantID, correlationID, "T+29m")
	if ev := fake.snapshot(); len(ev) != 0 {
		t.Fatalf("T+29m：还没到 TTL 就广播了 resolved：%+v", ev)
	}
	t.Log("T+29m 仍保留 OK（未广播、未落取消记录）")

	// T+31m：超过 TTL ⇒ 必须收敛，并留下四类可查证据。
	time.Sleep(2 * time.Minute)
	a.autoResolveStalePendingAskUser("web", chatID)
	if a.GetPendingAskUser("web", chatID) != nil {
		t.Fatal("T+31m：超过 TTL 仍未收回（pending 永远挂住）")
	}

	// b) 广播：侧栏/前端凭这条事件撤掉角标。
	ev := fake.snapshot()
	if len(ev) != 1 {
		t.Fatalf("T+31m：应恰好广播一条 ask_user_resolved，实际 %d 条：%+v", len(ev), ev)
	}
	got := ev[0]
	if got.Channel != "web" || got.ChatID != chatID || got.RequestID != requestID || got.Reason != "cancelled" {
		t.Fatalf("T+31m：resolved 广播内容不对（侧栏角标撤不掉）：%+v", got)
	}
	t.Logf("T+31m 广播 OK：%+v", got)

	// d) 收件箱：问句行 cancelled=true（否则用户看到一条永远答不上的提问）。
	qrows, _, _, err := svc.ListStructuredMessages(tenantID, sqlite.StructuredKindAskUser, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	question := findStructuredByCorrelation(qrows, correlationID)
	if question == nil {
		t.Fatalf("T+31m：收件箱里找不到 correlation_id=%s 的提问：%+v", correlationID, qrows)
	}
	if !question.Cancelled || question.Answered {
		t.Fatalf("T+31m：提问行应为 cancelled=true/answered=false，实际 %+v", *question)
	}

	// d) 取消记录：reason=ttl_expired（用户可查「为什么被关掉」）。
	arows, _, _, err := svc.ListStructuredMessages(tenantID, sqlite.StructuredKindAnswer, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	answer := findStructuredByCorrelation(arows, correlationID)
	if answer == nil {
		t.Fatalf("T+31m：没有 correlation_id=%s 的取消记录行：%+v", correlationID, arows)
	}
	if !answer.Cancelled {
		t.Fatalf("T+31m：取消记录行 cancelled=false：%+v", *answer)
	}
	var payload struct {
		Reason    string `json:"reason"`
		Cancelled bool   `json:"cancelled"`
	}
	if err := json.Unmarshal([]byte(answer.StructuredJSON), &payload); err != nil {
		t.Fatalf("T+31m：取消记录 structured_json 解析失败：%v（%s）", err, answer.StructuredJSON)
	}
	if payload.Reason != "ttl_expired" || !payload.Cancelled {
		t.Fatalf("T+31m：取消原因不是 ttl_expired：%s", answer.StructuredJSON)
	}

	// c) 历史可见记录：Replay / 时间线上能看到的「为什么关掉」。
	records, err := svc.GetFullHistory(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	historyFound := false
	for _, rec := range records {
		if rec.Type != sqlite.HistoryRecordAskAnswer {
			continue
		}
		var ans sqlite.AskAnswerRecord
		if err := json.Unmarshal(rec.Data, &ans); err != nil {
			continue
		}
		if strings.Contains(ans.Answer, wantHistory) {
			historyFound = true
			break
		}
	}
	if !historyFound {
		t.Fatalf("T+31m：历史里没有可见记录 %q（提问悄悄消失，用户看不出原因）", wantHistory)
	}
	t.Logf("T+31m 收回 OK：cancelled=%v reason=%s", question.Cancelled, payload.Reason)
}

// findStructuredByCorrelation 取指定 correlation_id 的最新一条结构化消息
// （ListStructuredMessages 按 id DESC 返回，首个命中即最新）。
func findStructuredByCorrelation(rows []sqlite.StructuredMessage, correlationID string) *sqlite.StructuredMessage {
	for i := range rows {
		if rows[i].CorrelationID == correlationID {
			return &rows[i]
		}
	}
	return nil
}

// assertNoAskUserCloseArtifacts 断言「提问还没到点」时没有任何收尾痕迹：
// 没有取消/回答记录行、提问行未 answered/未 cancelled、没有 resolved 广播。
//
// 为什么必须断言：只测「到点后收回了」的话，把 TTL 判据改成 0（立刻收回）
// 也会绿——那正是 D-03 的旧缺陷形态。
func assertNoAskUserCloseArtifacts(t *testing.T, svc *sqlite.SessionService, tenantID int64, correlationID, stage string) {
	t.Helper()
	answers, _, _, err := svc.ListStructuredMessages(tenantID, sqlite.StructuredKindAnswer, 0, 50)
	if err != nil {
		t.Fatalf("%s：列取消记录失败：%v", stage, err)
	}
	if row := findStructuredByCorrelation(answers, correlationID); row != nil {
		t.Fatalf("%s：未到 TTL 就写了回答/取消记录：%+v", stage, *row)
	}
	questions, _, _, err := svc.ListStructuredMessages(tenantID, sqlite.StructuredKindAskUser, 0, 50)
	if err != nil {
		t.Fatalf("%s：列收件箱提问失败：%v", stage, err)
	}
	if row := findStructuredByCorrelation(questions, correlationID); row != nil {
		if row.Cancelled || row.Answered {
			t.Fatalf("%s：未到 TTL 提问行就被置终态：%+v", stage, *row)
		}
	}
}
