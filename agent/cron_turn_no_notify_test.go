package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"xbot/bus"
	"xbot/llm"
	"xbot/tools"
)

// 测试用的线上 wire 格式字面量。**故意不使用实现里的常量**：before-red 必须在
// 未改动的树上也能编译（那时常量还不存在），只有断言该红。
// 实现见 agent/trigger_source.go：triggerSourceMetadataKey / triggerSourceCron。
const (
	testTriggerSourceKey  = "trigger_source"
	testTriggerSourceCron = "cron"
)

// newCronTurnTestAgent 造一个"活的、能真跑一轮"的子代理会话，挂在
// cronSubAgentKey 上；返回的 Agent 已装好 multiSession（SendToInteractiveSession
// 需要一个 tenant session 才能分配 turn_id / 落库）。
func newCronTurnTestAgent(t *testing.T) (*Agent, *tools.BackgroundTaskManager, *interactiveAgent) {
	t.Helper()
	a, mgr, _ := newCronRouteTestAgent(t)
	mt, _ := newAgentHistorySession(t)
	a.multiSession = mt

	ia := &interactiveAgent{
		roleName:     "court.gongbu",
		instance:     "inst-1",
		background:   true,
		parentKey:    "web:web-4",
		systemPrompt: llm.NewSystemMessage("You are gongbu."),
		messages:     []llm.ChatMessage{llm.NewUserMessage("earlier turn")},
		cfg: &RunConfig{
			LLMClient: &mockLLM{responses: []llm.LLMResponse{{Content: "cron turn done"}}},
			Model:     "test-model",
			Tools:     newTestRegistry(),
			AgentID:   "main/court.gongbu",
			Channel:   "agent",
			ChatID:    cronSubAgentKey,
			TurnID:    1,
		},
	}
	a.interactiveSubAgents.Store(cronSubAgentKey, ia)
	return a, mgr, ia
}

// waitTurnIdle 轮询到这一轮真的跑完（ia.running==false 且已有 lastReply），带超时。
func waitTurnIdle(t *testing.T, ia *interactiveAgent, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ia.mu.Lock()
		running, reply, lastErr := ia.running, ia.lastReply, ia.lastError
		ia.mu.Unlock()
		if !running && reply != "" {
			return
		}
		if !running && lastErr != "" {
			t.Fatalf("turn finished with error, no reply: %s", lastErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	ia.mu.Lock()
	running, reply, lastErr := ia.running, ia.lastReply, ia.lastError
	ia.mu.Unlock()
	t.Fatalf("turn did not finish within %s (running=%v lastReply=%q lastError=%q)", timeout, running, reply, lastErr)
}

// collectCompletedNotifies 在 window 内排空 NotifyCh，收集父会话的 Completed 通知。
// Progress 等其他通知被丢弃（本测试不关心），避免误判。
//
// ⚠️ 只在**没跑 bgNotifyLoop** 的测试里用（此时 NotifyCh 的唯一消费者就是测试）。
// 跑真 bgNotifyLoop 的测试必须用 parentCompletionNotices —— 否则测试与
// bgNotifyLoop 抢同一个 channel，会漏读成"恒绿"（2026-11-12 lead 评审打回项）。
func collectCompletedNotifies(mgr *tools.BackgroundTaskManager, window time.Duration) []*tools.SubAgentBgNotify {
	var got []*tools.SubAgentBgNotify
	deadline := time.After(window)
	for {
		select {
		case n := <-mgr.NotifyCh:
			if sn, ok := n.(*tools.SubAgentBgNotify); ok && sn.Type == tools.SubAgentBgNotifyCompleted {
				got = append(got, sn)
			}
		case <-deadline:
			return got
		}
	}
}

// parentCompletionNotices 在 window 内排空 bus.Inbound，返回看起来像"子代理完成
// 通知"的消息正文 —— 这是父会话侧的**可观察结果**（真链路终点）：
//
//	Completed 通知 → bgNotifyLoop → （父会话 "web:web-4" 不在内存）→ 转投该会话的
//	drainAndProcessNotifications → injectBgUserMessage → bus.Inbound
//
// 用它而不是直接读 mgr.NotifyCh：跑真 bgNotifyLoop 时 NotifyCh 的消费者就是
// bgNotifyLoop 自己，读它等于和被测代码抢消息。
func parentCompletionNotices(a *Agent, window time.Duration) []string {
	var got []string
	deadline := time.After(window)
	for {
		select {
		case msg := <-a.bus.Inbound:
			if strings.Contains(msg.Content, "completed.") {
				got = append(got, msg.Content)
			}
		case <-deadline:
			return got
		}
	}
}

// TestCronFired_TurnDoesNotNotifyParent 是真链路 e2e（判据 1/2 + 判据 5）：
//
//	SendCronFired → bgNotifyLoop → deliverCronFiredToSubAgentSession
//	→ SendToInteractiveSession（活会话）→ 这一轮跑完
//
// 契约（2026-11-12 用户令）：cron 触发的一轮**不向父会话发 Completed**，
// 但这一轮本身照常执行、照常落库（user + assistant 都在）。
//
// 未改代码时 ⇒ 本轮结束会在 interactive.go 的 Completed 投递点发出通知 ⇒ 本测试红。
func TestCronFired_TurnDoesNotNotifyParent(t *testing.T) {
	a, mgr, ia := newCronTurnTestAgent(t)
	go a.bgNotifyLoop()

	mgr.SendCronFired(&tools.CronFired{
		Key:     "agent:" + cronSubAgentKey,
		Sid:     "admin",
		Message: "check cluster health",
	})

	waitTurnIdle(t, ia, 10*time.Second)

	// 判据 2：cron turn 不通知父会话。
	//
	// 观察点在父会话侧（bus.Inbound）而不是 mgr.NotifyCh：本测试跑的是真
	// bgNotifyLoop，NotifyCh 的消费者就是它自己。
	if got := parentCompletionNotices(a, 2*time.Second); len(got) != 0 {
		t.Errorf("cron turn notified the parent session %d time(s); want 0 — "+
			"定时任务触发的一轮不得唤醒父会话（第一条：%q）", len(got), got[0])
	}
	// 也不能只是"躺在队列里没投递"：父会话的 bgRunPending 必须干净。
	if pending := a.pendingBgNotifications("web:web-4"); len(pending) != 0 {
		t.Errorf("cron turn left %d notification(s) queued for the parent session; want 0", len(pending))
	}

	// 判据 5：这一轮照常执行、照常落库 —— user + assistant（含 Detail）都在。
	sess, err := a.multiSession.GetOrCreateSession("agent", cronSubAgentKey)
	if err != nil {
		t.Fatalf("GetOrCreateSession(%q): %v", cronSubAgentKey, err)
	}
	history, err := sess.GetMessages()
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	var sawUser, sawAssistant, sawDetail bool
	for _, m := range history {
		switch m.Role {
		case "user":
			sawUser = true
		case "assistant":
			sawAssistant = true
			if m.Detail != "" {
				sawDetail = true
			}
		}
	}
	if !sawUser {
		t.Errorf("cron turn did not persist its user message; history=%d msgs", len(history))
	}
	if !sawAssistant {
		t.Errorf("cron turn did not persist its assistant message; history=%d msgs", len(history))
	}
	if !sawDetail {
		t.Errorf("cron turn persisted assistant without iteration Detail; history=%d msgs", len(history))
	}
	if !historyContains(history, "check cluster health") {
		t.Errorf("cron turn history lost the job message; history=%d msgs", len(history))
	}
	if !historyContains(history, "cron turn done") {
		t.Errorf("cron turn history lost the assistant reply; history=%d msgs", len(history))
	}
	// 判据 5 的可复算证据：把落库的每一行打出来（role/turn_id/正文/Detail 长度）。
	for i, m := range history {
		t.Logf("history[%d]: role=%s turn_id=%d content_len=%d detail_len=%d content=%.60q",
			i, m.Role, m.TurnID, len(m.Content), len(m.Detail), m.Content)
	}
}

// TestUserSendTurnsStillNotifyParent 是对照组（判据 3）：
// 普通 send（**不带**任何 trigger metadata）结束时必须照常通知父会话。
//
// 这条在未改树上也应该绿 —— 它是"抑制只针对 cron、不误伤正常用法"的守卫；
// 若变异让抑制恒开，这条会红。
func TestUserSendTurnsStillNotifyParent(t *testing.T) {
	a, mgr, ia := newCronTurnTestAgent(t)

	out, err := a.SendToInteractiveSession(context.Background(), "court.gongbu", bus.InboundMessage{
		Channel:  "web",
		ChatID:   "web-4",
		SenderID: "web-4",
		Content:  "用户派单：跑一下检查",
		Metadata: map[string]string{"instance_id": "inst-1"},
	})
	if err != nil {
		t.Fatalf("SendToInteractiveSession: %v", err)
	}
	if out != nil && out.Error != nil {
		t.Fatalf("SendToInteractiveSession returned error: %v", out.Error)
	}

	waitTurnIdle(t, ia, 10*time.Second)

	got := collectCompletedNotifies(mgr, 2*time.Second)
	if len(got) != 1 {
		t.Fatalf("user send got %d Completed notification(s) on parent NotifyCh; want exactly 1 — "+
			"普通用户 turn 必须通知父会话（抑制不得误伤）", len(got))
	}
}

// historyContains 判断任一消息的正文包含 needle（用于判据 5 的落库检查）。
func historyContains(history []llm.ChatMessage, needle string) bool {
	for _, m := range history {
		if strings.Contains(m.Content, needle) {
			return true
		}
	}
	return false
}

// bgTaskNotifForTest 跑一个立即完成的 bg task，把它在 NotifyCh 上的完成通知取出来
// （用于造"非 cron 节"，不依赖任何实现符号）。
func bgTaskNotifForTest(t *testing.T, mgr *tools.BackgroundTaskManager, chatKey string) tools.BgNotification {
	t.Helper()
	_ = mgr.Start(chatKey, "user-1", "echo hello", func(ctx context.Context, outputBuf func(string)) (int, error) {
		outputBuf("hello output")
		return 0, nil
	})
	select {
	case n := <-mgr.NotifyCh:
		return n
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for bg task notification")
	}
	return nil
}

// drainBatchNotify 把 notifs 攒成一批交给 drainAndProcessNotifications（父会话没有
// chatWorker ⇒ 直接 drain 注入），返回注入到 bus.Inbound 的那条合并消息。
func drainBatchNotify(t *testing.T, notifs []tools.BgNotification, chatKey string) bus.InboundMessage {
	t.Helper()
	a, _, _ := newCronRouteTestAgent(t)
	a.enqueueBgNotifications(notifs)
	a.drainAndProcessNotifications(chatKey)
	select {
	case msg := <-a.bus.Inbound:
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("drainAndProcessNotifications injected nothing into bus.Inbound")
	}
	return bus.InboundMessage{}
}

// TestDrainPureCronBatchMarksTriggerSource 是**回落路径**的 before-red（判据 1 的另一半）：
// 子代理会话已不在内存时，cron 事件走通用 drain → 主 Agent 在 "agent:<key>" 上跑一轮。
// 这条注入消息必须带 turn 级标记，否则主 Agent 的 cron turn 无法与用户 turn 区分。
func TestDrainPureCronBatchMarksTriggerSource(t *testing.T) {
	const chatKey = "cli:test-chat"
	msg := drainBatchNotify(t, []tools.BgNotification{
		&tools.CronFired{Key: chatKey, Sid: "user-1", Message: "巡检"},
	}, chatKey)

	if got := msg.Metadata[testTriggerSourceKey]; got != testTriggerSourceCron {
		t.Errorf("pure cron batch injected with %s=%q, want %q — 通用 drain 的纯 cron 批次必须带上触发来源",
			testTriggerSourceKey, got, testTriggerSourceCron)
	}
	if !strings.Contains(msg.Content, "巡检") {
		t.Errorf("batched cron message lost the job payload: %q", msg.Content)
	}
}

// TestDrainMixedBatchDoesNotMarkTriggerSource 判据 4（混合批次不误标，真 drain 层）：
// cron + 非 cron 合并成**一条** user 消息 = 一个 turn，这一轮不纯是 cron，
// 按 cron 抑制会吞掉人要看的真实结果 ⇒ 必须**不带**该键。
func TestDrainMixedBatchDoesNotMarkTriggerSource(t *testing.T) {
	const chatKey = "cli:test-chat"
	a, mgr, _ := newCronRouteTestAgent(t)
	bgNotif := bgTaskNotifForTest(t, mgr, chatKey)

	a.enqueueBgNotifications([]tools.BgNotification{
		&tools.CronFired{Key: chatKey, Sid: "user-1", Message: "巡检"},
		bgNotif,
	})
	a.drainAndProcessNotifications(chatKey)

	var msg bus.InboundMessage
	select {
	case msg = <-a.bus.Inbound:
	case <-time.After(2 * time.Second):
		t.Fatal("drainAndProcessNotifications injected nothing into bus.Inbound")
	}
	if !strings.Contains(msg.Content, "🔔 合并通知") {
		t.Fatalf("expected a merged batch message, got: %q", msg.Content)
	}
	if got, ok := msg.Metadata[testTriggerSourceKey]; ok {
		t.Errorf("mixed batch injected with %s=%q; want the key ABSENT — 混批不得按 cron 抑制", testTriggerSourceKey, got)
	}
}
