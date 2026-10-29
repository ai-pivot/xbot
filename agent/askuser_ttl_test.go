package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"xbot/bus"
	"xbot/channel"
	"xbot/llm"
	"xbot/protocol"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// D-03 的终点判据（不启服务）：
//   - 插话把「新鲜提问」保留下来（不得静默作废）；
//   - 只有超过 pendingAskUserTTL 才收回，且收回必须留下**用户可查**的终态：
//     内存 pending 清空 + 广播 resolved(cancelled)（前端据此撤角标）
//     + 历史配对 ask_answer("[cancelled]") + 收件箱里那条结构化 ask_user
//     行 cancelled=true（否则就是「还挂着但答不上」的静默蒸发）。
//
// 反证：把 autoResolveStalePendingAskUser 的 TTL 判据改回旧行为（任何新 turn
// 都作废），TestAskUserInterruptKeepsFreshPending 必红。

type fakeResolvedSender struct {
	mu     sync.Mutex
	events []protocol.AskUserResolvedEvent
}

func (f *fakeResolvedSender) SendAskUserResolved(ev protocol.AskUserResolvedEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
}
func (f *fakeResolvedSender) snapshot() []protocol.AskUserResolvedEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]protocol.AskUserResolvedEvent(nil), f.events...)
}
func (f *fakeResolvedSender) Name() string                             { return "fake" }
func (f *fakeResolvedSender) Start() error                             { return nil }
func (f *fakeResolvedSender) Stop()                                    {}
func (f *fakeResolvedSender) Send(channel.OutboundMsg) (string, error) { return "", nil }

func wireResolvedSender(a *Agent, fake *fakeResolvedSender) {
	a.channelRange = func(fn func(string, channel.Channel) bool) { fn("fake", fake) }
}

func backdatePending(t *testing.T, a *Agent, ch, chatID string, age time.Duration) {
	t.Helper()
	value, ok := a.waitingUserSessions.Load(qualifyChatID(ch, chatID))
	if !ok {
		t.Fatalf("pending entry not registered for %s:%s", ch, chatID)
	}
	entry := value.(*pendingAskUserEntry)
	entry.mu.Lock()
	entry.createdAt = time.Now().Add(-age)
	entry.mu.Unlock()
}

// 插话（新 turn 开始调用 same autoResolveStalePendingAskUser）不得作废新鲜提问。
func TestAskUserInterruptKeepsFreshPending(t *testing.T) {
	mt, _ := newAgentHistorySession(t)
	a := &Agent{multiSession: mt}
	fake := &fakeResolvedSender{}
	wireResolvedSender(a, fake)

	a.setPendingAskUser("web", "chat-fresh", &protocol.ProgressEvent{RequestID: "req-fresh"})
	a.autoResolveStalePendingAskUser("web", "chat-fresh")

	if pending := a.GetPendingAskUser("web", "chat-fresh"); pending == nil {
		t.Fatal("插话把待答提问丢了（D-03 回归）：pending 应为非 nil")
	}
	if ev := fake.snapshot(); len(ev) != 0 {
		t.Fatalf("插话不应广播 ask_user_resolved：%+v", ev)
	}
}

// 超时收回：内存 + 前端（广播）+ 历史配对 + 收件箱行 cancelled=true，四证齐全。
func TestAskUserInterruptReapsExpiredPending(t *testing.T) {
	mt, _ := newAgentHistorySession(t)
	sess, err := mt.GetOrCreateSession("web", "chat-stale")
	if err != nil {
		t.Fatal(err)
	}
	svc, ok := sess.SessionService().(*sqlite.SessionService)
	if !ok {
		t.Fatal("session service is not *sqlite.SessionService")
	}
	tenantID := sess.TenantID()

	// 历史里的 ask_question 控制记录（AppendAskAnswer 要求前有 ask_question，
	// 否则配对写不进去 —— 这就是 Replay 复活提问的根）。
	if _, err := sess.AppendMessage(llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "ask", Name: "AskUser", Arguments: `{}`}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendMessage(llm.NewToolMessage("AskUser", "ask", `{}`, "waiting")); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendAskQuestion(map[string]string{"request_id": "req-stale"}); err != nil {
		t.Fatal(err)
	}

	// 收件箱里的一条结构化提问（/api/message kind=ask_user 的落库形态）。
	if _, _, err := svc.AppendStructuredMessage(tenantID, sqlite.StructuredMessage{
		ChatID:        "chat-stale",
		Kind:          sqlite.StructuredKindAskUser,
		CorrelationID: "ask-ttl-1",
		Direction:     "in",
		Role:          "assistant",
		Content:       "选个颜色",
		SenderType:    "user",
		SenderID:      "web-1",
		SenderName:    "web-1",
		CreatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	a := &Agent{multiSession: mt}
	fake := &fakeResolvedSender{}
	wireResolvedSender(a, fake)

	a.setPendingAskUser("web", "chat-stale", &protocol.ProgressEvent{RequestID: "req-stale"})
	backdatePending(t, a, "web", "chat-stale", pendingAskUserTTL+time.Minute)

	a.autoResolveStalePendingAskUser("web", "chat-stale")

	if pending := a.GetPendingAskUser("web", "chat-stale"); pending != nil {
		t.Fatalf("超时提问应被收回，但 pending 仍在：%+v", pending)
	}
	// 前端撤角标的依据。
	ev := fake.snapshot()
	if len(ev) != 1 || ev[0].Reason != "cancelled" {
		t.Fatalf("超时收回必须广播一条 resolved(cancelled)，实际 %+v", ev)
	}
	if ev[0].Channel != "web" || ev[0].ChatID != "chat-stale" || ev[0].RequestID != "req-stale" {
		t.Fatalf("resolved 广播的 channel/chat_id/request_id 不对，前端无法定位要撤的角标：%+v", ev[0])
	}
	// Replay 不复活提问的依据。
	_, recordType, err := sess.SessionService().LatestAskControlRecord(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if recordType != sqlite.HistoryRecordAskAnswer {
		t.Fatalf("超时收回必须落 ask_answer 配对：最新控制记录 = %q", recordType)
	}
	// 历史里必须留下用户可见的一句话（御前判据：不能只写个 [cancelled] 记号）。
	const wantHistory = "[cancelled] 该提问因超时未回答已关闭（ttl_expired）"
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
		t.Fatalf("历史里没有可见记录 %q（用户看不出提问为什么消失）", wantHistory)
	}
	// 用户可查的终态：收件箱里那条提问必须 cancelled=true，且取消记录 reason=ttl_expired。
	rows, _, _, err := svc.ListStructuredMessages(tenantID, sqlite.StructuredKindAskUser, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("inbox 里找不到刚创建的 ask_user 行")
	}
	if !rows[0].Cancelled {
		t.Fatalf("超时收回后 inbox 行必须 cancelled=true（否则用户看到一条答不上的提问），实际 %+v", rows[0])
	}
	answers, _, _, err := svc.ListStructuredMessages(tenantID, sqlite.StructuredKindAnswer, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(answers) == 0 {
		t.Fatal("超时收回后没有取消记录行")
	}
	var cancelPayload struct {
		CancelReason string `json:"cancel_reason"`
		Cancelled    bool   `json:"cancelled"`
	}
	if err := json.Unmarshal([]byte(answers[0].StructuredJSON), &cancelPayload); err != nil {
		t.Fatalf("取消记录 structured_json 解析失败：%v", err)
	}
	if cancelPayload.CancelReason != "ttl_expired" || !cancelPayload.Cancelled {
		t.Fatalf("取消记录的 cancel_reason 必须是 ttl_expired：%s", answers[0].StructuredJSON)
	}
}

// TestChatProcessLoopCallsStaleAskUserReaper 是**调用点守卫**：D-03 的修复如果在
// chatProcessLoop 里被摘掉（例如有人把那段删了或改成无条件取消），上面两个行为
// 用例仍然会绿——它们直接调函数。这条用例把「call-site 必须存在，且必须走
// autoResolveStalePendingAskUser（而不是自己取消）」钉死在源码上。
//
// 口径与仓内既有守卫一致（TestRunConfigLiteralsDeclareReminderPolicy 同款：
// 不靠人眼，靠测试跑红）。
func TestChatProcessLoopCallsStaleAskUserReaper(t *testing.T) {
	src, err := os.ReadFile("agent.go")
	if err != nil {
		t.Fatalf("读取 agent.go 失败：%v", err)
	}
	text := string(src)
	const call = "a.autoResolveStalePendingAskUser(msg.Channel, msg.ChatID)"
	if !strings.Contains(text, call) {
		t.Fatalf("chatProcessLoop 里找不到 %q：新 turn 开始时不再检查陈旧提问，超时提问将永远挂住", call)
	}
	// 反例守卫：不得在 call-site 附近出现「无条件取消」的写法（旧缺陷的形态）。
	if strings.Contains(text, "a.resolvePendingAskUser(msg.Channel, msg.ChatID, \"cancelled\")") &&
		!strings.Contains(text, "autoResolveStalePendingAskUser") {
		t.Fatal("chatProcessLoop 直接取消 pending（D-03 回归形态）")
	}
}

// TestAskUserInterruptReapsExpiredThroughProcessLoop 是 D-03 在交付线上的**第二半**
// 判据：超时提问必须被 loop 的那一步收回。
//
// 为什么需要它：只断言"新鲜提问还在"的用例，在**删掉 call-site** 时也会绿
// （调用都没了，当然不会取消）。两半合起来才同时钉住"别乱取消"与"该收的必须收"。
func TestAskUserInterruptReapsExpiredThroughProcessLoop(t *testing.T) {
	mt, _ := newAgentHistorySession(t)
	a := &Agent{bus: bus.NewMessageBus(), multiSession: mt}

	chatKey := "web:chat-loop-stale"
	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	a.bgSessionStates.Store(chatKey, ss)

	a.setPendingAskUser("web", "chat-loop-stale", &protocol.ProgressEvent{RequestID: "req-stale-loop"})
	backdatePending(t, a, "web", "chat-loop-stale", pendingAskUserTTL+time.Minute)

	msgCh := make(chan bus.InboundMessage, 4)
	done := make(chan struct{})
	go func() {
		a.chatProcessLoop(context.Background(), chatKey, msgCh, ss)
		close(done)
	}()
	defer func() {
		close(msgCh)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Log("chatProcessLoop 未在 3s 内退出（不影响本断言）")
		}
	}()

	msgCh <- bus.InboundMessage{
		Channel: "web", ChatID: "chat-loop-stale", Content: "随便说一句", RequestID: "req-user2",
		Metadata: map[string]string{"turn_id": "1"},
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if a.GetPendingAskUser("web", "chat-loop-stale") == nil {
			return // 已按 TTL 收回
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("超时提问在 loop 起点没有被收回：call-site 丢了，或者 TTL 判据没生效（pending 会永远挂住）")
}

// TestAskUserTTLExpiryExposesCancelReason 锁住「取消原因可查」：
// 超时收回后，取消记录里必须带 reason=ttl_expired（API 把它作为 payload 返回），
// 否则用户只知道"没了"，不知道为什么没了。
func TestAskUserTTLExpiryExposesCancelReason(t *testing.T) {
	mt, _ := newAgentHistorySession(t)
	sess, err := mt.GetOrCreateSession("web", "chat-reason")
	if err != nil {
		t.Fatal(err)
	}
	svc, ok := sess.SessionService().(*sqlite.SessionService)
	if !ok {
		t.Fatal("session service is not *sqlite.SessionService")
	}
	tenantID := sess.TenantID()
	if _, _, err := svc.AppendStructuredMessage(tenantID, sqlite.StructuredMessage{
		ChatID: "chat-reason", Kind: sqlite.StructuredKindAskUser, CorrelationID: "ask-reason-1",
		Direction: "in", Role: "assistant", Content: "选个颜色",
		SenderType: "user", SenderID: "web-1", SenderName: "web-1", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	a := &Agent{multiSession: mt}
	a.setPendingAskUser("web", "chat-reason", &protocol.ProgressEvent{RequestID: "req-reason"})
	backdatePending(t, a, "web", "chat-reason", pendingAskUserTTL+time.Minute)
	a.autoResolveStalePendingAskUser("web", "chat-reason")

	answers, _, _, err := svc.ListStructuredMessages(tenantID, "answer", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(answers) == 0 {
		t.Fatal("超时收回后没有取消记录行")
	}
	found := false
	for _, row := range answers {
		if strings.Contains(row.StructuredJSON, `"cancel_reason":"ttl_expired"`) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("取消记录里没有 cancel_reason=ttl_expired（用户看不出为什么被关掉）：%+v", answers[0].StructuredJSON)
	}
}

// TestAskUserTTLExpiryBroadcastsCancelReason 锁住「前端能分辨超时vs主动取消」：
// resolved 事件必须是 Reason="cancelled" + CancelReason="ttl_expired"。
//
// 防什么：「TTL 收回与用户取消同值」——那样前端/日志只能看到 cancelled，
// 无法对用户解释"为什么问题不见了"。不防：用户主动 /cancel 的 Reason（另有路径）。
func TestAskUserTTLExpiryBroadcastsCancelReason(t *testing.T) {
	mt, _ := newAgentHistorySession(t)
	a := &Agent{multiSession: mt}
	fake := &fakeResolvedSender{}
	wireResolvedSender(a, fake)

	a.setPendingAskUser("web", "chat-reason-ev", &protocol.ProgressEvent{RequestID: "req-rev"})
	backdatePending(t, a, "web", "chat-reason-ev", pendingAskUserTTL+time.Minute)
	a.autoResolveStalePendingAskUser("web", "chat-reason-ev")

	ev := fake.snapshot()
	if len(ev) != 1 {
		t.Fatalf("应广播一条 resolved 事件，实际 %+v", ev)
	}
	if ev[0].Reason != "cancelled" {
		t.Fatalf("Reason 应保持 cancelled（兼容既有客户端），实际 %q", ev[0].Reason)
	}
	if ev[0].CancelReason != "ttl_expired" {
		t.Fatalf("超时收回必须带 cancel_reason=ttl_expired，实际 %q（前端无法分辨超时与主动取消）", ev[0].CancelReason)
	}
}

// TestAskUserTTLDoesNotBlockNotifications 锁住「TTL 是真上界」：
// 提问超过 TTL 后，该会话的后台通知必须能照常排空，不能被 pending 永久拦住。
//
// 防什么：handleBgNotifySignal 只看 HasPendingAskUserFast、不看年龄 ⇒ 用户不再
// 发言时通知永久滞留。不防：TTL 内的拦截至（那是有意行为，另有测试）。
func TestAskUserTTLDoesNotBlockNotifications(t *testing.T) {
	mt, _ := newAgentHistorySession(t)
	a := &Agent{multiSession: mt, bus: bus.NewMessageBus(), agentCtx: context.Background()}
	key := "web:chat-bg"
	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	a.bgSessionStates.Store(key, ss)

	a.setPendingAskUser("web", "chat-bg", &protocol.ProgressEvent{RequestID: "req-bg"})
	backdatePending(t, a, "web", "chat-bg", pendingAskUserTTL+time.Minute)
	a.enqueueBgNotification(&tools.CronFired{Key: key, Sid: "s", Message: "tick"})

	a.handleBgNotifySignal(key, ss)

	// 超时 pending 不得拦住 drain：通知应已被取走（缓冲区空）。
	if held := a.takePendingBgNotifications(key); len(held) != 0 {
		t.Fatalf("TTL 到点后通知仍被 pending 拦住（held=%d）：用户不再发言时通知会永久滞留", len(held))
	}
}

// TestAskUserFreshPendingStillBlocksNotifications 反向对照：TTL 内的 pending
// 仍然要拦住通知（提问期间不被打断的既有语义不能被这次修复搞丢）。
func TestAskUserFreshPendingStillBlocksNotifications(t *testing.T) {
	mt, _ := newAgentHistorySession(t)
	a := &Agent{multiSession: mt, bus: bus.NewMessageBus(), agentCtx: context.Background()}
	key := "web:chat-bg2"
	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	a.bgSessionStates.Store(key, ss)

	a.setPendingAskUser("web", "chat-bg2", &protocol.ProgressEvent{RequestID: "req-bg2"})
	a.enqueueBgNotification(&tools.CronFired{Key: key, Sid: "s", Message: "tick"})
	a.handleBgNotifySignal(key, ss)

	if held := a.takePendingBgNotifications(key); len(held) != 1 {
		t.Fatalf("新鲜 pending 必须继续拦住通知（held=%d, want 1）", len(held))
	}
}
