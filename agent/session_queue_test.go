package agent

import (
	"context"
	"strings"
	"testing"

	"xbot/bus"
	"xbot/llm"
	"xbot/tools"
)

// ─── Shadow queue unit tests ───

func TestQueueShadow_FIFOAppendDequeue(t *testing.T) {
	ss := &bgSessionState{}
	ss.queueAppend(queuedEntry{MsgID: "m1", TurnID: 1, Source: "user"})
	ss.queueAppend(queuedEntry{MsgID: "m2", TurnID: 2, Source: "notification"})
	ss.queueAppend(queuedEntry{MsgID: "m3", TurnID: 3, Source: "user"})

	snap := ss.queueSnapshot()
	if len(snap) != 3 {
		t.Fatalf("snapshot len=%d, want 3", len(snap))
	}
	// FIFO: snapshot order mirrors msgCh order exactly.
	for i, want := range []string{"m1", "m2", "m3"} {
		if snap[i].MsgID != want {
			t.Fatalf("snapshot[%d]=%s, want %s", i, snap[i].MsgID, want)
		}
	}

	// Dequeue in order — each returns cancelled=false.
	for _, want := range []string{"m1", "m2", "m3"} {
		if cancelled := ss.queueDequeue(want); cancelled {
			t.Fatalf("dequeue(%s) cancelled=true, want false", want)
		}
	}
	if got := ss.queueSnapshot(); len(got) != 0 {
		t.Fatalf("snapshot after full dequeue len=%d, want 0", len(got))
	}
	// Dequeue on empty queue is a shadow miss — returns false (process normally).
	if cancelled := ss.queueDequeue("unknown"); cancelled {
		t.Fatal("dequeue on empty queue returned cancelled=true")
	}
}

func TestQueueShadow_CancelSkipsDequeue(t *testing.T) {
	ss := &bgSessionState{}
	ss.queueAppend(queuedEntry{MsgID: "m1", TurnID: 1})
	ss.queueAppend(queuedEntry{MsgID: "m2", TurnID: 2})

	if !ss.queueMarkCancelled("m1") {
		t.Fatal("queueMarkCancelled(m1) = false, want true")
	}
	// Double-cancel is rejected (already cancelled).
	if ss.queueMarkCancelled("m1") {
		t.Fatal("double queueMarkCancelled(m1) = true, want false")
	}
	// Cancel of an unknown message is rejected.
	if ss.queueMarkCancelled("nope") {
		t.Fatal("queueMarkCancelled(unknown) = true, want false")
	}

	// Cancelled items are hidden from the snapshot (tray disappears immediately).
	snap := ss.queueSnapshot()
	if len(snap) != 1 || snap[0].MsgID != "m2" {
		t.Fatalf("snapshot after cancel=%+v, want only m2", snap)
	}

	// Dequeue of the cancelled head reports cancelled=true (chatProcessLoop skips it).
	if cancelled := ss.queueDequeue("m1"); !cancelled {
		t.Fatal("dequeue(m1) cancelled=false, want true")
	}
	// Dequeue of the next item proceeds normally.
	if cancelled := ss.queueDequeue("m2"); cancelled {
		t.Fatal("dequeue(m2) cancelled=true, want false")
	}
}

func TestQueuedEntrySourceClassification(t *testing.T) {
	cases := []struct {
		name     string
		metadata map[string]string
		content  string
		want     string
	}{
		{"notification", map[string]string{bgNotificationMetadataKey: "true"}, "bg task done", "notification"},
		{"answer", map[string]string{"ask_user_answered": "true"}, "yes", "answer"},
		{"resume", map[string]string{"resume_turn": "true"}, "", "resume"},
		{"command", nil, "/new", "command"},
		{"user", nil, "hello", "user"},
	}
	for _, tc := range cases {
		got := queuedEntrySource(bus.InboundMessage{Metadata: tc.metadata, Content: tc.content})
		if got != tc.want {
			t.Errorf("%s: queuedEntrySource=%q, want %q", tc.name, got, tc.want)
		}
	}
}

func newQueuedEntryPreview(runes int) string {
	return strings.Repeat("x", runes)
}

func TestQueuedEntryPreviewTruncates(t *testing.T) {
	e := newQueuedEntry(bus.InboundMessage{RequestID: "r1", Content: newQueuedEntryPreview(200)}, 7)
	got := []rune(e.Preview)
	if len(got) != queuePreviewRunes+1 { // +1 for the ellipsis rune
		t.Fatalf("preview runes=%d, want %d (+ellipsis)", len(got), queuePreviewRunes)
	}
	if !strings.HasSuffix(e.Preview, "…") {
		t.Fatalf("preview missing ellipsis: %q", e.Preview)
	}
	if e.TurnID != 7 || e.MsgID != "r1" {
		t.Fatalf("entry=%+v", e)
	}
}

// ─── Agent-level queue API tests ───

func TestCancelQueuedMessage(t *testing.T) {
	chatKey := "cli:queue-cancel"
	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	a := &Agent{}
	a.bgSessionStates.Store(chatKey, ss)
	defer a.bgSessionStates.Delete(chatKey)

	ss.queueAppend(queuedEntry{MsgID: "req-1", TurnID: 1, Source: "user"})
	ss.queueAppend(queuedEntry{MsgID: "req-2", TurnID: 2, Source: "user"})

	if !a.CancelQueuedMessage("cli", "queue-cancel", "req-1") {
		t.Fatal("CancelQueuedMessage(req-1) = false, want true")
	}
	// Already cancelled → false (idempotent rejection).
	if a.CancelQueuedMessage("cli", "queue-cancel", "req-1") {
		t.Fatal("double CancelQueuedMessage = true, want false")
	}
	// Unknown message → false.
	if a.CancelQueuedMessage("cli", "queue-cancel", "nope") {
		t.Fatal("CancelQueuedMessage(unknown) = true, want false")
	}
	// Unknown session → false (no panic).
	if a.CancelQueuedMessage("cli", "no-such-chat", "req-1") {
		t.Fatal("CancelQueuedMessage(unknown session) = true, want false")
	}
	// Snapshot API hides the cancelled item.
	items := a.QueueSnapshotFor("cli", "queue-cancel")
	if len(items) != 1 || items[0].MsgID != "req-2" || items[0].TurnID != 2 {
		t.Fatalf("snapshot=%+v, want only req-2", items)
	}
	// Dequeue skips the cancelled message.
	if cancelled := ss.queueDequeue("req-1"); !cancelled {
		t.Fatal("dequeue(req-1) cancelled=false, want true")
	}
}

func TestQueueSnapshotForUnknownSession(t *testing.T) {
	a := &Agent{}
	if items := a.QueueSnapshotFor("cli", "unknown"); items != nil {
		t.Fatalf("snapshot for unknown session=%+v, want nil", items)
	}
}

// ─── ⚡ User interrupt injection tests ───

func TestInjectUserInterrupt_BusyRoutesThroughAsyncPipeline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := tools.NewBackgroundTaskManager()
	a := &Agent{agentCtx: ctx}
	a.bgTaskMgr.Store(mgr)

	chatKey := "cli:interrupt-busy"
	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	ss.busy.Store(true)
	a.bgSessionStates.Store(chatKey, ss)
	defer a.bgSessionStates.Delete(chatKey)

	if !a.InjectUserInterrupt("cli", "interrupt-busy", "user-1", "先别停，顺便看看 X") {
		t.Fatal("InjectUserInterrupt(busy) = false, want true (routed for injection)")
	}
	// SendAsyncMessage publishes to mgr.NotifyCh — bgNotifyLoop (not running in
	// this test) is what buffers it into bgRunPending. Assert directly at the
	// channel boundary.
	select {
	case notif := <-mgr.NotifyCh:
		async, ok := notif.(*tools.AsyncMessageNotification)
		if !ok {
			t.Fatalf("notif type=%T, want *AsyncMessageNotification", notif)
		}
		if async.Source != tools.AsyncSourceUserInterrupt {
			t.Fatalf("source=%q, want %q", async.Source, tools.AsyncSourceUserInterrupt)
		}
		if async.SessionKey() != chatKey {
			t.Fatalf("key=%q, want %q", async.SessionKey(), chatKey)
		}
		if async.Content != "先别停，顺便看看 X" || async.SenderID() != "user-1" {
			t.Fatalf("notif=%+v", async)
		}
	default:
		t.Fatal("InjectUserInterrupt did not publish to NotifyCh (busy path must route through the async pipeline)")
	}
}

func TestInjectUserInterrupt_IdleDegradesToNormalMessage(t *testing.T) {
	a := &Agent{agentCtx: context.Background(), bus: bus.NewMessageBus()}
	mgr := tools.NewBackgroundTaskManager()
	a.bgTaskMgr.Store(mgr)

	// No registered session state (idle) — or busy=false.
	chatKey := "cli:interrupt-idle"
	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	a.bgSessionStates.Store(chatKey, ss)
	defer a.bgSessionStates.Delete(chatKey)

	if a.InjectUserInterrupt("cli", "interrupt-idle", "user-1", "plain message") {
		t.Fatal("InjectUserInterrupt(idle) = true, want false (degraded to normal send)")
	}
	select {
	case msg := <-a.bus.Inbound:
		if msg.Content != "plain message" || msg.Channel != "cli" || msg.ChatID != "interrupt-idle" {
			t.Fatalf("degraded message=%+v", msg)
		}
	default:
		t.Fatal("idle degradation did not inject a normal inbound message")
	}
}

// ─── Run drain tests: user_interrupt + async_message (busy-drop fix) ───

func TestDrainAndInjectBgNotifications_UserInterrupt(t *testing.T) {
	_, sess := newAgentHistorySession(t)
	a := &Agent{}
	chatKey := "test:interrupt-drain"
	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	a.bgSessionStates.Store(chatKey, ss)
	defer a.bgSessionStates.Delete(chatKey)

	a.enqueueBgNotifications([]tools.BgNotification{
		&tools.AsyncMessageNotification{Key: chatKey, Sid: "u1", Content: "插话内容", Source: tools.AsyncSourceUserInterrupt},
	})

	state := &runState{
		cfg: RunConfig{
			Session:                    sess,
			DrainBgNotifications:       a.wireBgNotificationDrain(chatKey),
			AcknowledgeBgNotifications: a.wireBgNotificationAcknowledge(chatKey),
		},
		persistence: NewPersistenceBridge(sess, 0),
	}
	if consumed := state.drainAndInjectBgNotifications(context.Background(), 1); consumed != 1 {
		t.Fatalf("consumed=%d, want 1", consumed)
	}
	if state.persistenceErr != nil {
		t.Fatal(state.persistenceErr)
	}
	// The interject must be a user_interrupt synthetic tool pair — NOT a user
	// message row and NOT silently dropped (the pre-fix bug for async messages).
	var found bool
	for _, m := range state.messages {
		if m.Role == "assistant" && len(m.ToolCalls) == 1 && m.ToolCalls[0].Name == "user_interrupt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no user_interrupt synthetic tool pair injected, messages=%+v", state.messages)
	}
	var toolMsg *llm.ChatMessage
	for i := range state.messages {
		if state.messages[i].Role == "tool" && state.messages[i].ToolName == "user_interrupt" {
			toolMsg = &state.messages[i]
		}
	}
	if toolMsg == nil {
		t.Fatalf("no user_interrupt tool result injected, messages=%+v", state.messages)
	}
	if !strings.Contains(toolMsg.Content, "插话内容") || !strings.Contains(toolMsg.Content, "用户插话") {
		t.Fatalf("tool result content=%q, want interject content with 用户插话 header", toolMsg.Content)
	}
	// Acknowledged — nothing left pending.
	if pending := a.pendingBgNotifications(chatKey); len(pending) != 0 {
		t.Fatalf("pending after drain=%d, want 0", len(pending))
	}
}

// TestDrainAndInjectBgNotifications_AsyncMessageNotDropped is the regression
// test for the long-standing busy-drop bug: AsyncMessageNotification (peer
// messages, webhook events) had NO case in the Run drain switch — the message
// was consumed (acknowledged) and silently dropped.
func TestDrainAndInjectBgNotifications_AsyncMessageNotDropped(t *testing.T) {
	_, sess := newAgentHistorySession(t)
	a := &Agent{}
	chatKey := "test:async-drain"
	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	a.bgSessionStates.Store(chatKey, ss)
	defer a.bgSessionStates.Delete(chatKey)

	a.enqueueBgNotifications([]tools.BgNotification{
		&tools.AsyncMessageNotification{Key: chatKey, Sid: "peer", Content: "peer message body", Source: tools.AsyncSourcePeer},
	})

	state := &runState{
		cfg: RunConfig{
			Session:                    sess,
			DrainBgNotifications:       a.wireBgNotificationDrain(chatKey),
			AcknowledgeBgNotifications: a.wireBgNotificationAcknowledge(chatKey),
		},
		persistence: NewPersistenceBridge(sess, 0),
	}
	if consumed := state.drainAndInjectBgNotifications(context.Background(), 1); consumed != 1 {
		t.Fatalf("consumed=%d, want 1", consumed)
	}
	if state.persistenceErr != nil {
		t.Fatal(state.persistenceErr)
	}
	var found bool
	for _, m := range state.messages {
		if m.Role == "tool" && m.ToolName == "async_message" && strings.Contains(m.Content, "peer message body") {
			found = true
		}
	}
	if !found {
		t.Fatalf("async_message tool result missing (message was dropped!), messages=%+v", state.messages)
	}
}
