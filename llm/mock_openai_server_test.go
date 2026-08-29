package llm

import (
	"context"
	"strings"
	"testing"

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
