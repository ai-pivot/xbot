package cli

import (
	"testing"

	"xbot/protocol"
)

// TestLinearConsistency_NewTurnResetsSeq verifies that a new agent turn
// resets lastAppliedSeq so events from the new turn are accepted.
func TestLinearConsistency_NewTurnResetsSeq(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	model.applyProgressSnapshot(&protocol.ProgressEvent{
		ChatID:    "cli:/test",
		Seq:       500,
		Phase:     "done",
		Iteration: 1,
	})

	if model.progressState.lastAppliedSeq != 500 {
		t.Fatalf("expected lastAppliedSeq=500, got %d", model.progressState.lastAppliedSeq)
	}

	model.startAgentTurn()

	if model.progressState.lastAppliedSeq != 0 {
		t.Fatalf("lastAppliedSeq not reset: got %d", model.progressState.lastAppliedSeq)
	}

	model.handleProgressMsg(cliProgressMsg{
		payload: &protocol.ProgressEvent{
			ChatID:    "cli:/test",
			Seq:       1,
			Phase:     "thinking",
			Iteration: 0,
		},
	})

	if model.progressState.current == nil {
		t.Fatal("turn 2 first event was blocked by stale lastAppliedSeq")
	}
}

// TestLinearConsistency_StaleStructuredEventDiscarded verifies that a stale
// structured event (lower Seq than already-applied) is discarded.
func TestLinearConsistency_StaleStructuredEventDiscarded(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Apply snapshot Seq=10
	model.applyProgressSnapshot(&protocol.ProgressEvent{
		ChatID:    "cli:/test",
		Seq:       10,
		Phase:     "thinking",
		Iteration: 2,
	})

	// Stale event Seq=5 → must be discarded
	model.handleProgressMsg(cliProgressMsg{
		payload: &protocol.ProgressEvent{
			ChatID:    "cli:/test",
			Seq:       5,
			Phase:     "tool_exec",
			Iteration: 1,
		},
	})

	if model.progressState.current.Iteration != 2 {
		t.Fatalf("stale event overwrote: got Iteration=%d, want 2",
			model.progressState.current.Iteration)
	}
}

// TestLinearConsistency_TickPullOverwritesStalePush verifies that tick pull
// (complete snapshot) correctly overrides any stale push events.
func TestLinearConsistency_TickPullOverwritesStalePush(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Push: iteration=2
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 2,
		Seq:       100,
	})

	// Tick pull: iteration=3
	model.applyProgressSnapshot(&protocol.ProgressEvent{
		ChatID:    "cli:/test",
		Seq:       101,
		Phase:     "thinking",
		Iteration: 3,
	})

	if model.progressState.current.Iteration != 3 {
		t.Fatalf("tick pull not applied: got Iteration=%d, want 3",
			model.progressState.current.Iteration)
	}
}

// TestLinearConsistency_FreshEventAccepted verifies that a fresh
// event (Seq > lastAppliedSeq) IS accepted after tick pull.
func TestLinearConsistency_FreshEventAccepted(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Tick pull: Seq=100
	model.applyProgressSnapshot(&protocol.ProgressEvent{
		ChatID:    "cli:/test",
		Seq:       100,
		Phase:     "thinking",
		Iteration: 2,
	})

	// Fresh event: Seq=101 > 100 → must be accepted
	model.handleProgressMsg(cliProgressMsg{
		payload: &protocol.ProgressEvent{
			ChatID:    "cli:/test",
			Seq:       101,
			Phase:     "tool_exec",
			Iteration: 2,
		},
	})

	if model.progressState.current.Phase != "tool_exec" {
		t.Fatalf("fresh event not applied: Phase=%q",
			model.progressState.current.Phase)
	}
}
