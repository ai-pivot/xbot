package llm

import (
	"context"
	"strings"
	"testing"
	"time"

	"xbot/internal/mockopenai"
)

// drainStreamEvents consumes eventChan until processStream closes it and
// returns all events in order.
func drainStreamEvents(eventChan chan StreamEvent) []StreamEvent {
	var events []StreamEvent
	for ev := range eventChan {
		events = append(events, ev)
	}
	return events
}

func newStreamTestClient(t *testing.T, chunks []mockopenai.Chunk) (*OpenAILLM, *mockopenai.Server) {
	t.Helper()
	srv := mockopenai.NewServer(t, chunks)
	client := NewOpenAILLM(OpenAIConfig{
		BaseURL:      srv.URL(),
		APIKey:       "test-key",
		DefaultModel: "mock-model",
		APIType:      APITypeChatCompletions,
	})
	return client, srv
}

// TestProcessStream_TruncatedToolCallArgs_StreamEndedWithoutFinishReason
// reproduces the "parse args: unexpected end of JSON input" bug chain:
// a proxy/gateway (MoL on sglang, observed 2026-08-30) cuts the SSE stream
// cleanly mid-tool-call — no finish_reason chunk arrives, but tool call
// deltas (name + partial arguments) were already seen. processStream's old
// logic inferred FinishReasonToolCalls whenever hasToolCalls was true, so the
// half-generated arguments JSON was passed downstream as a "complete" tool
// call → parseToolArgs failed with "unexpected end of JSON input" → the LLM
// retried the SAME oversized call → token burn loop (3 consecutive
// FileCreate/FileReplace failures in turn 325 of web:chat_BD94FA4BB469).
//
// The fix: when the stream ends without finish_reason and any accumulated
// tool call arguments are invalid JSON (truncated), emit EventError (the
// RetryLLM layer retries the whole request — same path as the existing
// "!hasToolCalls" truncation detection at the 1273-line check).
func TestProcessStream_TruncatedToolCallArgs_StreamEndedWithoutFinishReason(t *testing.T) {
	client, _ := newStreamTestClient(t, []mockopenai.Chunk{
		// Tool call delta arrives: name + first half of arguments.
		{ToolCalls: []mockopenai.ToolCall{
			{Index: 0, ID: "call_1", Name: "FileReplace", Arguments: `{"new_string":"abc`},
		}},
		// More argument deltas stream in...
		{ToolCalls: []mockopenai.ToolCall{
			{Index: 0, Arguments: `defgh`},
		}},
		// ...then the stream ends CLEANLY with NO finish_reason chunk
		// (proxy cut the connection; stream.Err() == nil, [DONE] or not —
		// no choice carries finish_reason).
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.newStreamingWithRetry(ctx, "mock-model", []ChatMessage{
		{Role: "user", Content: "hi"},
	}, nil, "", nil)
	if err != nil {
		t.Fatalf("newStreamingWithRetry: %v", err)
	}

	eventChan := make(chan StreamEvent, 64)
	go client.processStream(ctx, stream, eventChan, time.Now(), nil, "mock-model", nil, "")
	events := drainStreamEvents(eventChan)

	var gotErr string
	var doneFinishReason FinishReason
	for _, ev := range events {
		if ev.Type == EventError {
			gotErr = ev.Error
		}
		if ev.Type == EventDone {
			doneFinishReason = ev.FinishReason
		}
	}
	if gotErr == "" {
		t.Fatalf("stream ended without finish_reason + truncated tool call arguments: expected EventError (to trigger RetryLLM), got none. Events: %+v (done finish_reason=%q) — old behavior silently inferred tool_calls and passed the half JSON downstream (the parse-args bug)", events, doneFinishReason)
	}
	if !strings.Contains(gotErr, "truncated") {
		t.Fatalf("EventError should mention truncation, got: %q", gotErr)
	}
}

// TestProcessStream_CompleteToolCallArgs_NoFinishReason_StillInferred covers
// the LEGITIMATE version of the same scenario: gateway drops the final
// finish_reason chunk (or the [DONE]-only tail) but the tool call arguments
// are complete, valid JSON. This must keep the old behavior — infer
// FinishReasonToolCalls and complete normally. Otherwise every such stream
// would be retried needlessly.
func TestProcessStream_CompleteToolCallArgs_NoFinishReason_StillInferred(t *testing.T) {
	client, _ := newStreamTestClient(t, []mockopenai.Chunk{
		{ToolCalls: []mockopenai.ToolCall{
			{Index: 0, ID: "call_1", Name: "shell", Arguments: `{"command":"echo `},
		}},
		{ToolCalls: []mockopenai.ToolCall{
			{Index: 0, Arguments: `hi"}`},
		}},
		// No FinishReason — but full arguments `{"command":"echo hi"}`.
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.newStreamingWithRetry(ctx, "mock-model", []ChatMessage{
		{Role: "user", Content: "hi"},
	}, nil, "", nil)
	if err != nil {
		t.Fatalf("newStreamingWithRetry: %v", err)
	}

	eventChan := make(chan StreamEvent, 64)
	go client.processStream(ctx, stream, eventChan, time.Now(), nil, "mock-model", nil, "")
	events := drainStreamEvents(eventChan)

	for _, ev := range events {
		if ev.Type == EventError {
			t.Fatalf("complete tool call args without finish_reason must NOT error (old inference behavior preserved), got EventError: %q", ev.Error)
		}
	}
	var doneFinishReason FinishReason
	gotDone := false
	for _, ev := range events {
		if ev.Type == EventDone {
			gotDone = true
			doneFinishReason = ev.FinishReason
		}
	}
	if !gotDone {
		t.Fatalf("expected EventDone, got none. Events: %+v", events)
	}
	if doneFinishReason != FinishReasonToolCalls {
		t.Fatalf("finish_reason = %q, want %q (inferred from tool_calls)", doneFinishReason, FinishReasonToolCalls)
	}
}

// TestProcessStream_EmptyToolCallArgs_NoFinishReason_NoError covers no-arg
// tool calls: some providers send name/ID with EMPTY arguments (no-arg call),
// which must stay valid when the stream ends without finish_reason.
func TestProcessStream_EmptyToolCallArgs_NoFinishReason_NoError(t *testing.T) {
	client, _ := newStreamTestClient(t, []mockopenai.Chunk{
		{ToolCalls: []mockopenai.ToolCall{
			{Index: 0, ID: "call_1", Name: "list_tasks", Arguments: ``},
		}},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.newStreamingWithRetry(ctx, "mock-model", []ChatMessage{
		{Role: "user", Content: "hi"},
	}, nil, "", nil)
	if err != nil {
		t.Fatalf("newStreamingWithRetry: %v", err)
	}

	eventChan := make(chan StreamEvent, 64)
	go client.processStream(ctx, stream, eventChan, time.Now(), nil, "mock-model", nil, "")
	events := drainStreamEvents(eventChan)

	for _, ev := range events {
		if ev.Type == EventError {
			t.Fatalf("empty (no-arg) tool call arguments without finish_reason must NOT error, got EventError: %q", ev.Error)
		}
	}
	gotDone := false
	for _, ev := range events {
		if ev.Type == EventDone {
			gotDone = true
		}
	}
	if !gotDone {
		t.Fatalf("expected EventDone, got none. Events: %+v", events)
	}
}
