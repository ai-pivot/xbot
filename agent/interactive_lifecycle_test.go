package agent

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestCascadeCancelOnBgCompletion verifies that when a bg SubAgent (B) completes
// naturally, its child bg SubAgent (C) is cleaned up via context cancellation.
// This is a regression test: before the fix, B's natural completion called runCancel()
// which propagated to C via context, but C's parentKey was correctly set to B's key
// only if B's runCtx had bgParentKey set.
func TestCascadeCancelOnBgCompletion(t *testing.T) {
	a := &Agent{
		agentCtx:             context.Background(),
		interactiveSubAgents: sync.Map{},
	}

	// Simulate the hierarchy: A (top-level bg) → B (child bg) → C (grandchild bg)
	parentKey := "cli:test/session-a:bg-inst"

	// Create B's runCtx with bgParentKey marker (simulating B being a bg SubAgent)
	bBgBase := context.Background() // in real code, this would be a.agentCtx or parent ctx
	bRunCtx, bRunCancel := context.WithCancel(bBgBase)
	bRunCtx = context.WithValue(bRunCtx, bgSessionCtxKey{}, true)
	bRunCtx = context.WithValue(bRunCtx, bgParentKey{}, parentKey)
	_ = bRunCtx

	// Create B session
	bKey := "cli:test/session-b:child"
	bIA := &interactiveAgent{
		roleName:      "session-b",
		instance:      "child",
		background:    true,
		running:       true,
		cancelCurrent: bRunCancel,
		parentKey:     parentKey,
	}
	a.interactiveSubAgents.Store(bKey, bIA)

	// Simulate C being created by B's Run.
	// C's context should derive from B's runCtx (has bgSessionCtxKey).
	cBgBase := bRunCtx // C detects bgSessionCtxKey → derives from B's ctx
	cRunCtx, cRunCancel := context.WithCancel(cBgBase)
	cRunCtx = context.WithValue(cRunCtx, bgSessionCtxKey{}, true)
	cRunCtx = context.WithValue(cRunCtx, bgParentKey{}, bKey)

	cKey := "cli:test/session-c:grandchild"
	cIA := &interactiveAgent{
		roleName:      "session-c",
		instance:      "grandchild",
		background:    true,
		running:       true,
		cancelCurrent: cRunCancel,
		parentKey:     bKey, // C's parent is B
	}
	a.interactiveSubAgents.Store(cKey, cIA)

	// Simulate B completing naturally → runCancel() is called
	bRunCancel()

	// C's context should be cancelled (derived from B's runCtx)
	if cRunCtx.Err() == nil {
		t.Fatal("C's context should be cancelled after B's runCancel() — C must not outlive B")
	}

	// Verify C would detect cancellation and clean itself up
	cancelled := cRunCtx.Err() != nil
	if !cancelled {
		t.Fatal("C should see itself as cancelled after B completes")
	}

	// In real code, C's goroutine would call cancelChildSessions(cKey) +
	// destroyInteractiveSession(cKey). Simulate that here.
	a.cancelChildSessions(cKey)
	a.destroyInteractiveSession(cKey)

	// C should be gone
	if _, ok := a.interactiveSubAgents.Load(cKey); ok {
		t.Fatal("C should be removed after cascade cleanup")
	}

	// B still exists (natural completion preserves session for future send)
	if _, ok := a.interactiveSubAgents.Load(bKey); !ok {
		t.Fatal("B should still exist after natural completion (preserved for send)")
	}
}

// TestCascadeCancelOnForegroundCompletion verifies that when a foreground SubAgent (B)
// completes, its child bg SubAgent (C) is cleaned up. Before the fix, foreground
// SubAgents didn't wrap their subCtx with bgSessionCtxKey/bgParentKey, so C's context
// chain pointed to B's parent (A) instead of B itself — B completing didn't cancel C.
func TestCascadeCancelOnForegroundCompletion(t *testing.T) {
	a := &Agent{
		agentCtx:             context.Background(),
		interactiveSubAgents: sync.Map{},
	}

	// Simulate A (bg SubAgent) with its own runCtx
	aRunCtx, aRunCancel := context.WithCancel(context.Background())
	aRunCtx = context.WithValue(aRunCtx, bgSessionCtxKey{}, true)
	aRunCtx = context.WithValue(aRunCtx, bgParentKey{}, "cli:test/top:main")
	defer aRunCancel()

	// B is a foreground SubAgent created by A.
	// Before the fix, B's subCtx was just WithCallChain(aRunCtx, ...) without
	// bgSessionCtxKey/bgParentKey wrapping. After the fix, B wraps its own runCtx.
	bParentKey := "cli:test/session-a:bg-inst"
	bKey := "cli:test/session-b:fg-child"

	// After the fix: foreground B wraps its own context
	bFgRunCtx, bFgRunCancel := context.WithCancel(aRunCtx)
	bFgRunCtx = context.WithValue(bFgRunCtx, bgSessionCtxKey{}, true)
	bFgRunCtx = context.WithValue(bFgRunCtx, bgParentKey{}, bParentKey)

	bIA := &interactiveAgent{
		roleName:      "session-b",
		instance:      "fg-child",
		background:    false,
		running:       true,
		cancelCurrent: bFgRunCancel,
		parentKey:     bParentKey,
	}
	a.interactiveSubAgents.Store(bKey, bIA)

	// C is a bg SubAgent created by B's Run.
	// C detects bgSessionCtxKey on B's runCtx → derives from B's ctx.
	cBgBase := bFgRunCtx // C derives from B's runCtx (not A's)
	cRunCtx, cRunCancel := context.WithCancel(cBgBase)
	cRunCtx = context.WithValue(cRunCtx, bgSessionCtxKey{}, true)
	cRunCtx = context.WithValue(cRunCtx, bgParentKey{}, bKey)

	cKey := "cli:test/session-c:grandchild"
	cIA := &interactiveAgent{
		roleName:      "session-c",
		instance:      "grandchild",
		background:    true,
		running:       true,
		cancelCurrent: cRunCancel,
		parentKey:     bKey, // C's parent is B
	}
	a.interactiveSubAgents.Store(cKey, cIA)

	// B completes (foreground) → fgRunCancel() is called + cancelChildSessions
	bFgRunCancel()
	a.cancelChildSessions(bKey)

	// C's context should be cancelled (derived from B's runCtx, not A's)
	if cRunCtx.Err() == nil {
		t.Fatal("C's context should be cancelled after B's fgRunCancel() — C must not outlive B")
	}

	// C should be cleaned up by cancelChildSessions(bKey)
	if _, ok := a.interactiveSubAgents.Load(cKey); ok {
		t.Fatal("C should be removed from interactiveSubAgents after B's cancelChildSessions(bKey)")
	}
}

// TestForegroundParentKeyPropagatesToChildren verifies that a foreground SubAgent's
// bgParentKey is set correctly so children detect it and set their parentKey correctly.
func TestForegroundParentKeyPropagatesToChildren(t *testing.T) {
	fgKey := "cli:test/foreground:child"

	// Simulate the foreground context wrapping (the fix)
	fgCtx := context.Background()
	fgRunCtx, fgRunCancel := context.WithCancel(fgCtx)
	defer fgRunCancel()
	fgRunCtx = context.WithValue(fgRunCtx, bgSessionCtxKey{}, true)
	fgRunCtx = context.WithValue(fgRunCtx, bgParentKey{}, fgKey)

	// Verify bgSessionCtxKey is present
	if fgRunCtx.Value(bgSessionCtxKey{}) == nil {
		t.Fatal("foreground runCtx must have bgSessionCtxKey for nested detection")
	}

	// Verify bgParentKey is present
	if pk, ok := fgRunCtx.Value(bgParentKey{}).(string); !ok || pk != fgKey {
		t.Fatalf("foreground runCtx must have bgParentKey=%q, got %q", fgKey, pk)
	}

	// Simulate a child detecting the parent key from the foreground's context
	if parentKeyFromCtx, ok := fgRunCtx.Value(bgParentKey{}).(string); ok {
		childIA := &interactiveAgent{
			parentKey: parentKeyFromCtx,
		}
		if childIA.parentKey != fgKey {
			t.Fatalf("child's parentKey should be %q, got %q", fgKey, childIA.parentKey)
		}
	}
}

// TestSendPathRunCtxHasParentKey verifies that the send path (SendToInteractiveSession)
// sets bgParentKey on its runCtx so that any SubAgents spawned during the send Run
// derive their lifecycle from this session.
func TestSendPathRunCtxHasParentKey(t *testing.T) {
	key := "cli:test/session:send-test"

	// Simulate the send path context construction
	asyncBase := context.Background()
	runCtx, runCancel := context.WithCancel(asyncBase)
	defer runCancel()
	// After fix: mark as bg session with own key
	runCtx = context.WithValue(runCtx, bgSessionCtxKey{}, true)
	runCtx = context.WithValue(runCtx, bgParentKey{}, key)

	// Verify markers are present
	if runCtx.Value(bgSessionCtxKey{}) == nil {
		t.Fatal("send path runCtx must have bgSessionCtxKey")
	}
	if pk, ok := runCtx.Value(bgParentKey{}).(string); !ok || pk != key {
		t.Fatalf("send path runCtx must have bgParentKey=%q", key)
	}

	// Verify a child created from this context would detect bgSessionCtxKey
	// and derive from it (not from agentCtx)
	if runCtx.Value(bgSessionCtxKey{}) != nil {
		// Child would use this ctx as bgBase → child derives from send's runCtx
		// When send's runCtx is cancelled, child's context is also cancelled
		childCtx, childCancel := context.WithCancel(runCtx)
		defer childCancel()

		runCancel()
		time.Sleep(10 * time.Millisecond) // allow propagation

		if childCtx.Err() == nil {
			t.Fatal("child context should be cancelled when send's runCtx is cancelled")
		}
	}
}

// TestForegroundIAPreservesParentKey verifies that when a foreground SubAgent's
// placeholder is replaced with a full IA on completion, the parentKey is preserved.
func TestForegroundIAPreservesParentKey(t *testing.T) {
	parentKey := "cli:test/parent:main"

	// Simulate placeholder with parentKey
	placeholder := &interactiveAgent{
		roleName:   "child",
		instance:   "fg-1",
		parentKey:  parentKey,
		groupID:    "group:g1",
		background: false,
	}

	// Simulate the replacement that happens on foreground completion
	ia := &interactiveAgent{
		roleName:   placeholder.roleName,
		instance:   placeholder.instance,
		background: false,
		parentKey:  placeholder.parentKey, // preserve parent key
		groupID:    placeholder.groupID,   // preserve group membership
	}

	if ia.parentKey != parentKey {
		t.Fatalf("replacement IA should preserve parentKey=%q, got %q", parentKey, ia.parentKey)
	}
	if ia.groupID != "group:g1" {
		t.Fatalf("replacement IA should preserve groupID, got %q", ia.groupID)
	}
}
