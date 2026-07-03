package cli

import (
	"testing"

	"xbot/protocol"
)

// TestLinearConsistency_StaleStreamEventDiscarded verifies that a stale
// stream-only event (lower Seq than already-applied snapshot) is discarded.
//
// Scenario:
//  1. Tick pull applies iteration=3 snapshot with Seq=102
//  2. Stale stream event (Seq=99) arrives from progressCh coalescing delay
//  3. The stale event must NOT overwrite the current state
func TestLinearConsistency_StaleStreamEventDiscarded(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Simulate tick pull: apply a complete snapshot with Seq=102
	model.applyProgressSnapshot(&protocol.ProgressEvent{
		ChatID:        "cli:/test",
		Seq:           102,
		Phase:         "thinking",
		Iteration:     3,
		StreamContent: "hello world from tick",
	})

	if model.progressState.current.StreamContent != "hello world from tick" {
		t.Fatalf("expected tick content, got %q", model.progressState.current.StreamContent)
	}

	// Stale stream-only event arrives (Seq=99 < lastAppliedSeq=102)
	model.handleProgressMsg(cliProgressMsg{
		payload: &protocol.ProgressEvent{
			ChatID:        "cli:/test",
			Seq:           99,
			StreamContent: "STALE - should be discarded",
		},
	})

	// The stale event must have been discarded
	if model.progressState.current.StreamContent == "STALE - should be discarded" {
		t.Fatal("stale stream event (Seq=99) overwrote newer snapshot (Seq=102)")
	}
	if model.progressState.current.StreamContent != "hello world from tick" {
		t.Fatalf("current content corrupted by stale event: got %q",
			model.progressState.current.StreamContent)
	}
}

// TestLinearConsistency_NewTurnResetsSeq verifies that a new agent turn
// resets lastAppliedSeq so events from the new turn are accepted.
//
// Scenario:
//  1. Turn 1 ends with lastAppliedSeq=500
//  2. Turn 2 starts (startAgentTurn resets lastAppliedSeq to 0)
//  3. Turn 2's first event (Seq=1) must be accepted, not blocked
func TestLinearConsistency_NewTurnResetsSeq(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Turn 1: apply events up to Seq=500
	model.applyProgressSnapshot(&protocol.ProgressEvent{
		ChatID:    "cli:/test",
		Seq:       500,
		Phase:     "done",
		Iteration: 1,
	})

	if model.progressState.lastAppliedSeq != 500 {
		t.Fatalf("expected lastAppliedSeq=500, got %d", model.progressState.lastAppliedSeq)
	}

	// Turn 2 starts — must reset lastAppliedSeq
	model.startAgentTurn()

	if model.progressState.lastAppliedSeq != 0 {
		t.Fatalf("lastAppliedSeq not reset on new turn: got %d, want 0",
			model.progressState.lastAppliedSeq)
	}

	// Turn 2's first event (Seq=1) must be accepted
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
	if model.progressState.current.Phase != "thinking" {
		t.Fatalf("turn 2 first event not applied: Phase=%q",
			model.progressState.current.Phase)
	}
}

// TestLinearConsensity_TickPullOverwritesStalePush verifies that tick pull
// (complete snapshot) correctly overrides any stale push events that were
// applied between ticks.
//
// Scenario:
//  1. Push event: iteration=2, Seq=100
//  2. Stream-only push: Seq=101, StreamContent="partial"
//  3. Tick pull: iteration=3 snapshot, Seq=102, StreamContent="new iter"
//  4. State must show iteration=3 with correct stream content
func TestLinearConsistency_TickPullOverwritesStalePush(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Push: iteration=2 structured event
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 2,
		Seq:       100,
	})

	// Push: stream-only event (same iteration)
	sendProgress(model, &protocol.ProgressEvent{
		StreamContent: "partial text from iter 2",
		Seq:           101,
	})

	if model.progressState.current.StreamContent != "partial text from iter 2" {
		t.Fatalf("stream content not applied: got %q",
			model.progressState.current.StreamContent)
	}

	// Tick pull: iteration=3 snapshot (backend moved on)
	model.applyProgressSnapshot(&protocol.ProgressEvent{
		ChatID:        "cli:/test",
		Seq:           102,
		Phase:         "thinking",
		Iteration:     3,
		StreamContent: "new iteration 3 content",
	})

	// State must reflect the tick pull, not the stale push
	if model.progressState.current.Iteration != 3 {
		t.Fatalf("iteration not updated by tick pull: got %d, want 3",
			model.progressState.current.Iteration)
	}
	if model.progressState.current.StreamContent != "new iteration 3 content" {
		t.Fatalf("stream content not updated by tick pull: got %q",
			model.progressState.current.StreamContent)
	}

	// A stale stream event (Seq=99) must now be discarded
	model.handleProgressMsg(cliProgressMsg{
		payload: &protocol.ProgressEvent{
			ChatID:        "cli:/test",
			Seq:           99,
			StreamContent: "STALE",
		},
	})

	if model.progressState.current.StreamContent == "STALE" {
		t.Fatal("stale stream event overwrote tick pull snapshot")
	}
}

// TestLinearConsistency_FreshStreamEventAccepted verifies that a fresh
// stream-only event (Seq > lastAppliedSeq) IS accepted after tick pull.
func TestLinearConsistency_FreshStreamEventAccepted(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Tick pull: Seq=100
	model.applyProgressSnapshot(&protocol.ProgressEvent{
		ChatID:    "cli:/test",
		Seq:       100,
		Phase:     "thinking",
		Iteration: 2,
	})

	// Fresh stream event: Seq=101 > 100 → must be accepted
	model.handleProgressMsg(cliProgressMsg{
		payload: &protocol.ProgressEvent{
			ChatID:        "cli:/test",
			Seq:           101,
			StreamContent: "fresh content",
		},
	})

	if model.progressState.current.StreamContent != "fresh content" {
		t.Fatalf("fresh stream event not accepted: got %q",
			model.progressState.current.StreamContent)
	}
	if model.progressState.lastAppliedSeq != 101 {
		t.Fatalf("lastAppliedSeq not updated: got %d, want 101",
			model.progressState.lastAppliedSeq)
	}
}
