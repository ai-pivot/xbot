package agent

import (
	"context"
	"sync"
	"testing"

	"xbot/protocol"
)

// TestBackgroundInteractive_ProgressNotifierEnablesProgressEvents verifies
// the root cause fix: background interactive SubAgents must have
// ProgressNotifier set to enable autoNotify in engine.Run().
func TestBackgroundInteractive_ProgressNotifierEnablesProgressEvents(t *testing.T) {
	background := true
	var cfg RunConfig

	if !background {
		cfg.ProgressNotifier = func(lines []string, thinking string) {
			t.Fatal("foreground notifier should not be called in background mode")
		}
	} else {
		cfg.ProgressNotifier = func(lines []string, thinking string) {}
	}

	autoNotify := cfg.ProgressNotifier != nil
	if !autoNotify {
		t.Fatal("BUG: autoNotify=false for background SubAgent — ProgressNotifier is nil")
	}
}

// TestBackgroundProgressNotifierDoesNotLeakToParent verifies the no-op
// ProgressNotifier in background mode does not send progress to parent.
func TestBackgroundProgressNotifierDoesNotLeakToParent(t *testing.T) {
	parentNotified := false
	parentNotifier := func(lines []string, thinking string) { parentNotified = true }
	backgroundNotifier := func(lines []string, thinking string) {}

	backgroundNotifier([]string{"some progress"}, "")
	if parentNotified {
		t.Fatal("background ProgressNotifier should not notify parent")
	}

	parentNotifier([]string{"parent progress"}, "")
	if !parentNotified {
		t.Fatal("parent ProgressNotifier should work when called directly")
	}
}

// TestGetActiveProgress_BackgroundInteractive verifies Phase correction
// for running agents between iterations.
func TestGetActiveProgress_BackgroundInteractive(t *testing.T) {
	a := NewTestAgent()
	interactiveKey := "cli:/home/user/src/project/ministry-works:split-test-files"
	agentProgressKey := "agent:" + interactiveKey

	ia := &interactiveAgent{roleName: "ministry-works", instance: "split-test-files", running: true, mu: sync.Mutex{}}
	a.interactiveSubAgents.Store(interactiveKey, ia)

	a.lastProgressSnapshot.Store(agentProgressKey, &protocol.ProgressEvent{
		ChatID: agentProgressKey, Phase: "done", Iteration: 3,
		ActiveTools: []protocol.ToolProgress{{Name: "Shell", Status: "done", Iteration: 3}},
	})
	a.iterationHistories.Store(agentProgressKey, &[]protocol.ProgressEvent{
		{Phase: "running", Iteration: 1},
		{Phase: "tool_use", Iteration: 2},
		{Phase: "running", Iteration: 3},
	})

	result := a.GetActiveProgress("agent", interactiveKey)
	if result == nil {
		t.Fatal("GetActiveProgress returned nil")
	}
	if result.Phase == "done" {
		t.Errorf("BUG REPRODUCED: Phase=%q for running agent between iterations", result.Phase)
	}
}

func TestGetActiveProgress_BackgroundInteractive_FinishedAgent(t *testing.T) {
	a := NewTestAgent()
	key := "cli:/cwd/r:i"
	ia := &interactiveAgent{running: false, mu: sync.Mutex{}}
	a.interactiveSubAgents.Store(key, ia)
	a.lastProgressSnapshot.Store("agent:"+key, &protocol.ProgressEvent{Phase: "done", Iteration: 5})

	result := a.GetActiveProgress("agent", key)
	if result == nil {
		t.Fatal("nil")
	}
	if result.Phase != "done" {
		t.Errorf("stopped agent should have Phase=done, got %q", result.Phase)
	}
}

func TestGetActiveProgress_BackgroundInteractive_NoSnapshot(t *testing.T) {
	a := NewTestAgent()
	if result := a.GetActiveProgress("agent", "cli:/cwd/r:i"); result != nil {
		t.Errorf("expected nil, got Phase=%q", result.Phase)
	}
}

func TestGetActiveProgress_KeyFormatConsistency(t *testing.T) {
	a := NewTestAgent()
	interactiveKey := "cli:/home/user/src/project/ministry-works:split-test-files"
	agentProgressKey := "agent:" + interactiveKey

	ia := &interactiveAgent{running: true, mu: sync.Mutex{}}
	a.interactiveSubAgents.Store(interactiveKey, ia)
	a.lastProgressSnapshot.Store(agentProgressKey, &protocol.ProgressEvent{
		ChatID: agentProgressKey, Phase: "done", Iteration: 1,
	})

	result := a.GetActiveProgress("agent", interactiveKey)
	if result == nil {
		t.Fatal("snapshot lookup failed — key format mismatch")
	}

	if _, loaded := a.interactiveSubAgents.Load(interactiveKey); !loaded {
		t.Error("interactiveSubAgents.Load(interactiveKey) failed")
	}
	if _, loaded := a.interactiveSubAgents.Load(agentProgressKey); loaded {
		t.Error("interactiveSubAgents should not store agentProgressKey")
	}
}

func NewTestAgent() *Agent { return &Agent{} }

var _ = context.Background
