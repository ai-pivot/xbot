package hooks

import (
	"context"
	"testing"
	"time"
)

// m7: async hook handlers must run under a timeout cap. The old
// context.WithoutCancel(ctx) removed EVERY deadline — a hook definition with
// Timeout: 3600 (or a stuck command) hung the goroutine forever with no
// cancellation. The async context must carry a deadline bounded by
// maxAsyncHookTimeout, regardless of the def's own Timeout value.
func TestAsyncHookHasTimeoutCap(t *testing.T) {
	m := &Manager{executors: map[string]Executor{}}

	captured := make(chan context.Context, 1)
	fake := &capturingExecutor{ctxCh: captured}
	m.executors["test"] = fake

	h := hookEntry{def: &HookDef{
		Type:    "test",
		Async:   true,
		Command: "sleep 3600", // def timeout far above the cap
		Timeout: 3600,
	}}

	m.executeHandler(context.Background(), h, &testEvent{}, true)

	select {
	case asyncCtx := <-captured:
		deadline, ok := asyncCtx.Deadline()
		if !ok {
			t.Fatal("async hook context has NO deadline — a stuck handler goroutine runs forever (WithoutCancel removed every bound)")
		}
		if remaining := time.Until(deadline); remaining <= 0 || remaining > maxAsyncHookTimeout {
			t.Fatalf("async hook deadline = %v remaining, want bounded by maxAsyncHookTimeout (%v)", remaining, maxAsyncHookTimeout)
		}
		if d := time.Until(deadline); d > maxAsyncHookTimeout+2*time.Second {
			t.Fatalf("async hook deadline %v exceeds the cap %v", d, maxAsyncHookTimeout)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("async hook executor was never invoked")
	}
}

// capturingExecutor records the context its Execute was called with.
type capturingExecutor struct {
	ctxCh chan context.Context
}

func (e *capturingExecutor) Type() string { return "test" }

func (e *capturingExecutor) Execute(ctx context.Context, def *HookDef, event Event) (*Result, error) {
	e.ctxCh <- ctx
	return &Result{}, nil
}
