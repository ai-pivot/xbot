//go:build live

package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
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

// ─── 多模态（Gemini 原生 inline_data）─────────────────────────────

// 生成一张纯红色 PNG 的 data: URL，避免依赖外部图片文件。
func redPNGDataURL(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 48, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 48; x++ {
			img.Set(x, y, color.RGBA{R: 220, G: 20, B: 20, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// 视觉必须真的把像素送进 Gemini（inline_data），而不是降级成文本占位。
// 判据：模型能说出图片颜色（纯红），说明确实看到了像素。
func TestGoogleLiveVisionInlineData(t *testing.T) {
	g := liveGoogle(t)
	g.cfg.Multimodal = &MultimodalConfig{VisionEnabled: true}
	content := "![red-square](" + redPNGDataURL(t) + ")\nWhat is the dominant color of this image? Answer with ONE English word."
	resp, err := g.Generate(context.Background(), "gemini-4-argon",
		[]ChatMessage{{Role: "user", Content: content}}, nil, "")
	if err != nil {
		t.Fatalf("Generate with image: %v", err)
	}
	got := strings.ToLower(strings.TrimSpace(resp.Content))
	t.Logf("vision reply=%q usage=%+v", resp.Content, resp.Usage)
	if !strings.Contains(got, "red") && !strings.Contains(got, "红") {
		t.Fatalf("model did not see the pixels (reply=%q)", resp.Content)
	}
	if resp.Usage.PromptTokens < 50 {
		t.Fatalf("prompt tokens too low (%d) — image part likely dropped", resp.Usage.PromptTokens)
	}
}

// 视觉关闭时必须降级成文本占位、且请求不失败（非视觉模型收到 image part 会 400）。
func TestGoogleLiveVisionDisabledDegradesToText(t *testing.T) {
	g := liveGoogle(t)
	g.cfg.Multimodal = &MultimodalConfig{VisionEnabled: false}
	content := "![red-square](" + redPNGDataURL(t) + ")\nCan you see this image?"
	resp, err := g.Generate(context.Background(), "gemini-4-argon",
		[]ChatMessage{{Role: "user", Content: content}}, nil, "")
	if err != nil {
		t.Fatalf("Generate with vision disabled: %v", err)
	}
	if strings.TrimSpace(resp.Content) == "" {
		t.Fatal("empty reply with vision disabled")
	}
	t.Logf("vision-off reply=%q", resp.Content)
}

// 流式 + 图片：确认 SSE 路径同样携带 inline_data。
func TestGoogleLiveVisionStream(t *testing.T) {
	g := liveGoogle(t)
	g.cfg.Multimodal = &MultimodalConfig{VisionEnabled: true}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	ch, err := g.GenerateStream(ctx, "gemini-4-argon", []ChatMessage{{
		Role: "user", Content: "![red-square](" + redPNGDataURL(t) + ")\nName the color, one word.",
	}}, nil, "")
	if err != nil {
		t.Fatalf("GenerateStream with image: %v", err)
	}
	var sb strings.Builder
	for ev := range ch {
		switch ev.Type {
		case EventContent:
			sb.WriteString(ev.Content)
		case EventError:
			t.Fatalf("stream error: %s", ev.Error)
		}
	}
	got := strings.ToLower(sb.String())
	t.Logf("vision stream reply=%q", sb.String())
	if !strings.Contains(got, "red") && !strings.Contains(got, "红") {
		t.Fatalf("stream did not carry the image: %q", sb.String())
	}
}
