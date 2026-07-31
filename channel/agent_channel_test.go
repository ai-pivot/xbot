package channel

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAgentChannelRPC(t *testing.T) {
	var calls atomic.Int32
	runFn := func(ctx context.Context, task string) (string, error) {
		calls.Add(1)
		return "response to: " + task, nil
	}

	ac := NewAgentChannel("agent:test/inst1", runFn)
	if err := ac.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer ac.Stop()

	msg := OutboundMsg{Content: "hello"}
	result, err := ac.Send(msg)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if result != "response to: hello" {
		t.Errorf("expected 'response to: hello', got %q", result)
	}
	if calls.Load() != 1 {
		t.Errorf("expected 1 call, got %d", calls.Load())
	}
}

func TestAgentChannelMultipleRPC(t *testing.T) {
	runFn := func(ctx context.Context, task string) (string, error) {
		time.Sleep(10 * time.Millisecond) // simulate work
		return "done: " + task, nil
	}

	ac := NewAgentChannel("agent:test/multi", runFn)
	if err := ac.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer ac.Stop()

	// Sequential sends — each should get its own reply
	for i := 0; i < 5; i++ {
		msg := OutboundMsg{Content: "task" + time.Now().String()}
		result, err := ac.Send(msg)
		if err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
		if result[:5] != "done:" {
			t.Errorf("Send %d: unexpected result %q", i, result)
		}
	}
}

func TestAgentChannelClosed(t *testing.T) {
	runFn := func(ctx context.Context, task string) (string, error) {
		return "ok", nil
	}

	ac := NewAgentChannel("agent:test/closed", runFn)
	if err := ac.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ac.Stop()

	// Send after close should fail
	_, err := ac.Send(OutboundMsg{Content: "hello"})
	if err == nil {
		t.Error("expected error after Stop")
	}
}

func TestAgentChannelDoubleStop(t *testing.T) {
	runFn := func(ctx context.Context, task string) (string, error) {
		return "ok", nil
	}

	ac := NewAgentChannel("agent:test/double", runFn)
	if err := ac.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Double stop should not panic
	ac.Stop()
	ac.Stop()
}

func TestAgentChannelRunFnError(t *testing.T) {
	runFn := func(ctx context.Context, task string) (string, error) {
		return "Error: something went wrong", nil
	}

	ac := NewAgentChannel("agent:test/err", runFn)
	if err := ac.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer ac.Stop()

	result, err := ac.Send(OutboundMsg{Content: "fail"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if result != "Error: something went wrong" {
		t.Errorf("expected error message in result, got %q", result)
	}
}

func TestAgentChannelContextCancellation(t *testing.T) {
	started := make(chan struct{})
	runFn := func(ctx context.Context, task string) (string, error) {
		close(started)
		<-ctx.Done()
		return "cancelled", nil
	}

	ac := NewAgentChannel("agent:test/cancel", runFn)
	if err := ac.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer ac.Stop()

	// Start a Send that will block on context cancellation
	done := make(chan struct{})
	go func() {
		defer close(done)
		ac.Send(OutboundMsg{Content: "block"})
	}()

	// Wait for runFn to start
	select {
	case <-started:
		// good
	case <-time.After(time.Second):
		t.Fatal("runFn never started")
	}

	// Stop should cancel the context
	go func() {
		time.Sleep(50 * time.Millisecond)
		ac.Stop()
	}()

	// The blocked Send should eventually return
	select {
	case <-done:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("Send never returned after Stop")
	}
}

func TestAgentChannelInDispatcher(t *testing.T) {
	runFn := func(ctx context.Context, task string) (string, error) {
		return "dispatched: " + task, nil
	}

	// Create a minimal Dispatcher with nil MessageBus (sufficient for SendDirect)
	disp := NewDispatcher(nil)

	ac := NewAgentChannel("agent:test/dispatch", runFn)
	if err := ac.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	disp.Register(ac)

	// Send via Dispatcher
	msg := OutboundMsg{
		Channel: "agent:test/dispatch",
		Content: "hello dispatcher",
	}
	result, err := disp.SendDirect(msg)
	if err != nil {
		t.Fatalf("SendDirect: %v", err)
	}
	if result != "dispatched: hello dispatcher" {
		t.Errorf("expected 'dispatched: hello dispatcher', got %q", result)
	}

	// Cleanup
	disp.Unregister("agent:test/dispatch")
	ac.Stop()
}

func TestAgentChannelConcurrentSends(t *testing.T) {
	var completed atomic.Int32
	runFn := func(ctx context.Context, task string) (string, error) {
		time.Sleep(20 * time.Millisecond)
		return "ok: " + task, nil
	}

	ac := NewAgentChannel("agent:test/concurrent", runFn)
	if err := ac.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer ac.Stop()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			result, err := ac.Send(OutboundMsg{Content: "task"})
			if err != nil {
				t.Errorf("Send %d: %v", idx, err)
				return
			}
			if result[:3] != "ok:" {
				t.Errorf("Send %d: unexpected result %q", idx, result)
			}
			completed.Add(1)
		}(i)
	}
	wg.Wait()

	if completed.Load() != 10 {
		t.Errorf("expected 10 completions, got %d", completed.Load())
	}
}

func TestAgentChannelPanicRecovery(t *testing.T) {
	// Verify that a panic in runFn does NOT kill the processing goroutine.
	// The next request should succeed normally.
	callCount := int32(0)
	runFn := func(ctx context.Context, task string) (string, error) {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			panic("intentional test panic")
		}
		return "ok: " + task, nil
	}

	ch := NewAgentChannel("test-panic", runFn)
	if err := ch.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer ch.Stop()

	// First call: should trigger panic, recover, and return error message.
	result1, err := ch.Send(OutboundMsg{Content: "req1"})
	if err != nil {
		t.Fatalf("Send should not return error even after panic: %v", err)
	}
	if !strings.Contains(result1, "panic:") {
		t.Errorf("expected panic error message, got: %s", result1)
	}

	// Second call: should succeed normally, proving the goroutine survived.
	result2, err := ch.Send(OutboundMsg{Content: "req2"})
	if err != nil {
		t.Fatalf("second Send failed: %v", err)
	}
	if result2 != "ok: req2" {
		t.Errorf("expected 'ok: req2', got: %s", result2)
	}
}

// TestAgentChannelCircularWaitNoDeadlock verifies that two AgentChannels
// sending messages to each other do NOT deadlock. Before the fix (serial
// processing loop), this would hang forever because each channel's processing
// goroutine was blocked waiting for the other to reply.
func TestAgentChannelCircularWaitNoDeadlock(t *testing.T) {
	var chA, chB *AgentChannel

	// Channel A: when it receives a message, sends back to B
	runFnA := func(ctx context.Context, task string) (string, error) {
		if task == "ping" {
			// A sends "pong" to B — this would deadlock with serial processing
			result, err := chB.Send(OutboundMsg{Content: "from_A"})
			if err != nil {
				return "A error: " + err.Error(), nil
			}
			return "A got: " + result, nil
		}
		return "A ack: " + task, nil
	}

	// Channel B: just responds
	runFnB := func(ctx context.Context, task string) (string, error) {
		return "B ack: " + task, nil
	}

	chA = NewAgentChannel("agent:test/a", runFnA)
	chB = NewAgentChannel("agent:test/b", runFnB)

	if err := chA.Start(); err != nil {
		t.Fatalf("Start A: %v", err)
	}
	defer chA.Stop()
	if err := chB.Start(); err != nil {
		t.Fatalf("Start B: %v", err)
	}
	defer chB.Stop()

	// This would deadlock with serial processing — the test itself would time out.
	done := make(chan struct{})
	go func() {
		defer close(done)
		result, err := chA.Send(OutboundMsg{Content: "ping"})
		if err != nil {
			t.Errorf("Send to A: %v", err)
			return
		}
		if !strings.Contains(result, "A got: B ack: from_A") {
			t.Errorf("unexpected result: %s", result)
		}
	}()

	select {
	case <-done:
		// Success — no deadlock
	case <-time.After(5 * time.Second):
		t.Fatal("DEADLOCK DETECTED: circular SendMessage hung — the fix is not working")
	}
}

// TestAgentChannelBidirectionalSend verifies that two channels can
// simultaneously send to each other without deadlocking.
func TestAgentChannelBidirectionalSend(t *testing.T) {
	var chA, chB *AgentChannel

	runFnA := func(ctx context.Context, task string) (string, error) {
		// When A receives a message, it also sends to B
		result, err := chB.Send(OutboundMsg{Content: "from_A_" + task})
		if err != nil {
			return "", err
		}
		return "A:" + result, nil
	}

	runFnB := func(ctx context.Context, task string) (string, error) {
		return "B:" + task, nil
	}

	chA = NewAgentChannel("agent:test/bidi-a", runFnA)
	chB = NewAgentChannel("agent:test/bidi-b", runFnB)

	if err := chA.Start(); err != nil {
		t.Fatalf("Start A: %v", err)
	}
	defer chA.Stop()
	if err := chB.Start(); err != nil {
		t.Fatalf("Start B: %v", err)
	}
	defer chB.Stop()

	// Send to A — A will in turn send to B
	done := make(chan string, 1)
	go func() {
		result, err := chA.Send(OutboundMsg{Content: "hello"})
		if err != nil {
			done <- "error: " + err.Error()
			return
		}
		done <- result
	}()

	select {
	case result := <-done:
		if !strings.Contains(result, "A:B:from_A_hello") {
			t.Errorf("unexpected result: %s", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Bidirectional send timed out — possible deadlock")
	}
}

// TestAgentChannelCallerCtxCancellation verifies that Send() respects the
// caller's context cancellation (e.g. Ctrl+C propagation).
func TestAgentChannelCallerCtxCancellation(t *testing.T) {
	started := make(chan struct{})
	runFn := func(ctx context.Context, task string) (string, error) {
		close(started)
		// Simulate long-running work
		<-ctx.Done()
		return "cancelled", nil
	}

	ac := NewAgentChannel("agent:test/caller-cancel", runFn)
	if err := ac.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer ac.Stop()

	// Create a caller context that we can cancel
	callerCtx, callerCancel := context.WithCancel(context.Background())
	defer callerCancel()

	// Start a Send with caller context
	done := make(chan error, 1)
	go func() {
		_, err := ac.Send(OutboundMsg{
			Content: "long-task",
			Ctx:     callerCtx,
		})
		done <- err
	}()

	// Wait for runFn to start
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("runFn never started")
	}

	// Cancel the caller context (simulating Ctrl+C)
	callerCancel()

	// The Send should return with a cancellation error
	select {
	case err := <-done:
		if err == nil {
			t.Error("expected error from cancelled Send")
		}
		if !strings.Contains(err.Error(), "caller cancelled") {
			t.Errorf("expected caller cancellation error, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not return after caller context cancellation — Ctrl+C would not work")
	}
}

// TestAgentChannelStartWithContext verifies that the channel stops when
// the parent context is cancelled.
func TestAgentChannelStartWithContext(t *testing.T) {
	parentCtx, parentCancel := context.WithCancel(context.Background())

	var calls atomic.Int32
	runFn := func(ctx context.Context, task string) (string, error) {
		calls.Add(1)
		return "ok", nil
	}

	ac := NewAgentChannel("agent:test/parent-ctx", runFn)
	if err := ac.StartWithContext(parentCtx); err != nil {
		t.Fatalf("StartWithContext: %v", err)
	}

	// Should work normally
	result, err := ac.Send(OutboundMsg{Content: "hello"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if result != "ok" {
		t.Errorf("expected 'ok', got %q", result)
	}

	// Cancel parent — should stop the channel
	parentCancel()

	// Give it a moment to propagate
	time.Sleep(100 * time.Millisecond)

	// Send should now fail
	_, err = ac.Send(OutboundMsg{Content: "after-cancel"})
	if err == nil {
		t.Error("expected error after parent context cancellation")
	}
}

// TestAgentChannelDispatcherCtxPropagation verifies that SendMessageCtx
// propagates context through the Dispatcher to the AgentChannel.
func TestAgentChannelDispatcherCtxPropagation(t *testing.T) {
	started := make(chan struct{})
	runFn := func(ctx context.Context, task string) (string, error) {
		close(started)
		<-ctx.Done()
		return "cancelled", nil
	}

	disp := NewDispatcher(nil)
	ac := NewAgentChannel("agent:test/ctx-prop", runFn)
	if err := ac.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	disp.Register(ac)
	defer disp.Unregister("agent:test/ctx-prop")

	// Create caller context
	callerCtx, callerCancel := context.WithCancel(context.Background())
	defer callerCancel()

	// Start async send via Dispatcher
	done := make(chan error, 1)
	go func() {
		_, err := disp.SendMessageCtx(callerCtx, "agent:test/ctx-prop", "", "test")
		done <- err
	}()

	// Wait for runFn to start
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("runFn never started")
	}

	// Cancel caller context
	callerCancel()

	// Should return error
	select {
	case err := <-done:
		if err == nil {
			t.Error("expected error from cancelled SendMessageCtx")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SendMessageCtx did not return after context cancellation")
	}
}
