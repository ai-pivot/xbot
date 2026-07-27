package agent

import (
	"context"
	"sync"
	"testing"

	"xbot/bus"
	"xbot/channel"
	"xbot/protocol"
)

// mockProgressChannel captures progress events sent via SendProgress.
type mockProgressChannel struct {
	mu       sync.Mutex
	events   []*protocol.ProgressEvent
	outbound []channel.OutboundMsg
}

func (m *mockProgressChannel) SendProgress(chatID string, payload *protocol.ProgressEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, payload)
}

func (m *mockProgressChannel) SendStreamContent(chatID, content, reasoning string) {}

func (m *mockProgressChannel) Send(msg channel.OutboundMsg) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.outbound = append(m.outbound, msg)
	return "", nil
}

func (m *mockProgressChannel) Name() string { return "mock" }

func (m *mockProgressChannel) Start() error { return nil }
func (m *mockProgressChannel) Stop()        {}

func (m *mockProgressChannel) getEvents() []*protocol.ProgressEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]*protocol.ProgressEvent, len(m.events))
	copy(cp, m.events)
	return cp
}

func (m *mockProgressChannel) getOutbound() []channel.OutboundMsg {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]channel.OutboundMsg, len(m.outbound))
	copy(cp, m.outbound)
	return cp
}

// TestEmitTurnStarted_Notification verifies that emitTurnStarted produces a
// correct turn_started progress event with the TurnID and notification content.
func TestEmitTurnStarted_Notification(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockCh := &mockProgressChannel{}
	a := &Agent{
		bus:          bus.NewMessageBus(),
		agentCtx:     ctx,
		channelRange: func(fn func(string, channel.Channel) bool) { fn("mock", mockCh) },
	}

	msg := bus.InboundMessage{
		Channel:  "cli",
		ChatID:   "test-chat",
		SenderID: "user-1",
		Content:  "⏰ [定时任务触发] test",
		Metadata: map[string]string{bgNotificationMetadataKey: "true"},
	}

	a.emitTurnStarted(msg, 42)

	events := mockCh.getEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 progress event, got %d", len(events))
	}
	ev := events[0]
	if ev.Phase != "turn_started" {
		t.Errorf("Phase = %q, want %q", ev.Phase, "turn_started")
	}
	if ev.TurnID != 42 {
		t.Errorf("TurnID = %d, want 42", ev.TurnID)
	}
	if ev.TurnStart == nil {
		t.Fatal("TurnStart is nil")
	}
	if ev.TurnStart.Trigger != "notification" {
		t.Errorf("Trigger = %q, want %q", ev.TurnStart.Trigger, "notification")
	}
	if ev.TurnStart.Content != "⏰ [定时任务触发] test" {
		t.Errorf("Content = %q, want notification content", ev.TurnStart.Content)
	}
}

// TestEmitTurnStarted_UserTrigger verifies that user-typed messages get
// trigger="user" and empty content (the frontend already has the optimistic
// message — it just needs the TurnID).
func TestEmitTurnStarted_UserTrigger(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockCh := &mockProgressChannel{}
	a := &Agent{
		bus:          bus.NewMessageBus(),
		agentCtx:     ctx,
		channelRange: func(fn func(string, channel.Channel) bool) { fn("mock", mockCh) },
	}

	msg := bus.InboundMessage{
		Channel:   "cli",
		ChatID:    "test-chat",
		SenderID:  "user-1",
		Content:   "hello world",
		RequestID: "req-1",
	}

	a.emitTurnStarted(msg, 7)

	events := mockCh.getEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.TurnStart.Trigger != "user" {
		t.Errorf("Trigger = %q, want %q", ev.TurnStart.Trigger, "user")
	}
	if ev.TurnStart.Content != "" {
		t.Errorf("Content = %q, want empty (user-typed already displayed)", ev.TurnStart.Content)
	}
	if ev.TurnStart.RequestID != "req-1" {
		t.Errorf("RequestID = %q, want req-1", ev.TurnStart.RequestID)
	}
}

// TestTurnID_Monotonic verifies that bgSessionState.nextTurnID produces
// monotonically increasing values.
func TestTurnID_Monotonic(t *testing.T) {
	ss := &bgSessionState{}
	first := ss.nextTurnID()
	second := ss.nextTurnID()
	third := ss.nextTurnID()
	if first != 1 || second != 2 || third != 3 {
		t.Errorf("TurnIDs = %d, %d, %d; want 1, 2, 3", first, second, third)
	}
}

// TestSendMessage_StampTurnID verifies that sendMessage stamps the active
// turn's TurnID on the OutboundMsg.
func TestSendMessage_StampTurnID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockCh := &mockProgressChannel{}
	a := &Agent{
		bus:          bus.NewMessageBus(),
		agentCtx:     ctx,
		channelRange: func(fn func(string, channel.Channel) bool) { fn("mock", mockCh) },
		directSend:   mockCh.Send,
	}

	// No active turn → TurnID should be 0
	a.sendMessage("cli", "test-chat", "no turn")
	outbound := mockCh.getOutbound()
	if len(outbound) != 1 {
		t.Fatalf("expected 1 outbound, got %d", len(outbound))
	}
	if outbound[0].TurnID != 0 {
		t.Errorf("TurnID = %d, want 0 (no active turn)", outbound[0].TurnID)
	}

	// Set active turn → TurnID should be stamped
	key := qualifyChatID("cli", "test-chat")
	a.bgSessionStates.Store(key, &bgSessionState{})
	ss, _ := a.bgSessionStates.Load(key)
	ss.(*bgSessionState).setActiveTurn(99)

	a.sendMessage("cli", "test-chat", "with turn")
	outbound = mockCh.getOutbound()
	if len(outbound) != 2 {
		t.Fatalf("expected 2 outbound, got %d", len(outbound))
	}
	if outbound[1].TurnID != 99 {
		t.Errorf("TurnID = %d, want 99", outbound[1].TurnID)
	}
}
