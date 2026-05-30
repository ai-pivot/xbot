package plugin

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestPluginEventNotifier_SubscribeAndNotify(t *testing.T) {
	n := NewPluginEventNotifier()

	var received PluginEvent
	err := n.Subscribe(func(e PluginEvent) {
		received = e
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	ts := time.Now()
	n.Notify(PluginEvent{
		Type:      PluginEventActivated,
		PluginID:  "com.test.plugin",
		Timestamp: ts,
	})

	if received.Type != PluginEventActivated {
		t.Errorf("Type = %q, want %q", received.Type, PluginEventActivated)
	}
	if received.PluginID != "com.test.plugin" {
		t.Errorf("PluginID = %q, want %q", received.PluginID, "com.test.plugin")
	}
	if !received.Timestamp.Equal(ts) {
		t.Errorf("Timestamp = %v, want %v", received.Timestamp, ts)
	}
}

func TestPluginEventNotifier_Unsubscribe(t *testing.T) {
	n := NewPluginEventNotifier()

	callCount := 0
	cb := func(e PluginEvent) {
		callCount++
	}

	if err := n.Subscribe(cb); err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	n.Notify(PluginEvent{Type: PluginEventActivated, PluginID: "p1"})
	if callCount != 1 {
		t.Fatalf("expected 1 call before unsubscribe, got %d", callCount)
	}

	if err := n.Unsubscribe(cb); err != nil {
		t.Fatalf("Unsubscribe failed: %v", err)
	}

	n.Notify(PluginEvent{Type: PluginEventDeactivated, PluginID: "p1"})
	if callCount != 1 {
		t.Errorf("expected 1 call after unsubscribe, got %d", callCount)
	}
}

func TestPluginEventNotifier_MultipleCallbacks(t *testing.T) {
	n := NewPluginEventNotifier()

	var count1, count2 int32
	cb1 := func(e PluginEvent) { atomic.AddInt32(&count1, 1) }
	cb2 := func(e PluginEvent) { atomic.AddInt32(&count2, 1) }

	n.Subscribe(cb1)
	n.Subscribe(cb2)

	n.Notify(PluginEvent{Type: PluginEventActivated, PluginID: "p1"})
	n.Notify(PluginEvent{Type: PluginEventDeactivated, PluginID: "p1"})

	if atomic.LoadInt32(&count1) != 2 {
		t.Errorf("cb1 called %d times, want 2", count1)
	}
	if atomic.LoadInt32(&count2) != 2 {
		t.Errorf("cb2 called %d times, want 2", count2)
	}
}

func TestPluginEventNotifier_NotifyAfterUnsubscribe(t *testing.T) {
	n := NewPluginEventNotifier()

	callCount := 0
	cb := func(e PluginEvent) {
		callCount++
	}

	n.Subscribe(cb)
	n.Unsubscribe(cb)

	// Subscribe a different callback that should still work
	var received PluginEvent
	cb2 := func(e PluginEvent) { received = e }
	n.Subscribe(cb2)

	n.Notify(PluginEvent{Type: PluginEventInstalled, PluginID: "p2"})

	if callCount != 0 {
		t.Errorf("unsubscribed callback called %d times, want 0", callCount)
	}
	if received.Type != PluginEventInstalled {
		t.Errorf("remaining callback: Type = %q, want %q", received.Type, PluginEventInstalled)
	}
}

func TestPluginEventNotifier_SubscribeNil(t *testing.T) {
	n := NewPluginEventNotifier()
	err := n.Subscribe(nil)
	if err == nil {
		t.Fatal("expected error for nil callback")
		return
	}
}

func TestPluginEventNotifier_UnsubscribeNil(t *testing.T) {
	n := NewPluginEventNotifier()
	err := n.Unsubscribe(nil)
	if err == nil {
		t.Fatal("expected error for nil callback")
		return
	}
}

func TestPluginEventNotifier_UnsubscribeNotFound(t *testing.T) {
	n := NewPluginEventNotifier()
	cb := func(e PluginEvent) {}
	err := n.Unsubscribe(cb)
	if err == nil {
		t.Fatal("expected error for non-registered callback")
		return
	}
}

func TestPluginEventNotifier_PanicRecovery(t *testing.T) {
	n := NewPluginEventNotifier()
	panicCb := func(e PluginEvent) {
		panic("test panic")
	}
	normalCalled := false
	normalCb := func(e PluginEvent) {
		normalCalled = true
	}
	n.Subscribe(panicCb)
	n.Subscribe(normalCb)

	// Should not panic — panic in first callback should not affect second
	n.Notify(PluginEvent{Type: PluginEventActivated, PluginID: "test"})

	if !normalCalled {
		t.Error("normal callback should still be called after panic in previous callback")
	}
}
