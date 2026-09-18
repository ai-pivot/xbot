package agent

import (
	"context"
	"fmt"
	"strings"
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
		bus:      bus.NewMessageBus(),
		agentCtx: ctx,
	}
	a.bgTaskMgr.Store(mgr)

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

	a.enqueueBgNotification(notif)

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

	remaining := a.pendingBgNotifications("cli:test-chat")
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
		bus:      bus.NewMessageBus(),
		agentCtx: ctx,
	}
	a.bgTaskMgr.Store(mgr)

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
	a.enqueueBgNotifications(notifs)

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

	remaining := a.pendingBgNotifications("cli:chat-b")
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
		bus:      bus.NewMessageBus(),
		agentCtx: ctx,
	}
	a.bgTaskMgr.Store(mgr)

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
	pending := a.pendingBgNotifications(chatKey)

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
		bus:      bus.NewMessageBus(),
		agentCtx: ctx,
	}
	a.bgTaskMgr.Store(mgr)

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
		bus:      bus.NewMessageBus(),
		agentCtx: ctx,
	}
	a.bgTaskMgr.Store(mgr)

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
	a.enqueueBgNotifications(notifs)

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

	// ⛔ 契约（2026-09-18 用户 P0）：10 条通知 ⇒ **恰好 1 条** user 消息（一个 turn）。
	// 并发 drain 只有一个能拿到条目（另一个拿到 0）⇒ 不可能出现重复注入。
	var msgs []bus.InboundMessage
	timeout := time.After(2 * time.Second)
	for len(msgs) < 1 {
		select {
		case msg := <-a.bus.Inbound:
			msgs = append(msgs, msg)
		case <-timeout:
			t.Fatal("expected exactly 1 batched message in bus.Inbound, got none")
		}
	}

	// 不得有第二条（并发 drain 的重复注入回归）。
	select {
	case extra := <-a.bus.Inbound:
		t.Fatalf("should be exactly 1 batched message — possible duplicate: %q", extra.Content)
	default:
	}

	// 批量消息必须携带 10 段（每段含任务输出 "test output"），且带通知标记。
	if n := strings.Count(msgs[0].Content, "test output"); n != 10 {
		t.Errorf("batched message must carry all 10 notifications, got %d sections", n)
	}
	if msgs[0].Metadata["xbot_internal_bg_notification"] != "true" {
		t.Error("batched message missing bg notification marker")
	}

	t.Logf("SUCCESS: exactly 1 batched message for 10 notifications (no duplicates, no losses)")
}

// TestDrainAndProcessNotifications_AfterResponseSent verifies the KEY INVARIANT:
// drainAndProcessNotifications is called AFTER the turn's response is sent.
// This test simulates the chatProcessLoop ordering: processMessage → response → drain.
func TestDrainAndProcessNotifications_AfterResponseSent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := tools.NewBackgroundTaskManager()
	a := &Agent{
		bus:      bus.NewMessageBus(),
		agentCtx: ctx,
	}
	a.bgTaskMgr.Store(mgr)

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
	a.enqueueBgNotification(notif)

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

// TestBgNotifyLoop_NoDirectProcessing_WithActiveSession verifies that bgNotifyLoop
// does NOT send to bus.Inbound directly when the session has an active chatWorker.
// Instead, it only buffers and signals notifyCh. This avoids the race between
// injectCLIUserMessage and the agent's reply on asyncCh.
//
// NOTE: When no chatWorker exists (unregistered session), bgNotifyLoop DOES process
// directly — see TestBgNotifyLoop_CronFired_NoSession_ProcessesDirectly.
func TestBgNotifyLoop_NoDirectProcessing_WithActiveSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := tools.NewBackgroundTaskManager()
	a := &Agent{
		bus:      bus.NewMessageBus(),
		agentCtx: ctx,
	}
	a.bgTaskMgr.Store(mgr)

	chatKey := "cli:active-session"

	// Register a bgSessionState (simulates active chatWorker)
	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	a.bgSessionStates.Store(chatKey, ss)
	defer a.bgSessionStates.Delete(chatKey)

	// Start bgNotifyLoop
	go a.bgNotifyLoop()

	// Start 5 tasks for the REGISTERED session
	for i := 0; i < 5; i++ {
		_ = mgr.Start(chatKey, "user-1", "echo test", func(ctx context.Context, outputBuf func(string)) (int, error) {
			outputBuf("output")
			return 0, nil
		})
	}

	// Wait for bgNotifyLoop to read, buffer, and signal all 5 notifications
	requireEventual(t, 5*time.Second, 10*time.Millisecond, func() error {
		pending := a.pendingBgNotifications(chatKey)
		n := len(pending)
		if n < 5 {
			return fmt.Errorf("bgRunPending has %d/5 notifications", n)
		}
		return nil
	})

	// Check bus.Inbound — should be EMPTY (no direct processing for active session)
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
		t.Fatalf("bgNotifyLoop should NEVER send to bus.Inbound directly for active session, but sent %d messages — idle path leak!", unexpectedMsgs)
	}

	t.Logf("SUCCESS: 5 notifications buffered for active session, %d directly processed (want 0)", unexpectedMsgs)
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
		bus:      bus.NewMessageBus(),
		agentCtx: ctx,
	}
	a.bgTaskMgr.Store(mgr)

	// Buffer a CronFired notification
	cronNotif := &tools.CronFired{
		Key:     "cli:test-chat",
		Sid:     "user-1",
		Message: "check server status",
	}
	a.enqueueBgNotification(cronNotif)

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
	remaining := a.pendingBgNotifications("cli:test-chat")
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
		bus:      bus.NewMessageBus(),
		agentCtx: ctx,
	}
	a.bgTaskMgr.Store(mgr)

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
	pending := a.pendingBgNotifications(chatKey)

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
		bus:      bus.NewMessageBus(),
		agentCtx: ctx,
	}
	a.bgTaskMgr.Store(mgr)

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
	a.enqueueBgNotifications([]tools.BgNotification{bgNotif, cronNotif})

	a.drainAndProcessNotifications(chatKey)

	// ⛔ 契约（2026-09-18 用户 P0）：一次突发 = **一条** user 消息 = 一个 turn。
	// 旧实现逐条注入 ⇒ N 条通知在 busy 期堆进**用户可见队列**（截图：Next turn 83
	// + Turn 84–88），既不能立刻发出也拿不到"一次处理完"。
	var msgs []bus.InboundMessage
	timeout := time.After(2 * time.Second)
	for len(msgs) < 1 {
		select {
		case msg := <-a.bus.Inbound:
			msgs = append(msgs, msg)
		case <-timeout:
			t.Fatal("expected exactly 1 batched message in bus.Inbound, got none")
		}
	}
	// 不得有第二条（禁止逐条注入回归）。
	select {
	case extra := <-a.bus.Inbound:
		t.Fatalf("expected ONE batched message, got a second one: %q", extra.Content)
	default:
	}

	joined := msgs[0].Content
	if !strings.Contains(joined, "⏰") {
		t.Errorf("batched message must contain cron part (⏰), got: %s", joined)
	}
	if !strings.Contains(joined, "[System Notification]") {
		t.Errorf("batched message must contain bg task part ([System Notification]), got: %s", joined)
	}
	if !strings.Contains(joined, "\n\n---\n\n") {
		t.Errorf("parts must be joined by the documented separator, got: %s", joined)
	}

	t.Logf("SUCCESS: bg task + CronFired injected as ONE batched message")
}

// TestBgNotifyLoop_CronFired_NoSession_ProcessesDirectly is the regression test for
// the bug where cron triggers after service restart never fired until the user sent
// the first message. The root cause: bgNotifyLoop only signaled sessions registered
// in bgSessionStates, but those are created lazily by chatWorker (on first inbound
// message). After restart with no active session, cron notifications accumulated in
// bgRunPending with nobody to drain them.
//
// This test verifies that when no chatWorker exists for a session, bgNotifyLoop
// processes notifications directly via drainAndProcessNotifications, injecting the
// cron message into bus.Inbound without waiting for a user message.
func TestBgNotifyLoop_CronFired_NoSession_ProcessesDirectly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := tools.NewBackgroundTaskManager()
	a := &Agent{
		bus:      bus.NewMessageBus(),
		agentCtx: ctx,
	}
	a.bgTaskMgr.Store(mgr)

	chatKey := "cli:restart-test-chat"

	// CRITICAL: do NOT register a bgSessionState — simulates restart state
	// where no chatWorker has been created yet.

	// Start bgNotifyLoop
	go a.bgNotifyLoop()
	defer func() {
		cancel() // stop bgNotifyLoop
	}()

	// Send a CronFired — should be processed directly since no chatWorker exists
	mgr.SendCronFired(&tools.CronFired{
		Key:     chatKey,
		Sid:     "user-1",
		Message: "scheduled backup",
	})

	// The notification should appear in bus.Inbound (processed directly, not
	// just buffered). Wait up to 5s for the async processing.
	select {
	case msg := <-a.bus.Inbound:
		if msg.Channel != "cli" {
			t.Errorf("Channel = %q, want %q", msg.Channel, "cli")
		}
		if msg.ChatID != "restart-test-chat" {
			t.Errorf("ChatID = %q, want %q", msg.ChatID, "restart-test-chat")
		}
		if !containsPrefix(msg.Content, "⏰") {
			t.Errorf("Content should have ⏰ prefix for cron, got: %s", msg.Content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out — CronFired was not processed directly when no session exists " +
			"(bgNotifyLoop should drain immediately for sessions without chatWorker)")
	}

	// bgRunPending should be empty — drainAndProcessNotifications consumed it
	a.bgRunPendingMu.Lock()
	remaining := len(a.bgRunPending)
	a.bgRunPendingMu.Unlock()
	if remaining != 0 {
		t.Errorf("bgRunPending should be empty after direct processing, got %d items", remaining)
	}

	t.Logf("SUCCESS: CronFired processed directly without an active chatWorker")
}

// TestBgNotifyLoop_CronFired_NoSession_MultipleNotifications verifies that multiple
// cron triggers for a session without chatWorker are all processed, not just the first.
func TestBgNotifyLoop_CronFired_NoSession_MultipleNotifications(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := tools.NewBackgroundTaskManager()
	a := &Agent{
		bus:      bus.NewMessageBus(),
		agentCtx: ctx,
	}
	a.bgTaskMgr.Store(mgr)

	chatKey := "cli:multi-restart-chat"

	go a.bgNotifyLoop()
	defer cancel()

	// Send 3 cron notifications
	for i := 0; i < 3; i++ {
		mgr.SendCronFired(&tools.CronFired{
			Key:     chatKey,
			Sid:     "user-1",
			Message: fmt.Sprintf("task %d", i),
		})
	}

	// All 3 should be processed and appear in bus.Inbound
	timeout := time.After(5 * time.Second)
	count := 0
	for count < 3 {
		select {
		case <-a.bus.Inbound:
			count++
		case <-timeout:
			t.Fatalf("expected 3 messages in bus.Inbound, got %d", count)
		}
	}

	// bgRunPending should be empty
	a.bgRunPendingMu.Lock()
	remaining := len(a.bgRunPending)
	a.bgRunPendingMu.Unlock()
	if remaining != 0 {
		t.Errorf("bgRunPending should be empty, got %d items", remaining)
	}

	t.Logf("SUCCESS: all 3 CronFired notifications processed without chatWorker")
}

// containsPrefix checks if s starts with the given prefix string.
func containsPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// TestDrainAndProcessNotifications_LabelsSourceAndRange —— 用户要求（2026-09-18）：
// 「N 合 1 必须明显标识每条消息的范围和来源」。
// 契约：① 消息以 `🔔 合并通知 ×N` 总览开头（N>1）；② 每节以 `【i/N】` 标出范围；
// ③ 节头写明来源（后台任务 + task id / 子代理 role/instance / 定时任务 + 摘要）；
// ④ 节间仍用既有分隔符 "\n\n---\n\n"。
func TestDrainAndProcessNotifications_LabelsSourceAndRange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := tools.NewBackgroundTaskManager()
	a := &Agent{bus: bus.NewMessageBus(), agentCtx: ctx}
	a.bgTaskMgr.Store(mgr)

	chatKey := "cli:label-chat"
	// ① 后台任务（完成通知带真实 ID 与命令）
	_ = mgr.Start(chatKey, "user-1", "cargo check", func(ctx context.Context, outputBuf func(string)) (int, error) {
		outputBuf("ok")
		return 0, nil
	})
	var bgNotif tools.BgNotification
	select {
	case bgNotif = <-mgr.NotifyCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for bg task notification")
	}
	// ② 子代理完成 ③ 定时任务
	subNotif := &tools.SubAgentBgNotify{
		Key: chatKey, Type: tools.SubAgentBgNotifyCompleted,
		Role: "explore", Instance: "mem-1", Content: "SUB_DONE", Sid: "user-1",
	}
	cronNotif := &tools.CronFired{Key: chatKey, Sid: "user-1", Message: "check health"}
	a.enqueueBgNotifications([]tools.BgNotification{bgNotif, subNotif, cronNotif})

	a.drainAndProcessNotifications(chatKey)

	var msg bus.InboundMessage
	select {
	case msg = <-a.bus.Inbound:
	case <-time.After(2 * time.Second):
		t.Fatal("expected the batched notification message in bus.Inbound")
	}
	// 恰好一条（N 合 1，绝不逐条）。
	select {
	case extra := <-a.bus.Inbound:
		t.Fatalf("expected ONE batched message, got a second: %q", extra.Content)
	default:
	}

	c := msg.Content
	for _, want := range []string{
		"🔔 合并通知 ×3",               // 总览（范围）
		"【1/3】", "【2/3】", "【3/3】", // 每节的序号（范围）
		"后台任务 ",             // 来源：后台任务（带 task id）
		"子代理 explore/mem-1", // 来源：子代理 role/instance
		"定时任务",              // 来源：定时任务
		"\n\n---\n\n",       // 既有分隔符（契约不变）
		"SUB_DONE",          // 正文仍完整
	} {
		if !strings.Contains(c, want) {
			t.Errorf("batched message must contain %q;\n--- got ---\n%s", want, c)
		}
	}
}
