package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"xbot/llm"
)

// TestRun_PhaseDoneEmittedExactlyOnce guards against duplicate initProgress +
// defer progressFinalizer registration in Run(): a copy-pasted second block
// made LIFO defers fire progressFinalizer twice, so every Run emitted TWO
// PhaseDone events (double ProgressSeq bump + a pre-cleanup-todos PhaseDone
// violating the "PhaseDone carries POST-cleanup todos" defer-order contract
// documented at the surviving registration site).
func TestRun_PhaseDoneEmittedExactlyOnce(t *testing.T) {
	mock := &mockLLM{
		responses: []llm.LLMResponse{{Content: "done"}},
	}

	var phaseDoneCount atomic.Int32
	var lastSeq atomic.Uint64
	out := Run(context.Background(), RunConfig{
		LLMClient:        mock,
		Model:            "test-model",
		Tools:            newTestRegistry(),
		Messages:         baseMessages(),
		AgentID:          "main",
		Channel:          "test",
		ChatID:           "chat1",
		ProgressNotifier: func(_ []string, _ string) {},
		ProgressEventHandler: func(evt *ProgressEvent) {
			if evt.Structured != nil && evt.Structured.Phase == PhaseDone {
				phaseDoneCount.Add(1)
			}
			if evt.Structured != nil && evt.Structured.Seq > lastSeq.Load() {
				lastSeq.Store(evt.Structured.Seq)
			}
		},
	})
	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}
	if got := phaseDoneCount.Load(); got != 1 {
		t.Fatalf("PhaseDone emitted %d times, want exactly 1 (duplicate progressFinalizer registration)", got)
	}
}
