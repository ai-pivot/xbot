package llm

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	"xbot/internal/mockopenai"
)

// llmGoroutineProfileCount counts goroutines whose stack (including the
// "created by" line) contains the given substring.
func llmGoroutineProfileCount(substr string) int {
	p := pprof.Lookup("goroutine")
	var buf bytes.Buffer
	//nolint:errcheck // WriteTo to bytes.Buffer cannot fail
	p.WriteTo(&buf, 2)
	return strings.Count(buf.String(), substr)
}

// llmWaitForGoroutineGone polls until no goroutine matching substr remains
// (exit is asynchronous; allow a short grace period) and returns the final count.
func llmWaitForGoroutineGone(substr string, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	count := llmGoroutineProfileCount(substr)
	for count > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		count = llmGoroutineProfileCount(substr)
	}
	return count
}

// TestOpenAIProcessStreamWatcherExitsOnNormalCompletion reproduces the LLM
// stream watcher goroutine leak (production dump 2026-10-03: 765 leaked
// goroutines "created by xbot/llm", each pinning the finished stream).
// processStream starts a watcher whose only exit condition is ctx
// cancellation; on a stream that completes normally the caller never
// cancels the ctx, so the watcher blocks forever on <-ctxDone(). The fix
// adds a done channel closed when processStream returns.
func TestOpenAIProcessStreamWatcherExitsOnNormalCompletion(t *testing.T) {
	chunks := []mockopenai.Chunk{
		{Content: "hello"},
		{FinishReason: "stop", Usage: &mockopenai.Usage{PromptTokens: 5, CompletionTokens: 1, TotalTokens: 6}},
	}
	srv := mockopenai.NewServer(t, chunks)

	client := NewOpenAILLM(OpenAIConfig{
		BaseURL:      srv.URL(),
		APIKey:       "test-key",
		DefaultModel: "mock-model",
		APIType:      APITypeChatCompletions,
	})

	// A cancellable-but-never-cancelled ctx: Background.Done() returns nil and
	// the watcher is not started at all, which would silently skip the leak.
	// Production callers always pass derived (non-nil Done) contexts.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // cleanup only; the assertions below run first
	eventCh, err := client.GenerateStream(ctx, "mock-model",
		[]ChatMessage{{Role: "user", Content: "hi"}}, nil, "")
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	for range eventCh { // drain to normal completion
	}

	if n := llmWaitForGoroutineGone("(*OpenAILLM).processStream.func", 2*time.Second); n != 0 {
		t.Fatalf("OpenAI processStream watcher goroutine leaked: %d still alive after stream completed normally", n)
	}
}

// TestAnthropicProcessStreamWatcherExitsOnNormalCompletion is the same leak
// guard for the Anthropic SSE implementation, whose watcher additionally
// pins resp.Body (the finished HTTP connection) until ctx is cancelled.
func TestAnthropicProcessStreamWatcherExitsOnNormalCompletion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("mock server writer cannot flush")
			return
		}
		writeEvent := func(data string) {
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
		writeEvent(`{"type":"message_start"}`)
		writeEvent(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`)
		writeEvent(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":5,"output_tokens":1}}`)
		writeEvent(`{"type":"message_stop"}`)
	}))
	defer srv.Close()

	client := NewAnthropicLLM(AnthropicConfig{
		BaseURL:      srv.URL,
		APIKey:       "test-key",
		DefaultModel: "mock-model",
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // cleanup only; the assertions below run first
	eventCh, err := client.GenerateStream(ctx, "mock-model",
		[]ChatMessage{{Role: "user", Content: "hi"}}, nil, "")
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	for range eventCh { // drain to normal completion
	}

	if n := llmWaitForGoroutineGone("(*AnthropicLLM).processStream.func", 2*time.Second); n != 0 {
		t.Fatalf("Anthropic processStream watcher goroutine leaked: %d still alive after stream completed normally", n)
	}
}
