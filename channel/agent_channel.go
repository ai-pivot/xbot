package channel

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"xbot/bus"
	"xbot/clipanic"
)

// AgentChannel wraps a SubAgent as a Channel in Dispatcher.
// Enables unified routing: SendMessage(to="agent:reviewer/r1") → Dispatcher → AgentChannel.
//
// RPC mechanism: Each Send() creates a per-request reply channel.
// The processing goroutine writes the result to that specific channel.
// This prevents reply mixing under concurrent Send() calls.
//
// Concurrency: The processing loop dispatches each request to its own
// goroutine so that multiple RPCs can execute concurrently. This prevents
// circular-wait deadlocks when two agents SendMessage to each other
// simultaneously.
type AgentChannel struct {
	name  string
	runFn bus.RunFn
	ctx   context.Context // lifecycle context, cancelled by Stop() or parent cancellation
	// cancel cancels the lifecycle context. It is nil until Start() is called.
	cancel context.CancelFunc
	inbox  chan *rpcRequest
	closed atomic.Bool
	mu     sync.Mutex // guards closed check + inbox send
	wg     sync.WaitGroup
}

type rpcRequest struct {
	task    string
	replyCh chan<- string
}

// NewAgentChannel creates a new AgentChannel for a SubAgent.
func NewAgentChannel(name string, runFn bus.RunFn) *AgentChannel {
	return &AgentChannel{
		name:  name,
		runFn: runFn,
		// Buffer 32: increased from 16 because concurrent request dispatch (ac.wg.Go per
		// request) means multiple requests may arrive in quick succession before the
		// processing goroutines pick them up.
		inbox: make(chan *rpcRequest, 32),
	}
}

func (ac *AgentChannel) Name() string { return ac.name }

// Start launches the SubAgent processing loop.
func (ac *AgentChannel) Start() error {
	ctx, cancel := context.WithCancel(context.Background())
	ac.ctx = ctx
	ac.cancel = cancel

	ac.startLoop()
	return nil
}

// StartWithContext is like Start but binds the channel's lifecycle to parentCtx.
// When parentCtx is cancelled (e.g. agent shutdown, Ctrl+C), the channel stops
// and all pending Send() calls return errors.
func (ac *AgentChannel) StartWithContext(parentCtx context.Context) error {
	ctx, cancel := context.WithCancel(parentCtx)
	ac.ctx = ctx
	ac.cancel = cancel

	// Note: Stop() calls ac.cancel() which triggers ctx.Done(),
	// so this goroutine always exits via the ctx.Done() branch.
	go func() {
		select {
		case <-parentCtx.Done():
			ac.Stop()
		case <-ctx.Done():
			// Already stopped via Stop().
		}
	}()

	ac.startLoop()
	return nil
}

// startLoop runs the shared inbox dispatch goroutine.
// Each request is dispatched to its own goroutine for concurrent execution,
// preventing circular-wait deadlocks.
func (ac *AgentChannel) startLoop() {
	ac.wg.Go(func() {
		for {
			select {
			case <-ac.ctx.Done():
				return
			case req := <-ac.inbox:
				// Dispatch each request to its own goroutine so that
				// concurrent RPCs don't block each other. Without this,
				// two agents sending messages to each other create a
				// circular-wait deadlock because the processing loop
				// is single-threaded and can only handle one RunFn at a time.
				ac.wg.Go(func() {
					ac.processRequest(req)
				})
			}
		}
	})
}

// processRequest runs a single RPC request to completion.
func (ac *AgentChannel) processRequest(req *rpcRequest) {
	defer func() {
		if r := recover(); r != nil {
			clipanic.Report("channel.AgentChannel.runFn", ac.name, r)
			errMsg := fmt.Sprintf("panic: %v", r)
			select {
			case req.replyCh <- errMsg:
			case <-ac.ctx.Done():
			}
		}
	}()
	result, err := ac.runFn(ac.ctx, req.task)
	if err != nil {
		result = "Error: " + err.Error()
	}
	select {
	case req.replyCh <- result:
	case <-ac.ctx.Done():
	}
}

// Stop cancels the SubAgent and waits for it to finish.
// Does NOT close inbox — avoids send-on-closed-channel panic in Send().
// The processing loop exits via ctx.Done(); inbox is GC'd with AgentChannel.
func (ac *AgentChannel) Stop() {
	ac.mu.Lock()
	if ac.closed.Swap(true) {
		ac.mu.Unlock()
		return
	}
	ac.mu.Unlock()

	// Cancel context first so processing loop exits via ctx.Done().
	// Send() slow-path also unblocks via ctx.Done().
	if ac.cancel != nil {
		ac.cancel()
	}
	ac.wg.Wait()
}

// Send delivers a message to the SubAgent and waits for the reply (RPC).
// If msg.Ctx is set, the wait respects caller cancellation (e.g. Ctrl+C).
// Otherwise, only the channel's lifecycle context is used.
func (ac *AgentChannel) Send(msg OutboundMsg) (string, error) {
	replyCh := make(chan string, 1)
	req := &rpcRequest{task: msg.Content, replyCh: replyCh}

	ac.mu.Lock()
	if ac.closed.Load() {
		ac.mu.Unlock()
		return "", fmt.Errorf("agent channel %s is closed", ac.name)
	}
	// Fast path: try non-blocking send while holding lock (prevents send-on-closed-channel).
	// inbox buffer=32 makes this succeed in almost all cases.
	select {
	case ac.inbox <- req:
		ac.mu.Unlock()
	default:
		ac.mu.Unlock()
		// Slow path: inbox full, wait with context cancellation guard.
		// Stop() may cancel ctx while we wait — ac.ctx.Done() prevents hang.
		select {
		case ac.inbox <- req:
		case <-ac.ctx.Done():
			return "", fmt.Errorf("agent channel %s is stopped", ac.name)
		}
	}

	// Wait for reply, respecting both channel lifecycle and caller context.
	// msg.Ctx is set by the caller (e.g. SendMessage tool) to propagate
	// cancellation signals (Ctrl+C, tool timeout).
	callerCtx := msg.Ctx
	if callerCtx == nil {
		callerCtx = context.Background() // no caller cancellation
	}

	select {
	case reply := <-replyCh:
		return reply, nil
	case <-ac.ctx.Done():
		return "", fmt.Errorf("agent channel %s stopped while waiting for reply", ac.name)
	case <-callerCtx.Done():
		return "", fmt.Errorf("caller cancelled while waiting for reply from %s: %w", ac.name, callerCtx.Err())
	}
}

// IsClosed reports whether the channel is closed.
func (ac *AgentChannel) IsClosed() bool { return ac.closed.Load() }
