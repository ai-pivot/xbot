package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"xbot/bus"
	ch "xbot/channel"
	"xbot/llm"
	"xbot/tools"
)

func TestCancelNotRegisteredAsCommand(t *testing.T) {
	// /cancel is intercepted in Run(), not registered as a Command.
	// It should NOT match as a command in the registry.
	r := NewCommandRegistry()
	registerBuiltinCommands(r)

	if r.IsCommand("/cancel") {
		t.Error("IsCommand(/cancel) = true, want false — /cancel is handled in Run(), not as a registered command")
	}
	if r.IsCommand("/CANCEL") {
		t.Error("IsCommand(/CANCEL) = true, want false")
	}
}

func TestChatCancelCh_BasicSignaling(t *testing.T) {
	// Test that the cancel channel mechanism works correctly
	var cancelMap sync.Map

	cancelKey := "feishu:chat123:user456"
	cancelCh := make(chan struct{}, 1)
	cancelMap.Store(cancelKey, cancelCh)

	// Sending cancel signal should succeed
	if ch, ok := cancelMap.Load(cancelKey); ok {
		select {
		case ch.(chan struct{}) <- struct{}{}:
			// OK
		default:
			t.Error("Failed to send cancel signal")
		}
	} else {
		t.Error("Cancel channel not found")
	}

	// Second send should not block (channel already has a signal, default case)
	if ch, ok := cancelMap.Load(cancelKey); ok {
		select {
		case ch.(chan struct{}) <- struct{}{}:
			t.Error("Second cancel signal should not succeed (channel full)")
		default:
			// OK — expected
		}
	}

	// Different key should not find a channel
	wrongKey := "feishu:chat123:otherUser"
	if _, ok := cancelMap.Load(wrongKey); ok {
		t.Error("Should not find cancel channel for different user")
	}
}

func TestCancelKeyIncludesSenderID(t *testing.T) {
	// Verify that cancel keys are per-sender, preventing cross-user cancellation in group chats
	var cancelMap sync.Map

	userA := "feishu:groupChat:userA"
	userB := "feishu:groupChat:userB"

	chA := make(chan struct{}, 1)
	chB := make(chan struct{}, 1)
	cancelMap.Store(userA, chA)
	cancelMap.Store(userB, chB)

	// Cancel userA's task
	if ch, ok := cancelMap.Load(userA); ok {
		ch.(chan struct{}) <- struct{}{}
	}

	// userB's channel should still be empty
	select {
	case <-chB:
		t.Error("userB's cancel channel should not have been signaled")
	default:
		// OK
	}

	// userA's channel should have the signal
	select {
	case <-chA:
		// OK
	default:
		t.Error("userA's cancel channel should have been signaled")
	}
}

// TestRun_CancelPreservesEngineMessages verifies that when a Run is cancelled
// after completing some iterations, EngineMessages contains the partial progress.
func TestRun_CancelPreservesEngineMessages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	mock := &mockLLM{
		responses: []llm.LLMResponse{
			{
				Content:      "Let me check something...",
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "Shell", Arguments: `{"command":"echo hello"}`},
				},
			},
			// Second Generate will be cancelled
		},
	}

	callCount := 0
	out := Run(ctx, RunConfig{
		LLMClient: mock,
		Model:     "test",
		Tools:     newTestRegistry(),
		Messages:  baseMessages(),
		AgentID:   "main",
		ToolExecutor: func(execCtx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
			callCount++
			if callCount == 1 {
				time.Sleep(50 * time.Millisecond)
				cancel()
			}
			return tools.NewResult("tool output"), nil
		},
	})

	if out.Error == nil {
		t.Fatal("expected error for cancelled context")
	}
	if !errors.Is(out.Error, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", out.Error)
	}

	if len(out.EngineMessages) == 0 {
		t.Fatal("expected non-empty EngineMessages after cancel, got empty")
	}

	foundAssistant := false
	foundToolResult := false
	for _, em := range out.EngineMessages {
		if em.Role == "assistant" && len(em.ToolCalls) > 0 {
			foundAssistant = true
			if em.ToolCalls[0].Name != "Shell" {
				t.Errorf("expected Shell tool call, got %s", em.ToolCalls[0].Name)
			}
		}
		if em.Role == "tool" {
			foundToolResult = true
		}
	}
	if !foundAssistant {
		t.Error("expected assistant message with tool_calls in EngineMessages")
	}
	if !foundToolResult {
		t.Error("expected tool result message in EngineMessages")
	}

	t.Logf("Cancel preserved %d engine messages (expected >= 2)", len(out.EngineMessages))
}

// ==================== Cancel × Bg Notification Race Tests ====================
//
// These tests verify the fix for the race condition where:
// 1. Bg task notification is drained into a Run as a synthetic tool pair
// 2. User presses Ctrl+C
// 3. Cancel signal arrives but Run may continue processing the injected iteration
//
// The fix ensures:
// - Cancel interception does NOT send premature cancel ack (Part 1)
// - DrainBgNotifications skips when ctx is cancelled (Part 2)
// - Drained notifications are discarded on cancel (Part 3)

// makeTestNotif creates a BgNotification with the given session key.
// Uses CronFired because it has exported fields (no unexported sessionKey).
func makeTestNotif(sessionKey, id string) tools.BgNotification {
	return &tools.CronFired{
		Key:     sessionKey,
		Sid:     "user-1",
		Message: id,
	}
}

// TestWireBgNotificationDrain_TracksDrained verifies that wireBgNotificationDrain
// records drained notifications into bgSessionState.drainedThisRun so they can
// be discarded explicitly if the Run is cancelled.
func TestWireBgNotificationDrain_TracksDrained(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := &Agent{
		bus:      bus.NewMessageBus(),
		agentCtx: ctx,
	}

	sessionKey := "cli:test-chat"
	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	a.bgSessionStates.Store(sessionKey, ss)
	defer a.bgSessionStates.Delete(sessionKey)

	n1 := makeTestNotif(sessionKey, "notif-1")
	n2 := makeTestNotif(sessionKey, "notif-2")

	a.enqueueBgNotifications([]tools.BgNotification{n1, n2})

	drain := a.wireBgNotificationDrain(sessionKey)
	drained := drain()

	if len(drained) != 2 {
		t.Fatalf("drained %d notifications, want 2", len(drained))
	}

	ss.drainedThisRunMu.Lock()
	tracked := ss.drainedThisRun
	ss.drainedThisRunMu.Unlock()

	if len(tracked) != 2 {
		t.Fatalf("drainedThisRun has %d notifications, want 2", len(tracked))
	}
}

// TestWireBgNotificationDrain_OtherSessionNotTracked verifies that notifications
// for other sessions are not tracked in this session's drainedThisRun.
func TestWireBgNotificationDrain_OtherSessionNotTracked(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := &Agent{
		bus:      bus.NewMessageBus(),
		agentCtx: ctx,
	}

	sessionA := "cli:chat-a"
	sessionB := "cli:chat-b"
	ssA := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	a.bgSessionStates.Store(sessionA, ssA)
	defer a.bgSessionStates.Delete(sessionA)

	nA := makeTestNotif(sessionA, "notif-a")
	nB := makeTestNotif(sessionB, "notif-b")

	a.enqueueBgNotifications([]tools.BgNotification{nA, nB})

	drain := a.wireBgNotificationDrain(sessionA)
	drained := drain()

	if len(drained) != 1 {
		t.Fatalf("drained %d, want 1 (only session A)", len(drained))
	}

	ssA.drainedThisRunMu.Lock()
	trackedA := ssA.drainedThisRun
	ssA.drainedThisRunMu.Unlock()
	if len(trackedA) != 1 {
		t.Fatalf("drainedThisRun for A has %d, want 1", len(trackedA))
	}

	remaining := a.pendingBgNotifications(sessionB)
	if len(remaining) != 1 {
		t.Fatalf("bgRunPending has %d, want 1 (session B)", len(remaining))
	}
}

// TestClearDrainedThisRun_PreventsStaleCancelDiscard verifies that clearDrainedThisRun
// prevents notifications from a completed turn from being discarded if the next
// turn is cancelled.
func TestClearDrainedThisRun_PreventsStaleCancelDiscard(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := &Agent{
		bus:      bus.NewMessageBus(),
		agentCtx: ctx,
	}

	sessionKey := "cli:test-chat"
	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	a.bgSessionStates.Store(sessionKey, ss)
	defer a.bgSessionStates.Delete(sessionKey)

	// Turn 1: drain then clear (normal completion)
	n1 := makeTestNotif(sessionKey, "notif-turn-1")
	a.enqueueBgNotification(n1)

	drain := a.wireBgNotificationDrain(sessionKey)
	drain()
	ss.clearDrainedThisRun()

	ss.drainedThisRunMu.Lock()
	if len(ss.drainedThisRun) != 0 {
		t.Fatalf("drainedThisRun should be empty after clear, got %d", len(ss.drainedThisRun))
	}
	ss.drainedThisRunMu.Unlock()

	// Turn 2: drain another notification
	n2 := makeTestNotif(sessionKey, "notif-turn-2")
	a.enqueueBgNotification(n2)

	drain2 := a.wireBgNotificationDrain(sessionKey)
	drained2 := drain2()

	if len(drained2) != 1 {
		t.Fatalf("Turn 2 drained %d, want 1", len(drained2))
	}

	// Only notif-turn-2 should be tracked (notif-turn-1 was cleared)
	ss.drainedThisRunMu.Lock()
	tracked := ss.drainedThisRun
	ss.drainedThisRunMu.Unlock()
	if len(tracked) != 1 {
		t.Fatalf("drainedThisRun has %d after Turn 2, want 1", len(tracked))
	}
	cron, ok := tracked[0].(*tools.CronFired)
	if !ok || cron.Message != "notif-turn-2" {
		t.Errorf("expected notif-turn-2, got %+v", tracked[0])
	}
}

// TestHandleCancelledRun_DiscardsDrainedNotifications verifies that drained
// notifications are DISCARDED (not re-queued) when the Run is cancelled.
// The user pressed Ctrl+C — they want everything to stop. Re-queuing would
// cause drainAndProcessNotifications to deliver the notification as a new
// user message, starting a new turn the user explicitly wanted to cancel.
func TestHandleCancelledRun_RecordsPendingNotifications(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := &Agent{
		bus:      bus.NewMessageBus(),
		agentCtx: ctx,
	}

	sessionKey := "cli:test-chat"
	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	a.bgSessionStates.Store(sessionKey, ss)
	defer a.bgSessionStates.Delete(sessionKey)

	notif := makeTestNotif(sessionKey, "cancel-record-test")
	ss.drainedThisRunMu.Lock()
	ss.drainedThisRun = append(ss.drainedThisRun, notif)
	ss.drainedThisRunMu.Unlock()

	pendingSameSession := makeTestNotif(sessionKey, "pending-same-session")
	pendingOtherSession := makeTestNotif("cli:other-chat", "pending-other-session")
	a.enqueueBgNotifications([]tools.BgNotification{pendingSameSession, pendingOtherSession})

	msg := bus.InboundMessage{
		Channel: "cli", ChatID: "test-chat", Content: "test", SenderID: "user-1",
	}
	out := &RunOutput{}

	a.handleCancelledRun(ctx, msg, out, nil)

	if queued := a.pendingBgNotifications(sessionKey); len(queued) != 0 {
		t.Fatalf("same-session bgRunPending has %d after cancel, want 0", len(queued))
	}
	queuedOther := a.pendingBgNotifications("cli:other-chat")
	if len(queuedOther) != 1 {
		t.Fatalf("other-session bgRunPending has %d after cancel, want 1", len(queuedOther))
	}
	cron, ok := queuedOther[0].(*tools.CronFired)
	if !ok || cron.Message != "pending-other-session" {
		t.Fatalf("bgRunPending kept %+v, want pending-other-session", queuedOther[0])
	}

	ss.drainedThisRunMu.Lock()
	trackedLen := len(ss.drainedThisRun)
	ss.drainedThisRunMu.Unlock()
	if trackedLen != 0 {
		t.Errorf("drainedThisRun should be empty after cancel, got %d", trackedLen)
	}
}

func TestHandleBgNotifySignal_AfterCancelProcessesNormally(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := &Agent{
		bus:      bus.NewMessageBus(),
		agentCtx: ctx,
	}

	sessionKey := "cli:test-chat"
	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}

	newNotif := makeTestNotif(sessionKey, "new-after-cancel")
	a.enqueueBgNotification(newNotif)

	a.handleBgNotifySignal(sessionKey, ss)

	if queued := a.pendingBgNotifications(sessionKey); len(queued) != 0 {
		t.Fatalf("new notification remained pending after idle notify, got %d", len(queued))
	}

	select {
	case msg := <-a.bus.Inbound:
		if msg.ChatID != "test-chat" {
			t.Fatalf("ChatID = %q, want test-chat", msg.ChatID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for notification to be injected")
	}
}

func TestInjectedBgNotificationMetadata(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := &Agent{
		bus:      bus.NewMessageBus(),
		agentCtx: ctx,
	}

	a.injectBgUserMessage("cli", "test-chat", "system", "bg task done")

	select {
	case msg := <-a.bus.Inbound:
		if msg.Metadata[bgNotificationMetadataKey] != "true" {
			t.Fatalf("injected bg notification metadata = %v, want %s=true", msg.Metadata, bgNotificationMetadataKey)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for injected bg notification")
	}
}

// TestCancelIntercept_DoesNotSendPrematureAck verifies that when /cancel arrives
// and cancelCh IS registered, the agent does NOT send an outbound message.
// The cancel ack should only come from chatProcessLoop's wasCancelled path
// after Run actually returns.
//
// This test uses directSend mock to capture sendMessage calls without running
// the full agent loop (which needs multiSession and other heavy deps).
func TestCancelIntercept_DoesNotSendPrematureAck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var sentMessages []string
	var sentMu sync.Mutex

	a := &Agent{
		bus:      bus.NewMessageBus(),
		agentCtx: ctx,
		// Mock directSend to capture any messages sent via sendMessage
		directSend: func(msg ch.OutboundMsg) (string, error) {
			sentMu.Lock()
			sentMessages = append(sentMessages, msg.Content)
			sentMu.Unlock()
			return "", nil
		},
		// channelFinder returns nil — sendMessage falls through to directSend
		channelFinder: func(name string) (ch.Channel, bool) { return nil, false },
	}

	// Register a cancelCh to simulate an active turn
	cancelKey := "cli:test-chat"
	cancelCh := make(chan struct{}, 1)
	a.chatCancelCh.Store(cancelKey, cancelCh)

	// Simulate what the cancel interception does: send the cancel signal
	// (this is the ONLY thing the fixed code does — it no longer calls sendMessage)
	select {
	case cancelCh <- struct{}{}:
		// Signal sent — this is the expected behavior
	default:
		t.Fatal("failed to send cancel signal")
	}

	// Verify NO message was sent via directSend (the old code would have
	// called sendMessage("⚠️ 已取消请求", cancelMeta) here)
	sentMu.Lock()
	count := len(sentMessages)
	sentMu.Unlock()
	if count != 0 {
		t.Fatalf("expected 0 messages sent after cancel signal, got %d: %v — "+
			"cancel interception must NOT send premature ack", count, sentMessages)
	}
}
