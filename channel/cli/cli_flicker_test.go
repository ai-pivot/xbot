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
// sources (prev.Reasoning) are used.
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

// TestReasoningBoxVisibleDuringToolExec verifies that the reasoning box
// remains visible in the live iteration when structured progress events
// (sent during tool execution) carry Reasoning but NOT ReasoningStreamContent.
//
// Root cause: liveIterationBlocks only checked ReasoningStreamContent,
// ignoring the structured Reasoning field. During tool execution, every
// notifyProgress sends structured events without ReasoningStreamContent.
// The reasoning box would disappear until carryForwardProgressState
// restored ReasoningStreamContent (if it could), causing a visible flicker.
//
// Fix: fall back to structured Reasoning when ReasoningStreamContent is empty.
func TestReasoningBoxVisibleDuringToolExec(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Simulate reasoning streaming (DeepSeek-style reasoning_content)
	sendProgress(model, &protocol.ProgressEvent{
		ReasoningStreamContent: "I need to search for the file first.",
		ChatID:                 "cli:/test",
	})

	// Verify reasoning box is visible during streaming
	blocks := model.liveIterationBlocks(model.progressState.current, 80, "")
	found := false
	for _, b := range blocks {
		if b.kind == turnBlockReasoning && strings.Contains(b.text, "search for the file") {
			found = true
		}
	}
	if !found {
		t.Error("reasoning box should be visible during streaming (ReasoningStreamContent)")
	}

	// Structured event after LLM completes: has Reasoning, no ReasoningStreamContent.
	// This is exactly what notifyProgress sends during tool execution.
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 1,
		Reasoning: "I need to search for the file first.",
		ActiveTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "find . -name '*.go'", Status: "running"},
		},
		ChatID: "cli:/test",
	})

	// Verify reasoning box is STILL visible via structured Reasoning fallback
	blocks = model.liveIterationBlocks(model.progressState.current, 80, "")
	found = false
	for _, b := range blocks {
		if b.kind == turnBlockReasoning && strings.Contains(b.text, "search for the file") {
			found = true
		}
	}
	if !found {
		t.Error("BUG: reasoning box disappeared during tool execution — " +
			"liveIterationBlocks should fall back to structured Reasoning " +
			"when ReasoningStreamContent is empty")
	} else {
		t.Log("FIXED: reasoning box remains visible via structured Reasoning fallback")
	}
}

// TestReasoningBoxVisibleAcrossMultipleToolProgress verifies the reasoning
// box stays visible across multiple structured progress events (each tool
// execution step sends a notifyProgress).
func TestReasoningBoxVisibleAcrossMultipleToolProgress(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Reasoning streaming
	sendProgress(model, &protocol.ProgressEvent{
		ReasoningStreamContent: "Step 1: read the config. Step 2: validate.",
		ChatID:                 "cli:/test",
	})

	reasoning := "Step 1: read the config. Step 2: validate."

	// Multiple structured events during tool execution (each carries Reasoning)
	for _, tools := range [][]protocol.ToolProgress{
		{{Name: "Read", Label: "config.yaml", Status: "running"}},
		{{Name: "Read", Label: "config.yaml", Status: "done"}},
		{{Name: "Shell", Label: "validate", Status: "running"}},
	} {
		sendProgress(model, &protocol.ProgressEvent{
			Phase:       "tool_exec",
			Iteration:   1,
			Reasoning:   reasoning,
			ActiveTools: tools,
			ChatID:      "cli:/test",
		})

		blocks := model.liveIterationBlocks(model.progressState.current, 80, "")
		found := false
		for _, b := range blocks {
			if b.kind == turnBlockReasoning && strings.Contains(b.text, "Step 1") {
				found = true
			}
		}
		if !found {
			t.Errorf("reasoning box should stay visible across all tool progress events; "+
				"missing after tools=%v", tools)
		}
	}
	t.Log("FIXED: reasoning box stays visible across all tool execution progress events")
}

// TestReasoningPhaseDoneNoContamination verifies the PhaseDone path is also
// free from ReasoningStreamContent contamination.

// ==================== Iteration Transition Flicker Tests ====================
//
// These tests verify the fix for the flicker bug where an iteration would
// disappear for one frame and then reappear when transitioning from live
// (in-progress) to completed (snapshotted).
//
// Root cause: restoreIterationsFromSnapshot did a full rebuild (discarding
// existing iterations and recreating from backend snapshot) + called
// invalidateProgress() which cleared streamPrefixLen, forcing updateStreamingOnly
// to take the slow path (full prefix rebuild) on the next tick.
//
// Fix: incremental append (only add new iterations) + no invalidateProgress.

// TestRestoreIterations_IncrementalAppend verifies that restoring iterations
// from a snapshot only appends new iterations without rebuilding existing ones.
// This is critical for flicker prevention: existing iterations' rendered cache
// lines (streamCompletedLines) remain valid.
func TestRestoreIterations_IncrementalAppend(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Simulate push events that create iteration 1 via live rendering
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 1,
		Reasoning: "reasoning for iter 1",
		Content:   "content for iter 1",
		ChatID:    "cli:/test",
	})

	// Simulate tick pull carrying IterationHistory with 1 entry
	sendProgressWithHistory(model, &protocol.ProgressEvent{
		Iteration: 2,
		ChatID:    "cli:/test",
	}, protocol.ProgressEvent{
		Iteration:      1,
		Reasoning:      "reasoning for iter 1",
		Content:        "content for iter 1",
		CompletedTools: []protocol.ToolProgress{{Name: "Shell", Label: "echo hi", Status: "done"}},
	})

	if len(model.progressState.iterations) != 1 {
		t.Fatalf("expected 1 completed iteration, got %d", len(model.progressState.iterations))
	}

	iter1 := model.progressState.iterations[0]
	if iter1.Content != "content for iter 1" {
		t.Errorf("iter 1 content = %q, want %q", iter1.Content, "content for iter 1")
	}
	if iter1.Reasoning != "reasoning for iter 1" {
		t.Errorf("iter 1 reasoning = %q, want %q", iter1.Reasoning, "reasoning for iter 1")
	}

	// Now add iteration 2 to history — should append, not rebuild
	sendProgressWithHistory(model, &protocol.ProgressEvent{
		Iteration: 3,
		ChatID:    "cli:/test",
	}, protocol.ProgressEvent{
		Iteration: 1,
		Content:   "content for iter 1",
		Reasoning: "reasoning for iter 1",
	}, protocol.ProgressEvent{
		Iteration: 2,
		Content:   "content for iter 2",
		Reasoning: "reasoning for iter 2",
	})

	if len(model.progressState.iterations) != 2 {
		t.Fatalf("expected 2 completed iterations, got %d", len(model.progressState.iterations))
	}

	// Iteration 1 should be UNCHANGED (not rebuilt)
	if model.progressState.iterations[0].Content != "content for iter 1" {
		t.Errorf("iter 1 content changed after incremental append: %q",
			model.progressState.iterations[0].Content)
	}

	// Iteration 2 should be the new one
	if model.progressState.iterations[1].Content != "content for iter 2" {
		t.Errorf("iter 2 content = %q, want %q",
			model.progressState.iterations[1].Content, "content for iter 2")
	}
}

// TestRestoreIterations_PreservesExistingDataAcrossTicks verifies that
// data from push events is NOT lost when a tick pull brings IterationHistory.
// The old full-rebuild code would replace push-derived data with DB-derived
// data, potentially losing field precision (e.g. ElapsedWall).
func TestRestoreIterations_PreservesExistingDataAcrossTicks(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// Push event for iteration 1 with rich data
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 1,
		Content:   "push content",
		Reasoning: "push reasoning",
		ActiveTools: []protocol.ToolProgress{
			{Name: "Read", Label: "file.go", Status: "running"},
		},
		ChatID: "cli:/test",
	})

	// Tick pull: same iteration 1 now in IterationHistory (tool completed)
	sendProgressWithHistory(model, &protocol.ProgressEvent{
		Iteration: 2,
		ChatID:    "cli:/test",
	}, protocol.ProgressEvent{
		Iteration: 1,
		Content:   "push content",
		Reasoning: "push reasoning",
		CompletedTools: []protocol.ToolProgress{
			{Name: "Read", Label: "file.go", Status: "done", Summary: "42 lines"},
		},
	})

	if len(model.progressState.iterations) != 1 {
		t.Fatalf("expected 1 iteration, got %d", len(model.progressState.iterations))
	}

	iter := model.progressState.iterations[0]
	// Content and reasoning should match (from IterationHistory, which is authoritative)
	if iter.Content != "push content" {
		t.Errorf("content = %q, want %q", iter.Content, "push content")
	}
	if iter.Reasoning != "push reasoning" {
		t.Errorf("reasoning = %q, want %q", iter.Reasoning, "push reasoning")
	}
	// Tools should come from the IterationHistory snapshot
	if len(iter.Tools) != 1 || iter.Tools[0].Name != "Read" {
		t.Errorf("expected 1 tool (Read), got %+v", iter.Tools)
	}
	if iter.Tools[0].Status != "done" {
		t.Errorf("tool status = %q, want %q", iter.Tools[0].Status, "done")
	}
}

// TestRestoreIterations_IdempotentWhenCountMatches verifies that calling
// restoreIterationsFromSnapshot with the same count does nothing —
// no append, no rebuild, no cache invalidation.
func TestRestoreIterations_IdempotentWhenCountMatches(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()

	// First restore: 1 iteration
	sendProgressWithHistory(model, &protocol.ProgressEvent{
		Iteration: 2,
		ChatID:    "cli:/test",
	}, protocol.ProgressEvent{
		Iteration: 1,
		Content:   "original content",
	})

	if len(model.progressState.iterations) != 1 {
		t.Fatalf("expected 1 iteration after first restore, got %d", len(model.progressState.iterations))
	}

	originalSlice := model.progressState.iterations[0]

	// Second restore: same count (1 iteration), same data
	sendProgressWithHistory(model, &protocol.ProgressEvent{
		Iteration: 2,
		ChatID:    "cli:/test",
	}, protocol.ProgressEvent{
		Iteration: 1,
		Content:   "original content",
	})

	// Should still be exactly 1 iteration
	if len(model.progressState.iterations) != 1 {
		t.Fatalf("expected 1 iteration after idempotent restore, got %d", len(model.progressState.iterations))
	}

	// Data should be unchanged
	if model.progressState.iterations[0].Content != originalSlice.Content {
		t.Errorf("content changed during idempotent restore: %q vs %q",
			model.progressState.iterations[0].Content, originalSlice.Content)
	}
}
