package cli

import (
	"testing"

	"xbot/protocol"
)

// TestLinearConsistency_StaleStreamEventDiscarded verifies that a stale
// stream-only event (lower Seq than already-applied stream event) is discarded.
//
// Stream events use a separate lastStreamSeq counter, NOT lastAppliedSeq.
func TestLinearConsistency_StaleStreamEventDiscarded(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// First stream event: Seq=102
	model.handleProgressMsg(cliProgressMsg{
		payload: &protocol.ProgressEvent{
			ChatID:        "cli:/test",
			Seq:           102,
			StreamContent: "hello world from stream",
		},
	})

	if model.progressState.current == nil || model.progressState.current.StreamContent != "hello world from stream" {
		t.Fatalf("first stream event not applied")
	}

	// Stale stream-only event (Seq=99 < lastStreamSeq=102)
	model.handleProgressMsg(cliProgressMsg{
		payload: &protocol.ProgressEvent{
			ChatID:        "cli:/test",
			Seq:           99,
			StreamContent: "STALE - should be discarded",
		},
	})

	if model.progressState.current.StreamContent == "STALE - should be discarded" {
		t.Fatal("stale stream event (Seq=99) overwrote newer stream (Seq=102)")
	}
}

// TestLinearConsistency_NewTurnResetsSeq verifies that a new agent turn
// resets both lastAppliedSeq and lastStreamSeq.
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
	if model.progressState.lastStreamSeq != 0 {
		t.Fatalf("lastStreamSeq not reset: got %d", model.progressState.lastStreamSeq)
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

// TestLinearConsistency_StreamEventsDoNotBlockTickPull verifies the core fix:
// stream events (high Seq) must NOT block tick pull (lower Structured Seq).
//
// Without this fix, stream events inflate lastAppliedSeq, permanently
// blocking tick pull — the client never sees iteration changes.
func TestLinearConsistency_StreamEventsDoNotBlockTickPull(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Structured event: Seq=1, iteration 0
	model.applyProgressSnapshot(&protocol.ProgressEvent{
		ChatID:    "cli:/test",
		Seq:       1,
		Phase:     "thinking",
		Iteration: 0,
	})

	// Stream events flood: Seq 2..100 (reasoning chunks for iteration 0)
	for i := uint64(2); i <= 100; i++ {
		model.handleProgressMsg(cliProgressMsg{
			payload: &protocol.ProgressEvent{
				ChatID:        "cli:/test",
				Seq:           i,
				StreamContent: "reasoning chunk",
			},
		})
	}

	// lastStreamSeq should be 100, but lastAppliedSeq should still be 1
	if model.progressState.lastStreamSeq != 100 {
		t.Fatalf("lastStreamSeq should be 100, got %d", model.progressState.lastStreamSeq)
	}
	if model.progressState.lastAppliedSeq != 1 {
		t.Fatalf("lastAppliedSeq should be 1 (not inflated by stream events), got %d",
			model.progressState.lastAppliedSeq)
	}

	// Tick pull: backend moved to iteration 3, structured snapshot Seq=2
	// 2 > 1 (lastAppliedSeq) → MUST be accepted, NOT blocked by lastStreamSeq=100
	model.applyProgressSnapshot(&protocol.ProgressEvent{
		ChatID:    "cli:/test",
		Seq:       2,
		Phase:     "thinking",
		Iteration: 3,
	})

	if model.progressState.current == nil {
		t.Fatal("tick pull was blocked by stream event Seq inflation")
	}
	if model.progressState.current.Iteration != 3 {
		t.Fatalf("tick pull not applied: Iteration=%d, want 3",
			model.progressState.current.Iteration)
	}
}

// TestLinearConsistency_FreshStreamEventAccepted verifies that a fresh
// stream-only event (Seq > lastStreamSeq) IS accepted after tick pull.
func TestLinearConsistency_FreshStreamEventAccepted(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Tick pull: Seq=100 (structured)
	model.applyProgressSnapshot(&protocol.ProgressEvent{
		ChatID:    "cli:/test",
		Seq:       100,
		Phase:     "thinking",
		Iteration: 2,
	})

	// Fresh stream event: Seq=101 > lastStreamSeq(0) → must be accepted
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
	if model.progressState.lastStreamSeq != 101 {
		t.Fatalf("lastStreamSeq not updated: got %d, want 101",
			model.progressState.lastStreamSeq)
	}
	// lastAppliedSeq should NOT be updated by stream events
	if model.progressState.lastAppliedSeq != 100 {
		t.Fatalf("lastAppliedSeq should still be 100 (not updated by stream), got %d",
			model.progressState.lastAppliedSeq)
	}
}
