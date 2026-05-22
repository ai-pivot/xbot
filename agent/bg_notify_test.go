package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"xbot/bus"
	"xbot/tools"
)

// ==================== Background Task Notification ====================

func TestInjectInbound_IsCronFalse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &Agent{
		bus:      bus.NewMessageBus(),
		agentCtx: ctx,
	}

	go func() {
		a.injectInbound("cli", "test-chat", "system", "bg task done")
	}()

	msg := <-a.bus.Inbound

	if msg.IsCron {
		t.Error("injectInbound should set IsCron=false, got true — this would bypass persistence")
	}
	if msg.Channel != "cli" {
		t.Errorf("Channel = %q, want %q", msg.Channel, "cli")
	}
	if msg.ChatID != "test-chat" {
		t.Errorf("ChatID = %q, want %q", msg.ChatID, "test-chat")
	}
	if msg.Content != "bg task done" {
		t.Errorf("Content = %q, want %q", msg.Content, "bg task done")
	}
	if msg.RequestID == "" {
		t.Error("RequestID should be set")
	}
}

// TestDrainAndProcessNotifications_Synchronous verifies that
// drainAndProcessNotifications processes notifications synchronously and
// only drains notifications matching the given session key.
func TestDrainAndProcessNotifications_Synchronous(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := tools.NewBackgroundTaskManager()
	a := &Agent{
		bus:       bus.NewMessageBus(),
		agentCtx:  ctx,
		bgTaskMgr: mgr,
	}

	_ = mgr.Start("cli:test-chat", "user-1", "echo hello", func(ctx context.Context, outputBuf func(string)) (int, error) {
		outputBuf("hello output")
		return 0, nil
	})

	var notif tools.BgNotification
	select {
	case notif = <-mgr.NotifyCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for notification from NotifyCh")
	}

	a.bgRunPendingMu.Lock()
	a.bgRunPending = append(a.bgRunPending, notif)
	a.bgRunPendingMu.Unlock()

	a.drainAndProcessNotifications("cli:test-chat")

	select {
	case msg := <-a.bus.Inbound:
		if msg.ChatID != "test-chat" {
			t.Errorf("ChatID = %q, want %q", msg.ChatID, "test-chat")
		}
		if msg.Channel != "cli" {
			t.Errorf("Channel = %q, want %q", msg.Channel, "cli")
		}
	default:
		t.Fatal("drainAndProcessNotifications should have synchronously injected notification into bus.Inbound")
	}

	a.bgRunPendingMu.Lock()
	remaining := a.bgRunPending
	a.bgRunPendingMu.Unlock()
	if len(remaining) != 0 {
		t.Errorf("bgRunPending should be empty after draining matching session, got %d items", len(remaining))
	}
}

// TestDrainAndProcessNotifications_CrossSessionIsolation verifies that
// drainAndProcessNotifications only drains notifications matching the
// given session key, leaving other sessions' notifications in bgRunPending.
func TestDrainAndProcessNotifications_CrossSessionIsolation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := tools.NewBackgroundTaskManager()
	a := &Agent{
		bus:       bus.NewMessageBus(),
		agentCtx:  ctx,
		bgTaskMgr: mgr,
	}

	// Create two tasks for different sessions
	_ = mgr.Start("cli:chat-a", "user-1", "echo a", func(ctx context.Context, outputBuf func(string)) (int, error) {
		outputBuf("a")
		return 0, nil
	})
	_ = mgr.Start("cli:chat-b", "user-1", "echo b", func(ctx context.Context, outputBuf func(string)) (int, error) {
		outputBuf("b")
		return 0, nil
	})

	// Collect both notifications from NotifyCh
	var notifs []tools.BgNotification
	for len(notifs) < 2 {
		select {
		case n := <-mgr.NotifyCh:
			notifs = append(notifs, n)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for notifications")
		}
	}

	// Buffer both
	a.bgRunPendingMu.Lock()
	a.bgRunPending = append(a.bgRunPending, notifs...)
	a.bgRunPendingMu.Unlock()

	// Drain only chat-a's notifications
	a.drainAndProcessNotifications("cli:chat-a")

	// Should find chat-a's notification in bus.Inbound
	found := false
	timeout := time.After(2 * time.Second)
	for !found {
		select {
		case msg := <-a.bus.Inbound:
			if msg.ChatID == "chat-a" {
				found = true
			}
		case <-timeout:
			t.Fatal("chat-a's notification should be in bus.Inbound")
		}
	}

	// chat-b's notification should still be in bgRunPending
	a.bgRunPendingMu.Lock()
	remaining := a.bgRunPending
	a.bgRunPendingMu.Unlock()
	if len(remaining) != 1 {
		t.Fatalf("bgRunPending should have exactly 1 item (chat-b's), got %d", len(remaining))
	}
	if remaining[0].SessionKey() != "cli:chat-b" {
		t.Errorf("remaining notification session key = %q, want %q", remaining[0].SessionKey(), "cli:chat-b")
	}
}

// ==================== Regression: asyncCh race on bg notification ====================

// TestBgNotifyLoop_AlwaysBuffers_NoIdlePath is the core regression test for the
// bug where bgNotifyLoop's idle path used `go processBgNotification(n)`, causing
// injectCLIUserMessage to race with the agent's reply on asyncCh.
//
// With the new architecture, bgNotifyLoop ALWAYS buffers to bgRunPending and
// NEVER processes directly. This test verifies that invariant.
func TestBgNotifyLoop_AlwaysBuffers_NoIdlePath(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := tools.NewBackgroundTaskManager()
	a := &Agent{
		bus:       bus.NewMessageBus(),
		agentCtx:  ctx,
		bgTaskMgr: mgr,
	}

	chatKey := "cli:test-chat"

	// Register a bgSessionState (as chatWorker would)
	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	a.bgSessionStates.Store(chatKey, ss)
	defer a.bgSessionStates.Delete(chatKey)

	// Start bgNotifyLoop
	go a.bgNotifyLoop()

	// Start a bg task — it will complete immediately
	_ = mgr.Start(chatKey, "user-1", "echo test", func(ctx context.Context, outputBuf func(string)) (int, error) {
		outputBuf("test output")
		return 0, nil
	})

	// Wait for the notification to be signaled
	select {
	case <-ss.notifyCh:
		// Got signal — bgNotifyLoop buffered and signaled
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for notification signal — bgNotifyLoop didn't buffer")
	}

	// Verify notification is in bgRunPending (not processed directly)
	a.bgRunPendingMu.Lock()
	pending := a.bgRunPending
	a.bgRunPendingMu.Unlock()

	if len(pending) == 0 {
		t.Fatal("bgRunPending should have the notification — bgNotifyLoop must ALWAYS buffer, never process directly")
	}

	// Nothing should have been sent to bus.Inbound (no direct processing)
	select {
	case <-a.bus.Inbound:
		t.Fatal("bgNotifyLoop should NOT have sent anything to bus.Inbound — idle path removed")
	default:
		// Correct — nothing was injected directly
	}

	t.Logf("SUCCESS: bgNotifyLoop buffered %d notifications without processing", len(pending))
}

// TestBgSessionState_BusyFlag_GuardsIdleDrain verifies that chatWorker's
// notification handler checks the busy flag and skips draining when
// chatProcessLoop is active.
func TestBgSessionState_BusyFlag_GuardsIdleDrain(t *testing.T) {
	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}

	// Initially not busy
	if ss.busy.Load() {
		t.Fatal("initial busy should be false")
	}

	// Mark busy (as chatProcessLoop would before processMessage)
	ss.busy.Store(true)

	// Simulate notification signal arriving while busy
	ss.notifyCh <- struct{}{}

	// chatWorker should check busy and skip drain
	if !ss.busy.Load() {
		t.Fatal("busy should still be true — chatWorker should have skipped drain")
	}

	// After clearing busy, notification is still pending in notifyCh
	ss.busy.Store(false)

	// chatWorker can now drain on next signal
	select {
	case <-ss.notifyCh:
		// Signal consumed — chatWorker would drain now
	default:
		t.Fatal("notifyCh should still have pending signal after busy cleared")
	}
}

// TestBgNotifyLoop_SignalsMultipleSessions verifies that bgNotifyLoop
// signals the correct session when notifications arrive for different sessions.
func TestBgNotifyLoop_SignalsMultipleSessions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := tools.NewBackgroundTaskManager()
	a := &Agent{
		bus:       bus.NewMessageBus(),
		agentCtx:  ctx,
		bgTaskMgr: mgr,
	}

	// Register two sessions
	ssA := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	ssB := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	a.bgSessionStates.Store("cli:chat-a", ssA)
	a.bgSessionStates.Store("cli:chat-b", ssB)
	defer a.bgSessionStates.Delete("cli:chat-a")
	defer a.bgSessionStates.Delete("cli:chat-b")

	// Start bgNotifyLoop
	go a.bgNotifyLoop()

	// Start a task for chat-a
	_ = mgr.Start("cli:chat-a", "user-1", "echo a", func(ctx context.Context, outputBuf func(string)) (int, error) {
		outputBuf("a")
		return 0, nil
	})

	// Wait for chat-a's signal
	select {
	case <-ssA.notifyCh:
		t.Log("chat-a received notification signal")
	case <-time.After(5 * time.Second):
		t.Fatal("chat-a should have received notification signal")
	}

	// chat-b should NOT have been signaled
	select {
	case <-ssB.notifyCh:
		t.Fatal("chat-b should NOT have been signaled for chat-a's notification")
	default:
		// Correct
	}

	// Now start a task for chat-b
	_ = mgr.Start("cli:chat-b", "user-1", "echo b", func(ctx context.Context, outputBuf func(string)) (int, error) {
		outputBuf("b")
		return 0, nil
	})

	// Wait for chat-b's signal
	select {
	case <-ssB.notifyCh:
		t.Log("chat-b received notification signal")
	case <-time.After(5 * time.Second):
		t.Fatal("chat-b should have received notification signal")
	}
}

// TestDrainAndProcessNotifications_ConcurrentSafety verifies that
// concurrent calls to drainAndProcessNotifications from different
// goroutines (chatProcessLoop post-turn drain + chatWorker idle drain)
// don't double-process or lose notifications.
func TestDrainAndProcessNotifications_ConcurrentSafety(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := tools.NewBackgroundTaskManager()
	a := &Agent{
		bus:       bus.NewMessageBus(),
		agentCtx:  ctx,
		bgTaskMgr: mgr,
	}

	chatKey := "cli:test-chat"

	// Start 10 tasks — all complete immediately
	for i := 0; i < 10; i++ {
		_ = mgr.Start(chatKey, "user-1", "echo test", func(ctx context.Context, outputBuf func(string)) (int, error) {
			outputBuf("test output")
			return 0, nil
		})
	}

	// Collect all notifications from NotifyCh
	var notifs []tools.BgNotification
	for len(notifs) < 10 {
		select {
		case n := <-mgr.NotifyCh:
			notifs = append(notifs, n)
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for notifications, got %d/10", len(notifs))
		}
	}

	// Buffer all at once
	a.bgRunPendingMu.Lock()
	a.bgRunPending = append(a.bgRunPending, notifs...)
	a.bgRunPendingMu.Unlock()

	// Drain concurrently from two goroutines (simulating chatProcessLoop + chatWorker race)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		a.drainAndProcessNotifications(chatKey)
	}()
	go func() {
		defer wg.Done()
		a.drainAndProcessNotifications(chatKey)
	}()
	wg.Wait()

	// Count messages in bus.Inbound — should be exactly 10 (no duplicates, no losses)
	count := 0
	timeout := time.After(2 * time.Second)
	for count < 10 {
		select {
		case <-a.bus.Inbound:
			count++
		case <-timeout:
			t.Fatalf("expected 10 messages in bus.Inbound, got %d (duplicates or losses)", count)
		}
	}

	// Check no more messages (no duplicates)
	select {
	case <-a.bus.Inbound:
		t.Fatal("should not have more than 10 messages — possible duplicate")
	default:
	}

	t.Logf("SUCCESS: exactly 10 of 10 notifications processed (no duplicates, no losses)")
}

// TestDrainAndProcessNotifications_AfterResponseSent verifies the KEY INVARIANT:
// drainAndProcessNotifications is called AFTER the turn's response is sent.
// This test simulates the chatProcessLoop ordering: processMessage → response → drain.
func TestDrainAndProcessNotifications_AfterResponseSent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := tools.NewBackgroundTaskManager()
	a := &Agent{
		bus:       bus.NewMessageBus(),
		agentCtx:  ctx,
		bgTaskMgr: mgr,
	}

	chatKey := "cli:test-chat"

	// Track ordering of events
	var events []string
	var eventsMu sync.Mutex
	recordEvent := func(name string) {
		eventsMu.Lock()
		events = append(events, name)
		eventsMu.Unlock()
	}

	// Simulate the correct chatProcessLoop ordering:
	// 1. busy = true
	// 2. processMessage (simulated)
	// 3. response sent (simulated)
	// 4. busy = false
	// 5. drain notifications

	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}

	// Start a bg task and buffer notification
	_ = mgr.Start(chatKey, "user-1", "ordering-test", func(ctx context.Context, outputBuf func(string)) (int, error) {
		outputBuf("output")
		return 0, nil
	})
	notif := <-mgr.NotifyCh
	a.bgRunPendingMu.Lock()
	a.bgRunPending = append(a.bgRunPending, notif)
	a.bgRunPendingMu.Unlock()

	// Simulate chatProcessLoop turn
	ss.busy.Store(true)
	recordEvent("busy_true")

	recordEvent("processMessage")
	recordEvent("response_sent")

	ss.busy.Store(false)
	recordEvent("busy_false")

	a.drainAndProcessNotifications(chatKey)
	recordEvent("drain_complete")

	// Verify ordering
	eventsMu.Lock()
	defer eventsMu.Unlock()
	t.Logf("Event order: %v", events)

	expected := []string{"busy_true", "processMessage", "response_sent", "busy_false", "drain_complete"}
	if len(events) != len(expected) {
		t.Fatalf("expected %d events, got %d: %v", len(expected), len(events), events)
	}
	for i, e := range expected {
		if events[i] != e {
			t.Errorf("event[%d] = %q, want %q", i, events[i], e)
		}
	}

	// Verify notification was processed
	select {
	case <-a.bus.Inbound:
		t.Log("SUCCESS: notification processed after response sent")
	case <-time.After(2 * time.Second):
		t.Fatal("notification was not processed after drain")
	}

	if ss.busy.Load() {
		t.Fatal("busy should be false after turn completes")
	}
}

// TestBgNotifyLoop_NoDirectProcessing verifies that bgNotifyLoop
// never sends to bus.Inbound directly — it only buffers.
// This is the KEY architectural invariant of the redesign.
func TestBgNotifyLoop_NoDirectProcessing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := tools.NewBackgroundTaskManager()
	a := &Agent{
		bus:       bus.NewMessageBus(),
		agentCtx:  ctx,
		bgTaskMgr: mgr,
	}

	// Start bgNotifyLoop
	go a.bgNotifyLoop()

	// Start 5 tasks for an UNREGISTERED session (no bgSessionState)
	for i := 0; i < 5; i++ {
		_ = mgr.Start("cli:unregistered", "user-1", "echo test", func(ctx context.Context, outputBuf func(string)) (int, error) {
			outputBuf("output")
			return 0, nil
		})
	}

	// Wait for bgNotifyLoop to read and buffer all 5 notifications
	requireEventual(t, 5*time.Second, 10*time.Millisecond, func() error {
		a.bgRunPendingMu.Lock()
		n := len(a.bgRunPending)
		a.bgRunPendingMu.Unlock()
		if n < 5 {
			return fmt.Errorf("bgRunPending has %d/5 notifications", n)
		}
		return nil
	})

	// Check bus.Inbound — should be EMPTY (no direct processing)
	var unexpectedMsgs int
	for {
		select {
		case <-a.bus.Inbound:
			unexpectedMsgs++
		default:
			goto done
		}
	}
done:

	if unexpectedMsgs > 0 {
		t.Fatalf("bgNotifyLoop should NEVER send to bus.Inbound directly, but sent %d messages — idle path leak!", unexpectedMsgs)
	}

	t.Logf("SUCCESS: 5 notifications buffered, %d directly processed (want 0)", unexpectedMsgs)
}

func requireEventual(t *testing.T, timeout, interval time.Duration, check func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if err := check(); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(check().Error())
		}
		time.Sleep(interval)
	}
}

// ==================== CronFired Notification ====================

// TestDrainAndProcessNotifications_CronFired verifies that drainAndProcessNotifications
// processes a CronFired notification and injects it into bus.Inbound with the ⏰ prefix.
func TestDrainAndProcessNotifications_CronFired(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := tools.NewBackgroundTaskManager()
	a := &Agent{
		bus:       bus.NewMessageBus(),
		agentCtx:  ctx,
		bgTaskMgr: mgr,
	}

	// Buffer a CronFired notification
	cronNotif := &tools.CronFired{
		Key:     "cli:test-chat",
		Sid:     "user-1",
		Message: "check server status",
	}
	a.bgRunPendingMu.Lock()
	a.bgRunPending = append(a.bgRunPending, cronNotif)
	a.bgRunPendingMu.Unlock()

	a.drainAndProcessNotifications("cli:test-chat")

	select {
	case msg := <-a.bus.Inbound:
		if msg.ChatID != "test-chat" {
			t.Errorf("ChatID = %q, want %q", msg.ChatID, "test-chat")
		}
		if msg.Channel != "cli" {
			t.Errorf("Channel = %q, want %q", msg.Channel, "cli")
		}
		// Must have the ⏰ prefix from processCronFiredNotification
		if !containsPrefix(msg.Content, "⏰") {
			t.Errorf("Content should contain ⏰ prefix, got: %q", msg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("drainAndProcessNotifications should have injected CronFired into bus.Inbound")
	}

	// Verify nothing left in bgRunPending
	a.bgRunPendingMu.Lock()
	remaining := a.bgRunPending
	a.bgRunPendingMu.Unlock()
	if len(remaining) != 0 {
		t.Errorf("bgRunPending should be empty after draining, got %d items", len(remaining))
	}
}

// TestBgNotifyLoop_CronFired_BuffersAndSignals verifies that CronFired goes through
// the bgNotifyLoop buffering pipeline (not processed directly) and signals the session.
func TestBgNotifyLoop_CronFired_BuffersAndSignals(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := tools.NewBackgroundTaskManager()
	a := &Agent{
		bus:       bus.NewMessageBus(),
		agentCtx:  ctx,
		bgTaskMgr: mgr,
	}

	chatKey := "cli:test-chat"

	// Register a bgSessionState (as chatWorker would)
	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	a.bgSessionStates.Store(chatKey, ss)
	defer a.bgSessionStates.Delete(chatKey)

	// Start bgNotifyLoop
	go a.bgNotifyLoop()

	// Send a CronFired through NotifyCh
	mgr.SendCronFired(&tools.CronFired{
		Key:     chatKey,
		Sid:     "user-1",
		Message: "run backups",
	})

	// Wait for the notification to be signaled
	select {
	case <-ss.notifyCh:
		// Got signal — bgNotifyLoop buffered and signaled
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for notification signal — bgNotifyLoop didn't buffer CronFired")
	}

	// Verify notification is in bgRunPending (not processed directly)
	a.bgRunPendingMu.Lock()
	pending := a.bgRunPending
	a.bgRunPendingMu.Unlock()

	if len(pending) == 0 {
		t.Fatal("bgRunPending should have the CronFired notification")
	}

	// Verify it's actually a CronFired
	found := false
	for _, n := range pending {
		if cf, ok := n.(*tools.CronFired); ok {
			if cf.Message == "run backups" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("bgRunPending should contain the CronFired notification with correct message")
	}

	// Nothing should have been sent to bus.Inbound (no direct processing)
	select {
	case <-a.bus.Inbound:
		t.Fatal("bgNotifyLoop should NOT have sent anything to bus.Inbound — only buffers")
	default:
		// Correct — nothing was injected directly
	}
}

// TestDrainAndProcessNotifications_MixedTypes verifies that drainAndProcessNotifications
// handles both bg task completions and CronFired notifications in the same drain cycle.
func TestDrainAndProcessNotifications_MixedTypes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := tools.NewBackgroundTaskManager()
	a := &Agent{
		bus:       bus.NewMessageBus(),
		agentCtx:  ctx,
		bgTaskMgr: mgr,
	}

	chatKey := "cli:test-chat"

	// Start a bg task — it will complete immediately
	_ = mgr.Start(chatKey, "user-1", "echo hello", func(ctx context.Context, outputBuf func(string)) (int, error) {
		outputBuf("hello output")
		return 0, nil
	})

	// Collect the bg task notification
	var bgNotif tools.BgNotification
	select {
	case bgNotif = <-mgr.NotifyCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for bg task notification")
	}

	// Buffer both bg task and CronFired notifications
	cronNotif := &tools.CronFired{
		Key:     chatKey,
		Sid:     "user-1",
		Message: "check health",
	}
	a.bgRunPendingMu.Lock()
	a.bgRunPending = append(a.bgRunPending, bgNotif, cronNotif)
	a.bgRunPendingMu.Unlock()

	a.drainAndProcessNotifications(chatKey)

	// Should receive 2 messages in bus.Inbound
	var msgs []bus.InboundMessage
	timeout := time.After(2 * time.Second)
	for len(msgs) < 2 {
		select {
		case msg := <-a.bus.Inbound:
			msgs = append(msgs, msg)
		case <-timeout:
			t.Fatalf("expected 2 messages in bus.Inbound, got %d", len(msgs))
		}
	}

	// One should be a cron message (⏰ prefix), one should be a bg task message
	hasCron := false
	hasBgTask := false
	for _, msg := range msgs {
		if containsPrefix(msg.Content, "⏰") {
			hasCron = true
		} else {
			hasBgTask = true
		}
	}
	if !hasCron {
		t.Error("expected one message with ⏰ prefix (cron)")
	}
	if !hasBgTask {
		t.Error("expected one message without ⏰ prefix (bg task)")
	}

	t.Logf("SUCCESS: both bg task and CronFired notifications processed in mixed drain")
}

// containsPrefix checks if s starts with the given prefix string.
func containsPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
