package agent

import (
	"context"
	"sync/atomic"
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

// TestDrainSessionBgNotifications_Synchronous verifies that
// drainSessionBgNotifications processes notifications synchronously and
// only drains notifications matching the given session key.
func TestDrainSessionBgNotifications_Synchronous(t *testing.T) {
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

	a.drainSessionBgNotifications("cli:test-chat")

	select {
	case msg := <-a.bus.Inbound:
		if msg.ChatID != "test-chat" {
			t.Errorf("ChatID = %q, want %q", msg.ChatID, "test-chat")
		}
		if msg.Channel != "cli" {
			t.Errorf("Channel = %q, want %q", msg.Channel, "cli")
		}
	default:
		t.Fatal("drainSessionBgNotifications should have synchronously injected notification into bus.Inbound")
	}

	a.bgRunPendingMu.Lock()
	remaining := a.bgRunPending
	a.bgRunPendingMu.Unlock()
	if len(remaining) != 0 {
		t.Errorf("bgRunPending should be empty after draining matching session, got %d items", len(remaining))
	}
}

// TestDrainSessionBgNotifications_CrossSessionIsolation verifies that
// drainSessionBgNotifications only drains notifications matching the
// given session key, leaving other sessions' notifications in bgRunPending.
func TestDrainSessionBgNotifications_CrossSessionIsolation(t *testing.T) {
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
	a.drainSessionBgNotifications("cli:chat-a")

	// Should find chat-a's notification in bus.Inbound
	found := false
	timeout := time.After(2 * time.Second)
	for !found {
		select {
		case msg := <-a.bus.Inbound:
			if msg.ChatID == "chat-a" {
				found = true
			}
			// Ignore other messages
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

// TestBgRunActive_ReferenceCount verifies that bgRunActive works as a
// reference counter: multiple concurrent sessions can increment/decrement
// without clearing each other's active state.
func TestBgRunActive_ReferenceCount(t *testing.T) {
	a := &Agent{}

	// Initial state: 0
	if v := atomic.LoadInt32(&a.bgRunActive); v != 0 {
		t.Fatalf("initial bgRunActive = %d, want 0", v)
	}

	// Session A starts Run
	atomic.AddInt32(&a.bgRunActive, 1)
	if v := atomic.LoadInt32(&a.bgRunActive); v != 1 {
		t.Fatalf("after session A start: bgRunActive = %d, want 1", v)
	}

	// Session B starts Run (concurrent)
	atomic.AddInt32(&a.bgRunActive, 1)
	if v := atomic.LoadInt32(&a.bgRunActive); v != 2 {
		t.Fatalf("after session B start: bgRunActive = %d, want 2", v)
	}

	// Session A's Run finishes — should NOT clear to 0
	atomic.AddInt32(&a.bgRunActive, -1)
	if v := atomic.LoadInt32(&a.bgRunActive); v != 1 {
		t.Fatalf("after session A end: bgRunActive = %d, want 1 (session B still active)", v)
	}

	// Session B's Run finishes
	atomic.AddInt32(&a.bgRunActive, -1)
	if v := atomic.LoadInt32(&a.bgRunActive); v != 0 {
		t.Fatalf("after session B end: bgRunActive = %d, want 0", v)
	}
}

// TestBgRunActive_RouteAfterPartialExit simulates the exact race condition:
// Session A exits while Session B is still active. Notifications for Session B
// should still be buffered (not routed through idle path).
func TestBgRunActive_RouteAfterPartialExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := tools.NewBackgroundTaskManager()
	a := &Agent{
		bus:       bus.NewMessageBus(),
		agentCtx:  ctx,
		bgTaskMgr: mgr,
	}

	// Both sessions active
	atomic.AddInt32(&a.bgRunActive, 1) // Session A
	atomic.AddInt32(&a.bgRunActive, 1) // Session B

	// Session A exits — decrement only
	atomic.AddInt32(&a.bgRunActive, -1)

	// Now bgRunActive should be 1 (Session B still active)
	if v := atomic.LoadInt32(&a.bgRunActive); v != 1 {
		t.Fatalf("bgRunActive = %d after session A exit, want 1", v)
	}

	// Start a task for Session B — notification should still be buffered
	// because bgRunActive > 0
	_ = mgr.Start("cli:chat-b", "user-1", "task-b", func(ctx context.Context, outputBuf func(string)) (int, error) {
		outputBuf("b-done")
		return 0, nil
	})

	// Read notification directly from NotifyCh (simulating what bgNotifyLoop would do)
	select {
	case notif := <-mgr.NotifyCh:
		// bgNotifyLoop would check bgRunActive > 0 → buffer
		// We simulate that here:
		if v := atomic.LoadInt32(&a.bgRunActive); v > 0 {
			a.bgRunPendingMu.Lock()
			a.bgRunPending = append(a.bgRunPending, notif)
			a.bgRunPendingMu.Unlock()
		} else {
			t.Fatal("bgRunActive should be > 0 but is 0 — notification would be misrouted to idle path!")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for notification")
	}

	// Verify the notification is buffered and can be drained by Session B
	a.drainSessionBgNotifications("cli:chat-b")

	select {
	case msg := <-a.bus.Inbound:
		if msg.ChatID != "chat-b" {
			t.Errorf("ChatID = %q, want %q", msg.ChatID, "chat-b")
		}
	default:
		t.Fatal("Session B's notification should have been drained synchronously")
	}

	// Cleanup
	atomic.AddInt32(&a.bgRunActive, -1)
}
