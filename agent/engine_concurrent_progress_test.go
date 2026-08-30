package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"xbot/llm"
	"xbot/tools"
)

// TestRun_ConcurrentToolProgressDataRace reproduces the progressMu coverage
// gap under dispatchReadWriteSplit: concurrent read-tool goroutines write
// structuredProgress.ActiveTools[i] (execOneTool status flip +
// updateToolResultProgress result fields) and progressLines[pi]
// (updateToolResultLine) WITHOUT progressMu, while notifyProgress clones the
// same structuredProgress under progressMu from each goroutine. -race flags
// the unsynchronized writes vs the locked clone. The fix serializes every
// structuredProgress/progressLines access on progressMu.
func TestRun_ConcurrentToolProgressDataRace(t *testing.T) {
	slowRead := &mockTool{
		name: "Read",
		execFunc: func(_ *tools.ToolContext, _ string) (*tools.ToolResult, error) {
			// Overlap the two read-tool goroutines so both write their
			// ActiveTools slot while the other's notifyProgress clone runs.
			time.Sleep(25 * time.Millisecond)
			return &tools.ToolResult{Detail: "ok"}, nil
		},
	}
	mock := &mockLLM{
		responses: []llm.LLMResponse{
			{ToolCalls: []llm.ToolCall{
				{ID: "c1", Name: "Read", Arguments: `{"path":"a"}`},
				{ID: "c2", Name: "Read", Arguments: `{"path":"b"}`},
			}},
			{Content: "done"},
		},
	}

	var progressSeq atomic.Uint64
	out := Run(context.Background(), RunConfig{
		LLMClient:            mock,
		Model:                "test-model",
		Tools:                newTestRegistry(slowRead),
		Messages:             baseMessages(),
		AgentID:              "main",
		Channel:              "test",
		ChatID:               "chat1",
		EnableReadWriteSplit: true,
		ProgressSeq:          &progressSeq,
		// ProgressEventHandler presence enables autoNotify: every execOneTool
		// (running flip, before/after-exec notify) and updateToolResult*
		// write races against the locked clone in notifyProgress.
		ProgressEventHandler: func(evt *ProgressEvent) {
			_ = evt.Structured
		},
	})
	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}
}
