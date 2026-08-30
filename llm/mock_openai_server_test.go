package llm

import (
	"context"
	"strings"
	"testing"
	"time"

	"xbot/internal/mockopenai"
)

// TestOpenAILLM_ChatCompletions_StreamIntegration 走真实 OpenAI HTTP 客户端 →
// mock OpenAI SSE 服务器 → 完整 SSE 流解析，验证 content / reasoning_content /
// tool_calls / usage 均被正确累积。这覆盖了 LLM 层"假 OpenAI 接口返回假信息"
// 的全链路，而不是直接 mock LLMClient 接口。
func TestOpenAILLM_ChatCompletions_StreamIntegration(t *testing.T) {
	chunks := []mockopenai.Chunk{
		{ReasoningContent: "让我思考一下。"},
		{Content: "你好"},
		{Content: "，世界"},
		{
			ToolCalls: []mockopenai.ToolCall{
				{Index: 0, ID: "call_1", Name: "shell", Arguments: `{"command":"echo hi"}`},
			},
		},
		{FinishReason: "stop", Usage: &mockopenai.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}},
	}
	srv := mockopenai.NewServer(t, chunks)

	client := NewOpenAILLM(OpenAIConfig{
		BaseURL:      srv.URL(),
		APIKey:       "test-key",
		DefaultModel: "mock-model",
		APIType:      APITypeChatCompletions,
	})

	resp, err := client.Generate(context.Background(), "mock-model", []ChatMessage{
		{Role: "user", Content: "hi"},
	}, nil, "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if resp.Content != "你好，世界" {
		t.Fatalf("Content = %q, want 你好，世界", resp.Content)
	}
	if resp.ReasoningContent != "让我思考一下。" {
		t.Fatalf("ReasoningContent = %q, want 让我思考一下。", resp.ReasoningContent)
	}
	if resp.Usage.PromptTokens != 10 || resp.Usage.CompletionTokens != 5 {
		t.Fatalf("Usage = %+v, want prompt=10 completion=5", resp.Usage)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "shell" {
		t.Fatalf("ToolCalls = %+v, want one shell call", resp.ToolCalls)
	}

	// 验证请求确实发到了 mock server 且带模型名。Generate 会先尝试非 stream
	// 请求，收到 SSE 后 fallback 到 stream（真实客户端的正常行为），因此至少
	// 两个请求，最后一个（stream fallback）必须带模型名。
	reqs := srv.Requests()
	if len(reqs) < 2 {
		t.Fatalf("expected at least 2 requests (non-stream + stream fallback), got %d", len(reqs))
	}
	if !strings.Contains(string(reqs[len(reqs)-1]), `"model":"mock-model"`) {
		t.Fatalf("last request body missing model: %s", reqs[len(reqs)-1])
	}
}

// TestOpenAILLM_StreamUsage_CompletionTakesMax reproduces the zero-completion
// bug: some gateways (sglang/MoL on tool-call streams) emit a mid-stream usage
// chunk with the cumulative completion_tokens, then a FINAL usage chunk whose
// completion_tokens is 0. processStream's lastUsage assignment overwrote the
// accumulated value with the trailing zero → the agent recorded
// iteration tokens = 0 for tool iterations ("tool 的 sse 没计算" —
// input/cached tokens were fine because PromptTokens stayed non-zero, but
// CompletionTokens zeroed out). The fix takes the MAX completion across usage
// chunks (accumulation is monotonic; a trailing 0 is gateway noise).
func TestOpenAILLM_StreamUsage_CompletionTakesMax(t *testing.T) {
	chunks := []mockopenai.Chunk{
		{Content: "hello"},
		// Mid-stream cumulative usage (normal so far).
		{Usage: &mockopenai.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}},
		{Content: " world"},
		// Final chunk carries a usage with completion=0 (gateway bug).
		{FinishReason: "stop", Usage: &mockopenai.Usage{PromptTokens: 100, CompletionTokens: 0, TotalTokens: 100}},
	}
	srv := mockopenai.NewServer(t, chunks)

	client := NewOpenAILLM(OpenAIConfig{
		BaseURL:      srv.URL(),
		APIKey:       "test-key",
		DefaultModel: "mock-model",
		APIType:      APITypeChatCompletions,
	})

	resp, err := client.Generate(context.Background(), "mock-model", []ChatMessage{
		{Role: "user", Content: "hi"},
	}, nil, "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if resp.Usage.PromptTokens != 100 {
		t.Fatalf("PromptTokens = %d, want 100", resp.Usage.PromptTokens)
	}
	// The cumulative mid-stream value (50) must survive the trailing zero.
	if resp.Usage.CompletionTokens != 50 {
		t.Fatalf("CompletionTokens = %d, want 50 (mid-stream cumulative value must NOT be overwritten by the trailing zero — tool-iteration tokens recorded as 0)", resp.Usage.CompletionTokens)
	}
}

// TestProcessStream_CtxCancelUnblocksFullChannel reproduces the goroutine leak
// fixed by the select+ctx.Done guard on every eventChan send in processStream:
// the consumer (CollectStreamWithCallback) returns on ctx cancellation /
// idle timeout WITHOUT draining eventChan; once the channel buffer is full
// (slow/stalled consumer backpressure), a bare `eventChan <-` blocks forever —
// `defer close(eventChan)` and `defer stream.Close()` never run, leaking the
// goroutine and the HTTP body. With the guard, processStream must return
// promptly once ctx is cancelled, and its defers must run (eventChan closed).
//
// Repro shape: buffer-1 channel + no reader + 8 content chunks. processStream
// fills the buffer with event #1, then blocks on send #2. Cancelling ctx must
// unblock it (old code: permanent block → 10s timeout below fails).
func TestProcessStream_CtxCancelUnblocksFullChannel(t *testing.T) {
	chunks := make([]mockopenai.Chunk, 0, 8)
	for i := 0; i < 8; i++ {
		chunks = append(chunks, mockopenai.Chunk{Content: "x"})
	}
	srv := mockopenai.NewServer(t, chunks)

	client := NewOpenAILLM(OpenAIConfig{
		BaseURL:      srv.URL(),
		APIKey:       "test-key",
		DefaultModel: "mock-model",
		APIType:      APITypeChatCompletions,
	})

	// Open a REAL streaming response with a live ctx (the HTTP request must
	// succeed; only the consumer-side ctx below is cancelled).
	streamCtx, streamCancel := context.WithCancel(context.Background())
	defer streamCancel()
	stream, err := client.newStreamingWithRetry(streamCtx, "mock-model", []ChatMessage{
		{Role: "user", Content: "hi"},
	}, nil, "", nil)
	if err != nil {
		t.Fatalf("newStreamingWithRetry: %v", err)
	}

	// Consumer-side ctx, mirroring CollectStreamWithCallbackFrom's cancellation
	// path: it returns on ctx.Done() without draining the channel.
	procCtx, procCancel := context.WithCancel(context.Background())

	// Buffer-1 channel with NO reader: event #1 fills the buffer, send #2 is
	// where a bare `chan <-` would block forever.
	eventChan := make(chan StreamEvent, 1)

	done := make(chan struct{})
	go func() {
		defer close(done)
		client.processStream(procCtx, stream, eventChan, time.Now(), nil, "mock-model", nil, "")
	}()

	// Let processStream consume chunks from the (already fully-flushed) mock
	// SSE body, fill the buffer, and block on send #2.
	time.Sleep(500 * time.Millisecond)
	procCancel()

	select {
	case <-done:
		// processStream returned — no goroutine leak.
	case <-time.After(10 * time.Second):
		t.Fatal("processStream leaked: still blocked on a full eventChan 10s after ctx cancellation (bare chan send never unblocks)")
	}

	// defer close(eventChan) must have run: drain buffered events until the
	// channel reports closed.
	drainDeadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-eventChan:
			if !ok {
				return // closed — both defers ran, goroutine fully unwound
			}
		case <-drainDeadline:
			t.Fatal("eventChan never closed — defer close(eventChan) did not run (goroutine leaked)")
		}
	}
}
