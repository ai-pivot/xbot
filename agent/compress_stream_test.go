package agent

import (
	"context"
	"strings"
	"testing"

	"xbot/llm"
)

// streamModeLLM implements both llm.LLM (Generate) and llm.StreamingLLM
// (GenerateStream), recording which path was taken.
type streamModeLLM struct {
	generateCalled       bool
	generateStreamCalled bool
	content              string
}

func (m *streamModeLLM) Generate(_ context.Context, _ string, _ []llm.ChatMessage, _ []llm.ToolDefinition, _ string) (*llm.LLMResponse, error) {
	m.generateCalled = true
	return &llm.LLMResponse{Content: m.content, FinishReason: llm.FinishReasonStop}, nil
}

func (m *streamModeLLM) GenerateStream(_ context.Context, _ string, _ []llm.ChatMessage, _ []llm.ToolDefinition, _ string) (<-chan llm.StreamEvent, error) {
	m.generateStreamCalled = true
	ch := make(chan llm.StreamEvent, 2)
	ch <- llm.StreamEvent{Type: llm.EventContent, Content: m.content}
	ch <- llm.StreamEvent{Type: llm.EventDone, FinishReason: llm.FinishReasonStop}
	close(ch)
	return ch, nil
}

func (m *streamModeLLM) ListModels() []string { return []string{"test-model"} }

// TestCompactMessages_UsesStreamingForCompressionLLM — the compression LLM
// call MUST go through the STREAMING path, not the non-stream Generate path.
//
// Root cause this guards against (870k-context incident, 2026-08-30): with
// Stream:false the compression call goes through the non-stream retry path,
// whose per-attempt ctx carries a HARD 120s deadline (llm/retry.go
// perAttemptCtx). A large compaction summary (e.g. 52,884 tokens target for a
// 176k-token context) takes 9-17 minutes to generate at single-stream decode
// speeds — every attempt is killed at 120s, all 5 retries fail, the turn
// spends 16 minutes burning LLM calls (5× dead attempts + PreCompress) and the
// context never shrinks → re-triggers 5 iterations later → infinite loop.
//
// The streaming path (same as normal chat) has NO total deadline: the only
// timeout is the 120s IDLE timeout between SSE chunks, reset on EVERY received
// chunk (CollectStreamWithCallback) — an actively-streaming summary never
// times out. Compression must use the same semantics as chat.
//
// Fallback compatibility: clients that do NOT implement llm.StreamingLLM
// (e.g. the mockLLM in most agent tests) fall back to Generate — existing
// tests keep working.
func TestCompactMessages_UsesStreamingForCompressionLLM(t *testing.T) {
	mock := &streamModeLLM{content: "compacted working state summary"}

	msgs := []llm.ChatMessage{
		llm.NewSystemMessage("sys"),
		llm.NewUserMessage("task one"),
		llm.NewAssistantMessage("did thing one"),
		llm.NewUserMessage("task two"),
		llm.NewAssistantMessage("did thing two"),
		llm.NewUserMessage("latest question"), // tail anchor (last user msg)
	}

	result, err := compactMessages(context.Background(), msgs, mock, "test-model", 200000, 1000, 0)
	if err != nil {
		t.Fatalf("compactMessages: %v", err)
	}
	if result == nil || !strings.Contains(result.LLMView[1].Content, "compacted working state summary") {
		t.Fatalf("unexpected compaction result: %+v", result)
	}

	if !mock.generateStreamCalled {
		t.Errorf("BUG REPRODUCED: compression LLM call went NON-STREAM (Generate) — the non-stream path " +
			"carries perAttemptCtx's hard 120s deadline, which can never complete a large compaction summary " +
			"(52k-token target needs 9-17min at single-stream decode; killed at 120s × 5 retries = the 870k-context " +
			"infinite compression loop). Compression must stream: same idle-timeout semantics as chat " +
			"(120s WITHOUT any SSE chunk, reset on every chunk).")
	}
	if mock.generateCalled {
		t.Errorf("non-stream Generate was used even though the client supports streaming")
	}
}
