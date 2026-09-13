package agent

// 2026-09-13: the compression circuit breaker ("fuse") was DELETED by user
// decision. It used to set compressAbandoned (disabling compression for the
// REST OF THE RUN) after
//   (a) two consecutive AUTO triggers whose real API prompt_tokens had not
//       dropped ≥5% (consecutiveIneffectiveCompress / lastCompressTriggerTokens), or
//   (b) a post-compress + post-truncation context still above the trigger line.
// The flag gated the trigger itself, so a model-requested compaction
// (`compact_context`) was silently vetoed too — the engine lost its only way
// back. Overflow is already impossible without the fuse (error-driven forcible
// compression: handleInputTooLong / context_window_exceeded, plus the
// post-compress truncation safety net), and a fuse that stops compressing is
// strictly worse than the loop it prevented.
//
// These two tests are the behavioral contract that replaced it:
//   ① no latch — ineffective auto compressions never disable compression;
//   ② an explicit request is always executed.

import (
	"context"
	"testing"

	"xbot/llm"
)

// latchMsgs is the minimal 4-message conversation maybeCompress wants
// (system + 3 turns, above the len(s.messages) > 3 gate).
func latchMsgs() []llm.ChatMessage {
	return []llm.ChatMessage{
		llm.NewSystemMessage("system"),
		llm.NewUserMessage("hello"),
		llm.NewAssistantMessage("hi"),
		llm.NewUserMessage("do a long task"),
	}
}

// smallResultCM returns a ContextManager whose compressed output stays far
// below the trigger line (≈21k tokens vs ≈91.5k) — no aggressiveTruncate and no
// post-compress warning, so the ONLY thing these tests exercise is the trigger
// gate itself. calls counts real compression runs.
func smallResultCM(calls *int) *mockContextManager {
	return &mockContextManager{
		compressFn: func(_ context.Context, _ []llm.ChatMessage, _ llm.LLM, _ string, _ int64) (*CompressResult, error) {
			*calls++
			return &CompressResult{
				LLMView:          bigLLMView(30000, 4, 100),
				CompressedTokens: 500,
			}, nil
		},
	}
}

// driveIterations runs maybeCompress n times (cooldown bookkeeping without
// changing the real prompt_tokens in between).
func driveIterations(t *testing.T, state *runState, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := state.maybeCompress(context.Background()); err != nil {
			t.Fatalf("maybeCompress (driver iteration %d/%d): %v", i+1, n, err)
		}
	}
}

// Test ① — there is NO latch: two consecutive AUTO compressions that made no
// progress (real prompt_tokens never dropped ≥5%) must NOT disable compression
// for the rest of the Run. The next auto trigger, after the 5-iteration
// cooldown, still compresses.
//
// RED before the fuse removal: the 3rd trigger returned nil without compressing
// (compressAbandoned=true), so compression calls stopped at 2 and stayed there.
func TestMaybeCompress_AutoHasNoLatch(t *testing.T) {
	compressCalls := 0
	cm := smallResultCM(&compressCalls)
	state := newCompressLoopState(t, cm, latchMsgs(), 190000) // over the ≈91.5k trigger line

	// auto #1
	if err := state.maybeCompress(context.Background()); err != nil {
		t.Fatalf("auto #1: %v", err)
	}
	// The compaction did not drop the real prompt_tokens ≥5% — "ineffective".
	state.tokenTracker.RecordLLMCall(190500, 100)
	driveIterations(t, state, 5) // cooldown expiry → auto #2 (ineffective #2)
	if compressCalls != 2 {
		t.Fatalf("setup: want 2 auto compressions before the latch point, got %d", compressCalls)
	}

	// auto #3 — the old fuse latched here; now it must run.
	state.tokenTracker.RecordLLMCall(190200, 100)
	driveIterations(t, state, 5)
	if compressCalls != 3 {
		t.Errorf("BUG REPRODUCED: two ineffective AUTO compressions latched compression OFF for the rest of the Run — "+
			"compression calls = %d after the third over-threshold trigger, want 3. "+
			"No-progress auto compression must stay bounded by the 5-iteration cooldown, never by a permanent fuse.", compressCalls)
	}

	// ...and it keeps running: the cooldown throttles, it does not disable.
	state.tokenTracker.RecordLLMCall(190800, 100)
	driveIterations(t, state, 5)
	if compressCalls != 4 {
		t.Errorf("compression must still be available after repeated ineffective compactions — got %d calls, want 4 (latch re-appeared?)", compressCalls)
	}

	// The cooldown itself is intact: iterations inside the window do not compress.
	state.tokenTracker.RecordLLMCall(190400, 100)
	driveIterations(t, state, 4) // cooldown NOT expired (needs 5)
	if compressCalls != 4 {
		t.Errorf("the 5-iteration cooldown must still throttle the auto trigger, got %d calls after 4 iterations, want 4", compressCalls)
	}
}

// Test ② — an explicit `compact_context` request is ALWAYS executed: three
// consecutive requests, far below the auto trigger line (40k vs ≈91.5k) with
// real prompt_tokens that never drop ≥5% — the exact mid/low-context regime from
// the incident, where the old no-progress counter counted the model's own
// requests and latched the engine off after the second one.
//
// RED before the fuse removal: the 3rd request was dropped by the fused engine
// (2 compression calls instead of 3).
func TestMaybeCompress_ExplicitRequestAlwaysRuns(t *testing.T) {
	compressCalls := 0
	cm := smallResultCM(&compressCalls)
	state := newCompressLoopState(t, cm, latchMsgs(), 40000)

	tokens := []int64{39000, 38500, 38200} // never a ≥5% drop
	for i := 0; i < 3; i++ {
		state.selfCompactRequested = true
		if err := state.maybeCompress(context.Background()); err != nil {
			t.Fatalf("explicit request #%d: %v", i+1, err)
		}
		if compressCalls != i+1 {
			t.Fatalf("BUG REPRODUCED: model-requested compaction #%d was NOT executed (compression calls = %d, want %d) — "+
				"an explicit request must never be vetoed by engine state (cooldown, no-progress counter or the deleted fuse). "+
				"(consecutive requests at the same context magnitude are the norm: the context regrows, that is not evidence about compression.)",
				i+1, compressCalls, i+1)
		}
		if state.selfCompactRequested {
			t.Errorf("explicit request #%d must be consumed by the check that honored it (flag left set → refires next iteration)", i+1)
		}
		if i < len(tokens) {
			state.tokenTracker.RecordLLMCall(tokens[i], 100)
		}
	}
}
