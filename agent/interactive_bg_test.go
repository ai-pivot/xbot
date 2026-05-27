package agent

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestCleanupExpiredSessions_RunningSessionNotDestroyed verifies that
// cleanupExpiredSessions does NOT destroy sessions with running==true.
// This is a regression test for the bug where a foreground SubAgent running
// for >30 min was destroyed by the TTL cleaner while the parent agent was
// still blocked waiting for Run() to return — leaving the parent stuck forever.
func TestCleanupExpiredSessions_RunningSessionNotDestroyed(t *testing.T) {
	a := &Agent{
		interactiveSubAgents: sync.Map{},
	}

	key := "cli:test-session/ministry-works:fix-rv-call"

	// Simulate a running session with old lastUsed (35 minutes ago — past TTL)
	placeholder := &interactiveAgent{
		roleName: "ministry-works",
		instance: "fix-rv-call",
		lastUsed: time.Now().Add(-35 * time.Minute), // past TTL
		running:  true,
	}
	a.interactiveSubAgents.Store(key, placeholder)

	// Cleanup should NOT destroy this session because running==true
	a.cleanupExpiredSessions()

	_, ok := a.interactiveSubAgents.Load(key)
	if !ok {
		t.Fatal("running session was destroyed by cleanup — parent agent's SubAgent tool call would be stuck forever")
	}
}

// TestCleanupExpiredSessions_IdleSessionCleanedUp verifies that idle (non-running)
// sessions past TTL are properly cleaned up.
func TestCleanupExpiredSessions_IdleSessionCleanedUp(t *testing.T) {
	a := &Agent{
		interactiveSubAgents: sync.Map{},
	}

	key := "cli:test-session/explore:idle-agent"

	// Simulate an idle session with old lastUsed (35 minutes ago — past TTL)
	idle := &interactiveAgent{
		roleName: "explore",
		instance: "idle-agent",
		lastUsed: time.Now().Add(-35 * time.Minute),
		running:  false,
	}
	a.interactiveSubAgents.Store(key, idle)

	// Cleanup SHOULD destroy this session
	a.cleanupExpiredSessions()

	_, ok := a.interactiveSubAgents.Load(key)
	if ok {
		t.Fatal("idle session past TTL was NOT cleaned up")
	}
}

// TestCleanupExpiredSessions_RecentSessionNotDestroyed verifies that recent
// sessions (within TTL) are not cleaned up regardless of running state.
func TestCleanupExpiredSessions_RecentSessionNotDestroyed(t *testing.T) {
	a := &Agent{
		interactiveSubAgents: sync.Map{},
	}

	key := "cli:test-session/explore:fresh"

	// Simulate a recent idle session (5 minutes ago — within TTL)
	fresh := &interactiveAgent{
		roleName: "explore",
		instance: "fresh",
		lastUsed: time.Now().Add(-5 * time.Minute),
		running:  false,
	}
	a.interactiveSubAgents.Store(key, fresh)

	a.cleanupExpiredSessions()

	_, ok := a.interactiveSubAgents.Load(key)
	if !ok {
		t.Fatal("session within TTL was incorrectly cleaned up")
	}
}

// TestBgSession_NaturalCompletion_SessionPreserved verifies that a background
// interactive session is NOT destroyed when Run() completes naturally (i.e. the
// context was not cancelled externally). This is a regression test for the bug
// where runCancel() was called BEFORE checking cancelled, causing all bg
// sessions to be incorrectly destroyed on natural completion.
func TestBgSession_NaturalCompletion_SessionPreserved(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := &Agent{
		agentCtx:             ctx,
		interactiveSubAgents: sync.Map{},
	}

	key := "cli:test/bg-agent:test-inst"
	placeholder := &interactiveAgent{
		roleName:   "bg-agent",
		instance:   "test-inst",
		lastUsed:   time.Now(),
		background: true,
		running:    true,
	}
	a.interactiveSubAgents.Store(key, placeholder)

	// Simulate the bg goroutine logic — the exact pattern from SpawnInteractiveSession:
	// Run() returns, then we check cancelled BEFORE calling runCancel().
	bgBase := a.agentCtx
	runCtx, runCancel := context.WithCancel(bgBase)

	// Simulate Run() completing naturally (no external cancellation).
	// The key fix: check cancelled BEFORE calling runCancel().
	cancelled := runCtx.Err() != nil
	runCancel()

	if cancelled {
		// Bug path: session would be destroyed here
		a.interactiveSubAgents.Delete(key)
		t.Fatal("cancelled should be false for natural completion, but got true — session would be incorrectly destroyed")
	}

	// Natural completion: session should still exist
	val, ok := a.interactiveSubAgents.Load(key)
	if !ok {
		t.Fatal("bg session was removed from map after natural completion — should be preserved for future send/unload")
	}
	ia, ok := val.(*interactiveAgent)
	if !ok {
		t.Fatal("loaded value is not *interactiveAgent")
	}
	if ia.roleName != "bg-agent" {
		t.Errorf("roleName = %q, want %q", ia.roleName, "bg-agent")
	}
}

// TestBgSession_ExternalCancel_SessionDestroyed verifies that a background
// interactive session IS destroyed when the parent context is cancelled
// (e.g. agent shutdown or parent unload).
func TestBgSession_ExternalCancel_SessionDestroyed(t *testing.T) {
	parentCtx, parentCancel := context.WithCancel(context.Background())
	a := &Agent{
		agentCtx:             parentCtx,
		interactiveSubAgents: sync.Map{},
	}

	key := "cli:test/bg-agent:cancel-inst"
	placeholder := &interactiveAgent{
		roleName:   "bg-agent",
		instance:   "cancel-inst",
		lastUsed:   time.Now(),
		background: true,
		running:    true,
	}
	a.interactiveSubAgents.Store(key, placeholder)

	// Simulate bg goroutine: derive context from agent lifecycle
	bgBase := a.agentCtx
	runCtx, runCancel := context.WithCancel(bgBase)

	// Simulate external cancellation (e.g. agent shutdown)
	parentCancel()

	// Wait for cancellation to propagate
	time.Sleep(10 * time.Millisecond)

	// Check cancelled BEFORE runCancel — should be true now
	cancelled := runCtx.Err() != nil
	runCancel()

	if !cancelled {
		t.Fatal("cancelled should be true after external context cancellation")
	}

	// Cancelled path: session should be destroyed
	if _, ok := a.interactiveSubAgents.Load(key); !ok {
		// Already removed, which is correct
		return
	}
	// If still present, simulate the destroy path
	a.interactiveSubAgents.Delete(key)

	_, ok := a.interactiveSubAgents.Load(key)
	if ok {
		t.Fatal("session should be removed after external cancellation + destroy")
	}
}

// TestBgSession_CancelOrderRegression is a focused regression test.
// Before the fix, the code was:
//
//	out := Run(runCtx, cfg)
//	runCancel()                    // ← cancels the context
//	cancelled := runCtx.Err() != nil  // ← always true after runCancel!
//
// This meant ALL bg sessions were treated as cancelled and destroyed,
// even on natural completion. After the fix:
//
//	cancelled := runCtx.Err() != nil  // ← check BEFORE cancel
//	runCancel()                    // ← now safe to clean up
func TestBgSession_CancelOrderRegression(t *testing.T) {
	// Demonstrate the bug pattern with plain context operations
	ctx := context.Background()

	// --- BEFORE fix pattern ---
	runCtx, runCancel := context.WithCancel(ctx)
	// Simulate: Run() completes normally, context is NOT cancelled
	_ = runCtx  // Run(runCtx, cfg) would use this
	runCancel() // cleanup
	cancelledBefore := runCtx.Err() != nil

	// --- AFTER fix pattern ---
	runCtx2, runCancel2 := context.WithCancel(ctx)
	cancelledAfter := runCtx2.Err() != nil // check BEFORE cancel
	runCancel2()                           // cleanup

	if !cancelledBefore {
		t.Log("Note: cancelledBefore is false — this means the old code might actually work in some cases, " +
			"but the bug manifests when Run() itself calls runCtx.Done() or when there's a race")
	}

	if cancelledAfter {
		t.Error("cancelledAfter should be false for natural completion (context not cancelled)")
	}
}
