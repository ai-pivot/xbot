package agent

import (
	"testing"
)

// TestSnapshotIterationTokens_PerCallNotCumulative reproduces the "iteration
// tokens recorded as 0" bug: TokenTracker.RecordLLMCall OVERWRITES completion
// with each call's own value (per-call semantics — the API reports this
// call's completion_tokens, not a cumulative counter). But
// snapshotCompletedIteration computed snap.Tokens as a DELTA against the
// tracker (cur - lastSnapshotCompletionTokens) under a cumulative-value
// assumption. When iteration N-1 produced 500 completion tokens and
// iteration N (a small tool call, e.g. 25 tokens) follows,
// cur(25) < lastSnapshot(500) → the delta guard clamped snap.Tokens to 0.
//
// DB evidence: ~50-66% of iteration_history rows have tokens=0 across v58
// (pre-v59) and current rows — small-output iterations following
// large-output ones lose their tokens. input_tokens/cached_tokens (v59
// accumulators, += per call) are correct — only the delta-based tokens is
// wrong. Fix: per-iteration OUTPUT accumulator (iterOutputTokens, += per
// call in callLLM, reset at beginIteration) — same semantics as
// iterInputTokens.
func TestSnapshotIterationTokens_PerCallNotCumulative(t *testing.T) {
	s := &runState{
		cfg:                RunConfig{},
		tokenTracker:       NewTokenTracker(0, 0),
		structuredProgress: &StructuredProgress{},
	}

	// Iteration 1: a large-output LLM call (long reasoning, 500 tokens).
	s.tokenTracker.RecordLLMCall(1000, 500)
	s.iterOutputTokens = 500 // fix field: += per call in callLLM
	s.snapshotCompletedIteration(1)
	if len(s.iterationSnapshots) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(s.iterationSnapshots))
	}
	if got := s.iterationSnapshots[0].Tokens; got != 500 {
		t.Errorf("iteration 1 Tokens = %d, want 500", got)
	}

	// Iteration 2: a SMALL tool-call iteration (25 tokens) after the large one.
	// The tracker now holds 25 (RecordLLMCall overwrites, not accumulates);
	// the old delta logic computed 25 - 500 < 0 → clamped to 0 (the bug).
	s.tokenTracker.RecordLLMCall(1050, 25)
	s.iterOutputTokens = 25
	s.snapshotCompletedIteration(2)
	if len(s.iterationSnapshots) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(s.iterationSnapshots))
	}
	if got := s.iterationSnapshots[1].Tokens; got != 25 {
		t.Errorf("iteration 2 Tokens = %d, want 25 (small-output iteration after a large one must record its own real value, not a clamped delta)", got)
	}

	// Iteration 3: output grows again (300) — delta logic would give
	// 300-25=275 (WRONG, not the call's own value); the accumulator gives 300.
	s.tokenTracker.RecordLLMCall(1100, 300)
	s.iterOutputTokens = 300
	s.snapshotCompletedIteration(3)
	if got := s.iterationSnapshots[2].Tokens; got != 300 {
		t.Errorf("iteration 3 Tokens = %d, want 300 (per-call value, not tracker delta)", got)
	}
}
