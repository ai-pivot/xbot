package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"xbot/tools"
)

// newBlackboardTestHub builds a hub over a real BackgroundTaskManager, so the
// notifications it produces travel the production pipeline (NotifyCh) instead of
// a test double. The coalescing window is shortened so tests do not sleep for
// seconds.
func newBlackboardTestHub(t *testing.T, window time.Duration) (*blackboardHub, *tools.BackgroundTaskManager) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a := &Agent{agentCtx: ctx}
	mgr := tools.NewBackgroundTaskManager()
	a.bgTaskMgr.Store(mgr)
	hub := newBlackboardHub(a)
	hub.window = window
	return hub, mgr
}

// recvNotification waits for one notification or fails.
func recvNotification(t *testing.T, mgr *tools.BackgroundTaskManager, wait time.Duration) string {
	t.Helper()
	select {
	case n := <-mgr.NotifyCh:
		return n.(*tools.AsyncMessageNotification).Content
	case <-time.After(wait):
		t.Fatalf("no notification within %s", wait)
		return ""
	}
}

// expectNoNotification asserts the pipeline stays quiet for the given window.
func expectNoNotification(t *testing.T, mgr *tools.BackgroundTaskManager, wait time.Duration) {
	t.Helper()
	select {
	case n := <-mgr.NotifyCh:
		t.Fatalf("unexpected notification: %s", n.(*tools.AsyncMessageNotification).Content)
	case <-time.After(wait):
	}
}

func bbEvent(board, key, op, actor string) tools.BlackboardEvent {
	return tools.BlackboardEvent{Board: board, Key: key, Op: op, Actor: actor, Title: key, Revision: 1, At: time.Now().UnixMilli()}
}

// TestBlackboardHub_WatchRegistry pins the subscription semantics: per session,
// per board, idempotent, with the latest prefix winning.
func TestBlackboardHub_WatchRegistry(t *testing.T) {
	hub, _ := newBlackboardTestHub(t, 50*time.Millisecond)

	hub.WatchBoard("cli:s1", "cli:s1", "")
	hub.WatchBoard("cli:s1", "cli:s1", "api-") // latest prefix wins
	hub.WatchBoard("cli:s1", "@dev", "")
	if got := hub.ListWatches("cli:s1"); len(got) != 2 {
		t.Fatalf("watches = %v, want 2 (board dedup by id)", got)
	}
	hub.WatchBoard("cli:s2", "cli:s1", "")
	if got := hub.ListWatches("cli:s2"); len(got) != 1 {
		t.Fatalf("second session watches = %v, want 1", got)
	}

	if !hub.UnwatchBoard("cli:s1", "@dev") {
		t.Fatal("unwatch of an existing board must report true")
	}
	if hub.UnwatchBoard("cli:s1", "@dev") {
		t.Fatal("unwatch of a missing board must report false")
	}
	if got := hub.ListWatches("cli:s1"); len(got) != 1 || got[0].Prefix != "api-" {
		t.Fatalf("after unwatch: %v, want one watch with prefix api-", got)
	}
}

// TestBlackboardHub_NotifiesWatchersAndNeverTheWriter: a change reaches the
// sessions that watch the board (through the shared notification pipeline) and
// never wakes the agent that made it.
func TestBlackboardHub_NotifiesWatchersAndNeverTheWriter(t *testing.T) {
	hub, mgr := newBlackboardTestHub(t, 50*time.Millisecond)
	hub.WatchBoard("cli:watcher", "cli:s1", "")

	// The writer is itself a watcher: it must not be notified of its own change.
	hub.WatchBoard("cli:s1", "cli:s1", "")
	hub.PublishBlackboardChange(bbEvent("cli:s1", "api", "post", "cli:s1"))

	content := recvNotification(t, mgr, time.Second)
	if !strings.Contains(content, "api") || !strings.Contains(content, "cli:s1") {
		t.Fatalf("digest = %q, want it to name the board and the changed key", content)
	}
	// The writer's own session received nothing (only the watcher's digest was
	// queued).
	expectNoNotification(t, mgr, 100*time.Millisecond)
}

// TestBlackboardHub_CoalescesBursts: a burst of changes on one board reaches a
// watcher as at most one notification per window — with the suppressed changes
// delivered on the trailing edge, never dropped.
func TestBlackboardHub_CoalescesBursts(t *testing.T) {
	hub, mgr := newBlackboardTestHub(t, 200*time.Millisecond)
	hub.WatchBoard("cli:watcher", "cli:s1", "")

	// Leading edge: the first change notifies immediately.
	hub.PublishBlackboardChange(bbEvent("cli:s1", "k1", "post", "cli:other"))
	first := recvNotification(t, mgr, time.Second)
	if !strings.Contains(first, "k1") {
		t.Fatalf("first digest = %q, want it to mention k1", first)
	}

	// Burst inside the window: nothing more until the window closes.
	for _, k := range []string{"k2", "k3", "k4"} {
		hub.PublishBlackboardChange(bbEvent("cli:s1", k, "update", "cli:other"))
	}
	expectNoNotification(t, mgr, 50*time.Millisecond)

	// Trailing edge: exactly ONE digest carrying the whole suppressed burst.
	trailing := recvNotification(t, mgr, time.Second)
	for _, k := range []string{"k2", "k3", "k4"} {
		if !strings.Contains(trailing, k) {
			t.Errorf("trailing digest %q lost %s", trailing, k)
		}
	}
	expectNoNotification(t, mgr, 300*time.Millisecond)
}

// TestBlackboardHub_RespectsPrefixAndUnwatch: the prefix filter narrows what a
// watcher hears, and unwatching stops delivery immediately.
func TestBlackboardHub_RespectsPrefixAndUnwatch(t *testing.T) {
	hub, mgr := newBlackboardTestHub(t, 50*time.Millisecond)
	hub.WatchBoard("cli:watcher", "cli:s1", "api-")

	hub.PublishBlackboardChange(bbEvent("cli:s1", "db-migration", "post", "cli:other"))
	expectNoNotification(t, mgr, 150*time.Millisecond)

	hub.PublishBlackboardChange(bbEvent("cli:s1", "api-impl", "post", "cli:other"))
	content := recvNotification(t, mgr, time.Second)
	if !strings.Contains(content, "api-impl") {
		t.Fatalf("digest = %q, want api-impl", content)
	}

	hub.UnwatchBoard("cli:watcher", "cli:s1")
	hub.PublishBlackboardChange(bbEvent("cli:s1", "api-other", "post", "cli:other"))
	expectNoNotification(t, mgr, 150*time.Millisecond)
}

// TestBlackboardHub_BoardIsolation: watching one board must not deliver changes
// from another.
func TestBlackboardHub_BoardIsolation(t *testing.T) {
	hub, mgr := newBlackboardTestHub(t, 50*time.Millisecond)
	hub.WatchBoard("cli:watcher", "cli:s1", "")

	hub.PublishBlackboardChange(bbEvent("@dev-team", "x", "post", "cli:other"))
	expectNoNotification(t, mgr, 150*time.Millisecond)

	hub.PublishBlackboardChange(bbEvent("cli:s1", "x", "post", "cli:other"))
	if content := recvNotification(t, mgr, time.Second); !strings.Contains(content, "cli:s1") {
		t.Fatalf("digest = %q, want the watched board", content)
	}
}
