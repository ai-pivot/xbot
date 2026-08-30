package agent

import (
	"context"
	"testing"

	"xbot/llm"
	"xbot/tools"
)

// TestRun_IterationStart verifies that a resumed Run (IterationStart > 0)
// continues the interrupted turn's iteration numbering instead of restarting
// at 1. The resume reuses the interrupted turn's id; iteration numbers are
// turn-scoped (iteration_history keyed by turn_id + iteration), so restarting
// at 1 would collide with the interrupted Run's records and break the
// frontend's per-turn iteration advance/merge checks (the committed turn's
// max iteration would shadow every incoming iteration as a "replay").
func TestRun_IterationStart(t *testing.T) {
	// Interrupted Run already recorded iterations 1..2 for the reused turn.
	const iterationStart = 2

	// Resumed Run: one tool-call iteration, then the final reply.
	mock := &mockLLM{
		responses: []llm.LLMResponse{
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "Shell", Arguments: `{}`},
				},
			},
			{Content: "done", FinishReason: llm.FinishReasonStop},
		},
	}

	var snapshots []IterationSnapshot
	out := Run(context.Background(), RunConfig{
		LLMClient:      mock,
		Model:          "test",
		Tools:          newTestRegistry(),
		Messages:       baseMessages(),
		AgentID:        "main",
		IterationStart: iterationStart,
		MaxIterations:  5,
		OnIterationSnapshot: func(snap IterationSnapshot) {
			snapshots = append(snapshots, snap)
		},
		ToolExecutor: func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
			return tools.NewResult("ok"), nil
		},
	})

	if out.Error != nil {
		t.Fatalf("Run error: %v", out.Error)
	}
	// The resumed Run's two iterations must be numbered 3 and 4 — continuing
	// the interrupted turn's 1..2, not restarting at 1.
	if len(out.IterationHistory) < 2 {
		t.Fatalf("expected >= 2 iteration snapshots, got %d (%+v)", len(out.IterationHistory), out.IterationHistory)
	}
	for i, want := range []int{3, 4} {
		if got := out.IterationHistory[i].Iteration; got != want {
			t.Errorf("IterationHistory[%d].Iteration = %d, want %d (iteration numbering must continue past IterationStart)", i, got, want)
		}
	}
	if len(snapshots) < 2 {
		t.Fatalf("expected >= 2 OnIterationSnapshot callbacks, got %d", len(snapshots))
	}
	for i, want := range []int{3, 4} {
		if got := snapshots[i].Iteration; got != want {
			t.Errorf("snapshots[%d].Iteration = %d, want %d", i, got, want)
		}
	}
	// The final output content must still be produced normally.
	if out.Content != "done" {
		t.Errorf("out.Content = %q, want %q", out.Content, "done")
	}
}
