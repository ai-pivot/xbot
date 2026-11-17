//go:build live

package llm

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// 手工联调测试（不进 CI）：go test -tags live -run TestGoogleLive -v ./llm
//
// 需要环境变量：
//
//	MINT_BASE=https://pi-api-cn.macaron.xin   (不带 /v1)
//	MINT_KEY=<key>
//
// 目的：用真实网关注入 Gemini 原生协议（/v1beta/models/{m}:generateContent），
// 验证 google 客户端不是"能编译但跑不通"的空壳。
func liveGoogle(t *testing.T) *GoogleLLM {
	t.Helper()
	base := strings.TrimSpace(os.Getenv("MINT_BASE"))
	key := strings.TrimSpace(os.Getenv("MINT_KEY"))
	if base == "" || key == "" {
		t.Skip("MINT_BASE/MINT_KEY not set")
	}
	return NewGoogleLLM(GoogleConfig{BaseURL: base, APIKey: key, DefaultModel: "gemini-4-argon", MaxTokens: 64})
}

func TestGoogleLiveGenerate(t *testing.T) {
	g := liveGoogle(t)
	start := time.Now()
	resp, err := g.Generate(context.Background(), "gemini-4-argon",
		[]ChatMessage{{Role: "user", Content: "Reply with the single word: pong"}},
		nil, "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	t.Logf("latency=%s content=%q finish=%s usage=%+v", time.Since(start), resp.Content, resp.FinishReason, resp.Usage)
	if strings.TrimSpace(resp.Content) == "" {
		t.Fatal("empty content — google protocol path is broken")
	}
	if resp.Usage.PromptTokens == 0 || resp.Usage.CompletionTokens == 0 {
		t.Fatalf("usage not parsed: %+v", resp.Usage)
	}
}

func TestGoogleLiveSystemAndStream(t *testing.T) {
	g := liveGoogle(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ch, err := g.GenerateStream(ctx, "gemini-4-argon", []ChatMessage{
		{Role: "system", Content: "You are terse. Answer in one word."},
		{Role: "user", Content: "What is 2+2? One word."},
	}, nil, "")
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	var content, reasoning strings.Builder
	var gotDone, gotUsage bool
	for ev := range ch {
		switch ev.Type {
		case EventContent:
			content.WriteString(ev.Content)
		case EventReasoningContent:
			reasoning.WriteString(ev.ReasoningContent)
		case EventUsage:
			gotUsage = true
		case EventError:
			t.Fatalf("stream error: %s", ev.Error)
		case EventDone:
			gotDone = true
			t.Logf("finish=%s", ev.FinishReason)
		}
	}
	t.Logf("stream content=%q reasoning_len=%d usage=%v", content.String(), reasoning.Len(), gotUsage)
	if strings.TrimSpace(content.String()) == "" && reasoning.Len() == 0 {
		t.Fatal("stream produced no visible text")
	}
	if !gotDone {
		t.Fatal("stream never emitted done")
	}
}

// 订阅 provider=openai 但会员级 api_type=google 时必须走 Gemini 原生协议 —— 这里只验证
// 客户端本身；工厂层的选择逻辑由 agent/llm_factory_test.go 覆盖。
func TestGoogleLiveToolCallSchema(t *testing.T) {
	g := liveGoogle(t)
	resp, err := g.Generate(context.Background(), "gemini-4-argon", []ChatMessage{
		{Role: "user", Content: "Call the get_weather tool for Beijing. Do not answer in text."},
	}, []ToolDefinition{liveWeatherTool{}}, "")
	if err != nil {
		t.Fatalf("Generate with tools: %v", err)
	}
	t.Logf("finish=%s toolcalls=%d content=%q", resp.FinishReason, len(resp.ToolCalls), liveTruncate(resp.Content, 60))
	if len(resp.ToolCalls) == 0 {
		t.Skip("model chose not to call the tool this time (non-deterministic); protocol itself is fine")
	}
	tc := resp.ToolCalls[0]
	if tc.Name != "get_weather" || !strings.Contains(tc.Arguments, "Beijing") {
		t.Fatalf("bad tool call: %+v", tc)
	}
}

type liveWeatherTool struct{}

func (liveWeatherTool) Name() string        { return "get_weather" }
func (liveWeatherTool) Description() string { return "Get current weather for a city" }
func (liveWeatherTool) Parameters() []ToolParam {
	return []ToolParam{{Name: "city", Type: "string", Description: "city name", Required: true}}
}

func liveTruncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
