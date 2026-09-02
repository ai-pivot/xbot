package xbot

import (
	"context"
	"testing"

	"xbot/llm"
)

// streamModeLLM implements both llm.LLM (Generate) and llm.StreamingLLM
// (GenerateStream), recording which path was taken. Stream events are fed
// through the GenerateStream channel so CollectStream assembles a real
// LLMResponse (content or tool_calls).
type streamModeLLM struct {
	generateCalled       bool
	generateStreamCalled bool

	// response is returned by the non-stream Generate fallback.
	response *llm.LLMResponse
	// streamEvents are fed into the GenerateStream channel (closed after).
	streamEvents []llm.StreamEvent
	// streamErr, when set, is returned by GenerateStream before any event.
	streamErr error
}

func (m *streamModeLLM) Generate(_ context.Context, _ string, _ []llm.ChatMessage, _ []llm.ToolDefinition, _ string) (*llm.LLMResponse, error) {
	m.generateCalled = true
	if m.response != nil {
		return m.response, nil
	}
	return &llm.LLMResponse{Content: "non-stream fallback", FinishReason: llm.FinishReasonStop}, nil
}

func (m *streamModeLLM) GenerateStream(_ context.Context, _ string, _ []llm.ChatMessage, _ []llm.ToolDefinition, _ string) (<-chan llm.StreamEvent, error) {
	m.generateStreamCalled = true
	if m.streamErr != nil {
		return nil, m.streamErr
	}
	ch := make(chan llm.StreamEvent, len(m.streamEvents)+1)
	for _, ev := range m.streamEvents {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func (m *streamModeLLM) ListModels() []string { return nil }

// TestGenerateSessionSummary_UsesStream — every memory LLM call must go
// through the STREAMING path, not the non-stream Generate path.
//
// Root cause this guards (web:chat_BD94FA4BB469 turn 367, 2026-08-30): the
// memory LLM calls (updateCoreSummary / generateSessionSummary /
// extractAtomicMemories) used llmClient.Generate — the non-stream retry path
// whose perAttemptCtx carries a HARD 120s deadline (llm/retry.go). On a busy
// cluster the PostCompress updateCoreSummary call was killed at exactly 120s,
// 5 retries × 2 minutes each, burning ~10 minutes AFTER a successful
// compression while the turn sat "stuck". The streaming path has no total
// deadline — only the 120s IDLE timeout between SSE chunks, reset on every
// chunk (CollectStreamWithCallback) — same semantics the compression LLM call
// itself now uses (compress.go Stream:true).
func TestGenerateSessionSummary_UsesStream(t *testing.T) {
	mock := &streamModeLLM{streamEvents: []llm.StreamEvent{
		{Type: llm.EventContent, Content: "User worked on xbot memory streaming fix."},
		{Type: llm.EventDone, FinishReason: llm.FinishReasonStop},
	}}
	m := &XbotMemory{}

	summary, _ := m.generateSessionSummary(context.Background(), mock, "test", []llm.ChatMessage{
		llm.NewUserMessage("hello"),
		llm.NewAssistantMessage("hi"),
	})

	if !mock.generateStreamCalled {
		t.Errorf("BUG REPRODUCED: generateSessionSummary went NON-STREAM (Generate) — the non-stream path " +
			"carries perAttemptCtx's hard 120s deadline; on a busy cluster the memory summary call dies at " +
			"exactly 120s × 5 retries (turn 367 incident). Memory LLM calls must stream: 120s idle timeout, " +
			"reset on every SSE chunk.")
	}
	if mock.generateCalled {
		t.Error("non-stream Generate was used even though the client supports streaming")
	}
	if summary == "" {
		t.Errorf("streamed summary not collected, got %q", summary)
	}
}

// TestExtractAtomicMemories_UsesStream — the PreCompress memory-extraction
// call must stream as well, and streamed tool_calls (extract_memories) must
// be collected into a usable LLMResponse.
func TestExtractAtomicMemories_UsesStream(t *testing.T) {
	toolArgs := `{"memories":[{"type":"fact","content":"user prefers dark theme","keywords":"theme,dark","importance":0.8}]}`
	mock := &streamModeLLM{streamEvents: []llm.StreamEvent{
		{Type: llm.EventToolCall, ToolCall: &llm.ToolCallDelta{Index: 0, ID: "tc1", Name: "extract_memories", Arguments: toolArgs}},
		{Type: llm.EventDone, FinishReason: llm.FinishReasonToolCalls},
	}}
	m := &XbotMemory{}

	entries := m.extractAtomicMemories(context.Background(), mock, "test", []llm.ChatMessage{
		llm.NewUserMessage("I prefer dark theme for all my tools"),
	}, 0)

	if !mock.generateStreamCalled {
		t.Errorf("BUG REPRODUCED: extractAtomicMemories went NON-STREAM (Generate) — same 120s hard-deadline " +
			"risk as generateSessionSummary; the PreCompress leg of the compression pipeline (5+ minutes on " +
			"870k contexts) must not also sit in a 5×120s retry loop.")
	}
	if mock.generateCalled {
		t.Error("non-stream Generate was used even though the client supports streaming")
	}
	if len(entries) != 1 {
		t.Fatalf("streamed tool_calls not collected: got %d entries, want 1", len(entries))
	}
	if entries[0].Content != "user prefers dark theme" || entries[0].Type != "fact" {
		t.Errorf("extracted entry mismatch: %+v", entries[0])
	}
}

// TestGenerateLLM_NonStreamFallback — clients that do NOT implement
// llm.StreamingLLM (test mocks, future providers) still work via Generate.
func TestGenerateLLM_NonStreamFallback(t *testing.T) {
	nonStream := &nonStreamOnlyLLM{content: "fallback ok"}
	m := &XbotMemory{}

	resp, err := m.generateLLM(context.Background(), nonStream, "test", []llm.ChatMessage{llm.NewUserMessage("hi")}, nil)
	if err != nil {
		t.Fatalf("generateLLM fallback error: %v", err)
	}
	if !nonStream.generateCalled {
		t.Error("non-stream client must fall back to Generate")
	}
	if resp.Content != "fallback ok" {
		t.Errorf("fallback content = %q", resp.Content)
	}
}

// nonStreamOnlyLLM implements only llm.LLM (no GenerateStream).
type nonStreamOnlyLLM struct {
	generateCalled bool
	content        string
}

func (n *nonStreamOnlyLLM) Generate(_ context.Context, _ string, _ []llm.ChatMessage, _ []llm.ToolDefinition, _ string) (*llm.LLMResponse, error) {
	n.generateCalled = true
	return &llm.LLMResponse{Content: n.content, FinishReason: llm.FinishReasonStop}, nil
}

func (n *nonStreamOnlyLLM) ListModels() []string { return nil }
