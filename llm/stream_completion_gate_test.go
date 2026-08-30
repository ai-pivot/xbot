package llm

import (
	"context"
	"strings"
	"testing"

	"xbot/internal/mockopenai"
)

// TestGenerate_CorruptedToolCallArgs_FinishReasonArrived reproduces the
// MAJORITY corruption shape observed in production (DB evidence, 2026-08-29:
// 14 failures in one day, all "invalid character ... after object
// key:value pair"): the gateway loses/repeats SSE chunks MID-STREAM while
// still delivering a normal finish_reason. The stream "completes" cleanly —
// no truncation warning, no EventError — but the accumulated tool call
// arguments are spliced garbage (e.g. `{"id": 2", "status": ...`).
//
// Old behavior: CollectStreamWithCallback returned (resp, nil) — the
// corrupted JSON flowed into tool execution and failed with an opaque
// "parse args: invalid character ... after object key:value pair" that the
// LLM retried blindly (token burn). The fix: the stream-completion gate in
// CollectStreamWithCallbackFrom validates tool call arguments JSON and
// returns a retryable error so RetryLLM regenerates the whole request.
func TestGenerate_CorruptedToolCallArgs_FinishReasonArrived(t *testing.T) {
	client, _ := newStreamTestClient(t, []mockopenai.Chunk{
		{ToolCalls: []mockopenai.ToolCall{
			{Index: 0, ID: "call_1", Name: "TodoWrite", Arguments: `{"todos": [{"id": 1, "status": "doing", "text": "a"}, {"id": 2`},
		}},
		// Mid-stream chunk LOSS: the continuation arrives without the
		// `"status": "pending"` segment prefix (a gateway dropped/repeated
		// chunks) — spliced args are invalid JSON:
		//   {"todos": [...{"id": 2", "status": ...}]}
		{ToolCalls: []mockopenai.ToolCall{
			{Index: 0, Arguments: `", "status": "pending", "text": "b"}]}`},
		}},
		// finish_reason arrives NORMALLY — this is the majority shape: no
		// truncation branch (1306) fires, stream "completes" cleanly.
		{FinishReason: "tool_calls"},
	})

	resp, err := client.Generate(context.Background(), "mock-model", []ChatMessage{
		{Role: "user", Content: "hi"},
	}, nil, "")
	if err == nil {
		// Old behavior — corrupted args passed through as a "successful" response.
		t.Fatalf("stream completed with corrupted tool call arguments: expected retryable error, got nil. ToolCalls[0].Arguments=%q", resp.ToolCalls[0].Arguments)
	}
	if !strings.Contains(err.Error(), "tool call arguments corrupted") {
		t.Fatalf("error should identify corrupted tool call arguments, got: %v", err)
	}
}

// TestGenerate_CompleteToolCallArgs_FinishReasonArrived is the control:
// same shape (finish_reason arrives) but arguments are complete valid JSON —
// must complete normally with no error.
func TestGenerate_CompleteToolCallArgs_FinishReasonArrived(t *testing.T) {
	client, _ := newStreamTestClient(t, []mockopenai.Chunk{
		{ToolCalls: []mockopenai.ToolCall{
			{Index: 0, ID: "call_1", Name: "TodoWrite", Arguments: `{"todos": [{"id": 1, `},
		}},
		{ToolCalls: []mockopenai.ToolCall{
			{Index: 0, Arguments: `"status": "doing", "text": "a"}]}`},
		}},
		{FinishReason: "tool_calls"},
	})

	resp, err := client.Generate(context.Background(), "mock-model", []ChatMessage{
		{Role: "user", Content: "hi"},
	}, nil, "")
	if err != nil {
		t.Fatalf("complete args with normal finish_reason must not error, got: %v", err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "TodoWrite" {
		t.Fatalf("ToolCalls = %+v, want one TodoWrite call", resp.ToolCalls)
	}
	if got := resp.ToolCalls[0].Arguments; got != `{"todos": [{"id": 1, "status": "doing", "text": "a"}]}` {
		t.Fatalf("Arguments = %q, want the spliced complete JSON", got)
	}
}
