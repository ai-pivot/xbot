package llm

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLive_GatewayParallelToolCalls is a MANUAL, env-gated probe that drives the
// REAL production path (GenerateStream + CollectStreamWithCallbackFrom) against
// the REAL gateway with a large tool set, to see whether parallel tool calls
// come back with their arguments intact.
//
//	Run: XBOT_LIVE_GATEWAY=1 XBOT_LIVE_BASE=... XBOT_LIVE_KEY=... XBOT_LIVE_MODEL=... \
//	     go test ./llm -run TestLive_GatewayParallelToolCalls -v
func TestLive_GatewayParallelToolCalls(t *testing.T) {
	if os.Getenv("XBOT_LIVE_GATEWAY") != "1" {
		t.Skip("set XBOT_LIVE_GATEWAY=1 to run the live gateway probe")
	}
	base := os.Getenv("XBOT_LIVE_BASE")
	key := os.Getenv("XBOT_LIVE_KEY")
	model := os.Getenv("XBOT_LIVE_MODEL")

	client := NewOpenAILLM(OpenAIConfig{
		BaseURL:      base,
		APIKey:       key,
		DefaultModel: model,
		APIType:      APITypeChatCompletions,
	})

	tools := []ToolDefinition{}
	names := []string{"get_weather", "get_time", "get_news", "get_stock", "get_flight"}
	for _, n := range names {
		tools = append(tools, fakeTool{name: n})
	}

	msgs := []ChatMessage{
		{Role: "system", Content: "You are a test harness. Call EVERY requested tool in the SAME assistant turn, in parallel. Also produce a short reasoning before deciding."},
		{Role: "user", Content: "In one turn, call all five tools: get_weather(Beijing), get_time(Tokyo), get_news(London), get_stock(NYSE:AAPL), get_flight(CA981)."},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	eventCh, err := client.GenerateStream(ctx, model, msgs, tools, "think-max")
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	resp, err := CollectStreamWithCallbackFrom(ctx, eventCh, time.Now(), nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("CollectStream: %v", err)
	}
	t.Logf("content=%q", truncateStr(resp.Content, 120))
	t.Logf("reasoning len=%d", len(resp.ReasoningContent))
	t.Logf("finish=%s", resp.FinishReason)
	for i, tc := range resp.ToolCalls {
		t.Logf("tool[%d] id=%s name=%s args=%q", i, tc.ID, tc.Name, tc.Arguments)
	}
	if len(resp.ToolCalls) < 2 {
		t.Fatalf("expected >=2 tool calls, got %d", len(resp.ToolCalls))
	}
	bad := 0
	for _, tc := range resp.ToolCalls {
		if strings.TrimSpace(tc.Arguments) == "" {
			bad++
		}
	}
	if bad > 0 {
		t.Fatalf("REPRODUCED: %d/%d tool calls have EMPTY arguments", bad, len(resp.ToolCalls))
	}
}

type fakeTool struct{ name string }

func (f fakeTool) Name() string        { return f.name }
func (f fakeTool) Description() string { return "test tool " + f.name }
func (f fakeTool) Parameters() []ToolParam {
	return []ToolParam{{Name: "city", Type: "string", Description: "target", Required: true}}
}
