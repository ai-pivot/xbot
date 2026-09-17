package agent

import (
	"context"
	"path/filepath"
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

// fakeAskUserResolvedSender records AskUserResolvedEvent broadcasts. It
// implements only channel.Channel + channel.AskUserResolvedSender so
// unrelated broadcasts (progress / session state / queue state) cannot
// pollute the event counts.
type fakeAskUserResolvedSender struct {
	mu     sync.Mutex
	events []protocol.AskUserResolvedEvent
}

func (f *fakeAskUserResolvedSender) SendAskUserResolved(ev protocol.AskUserResolvedEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
}

func (f *fakeAskUserResolvedSender) Name() string                             { return "fake" }
func (f *fakeAskUserResolvedSender) Start() error                             { return nil }
func (f *fakeAskUserResolvedSender) Stop()                                    {}
func (f *fakeAskUserResolvedSender) Send(channel.OutboundMsg) (string, error) { return "", nil }

func (f *fakeAskUserResolvedSender) snapshot() []protocol.AskUserResolvedEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]protocol.AskUserResolvedEvent(nil), f.events...)
}

func wireFakeAskUserSender(a *Agent, fake *fakeAskUserResolvedSender) {
	a.channelRange = func(fn func(string, channel.Channel) bool) {
		fn("fake", fake)
	}
}

func waitForCondition(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// newAskURuntimeAgent builds a real in-process Agent backed by a temp DB
// (the same shape serverapp tests use) for the end-to-end drives below.
func newAskURuntimeAgent(t *testing.T) *Agent {
	t.Helper()
	dir := t.TempDir()
	ag, err := New(Config{
		WorkDir:        dir,
		DBPath:         filepath.Join(dir, "xbot.db"),
		XbotHome:       dir,
		SandboxMode:    "none",
		MemoryProvider: "flat",
		Bus:            bus.NewMessageBus(),
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}
	// The global semaphore is normally created at the top of Agent.Run
	// (agent.go Run → globalSem). These tests drive chatProcessLoop directly,
	// so initialize it through the production API.
	ag.SetMaxConcurrency(2)
	// Production injects the channel lookup from main.go; processMessage reads
	// it on the very first message (wantsPreReplyNotify). Inject a lookup that
	// finds nothing — pre-reply acks are simply skipped.
	ag.SetChannelFinder(func(string) (channel.Channel, bool) { return nil, false })
	t.Cleanup(func() { _ = ag.Close() })
	return ag
}

// driveChatProcessLoop runs the real per-session processing loop and returns
// the channel to feed messages into.
func driveChatProcessLoop(t *testing.T, ag *Agent, key string, ss *bgSessionState) chan<- bus.InboundMessage {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	msgCh := make(chan bus.InboundMessage, 8)
	go ag.chatProcessLoop(ctx, key, msgCh, ss)
	t.Cleanup(func() {
		cancel()
		close(msgCh)
	})
	return msgCh
}

// T4a (producer)/cancel: interceptCancel 在无活跃 Run + pending 时把 prompt
// 解析为 cancelled，并广播 resolved(cancelled)（RequestID 取自内存项）。
func TestCancelPathBroadcastsAskUserResolved(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus()}
	fake := &fakeAskUserResolvedSender{}
	wireFakeAskUserSender(a, fake)
	a.setPendingAskUser("web", "chat-1", &protocol.ProgressEvent{RequestID: "req-c"})

	a.interceptCancel(bus.InboundMessage{Channel: "web", ChatID: "chat-1", Content: "/cancel"})

	events := fake.snapshot()
	if len(events) != 1 {
		t.Fatalf("resolved events = %+v, want exactly one", events)
	}
	ev := events[0]
	if ev.Reason != "cancelled" || ev.Channel != "web" || ev.ChatID != "chat-1" || ev.RequestID != "req-c" {
		t.Fatalf("resolved event = %+v, want cancelled for web:chat-1 req-c", ev)
	}
}

// T4b (producer)/rewind: RewindHistory 清除 pending 并广播 resolved(rewound)。
func TestRewindPathBroadcastsAskUserResolved(t *testing.T) {
	ag := newAskURuntimeAgent(t)
	sess, err := ag.MultiSession().GetOrCreateSession("web", "chat-rw")
	if err != nil {
		t.Fatal(err)
	}
	u1, err := sess.AppendMessage(llm.NewUserMessage("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendMessage(llm.NewAssistantMessage("hi")); err != nil {
		t.Fatal(err)
	}

	fake := &fakeAskUserResolvedSender{}
	wireFakeAskUserSender(ag, fake)
	ag.setPendingAskUser("web", "chat-rw", &protocol.ProgressEvent{RequestID: "req-rw"})

	if _, err := ag.RewindHistory("web", "chat-rw", u1); err != nil {
		t.Fatalf("rewind: %v", err)
	}
	events := fake.snapshot()
	if len(events) != 1 || events[0].Reason != "rewound" || events[0].ChatID != "chat-rw" {
		t.Fatalf("resolved events = %+v, want one rewound for chat-rw", events)
	}
	if pending := ag.GetPendingAskUser("web", "chat-rw"); pending != nil {
		t.Fatalf("pending AskUser survived rewind: %+v", pending)
	}
}

// T4d (producer)/answer: ask_answer 落库成功后广播 resolved(answered)；
// 自动取消不得抢先（answer 消息豁免 busy-entry 自动取消）。
func TestAnswerPathBroadcastsAnsweredAfterPersist(t *testing.T) {
	ag := newAskURuntimeAgent(t)
	sess, err := ag.MultiSession().GetOrCreateSession("web", "chat-ans")
	if err != nil {
		t.Fatal(err)
	}
	seedAskQuestion(t, sess, "req-ans")

	fake := &fakeAskUserResolvedSender{}
	wireFakeAskUserSender(ag, fake)

	key := "web:chat-ans"
	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	ag.bgSessionStates.Store(key, ss)
	msgCh := driveChatProcessLoop(t, ag, key, ss)

	msgCh <- bus.InboundMessage{
		Channel: "web", ChatID: "chat-ans", Content: "yes", RequestID: "req-ans-msg",
		Metadata: map[string]string{"ask_user_answered": "true", "turn_id": "1"},
	}

	waitForCondition(t, 20*time.Second, "resolved(answered) broadcast", func() bool {
		return len(fake.snapshot()) >= 1
	})
	events := fake.snapshot()
	if len(events) != 1 || events[0].Reason != "answered" {
		t.Fatalf("resolved events = %+v, want exactly one answered (no premature cancelled)", events)
	}
	if pending := ag.GetPendingAskUser("web", "chat-ans"); pending != nil {
		t.Fatalf("pending AskUser survived the persisted answer: %+v", pending)
	}
	_, recordType, err := sqlite.NewSessionService(ag.MultiSession().DB()).LatestAskControlRecord(sess.TenantID())
	if err != nil {
		t.Fatal(err)
	}
	if recordType != sqlite.HistoryRecordAskAnswer {
		t.Fatalf("DB latest control record = %q, want ask_answer", recordType)
	}
}

// T3: ask_answer 未落库（这里 append 直接失败）时缓存不得被先清、也不得
// 广播 resolved —— 失败重试期间面板必须保留（旧代码在入队时就清了缓存，
// 崩溃/排队取消后 DB 仍 pending ⇒ 面板复活）。
func TestFailedAnswerPersistKeepsPendingCache(t *testing.T) {
	ag := newAskURuntimeAgent(t)
	// Deliberately NO ask_question seeded: the append must fail, leaving the
	// interaction unresolved.
	if _, err := ag.MultiSession().GetOrCreateSession("web", "chat-t3"); err != nil {
		t.Fatal(err)
	}
	fake := &fakeAskUserResolvedSender{}
	wireFakeAskUserSender(ag, fake)

	ag.setPendingAskUser("web", "chat-t3", &protocol.ProgressEvent{RequestID: "req-t3"})

	key := "web:chat-t3"
	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	ag.bgSessionStates.Store(key, ss)
	msgCh := driveChatProcessLoop(t, ag, key, ss)

	msgCh <- bus.InboundMessage{
		Channel: "web", ChatID: "chat-t3", Content: "my answer", RequestID: "req-t3-msg",
		Metadata: map[string]string{"ask_user_answered": "true", "turn_id": "1"},
	}

	// Positive signal that the answer branch was reached and failed at the
	// persist step: "…AskUser question is no longer pending…" reaches the user.
	var mu sync.Mutex
	var seen []string
	waitForCondition(t, 20*time.Second, "answer persist failure surfaced", func() bool {
		select {
		case out := <-ag.bus.Outbound:
			mu.Lock()
			seen = append(seen, out.Content)
			mu.Unlock()
			return strings.Contains(out.Content, "AskUser")
		default:
			return false
		}
	})

	if pending := ag.GetPendingAskUser("web", "chat-t3"); pending == nil {
		t.Fatalf("pending cache was cleared although the answer never persisted (seen=%v)", seen)
	}
	if events := fake.snapshot(); len(events) != 0 {
		t.Fatalf("resolved broadcast although the answer never persisted: %+v", events)
	}
}

// T6: 会话进入 busy 处理新回合时残留 pending 被自动取消 —— 落库配对
// (ask_answer "[cancelled]") + 广播 resolved(cancelled) + 之后 pending nil。
func TestNewTurnAutoCancelsStalePendingAskUser(t *testing.T) {
	ag := newAskURuntimeAgent(t)
	sess, err := ag.MultiSession().GetOrCreateSession("web", "chat-t6")
	if err != nil {
		t.Fatal(err)
	}
	seedAskQuestion(t, sess, "req-t6")

	fake := &fakeAskUserResolvedSender{}
	wireFakeAskUserSender(ag, fake)

	key := "web:chat-t6"
	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	ag.bgSessionStates.Store(key, ss)
	msgCh := driveChatProcessLoop(t, ag, key, ss)

	msgCh <- bus.InboundMessage{
		Channel: "web", ChatID: "chat-t6", Content: "moved on", RequestID: "req-new-turn",
		Metadata: map[string]string{"turn_id": "1"},
	}

	waitForCondition(t, 20*time.Second, "auto-cancel resolved(cancelled) broadcast", func() bool {
		return len(fake.snapshot()) >= 1
	})
	events := fake.snapshot()
	if events[0].Reason != "cancelled" || events[0].Channel != "web" || events[0].ChatID != "chat-t6" {
		t.Fatalf("resolved events = %+v, want cancelled for web:chat-t6", events)
	}
	if pending := ag.GetPendingAskUser("web", "chat-t6"); pending != nil {
		t.Fatalf("pending AskUser survived the new turn: %+v", pending)
	}
	_, recordType, err := sqlite.NewSessionService(ag.MultiSession().DB()).LatestAskControlRecord(sess.TenantID())
	if err != nil {
		t.Fatal(err)
	}
	if recordType != sqlite.HistoryRecordAskAnswer {
		t.Fatalf("DB pairing missing: latest control record = %q, want ask_answer", recordType)
	}
}

// T7: 提问期间（WaitingUser 暂停：busy=false + pending）后台通知不得被
// drain；pending 解析后 drain 恢复。
func TestBgNotificationsHeldWhileAskUserPending(t *testing.T) {
	mt, _ := newAgentHistorySession(t)
	a := &Agent{multiSession: mt, bus: bus.NewMessageBus(), agentCtx: context.Background()}
	key := "test:chat"
	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	a.bgSessionStates.Store(key, ss)

	// WaitingUser pause state: pending prompt, busy=false (the new invariant).
	a.setPendingAskUser("test", "chat", &protocol.ProgressEvent{RequestID: "req-t7"})
	a.enqueueBgNotification(&tools.CronFired{Key: key, Sid: "s", Message: "tick"})

	a.handleBgNotifySignal(key, ss)

	held := a.takePendingBgNotifications(key)
	if len(held) != 1 {
		t.Fatalf("notification was drained while an AskUser prompt is pending (still buffered=%d, want 1)", len(held))
	}
	select {
	case out := <-a.bus.Inbound:
		t.Fatalf("notification injected into the session while waiting for an answer: %+v", out)
	default:
	}

	// After the prompt resolves, the gate must open again.
	a.enqueueBgNotification(&tools.CronFired{Key: key, Sid: "s", Message: "tick"})
	a.resolvePendingAskUser("test", "chat", "cancelled")
	a.handleBgNotifySignal(key, ss)
	select {
	case out := <-a.bus.Inbound:
		if !strings.Contains(out.Content, "定时任务") {
			t.Fatalf("unexpected drained inbound: %+v", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("held notification was not drained after the prompt resolved")
	}
}
