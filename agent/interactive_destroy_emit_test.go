package agent

import (
	"testing"
	"time"

	"xbot/channel"
	"xbot/protocol"
)

// destroyEventChannel captures SessionStateSender events for assertions.
type destroyEventChannel struct {
	events []protocol.SessionEvent
}

func (c *destroyEventChannel) Name() string { return "web" }
func (c *destroyEventChannel) Start() error { return nil }
func (c *destroyEventChannel) Stop()        {}
func (c *destroyEventChannel) Send(channel.OutboundMsg) (string, error) {
	return "", nil
}
func (c *destroyEventChannel) SendSessionState(event protocol.SessionEvent) {
	c.events = append(c.events, event)
}

// TestDestroyInteractiveSessionEmitsRemovedSubagentStopped verifies the sidebar
// removal contract: destroyInteractiveSession is the single funnel for ALL
// removal paths (TTL eviction, unload, cancel, panic recovery, spawn-failure
// cleanup, cascade child cleanup), and each must emit subagent_stopped with
// removed=true so the frontend deletes the row instead of parking it as a
// stale "running"/"idle" entry ("subagent 被卸载了却还显示").
func TestDestroyInteractiveSessionEmitsRemovedSubagentStopped(t *testing.T) {
	events := &destroyEventChannel{}
	a := &Agent{channelFinder: func(name string) (channel.Channel, bool) {
		return events, name == "web"
	}}
	key := "web:chat-1/review:inst-1"
	a.interactiveSubAgents.Store(key, &interactiveAgent{roleName: "review", instance: "inst-1"})

	a.destroyInteractiveSession(key)

	if len(events.events) != 1 {
		t.Fatalf("destroyInteractiveSession must emit exactly one subagent_stopped, got %#v", events.events)
	}
	ev := events.events[0]
	if ev.Action != "subagent_stopped" || !ev.Removed {
		t.Fatalf("event = %#v (want subagent_stopped with removed=true)", ev)
	}
	if ev.Role != "review" || ev.Instance != "inst-1" || ev.SessionKey != key {
		t.Fatalf("event identity = %#v", ev)
	}
	if ev.Channel != "web" || ev.ChatID != "chat-1" || ev.ParentID != "chat-1" {
		t.Fatalf("parent routing = %#v (want web:chat-1)", ev)
	}
	if _, ok := a.interactiveSubAgents.Load(key); ok {
		t.Fatal("registry entry survived destroyInteractiveSession")
	}
}

// TestDestroyInteractiveSessionEmitsOncePerKey verifies idempotence for double
// destroy (the second call must not re-emit — the registry entry is gone).
func TestDestroyInteractiveSessionEmitsOncePerKey(t *testing.T) {
	events := &destroyEventChannel{}
	a := &Agent{channelFinder: func(name string) (channel.Channel, bool) {
		return events, name == "web"
	}}
	key := "web:chat-1/review:inst-1"
	a.interactiveSubAgents.Store(key, &interactiveAgent{roleName: "review", instance: "inst-1"})

	a.destroyInteractiveSession(key)
	a.destroyInteractiveSession(key)

	if len(events.events) != 1 {
		t.Fatalf("double destroy must emit exactly once (idempotent), got %#v", events.events)
	}
}

// TestCleanupExpiredSessionsEmitsRemovedSubagentStopped verifies the TTL-eviction
// path: an idle session past interactiveSessionTTL is destroyed AND emits
// subagent_stopped(removed) — previously the eviction was silent, leaving the
// sidebar row until the next tree refresh. Running sessions must never be
// reaped.
func TestCleanupExpiredSessionsEmitsRemovedSubagentStopped(t *testing.T) {
	events := &destroyEventChannel{}
	a := &Agent{channelFinder: func(name string) (channel.Channel, bool) {
		return events, name == "web"
	}}
	key := "web:chat-1/explore:mem-1"
	a.interactiveSubAgents.Store(key, &interactiveAgent{
		roleName: "explore",
		instance: "mem-1",
		lastUsed: time.Now().Add(-2 * interactiveSessionTTL),
		running:  false,
	})
	runningKey := "web:chat-1/build:keep"
	a.interactiveSubAgents.Store(runningKey, &interactiveAgent{
		roleName: "build",
		instance: "keep",
		lastUsed: time.Now().Add(-2 * interactiveSessionTTL),
		running:  true, // running sessions are never reaped (parent may block on Run)
	})

	a.cleanupExpiredSessions()

	if len(events.events) != 1 {
		t.Fatalf("cleanup must emit exactly one removed event (running sessions must not be reaped), got %#v", events.events)
	}
	ev := events.events[0]
	if ev.Action != "subagent_stopped" || !ev.Removed || ev.SessionKey != key {
		t.Fatalf("TTL eviction event = %#v", ev)
	}
	if _, ok := a.interactiveSubAgents.Load(runningKey); !ok {
		t.Fatal("running session was reaped by TTL cleanup")
	}
}

// TestHasPendingAskUserFast verifies the in-memory WaitingUser probe used by the
// session tree to mark waiting_input rows (the sidebar and the panel must agree
// during an AskUser pause — chatCancelCh is deregistered there, so
// IsProcessingByChannel reports false while the panel shows busy).
func TestHasPendingAskUserFast(t *testing.T) {
	a := &Agent{}
	if a.HasPendingAskUserFast("web", "chat-1") {
		t.Fatal("no pending ask registered")
	}
	a.setPendingAskUser("web", "chat-1", &protocol.ProgressEvent{RequestID: "r1"})
	if !a.HasPendingAskUserFast("web", "chat-1") {
		t.Fatal("pending ask must be visible (in-memory, no DB replay)")
	}
	if a.HasPendingAskUserFast("web", "chat-2") {
		t.Fatal("pending ask must be per-session")
	}
	if a.HasPendingAskUserFast("", "chat-1") || a.HasPendingAskUserFast("web", "") {
		t.Fatal("empty channel/chatID must not probe")
	}
	a.ClearPendingAskUser("web", "chat-1")
	if a.HasPendingAskUserFast("web", "chat-1") {
		t.Fatal("cleared pending ask must not be visible")
	}
}

// TestParseInteractiveKeyParentDegenerate verifies the key parser used by
// emitRemovedSubAgentEvent for parent routing — chatIDs containing "/" (CLI
// workdir sessions) and ":" (CLI workdir:name suffixes), plus nested agent
// fullKeys.
func TestParseInteractiveKeyParentDegenerate(t *testing.T) {
	cases := []struct {
		key     string
		channel string
		chatID  string
	}{
		{"web:chat-1/review:inst-1", "web", "chat-1"},
		{"web:chat-1/review", "web", "chat-1"},
		{"cli:/home/user/workspace:Agent-main/review:inst-1", "cli", "/home/user/workspace:Agent-main"},
		{"cli:/home/user/workspace:Agent-main/review", "cli", "/home/user/workspace:Agent-main"},
		// Nested SubAgent: the parent is itself an agent fullKey.
		{"agent:web:chat-1/explore:mem-1/review:inst-2", "agent", "web:chat-1/explore:mem-1"},
	}
	for _, tc := range cases {
		gotChannel, gotChatID := parseInteractiveKeyParent(tc.key)
		if gotChannel != tc.channel || gotChatID != tc.chatID {
			t.Fatalf("parseInteractiveKeyParent(%q) = (%q, %q), want (%q, %q)", tc.key, gotChannel, gotChatID, tc.channel, tc.chatID)
		}
	}
	if ch, id := parseInteractiveKeyParent("noseparator"); ch != "" || id != "" {
		t.Fatal("key without separator must not parse")
	}
}
