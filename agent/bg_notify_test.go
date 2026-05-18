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
	// injectInbound must NOT set IsCron=true, otherwise processMessage
	// routes through processCronMessage which skips persistence.

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

// TestDrainRemainingBgNotifications_Synchronous verifies that
// drainRemainingBgNotifications processes notifications synchronously
// (not via goroutines). This is critical for preventing the race condition
// where notifications arrive after Run() exits but before bgRunActive=0,
// and need to be injected into bus.Inbound before processMessage returns.
func TestDrainRemainingBgNotifications_Synchronous(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := tools.NewBackgroundTaskManager()
	a := &Agent{
		bus:       bus.NewMessageBus(),
		agentCtx:  ctx,
		bgTaskMgr: mgr,
	}

	// Use Start() to create a task with proper sessionKey.
	// execFn completes immediately so the notification is sent to NotifyCh.
	_ = mgr.Start("cli:test-chat", "user-1", "echo hello", func(ctx context.Context, outputBuf func(string)) (int, error) {
		outputBuf("hello output")
		return 0, nil
	})

	// Wait for the notification from NotifyCh — this is the synchronization
	// point that guarantees all task fields are fully written (no data race).
	var notif tools.BgNotification
	select {
	case notif = <-mgr.NotifyCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for notification from NotifyCh")
	}

	// Buffer the notification (as bgNotifyLoop would do)
	a.bgRunPendingMu.Lock()
	a.bgRunPending = append(a.bgRunPending, notif)
	a.bgRunPendingMu.Unlock()

	// Drain should process synchronously — by the time drain returns,
	// the notification should already be in bus.Inbound
	a.drainRemainingBgNotifications()

	// Verify the notification was processed and injected into bus.Inbound
	select {
	case msg := <-a.bus.Inbound:
		if msg.ChatID != "test-chat" {
			t.Errorf("ChatID = %q, want %q", msg.ChatID, "test-chat")
		}
		if msg.Channel != "cli" {
			t.Errorf("Channel = %q, want %q", msg.Channel, "cli")
		}
		if msg.IsCron {
			t.Error("bg notification should not be cron")
		}
	default:
		t.Fatal("drainRemainingBgNotifications should have synchronously injected the notification into bus.Inbound, but no message was found")
	}

	// Verify bgRunPending is now empty
	a.bgRunPendingMu.Lock()
	remaining := a.bgRunPending
	a.bgRunPendingMu.Unlock()
	if len(remaining) != 0 {
		t.Errorf("bgRunPending should be empty after drain, got %d items", len(remaining))
	}
}

// TestDrainBeforeBgRunActiveClears verifies that draining notifications
// before clearing bgRunActive prevents the race condition where
// bgNotifyLoop routes a notification through the idle path because
// bgRunActive was already set to 0.
func TestDrainBeforeBgRunActiveClears(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := tools.NewBackgroundTaskManager()
	a := &Agent{
		bus:       bus.NewMessageBus(),
		agentCtx:  ctx,
		bgTaskMgr: mgr,
	}

	// Simulate: Run is active, notification is buffered
	atomic.StoreInt32(&a.bgRunActive, 1)

	_ = mgr.Start("cli:test-chat-2", "user-1", "long-task", func(ctx context.Context, outputBuf func(string)) (int, error) {
		outputBuf("task output")
		return 0, nil
	})

	// Wait for notification from NotifyCh (synchronization point — no race)
	var notif tools.BgNotification
	select {
	case notif = <-mgr.NotifyCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for notification from NotifyCh")
	}

	// Buffer the notification (as bgNotifyLoop would do while bgRunActive==1)
	a.bgRunPendingMu.Lock()
	a.bgRunPending = append(a.bgRunPending, notif)
	a.bgRunPendingMu.Unlock()

	// First drain (before bgRunActive=0) — should process synchronously
	a.drainRemainingBgNotifications()

	// The notification should already be in bus.Inbound
	select {
	case msg := <-a.bus.Inbound:
		if msg.ChatID != "test-chat-2" {
			t.Errorf("ChatID = %q, want %q", msg.ChatID, "test-chat-2")
		}
	default:
		t.Fatal("first drain should have synchronously injected notification before bgRunActive was cleared")
	}

	// Now clear bgRunActive (as processMessage does after drain)
	atomic.StoreInt32(&a.bgRunActive, 0)

	// Second drain should find nothing (already drained)
	a.drainRemainingBgNotifications()

	a.bgRunPendingMu.Lock()
	remaining := a.bgRunPending
	a.bgRunPendingMu.Unlock()
	if len(remaining) != 0 {
		t.Errorf("bgRunPending should be empty after second drain, got %d items", len(remaining))
	}
}
