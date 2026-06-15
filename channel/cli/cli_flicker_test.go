package cli

import (
	"strings"
	"testing"

	"xbot/protocol"
)

// TestReasoningNoContaminationOnIterationChange verifies that stream-only
// reasoning for iteration N+1 does NOT contaminate iteration N's snapshot.
//
// Root cause: snapshotIterationChange used prev.ReasoningStreamContent as
// reasoning fallback. Stream-only events for N+1 arrive BEFORE the structured
// event for N+1, updating ReasoningStreamContent while current.Iteration
// is still N.
//
// Fix: removed prev.ReasoningStreamContent fallback entirely. Only structured
// sources (prev.Reasoning, reasoningByIter, lastReasoning) are used.
// If structured Reasoning is not available, the snapshot has empty reasoning —
// better to have no reasoning than WRONG reasoning.
func TestReasoningNoContaminationOnIterationChange(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Iteration 1: structured event with reasoning
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 1,
		Reasoning: "structured reasoning for iter 1",
		ChatID:    "cli:/test",
	})

	// Stream reasoning for iter 2 arrives (contaminates current.RSC)
	sendProgress(model, &protocol.ProgressEvent{
		ReasoningStreamContent: "stream reasoning for iter 2 (arrives early)",
		ChatID:                 "cli:/test",
	})

	// Structured event for iter 2 triggers snapshot of iter 1
	sendProgress(model, &protocol.ProgressEvent{
		Iteration: 2,
		ChatID:    "cli:/test",
	})

	for _, snap := range model.progressState.iterations {
		if snap.Iteration == 1 {
			if strings.Contains(snap.Reasoning, "iter 2") {
				t.Errorf("BUG: Iteration 1 snapshot contaminated with iteration 2's reasoning: %q",
					snap.Reasoning)
			}
			if snap.Reasoning != "structured reasoning for iter 1" {
				t.Errorf("expected structured reasoning, got %q", snap.Reasoning)
			}
			t.Logf("FIXED: Iteration 1 snapshot reasoning = %q (no contamination)", snap.Reasoning)
		}
	}
}

// TestReasoningNoContaminationMultiIter verifies no contamination across
// multiple iteration transitions.
func TestReasoningNoContaminationMultiIter(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Iteration 1 with structured reasoning
	sendProgress(model, &protocol.ProgressEvent{
		Iteration: 1,
		Reasoning: "iter1 structured reasoning",
		ChatID:    "cli:/test",
	})

	// Stream reasoning for iter 2 (contaminates current)
	sendProgress(model, &protocol.ProgressEvent{
		ReasoningStreamContent: "iter2 stream reasoning (early)",
		ChatID:                 "cli:/test",
	})

	// Structured event for iter 2
	sendProgress(model, &protocol.ProgressEvent{
		Iteration: 2,
		Reasoning: "iter2 structured reasoning",
		ChatID:    "cli:/test",
	})

	// Stream reasoning for iter 3 (contaminates current)
	sendProgress(model, &protocol.ProgressEvent{
		ReasoningStreamContent: "iter3 stream reasoning (early)",
		ChatID:                 "cli:/test",
	})

	// Structured event for iter 3
	sendProgress(model, &protocol.ProgressEvent{
		Iteration: 3,
		ChatID:    "cli:/test",
	})

	for _, snap := range model.progressState.iterations {
		switch snap.Iteration {
		case 1:
			if snap.Reasoning != "iter1 structured reasoning" {
				t.Errorf("Iter 1: expected structured reasoning, got %q", snap.Reasoning)
			}
		case 2:
			if snap.Reasoning != "iter2 structured reasoning" {
				t.Errorf("Iter 2: expected structured reasoning, got %q", snap.Reasoning)
			}
		}
		t.Logf("Iteration %d snapshot reasoning: %q", snap.Iteration, snap.Reasoning)
	}
}

// TestReasoningStructuredPriority verifies structured Reasoning takes priority.
func TestReasoningStructuredPriority(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	sendProgress(model, &protocol.ProgressEvent{
		Iteration:              1,
		Reasoning:              "structured (authoritative)",
		ReasoningStreamContent: "stream (partial)",
		ChatID:                 "cli:/test",
	})

	sendProgress(model, &protocol.ProgressEvent{
		Iteration: 2,
		ChatID:    "cli:/test",
	})

	for _, snap := range model.progressState.iterations {
		if snap.Iteration == 1 {
			if snap.Reasoning != "structured (authoritative)" {
				t.Errorf("structured Reasoning should take priority. Got: %q", snap.Reasoning)
			}
			t.Logf("CORRECT: structured reasoning took priority: %q", snap.Reasoning)
		}
	}
}

// TestReasoningPhaseDoneNoContamination verifies the PhaseDone path is also
// free from ReasoningStreamContent contamination.
func TestReasoningPhaseDoneNoContamination(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Iteration 1 with structured reasoning
	sendProgress(model, &protocol.ProgressEvent{
		Iteration: 1,
		Reasoning: "iter1 structured reasoning",
		ChatID:    "cli:/test",
	})

	// Stream reasoning that would contaminate (if bug exists)
	sendProgress(model, &protocol.ProgressEvent{
		ReasoningStreamContent: "late stream reasoning (potential contaminant)",
		ChatID:                 "cli:/test",
	})

	// PhaseDone — iterations move to pendingToolSummary, then cleared
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "done",
		Iteration: 1,
		ChatID:    "cli:/test",
	})

	// Check pendingToolSummary (iterations are moved there by handleProgressDone)
	if model.pendingToolSummary == nil {
		t.Fatal("pendingToolSummary should be set after PhaseDone")
	}
	found := false
	for _, snap := range model.pendingToolSummary.iterations {
		if snap.Iteration == 1 {
			found = true
			if strings.Contains(snap.Reasoning, "potential contaminant") {
				t.Errorf("BUG: PhaseDone snapshot contaminated: %q", snap.Reasoning)
			}
			if snap.Reasoning != "iter1 structured reasoning" {
				t.Errorf("PhaseDone reasoning mismatch: got %q", snap.Reasoning)
			}
			t.Logf("FIXED: PhaseDone snapshot reasoning = %q", snap.Reasoning)
		}
	}
	if !found {
		t.Error("PhaseDone should have created a snapshot for iteration 1 in pendingToolSummary")
	}
}
