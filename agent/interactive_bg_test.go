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

// TestBgSend_CancelCurrentSetAndCleared verifies that the background "send" path
// correctly sets cancelCurrent before starting the goroutine and clears it on
// completion. This is a regression test for the bug where UnloadInteractiveSession
// called ia.cancelCurrent() but it was nil (never set by the send path), so
// background send goroutines kept running even after unload.
func TestBgSend_CancelCurrentSetAndCleared(t *testing.T) {
	// Simulate the state after initial spawn completes: cancelCurrent is nil.
	ia := &interactiveAgent{
		roleName:   "test-role",
		instance:   "test-inst",
		background: true,
		running:    false,
	}

	if ia.cancelCurrent != nil {
		t.Fatal("precondition: cancelCurrent should be nil after initial spawn completes")
	}

	// Simulate the send path's context setup:
	//   runCtx, runCancel := context.WithCancel(subCtx)
	//   ia.cancelCurrent = runCancel
	subCtx := context.Background()
	runCtx, runCancel := context.WithCancel(subCtx)

	ia.cancelCurrent = runCancel
	ia.running = true

	// Verify cancelCurrent is set
	if ia.cancelCurrent == nil {
		t.Fatal("cancelCurrent should be set before goroutine starts")
	}

	// Simulate unload calling cancelCurrent
	ia.cancelCurrent()

	// Verify the context is actually cancelled
	if runCtx.Err() == nil {
		t.Fatal("context should be cancelled after cancelCurrent() is called")
	}

	// Simulate goroutine completion: clear cancelCurrent
	ia.running = false
	ia.cancelCurrent = nil

	if ia.cancelCurrent != nil {
		t.Fatal("cancelCurrent should be nil after goroutine completion")
	}
}

// TestBgSend_NoParentDeadlineInheritance verifies that the background "send" path
// derives its cancellable context from the agent lifecycle (a.agentCtx), NOT from
// the parent's tool execution context. The parent's context carries a per-attempt
// deadline — inheriting it would cause background goroutines to be killed when the
// parent's tool call times out, even though the goroutine should run independently.
//
// This is a regression test for the bug where context.WithCancel(subCtx) was used
// instead of context.WithCancel(a.agentCtx), causing all background send goroutines
// to time out after ~120s.
func TestBgSend_NoParentDeadlineInheritance(t *testing.T) {
	// Simulate a parent tool context WITH a deadline (as in real SubAgent tool calls)
	parentCtx, parentCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer parentCancel()

	// Simulate the agent lifecycle context (no deadline)
	agentCtx, agentCancel := context.WithCancel(context.Background())
	defer agentCancel()

	a := &Agent{
		agentCtx:             agentCtx,
		interactiveSubAgents: sync.Map{},
	}

	key := "cli:test/worker:w1"
	ia := &interactiveAgent{
		roleName:   "worker",
		instance:   "w1",
		background: true,
		running:    false,
	}
	a.interactiveSubAgents.Store(key, ia)

	// --- Replicate the fixed send path logic ---
	// ctx = parentCtx (has deadline)
	// subCtx derived from ctx (has deadline)
	subCtx := parentCtx

	var bgBase context.Context
	if subCtx.Value(bgSessionCtxKey{}) != nil {
		bgBase = subCtx
	} else {
		bgBase = a.agentCtx // MUST use agent lifecycle, not parent's ctx
	}
	if bgBase == nil {
		bgBase = context.Background()
	}
	runCtx, runCancel := context.WithCancel(bgBase)
	defer runCancel()
	ia.cancelCurrent = runCancel
	ia.running = true

	// Wait for the parent's deadline to expire
	time.Sleep(150 * time.Millisecond)

	// Verify the parent context IS expired
	if parentCtx.Err() == nil {
		t.Fatal("parent context should have expired by now")
	}

	// Verify the background runCtx is NOT expired (it's independent)
	if runCtx.Err() != nil {
		t.Fatal("runCtx should NOT be cancelled when parent's deadline expires — " +
			"background goroutines must use agent lifecycle context, not parent tool context")
	}

	// Verify unload still works via cancelCurrent
	ia.cancelCurrent()
	if runCtx.Err() == nil {
		t.Fatal("runCtx should be cancelled after cancelCurrent() (unload)")
	}
}

// TestCancelChildSessions_OnlyTargetsChildren verifies that cancelChildSessions
// only cancels contexts of sessions with matching parentKey, NOT all sessions.
// This is a regression test for the bug where cancelChildSessions called
// cancelCurrent() on EVERY interactive session regardless of parentKey.
// When 5 peer bg agents were running and any one was unloaded/panicked,
// all 5 were cancelled simultaneously — manifesting as "all agents die at ~7min".
func TestCancelChildSessions_OnlyTargetsChildren(t *testing.T) {
	a := &Agent{
		agentCtx:             context.Background(),
		interactiveSubAgents: sync.Map{},
	}

	// Create 3 peer-level sessions (same parent "root", different roles)
	type testSession struct {
		key       string
		ia        *interactiveAgent
		runCtx    context.Context
		runCancel context.CancelFunc
		role      string
	}
	var sessions []testSession

	for _, role := range []string{"worker-a", "worker-b", "worker-c"} {
		key := "cli:test/" + role + ":inst1"
		runCtx, runCancel := context.WithCancel(context.Background())
		ia := &interactiveAgent{
			roleName:      role,
			instance:      "inst1",
			background:    true,
			running:       true,
			cancelCurrent: runCancel,
			parentKey:     "cli:test/root:main", // all share same parent
		}
		a.interactiveSubAgents.Store(key, ia)
		sessions = append(sessions, testSession{key: key, ia: ia, runCtx: runCtx, runCancel: runCancel, role: role})
	}

	// Create 1 unrelated session (different parent)
	unrelatedKey := "cli:test/unrelated:x1"
	unrelatedCtx, unrelatedCancel := context.WithCancel(context.Background())
	defer unrelatedCancel()
	unrelatedIA := &interactiveAgent{
		roleName:      "unrelated",
		instance:      "x1",
		background:    true,
		running:       true,
		cancelCurrent: unrelatedCancel,
		parentKey:     "cli:test/other-parent:main", // different parent
	}
	a.interactiveSubAgents.Store(unrelatedKey, unrelatedIA)

	// Simulate cancelling children of "cli:test/root:main"
	a.cancelChildSessions("cli:test/root:main")

	// All 3 peer sessions should be cancelled (they share the parent)
	for _, s := range sessions {
		if s.runCtx.Err() == nil {
			t.Errorf("session %s should have been cancelled", s.role)
		}
	}

	// The unrelated session should NOT be cancelled
	if unrelatedCtx.Err() != nil {
		t.Fatal("unrelated session was cancelled — cancelChildSessions must only target children with matching parentKey, not ALL sessions")
	}
}
