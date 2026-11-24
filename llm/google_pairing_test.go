package llm

import (
	"context"
	"testing"
)

// Gemini 硬性约束：一个 model turn 里的 N 个 functionCall，必须由紧接着的
// **同一个 user turn** 里的 N 个 functionResponse 一一对应，否则整个请求 400：
//
//	"Please ensure that the number of function response parts is equal to the
//	 number of function call parts of the function call turn."
//
// xbot 并行工具调用会产生 N 条连续 tool 消息，这里覆盖三种历史形态。
func TestToGeminiRequestMergesParallelToolResponses(t *testing.T) {
	g := NewGoogleLLM(GoogleConfig{BaseURL: "http://x", APIKey: "k", DefaultModel: "gemini-4-argon"})
	msgs := []ChatMessage{
		{Role: "user", Content: "check three cities"},
		{Role: "assistant", Content: "", ToolCalls: []ToolCall{
			{ID: "a", Name: "get_weather", Arguments: `{"city":"A"}`},
			{ID: "b", Name: "get_weather", Arguments: `{"city":"B"}`},
			{ID: "c", Name: "get_weather", Arguments: `{"city":"C"}`},
		}},
		{Role: "tool", ToolCallID: "a", ToolName: "get_weather", Content: "sunny"},
		{Role: "tool", ToolCallID: "b", ToolName: "get_weather", Content: "rain"},
		{Role: "tool", ToolCallID: "c", ToolName: "get_weather", Content: "snow"},
		{Role: "user", Content: "thanks"},
	}
	req := g.toGeminiRequest(context.Background(), "gemini-4-argon", msgs, nil, "")

	calls, resps, respTurns := 0, 0, 0
	for _, c := range req.Contents {
		for _, p := range c.Parts {
			if p.FunctionCall != nil {
				calls++
			}
			if p.FunctionResponse != nil {
				resps++
			}
		}
		if isToolResponseTurn(c) {
			respTurns++
		}
	}
	if calls != 3 {
		t.Fatalf("functionCall parts = %d, want 3", calls)
	}
	if respTurns != 1 {
		t.Fatalf("tool-response turns = %d, want 1 (responses must be merged into one user turn)", respTurns)
	}
	if resps != 3 {
		t.Fatalf("functionResponse parts = %d, want 3", resps)
	}
	// 结构必须是 model(calls) 紧跟同 turn 的 user(responses)
	for i, c := range req.Contents {
		n := 0
		for _, p := range c.Parts {
			if p.FunctionCall != nil {
				n++
			}
		}
		if n == 0 {
			continue
		}
		if i+1 >= len(req.Contents) || !isToolResponseTurn(req.Contents[i+1]) {
			t.Fatalf("model turn #%d with %d functionCall not followed by a tool-response turn", i, n)
		}
		m := 0
		for _, p := range req.Contents[i+1].Parts {
			if p.FunctionResponse != nil {
				m++
			}
		}
		if m != n {
			t.Fatalf("model turn #%d: %d calls but %d responses (Gemini rejects this)", i, n, m)
		}
	}
}

// 工具结果缺失（取消/跳过）时必须补占位，否则 Gemini 400。
func TestToGeminiRequestPadsMissingToolResponses(t *testing.T) {
	g := NewGoogleLLM(GoogleConfig{BaseURL: "http://x", APIKey: "k", DefaultModel: "gemini-4-argon"})
	msgs := []ChatMessage{
		{Role: "user", Content: "check two cities"},
		{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "a", Name: "get_weather", Arguments: `{}`},
			{ID: "b", Name: "get_weather", Arguments: `{}`},
		}},
		{Role: "tool", ToolCallID: "a", ToolName: "get_weather", Content: "sunny"},
	}
	req := g.toGeminiRequest(context.Background(), "gemini-4-argon", msgs, nil, "")

	found := false
	for i, c := range req.Contents {
		calls := 0
		for _, p := range c.Parts {
			if p.FunctionCall != nil {
				calls++
			}
		}
		if calls == 0 || i+1 >= len(req.Contents) {
			continue
		}
		resps := 0
		for _, p := range req.Contents[i+1].Parts {
			if p.FunctionResponse != nil {
				resps++
			}
		}
		if resps != calls {
			t.Fatalf("%d calls but %d responses after padding", calls, resps)
		}
		found = true
	}
	if !found {
		t.Fatal("no functionCall turn found in request")
	}
}

// 跨 provider 带进来的孤儿 tool 消息不能变成非法 turn，应降级成文本。
func TestToGeminiRequestDowngradesOrphanToolMessages(t *testing.T) {
	g := NewGoogleLLM(GoogleConfig{BaseURL: "http://x", APIKey: "k", DefaultModel: "gemini-4-argon"})
	msgs := []ChatMessage{
		{Role: "tool", ToolCallID: "x", ToolName: "get_weather", Content: "sunny"},
		{Role: "user", Content: "hello"},
	}
	req := g.toGeminiRequest(context.Background(), "gemini-4-argon", msgs, nil, "")
	for _, c := range req.Contents {
		if isToolResponseTurn(c) {
			t.Fatalf("orphan tool message must not become a functionResponse turn: %+v", c)
		}
	}
	joined := ""
	for _, c := range req.Contents {
		for _, p := range c.Parts {
			joined += p.Text
		}
	}
	if joined == "" {
		t.Fatal("orphan tool result disappeared entirely; expected a text fallback")
	}
}
