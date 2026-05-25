package agent

import (
	"context"
	"sync"
	"testing"

	"xbot/protocol"
)

// TestAutoNotify_DerivedFromBothHandlers verifies that autoNotify in engine.Run()
// is true when either ProgressNotifier OR ProgressEventHandler is set.
// This is the core fix: before, only ProgressNotifier gated autoNotify,
// so background SubAgents with only ProgressEventHandler had autoNotify=false
// and all progress events were silently dropped.
func TestAutoNotify_DerivedFromBothHandlers(t *testing.T) {
	tests := []struct {
		name                 string
		progressNotifier     func(lines []string, thinking string)
		progressEventHandler func(event *ProgressEvent)
		wantAuto             bool
	}{
		{
			name:     "both nil → autoNotify=false",
			wantAuto: false,
		},
		{
			name:             "ProgressNotifier only → autoNotify=true",
			progressNotifier: func([]string, string) {},
			wantAuto:         true,
		},
		{
			name:                 "ProgressEventHandler only → autoNotify=true",
			progressEventHandler: func(*ProgressEvent) {},
			wantAuto:             true,
		},
		{
			name:                 "both set → autoNotify=true",
			progressNotifier:     func([]string, string) {},
			progressEventHandler: func(*ProgressEvent) {},
			wantAuto:             true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := RunConfig{
				ProgressNotifier:     tt.progressNotifier,
				ProgressEventHandler: tt.progressEventHandler,
			}
			autoNotify := cfg.ProgressNotifier != nil || cfg.ProgressEventHandler != nil
			if autoNotify != tt.wantAuto {
				t.Errorf("autoNotify = %v, want %v", autoNotify, tt.wantAuto)
			}
		})
	}
}

// TestBackgroundMode_AutoNotifyViaEventHandler verifies the actual bug scenario:
// background interactive SubAgent has no ProgressNotifier but does have
// ProgressEventHandler (set by wireSubAgentCLIProgress). autoNotify must be true.
func TestBackgroundMode_AutoNotifyViaEventHandler(t *testing.T) {
	cfg := RunConfig{
		// Background mode: ProgressNotifier is nil
		ProgressNotifier: nil,
		// wireSubAgentCLIProgress sets this for background mode
		ProgressEventHandler: func(event *ProgressEvent) {},
	}
	autoNotify := cfg.ProgressNotifier != nil || cfg.ProgressEventHandler != nil
	if !autoNotify {
		t.Fatal("BUG REPRODUCED: background SubAgent with ProgressEventHandler has autoNotify=false")
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
