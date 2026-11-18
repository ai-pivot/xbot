package llm

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// TestGeminiThoughtSignatureRoundTripNonStream 验证非流式响应中：
//  1. thought:true part 的文本与 functionCall / text part 上的 thoughtSignature 被提取进 ReasoningItems；
//  2. 把该响应挂到历史 ChatMessage 后，toGeminiRequest 能原样还原出 thought:true part、
//     functionCall.id 以及对应 part 上的 thoughtSignature。
func TestGeminiThoughtSignatureRoundTripNonStream(t *testing.T) {
	gr := &geminiResponse{
		Candidates: []geminiCandidate{{
			FinishReason: "STOP",
			Content: geminiContent{
				Role: "model",
				Parts: []geminiPart{
					{Text: "deliberating about weather", Thought: true},
					{
						FunctionCall: &geminiFuncCall{
							ID:   "call_9001",
							Name: "get_weather",
							Args: json.RawMessage(`{"city":"Beijing"}`),
						},
						ThoughtSignature: "SIG_CALL_9001",
					},
					{
						FunctionCall: &geminiFuncCall{
							ID:   "call_9002",
							Name: "get_weather",
							Args: json.RawMessage(`{"city":"Shanghai"}`),
						},
					},
				},
			},
		}},
	}
	resp, err := convertGeminiResponse(gr)
	if err != nil {
		t.Fatalf("convertGeminiResponse: %v", err)
	}
	if resp.ReasoningContent != "deliberating about weather" {
		t.Fatalf("ReasoningContent = %q", resp.ReasoningContent)
	}
	if len(resp.ReasoningItems) == 0 {
		t.Fatal("expected ReasoningItems to capture thoughtSignature and thought text")
	}

	g := NewGoogleLLM(GoogleConfig{BaseURL: "http://x", APIKey: "k", DefaultModel: "gemini-4-argon"})
	msgs := []ChatMessage{
		{Role: "user", Content: "check weather"},
		{
			Role:             "assistant",
			Content:          resp.Content,
			ReasoningContent: resp.ReasoningContent,
			ReasoningItems:   resp.ReasoningItems,
			ToolCalls:        resp.ToolCalls,
		},
		{Role: "tool", ToolCallID: "call_9001", ToolName: "get_weather", Content: "sunny"},
		{Role: "tool", ToolCallID: "call_9002", ToolName: "get_weather", Content: "rainy"},
	}
	req := g.toGeminiRequest(context.Background(), "gemini-4-argon", msgs, nil, "")
	if len(req.Contents) < 2 {
		t.Fatalf("contents = %+v", req.Contents)
	}
	modelParts := req.Contents[1].Parts
	if len(modelParts) != 3 {
		t.Fatalf("model parts count = %d, want 3 (1 thought + 2 functionCalls): %+v", len(modelParts), modelParts)
	}
	if !modelParts[0].Thought || modelParts[0].Text != "deliberating about weather" {
		t.Fatalf("part[0] = %+v, want thought part", modelParts[0])
	}
	if modelParts[1].FunctionCall == nil || modelParts[1].FunctionCall.ID != "call_9001" || modelParts[1].ThoughtSignature != "SIG_CALL_9001" {
		t.Fatalf("part[1] = %+v, want call_9001 with SIG_CALL_9001", modelParts[1])
	}
	if modelParts[2].FunctionCall == nil || modelParts[2].FunctionCall.ID != "call_9002" || modelParts[2].ThoughtSignature != "" {
		t.Fatalf("part[2] = %+v, want call_9002 without signature", modelParts[2])
	}
}

// TestGeminiThoughtSignatureRoundTripStream 验证流式 SSE 下：
//   - 即使最后承载 thoughtSignature 的 chunk 是 {"text":"","thoughtSignature":"..."}（真实网关形态），
//     也能通过 EventReasoningItem → CollectStream 完整收集并在下一轮请求中还原。
func TestGeminiThoughtSignatureRoundTripStream(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"text":"thinking step 1 ","thought":true}]}}]}`,
		`data: {"candidates":[{"content":{"parts":[{"text":"thinking step 2","thought":true}]}}]}`,
		`data: {"candidates":[{"content":{"parts":[{"text":"Final answer."}]}}]}`,
		`data: {"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"SIG_TEXT_FINAL"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":25,"thoughtsTokenCount":10}}`,
		"",
	}, "\n\n")

	g := NewGoogleLLM(GoogleConfig{BaseURL: "http://x", APIKey: "k", DefaultModel: "gemini-4-argon"})
	ch := make(chan StreamEvent, 64)
	go g.pumpStream(context.Background(), "gemini-4-argon", io.NopCloser(strings.NewReader(sse)), ch)

	resp, err := CollectStream(context.Background(), ch)
	if err != nil {
		t.Fatalf("CollectStream: %v", err)
	}
	if resp.ReasoningContent != "thinking step 1 thinking step 2" {
		t.Fatalf("ReasoningContent = %q", resp.ReasoningContent)
	}
	if resp.Content != "Final answer." {
		t.Fatalf("Content = %q", resp.Content)
	}

	req := g.toGeminiRequest(context.Background(), "gemini-4-argon", []ChatMessage{
		{Role: "user", Content: "q1"},
		{
			Role:             "assistant",
			Content:          resp.Content,
			ReasoningContent: resp.ReasoningContent,
			ReasoningItems:   resp.ReasoningItems,
		},
		{Role: "user", Content: "q2"},
	}, nil, "")

	modelParts := req.Contents[1].Parts
	if len(modelParts) != 2 {
		t.Fatalf("model parts = %+v, want 2 parts (thought + text)", modelParts)
	}
	if !modelParts[0].Thought || modelParts[0].Text != "thinking step 1 thinking step 2" {
		t.Fatalf("part[0] = %+v, want thought part", modelParts[0])
	}
	if modelParts[1].Text != "Final answer." || modelParts[1].ThoughtSignature != "SIG_TEXT_FINAL" {
		t.Fatalf("part[1] = %+v, want text='Final answer.' with signature='SIG_TEXT_FINAL'", modelParts[1])
	}
}

// TestGeminiCrossProviderHistoryDoesNotInjectUnverifiedThoughts 验证跨模型切换安全性：
// 当历史 assistant 消息来自 DeepSeek / OpenAI（只有 ReasoningContent 或 rs_* ReasoningItems，
// 没有 gemini:sig:* 签名）时，toGeminiRequest 绝不伪造无签名的 thought:true part。
func TestGeminiCrossProviderHistoryDoesNotInjectUnverifiedThoughts(t *testing.T) {
	g := NewGoogleLLM(GoogleConfig{BaseURL: "http://x", APIKey: "k", DefaultModel: "gemini-4-argon"})
	msgs := []ChatMessage{
		{Role: "user", Content: "hello"},
		{
			Role:             "assistant",
			Content:          "from openai/deepseek",
			ReasoningContent: "some unverified reasoning chain",
			ReasoningItems: []ReasoningItem{
				{ID: "rs_openai_123", EncryptedContent: "openai_blob"},
			},
		},
		{Role: "user", Content: "follow up"},
	}
	req := g.toGeminiRequest(context.Background(), "gemini-4-argon", msgs, nil, "")
	modelParts := req.Contents[1].Parts
	if len(modelParts) != 1 || modelParts[0].Thought || modelParts[0].Text != "from openai/deepseek" {
		t.Fatalf("cross-provider assistant parts = %+v, want single plain text part without thought", modelParts)
	}
}
