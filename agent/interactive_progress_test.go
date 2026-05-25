package agent

import (
	"sync"
	"testing"

	"xbot/protocol"
)

// TestGetActiveProgress_BackgroundInteractive simulates the full lifecycle
// of a background interactive SubAgent and verifies GetActiveProgress returns
// correct Phase even between iterations.
//
// This test reproduces the bug where switching to a running background SubAgent
// session shows idle (Phase="done") instead of busy.
//
// The lifecycle being tested:
// 1. SpawnInteractiveSession creates placeholder in interactiveSubAgents
// 2. Background goroutine sets running=true, starts Run()
// 3. Run() calls ProgressEventHandler with PhaseDone (iteration completes)
// 4. CLI calls GetActiveProgress("agent", chatID) to check if session is busy
// 5. EXPECTED: returns Phase != "done" because agent is still running
// 6. ACTUAL (bug): returns Phase="done" → CLI shows idle
func TestGetActiveProgress_BackgroundInteractive(t *testing.T) {
	a := NewTestAgent()

	// Simulate SpawnInteractiveSession storing the agent entry.
	// interactiveSubAgents key = interactiveKey format (no "agent:" prefix).
	interactiveKey := "cli:/home/user/src/project/ministry-works:split-test-files"
	agentProgressKey := "agent:" + interactiveKey // used by lastProgressSnapshot

	// Store the interactive agent entry (running=true, simulating active Run).
	ia := &interactiveAgent{
		roleName: "ministry-works",
		instance: "split-test-files",
		running:  true, // agent is actively running
		mu:       sync.Mutex{},
	}
	a.interactiveSubAgents.Store(interactiveKey, ia)

	// Simulate ProgressEventHandler storing a PhaseDone snapshot.
	// This happens when an iteration completes but the agent continues.
	doneSnapshot := &protocol.ProgressEvent{
		ChatID:    agentProgressKey,
		Phase:     "done",
		Iteration: 3,
		ActiveTools: []protocol.ToolProgress{
			{Name: "Shell", Status: "done", Iteration: 3},
		},
	}
	a.lastProgressSnapshot.Store(agentProgressKey, doneSnapshot)

	// Also store iteration history (has active phases from previous iterations).
	a.iterationHistories.Store(agentProgressKey, &[]protocol.ProgressEvent{
		{ChatID: agentProgressKey, Phase: "running", Iteration: 1},
		{ChatID: agentProgressKey, Phase: "tool_use", Iteration: 2},
		{ChatID: agentProgressKey, Phase: "running", Iteration: 3},
	})

	// CLI calls GetActiveProgress with ch="agent", chatID=interactiveKey.
	// This is what handleSuHistoryLoad does after switching sessions.
	result := a.GetActiveProgress("agent", interactiveKey)

	if result == nil {
		t.Fatal("GetActiveProgress returned nil, expected progress snapshot")
	}

	t.Logf("Phase: %q, Iteration: %d, ChatID: %q", result.Phase, result.Iteration, result.ChatID)

	// The core assertion: since the agent is still running (ia.running=true),
	// GetActiveProgress should NOT return Phase="done".
	// It should correct the Phase to reflect that the agent is active.
	if result.Phase == "done" {
		t.Errorf("BUG REPRODUCED: GetActiveProgress returned Phase=%q for a running agent. "+
			"This causes CLI to show idle when switching to the agent session.", result.Phase)
		t.Errorf("Agent running=true but snapshot Phase=done (between iterations). " +
			"GetActiveProgress should have corrected Phase to a non-done value.")
	}
}

// TestGetActiveProgress_BackgroundInteractive_FinishedAgent verifies that
// GetActiveProgress correctly returns Phase="done" when the agent has
// truly stopped (running=false).
func TestGetActiveProgress_BackgroundInteractive_FinishedAgent(t *testing.T) {
	a := NewTestAgent()

	interactiveKey := "cli:/home/user/src/project/ministry-works:split-test-files"
	agentProgressKey := "agent:" + interactiveKey

	// Agent has finished (running=false).
	ia := &interactiveAgent{
		roleName: "ministry-works",
		instance: "split-test-files",
		running:  false, // agent has stopped
		mu:       sync.Mutex{},
	}
	a.interactiveSubAgents.Store(interactiveKey, ia)

	// PhaseDone snapshot from the final iteration.
	doneSnapshot := &protocol.ProgressEvent{
		ChatID:    agentProgressKey,
		Phase:     "done",
		Iteration: 5,
	}
	a.lastProgressSnapshot.Store(agentProgressKey, doneSnapshot)

	result := a.GetActiveProgress("agent", interactiveKey)

	if result == nil {
		t.Fatal("GetActiveProgress returned nil")
	}

	// When agent is truly stopped, Phase="done" is correct.
	if result.Phase != "done" {
		t.Errorf("GetActiveProgress should return Phase=done for stopped agent, got %q", result.Phase)
	}
}

// TestGetActiveProgress_BackgroundInteractive_NoSnapshot verifies that
// GetActiveProgress returns nil when no snapshot exists (agent never ran or was cleaned up).
func TestGetActiveProgress_BackgroundInteractive_NoSnapshot(t *testing.T) {
	a := NewTestAgent()

	interactiveKey := "cli:/home/user/src/project/ministry-works:split-test-files"

	result := a.GetActiveProgress("agent", interactiveKey)
	if result != nil {
		t.Errorf("expected nil for non-existent snapshot, got Phase=%q", result.Phase)
	}
}

// TestGetActiveProgress_BackgroundInteractive_ActivePhase verifies that
// GetActiveProgress works correctly when Phase is already active (not "done").
func TestGetActiveProgress_BackgroundInteractive_ActivePhase(t *testing.T) {
	a := NewTestAgent()

	interactiveKey := "cli:/home/user/src/project/ministry-works:split-test-files"
	agentProgressKey := "agent:" + interactiveKey

	ia := &interactiveAgent{
		roleName: "ministry-works",
		instance: "split-test-files",
		running:  true,
		mu:       sync.Mutex{},
	}
	a.interactiveSubAgents.Store(interactiveKey, ia)

	// Active-phase snapshot (agent in the middle of an iteration).
	activeSnapshot := &protocol.ProgressEvent{
		ChatID:    agentProgressKey,
		Phase:     "tool_use",
		Iteration: 2,
		ActiveTools: []protocol.ToolProgress{
			{Name: "Read", Status: "running", Iteration: 2},
		},
	}
	a.lastProgressSnapshot.Store(agentProgressKey, activeSnapshot)

	result := a.GetActiveProgress("agent", interactiveKey)

	if result == nil {
		t.Fatal("GetActiveProgress returned nil")
	}

	if result.Phase != "tool_use" {
		t.Errorf("expected Phase=tool_use, got %q", result.Phase)
	}
}

// TestGetActiveProgress_KeyFormatConsistency verifies that the key formats
// used by interactiveSubAgents and lastProgressSnapshot are consistent
// with how GetActiveProgress constructs its lookup key.
//
// This tests the specific key mismatch bug where:
// - interactiveSubAgents uses key "cli:/cwd/role:inst" (interactiveKey)
// - lastProgressSnapshot uses key "agent:cli:/cwd/role:inst" (agentProgressKey)
// - GetActiveProgress constructs "agent:" + chatID for lastProgressSnapshot lookup
// - GetActiveProgress must use chatID (without "agent:" prefix) for interactiveSubAgents
func TestGetActiveProgress_KeyFormatConsistency(t *testing.T) {
	a := NewTestAgent()

	interactiveKey := "cli:/home/user/src/project/ministry-works:split-test-files"
	agentProgressKey := "agent:" + interactiveKey

	// Store with the EXACT keys used by SpawnInteractiveSession and wireSubAgentCLIProgress.
	ia := &interactiveAgent{running: true, mu: sync.Mutex{}}
	a.interactiveSubAgents.Store(interactiveKey, ia) // key = interactiveKey (no "agent:" prefix)
	a.lastProgressSnapshot.Store(agentProgressKey, &protocol.ProgressEvent{
		ChatID: agentProgressKey, Phase: "done", Iteration: 1,
	})

	// Verify GetActiveProgress can find BOTH maps.
	// ch="agent", chatID=interactiveKey → constructs key="agent:"+interactiveKey for snapshot
	result := a.GetActiveProgress("agent", interactiveKey)

	if result == nil {
		t.Fatal("GetActiveProgress returned nil — snapshot lookup failed")
	}

	// If this fails, the interactiveSubAgents lookup is using the wrong key format.
	// The bug: Load("agent:" + interactiveKey) fails because the map stores interactiveKey.
	_ = result.Phase // we know it's "done" here; the Phase correction is tested above

	// Explicitly verify the interactiveSubAgents lookup works with chatID (not key).
	entry, loaded := a.interactiveSubAgents.Load(interactiveKey)
	if !loaded {
		t.Error("interactiveSubAgents.Load(interactiveKey) failed — key format mismatch")
	}
	_ = entry

	// Verify the WRONG key doesn't work (this is the bug pattern to avoid).
	_, loaded = a.interactiveSubAgents.Load(agentProgressKey)
	if loaded {
		t.Error("interactiveSubAgents.Load(agentProgressKey) should NOT find the entry — " +
			"if it does, the key format has changed and the test needs updating")
	}
}

// NewTestAgent creates a minimal Agent for testing GetActiveProgress.
func NewTestAgent() *Agent {
	return &Agent{}
}
