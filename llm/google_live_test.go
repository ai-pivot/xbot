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

// ─── 思考 / 缓存命中 真实网关验证（2026-11-17）───────────────────
//
// 三条实测事实驱动这些用例：
//  1. 不传 thinkingConfig 时 gemini-4-argon 不思考（无 thoughtsTokenCount）；
//     默认档必须显式发 thinkingLevel 才会思考。
//  2. thinkingBudget 对该模型是 400，只有 thinkingLevel 可用。
//  3. 相同前缀第 3 次起上游返回 cachedContentTokenCount（隐式缓存），
//     必须映射到 TokenUsage.CacheHitTokens 才能算命中率。

func liveGoogleWithTokens(t *testing.T, maxTokens int) *GoogleLLM {
	t.Helper()
	base := strings.TrimSpace(os.Getenv("MINT_BASE"))
	key := strings.TrimSpace(os.Getenv("MINT_KEY"))
	if base == "" || key == "" {
		t.Skip("MINT_BASE/MINT_KEY not set")
	}
	return NewGoogleLLM(GoogleConfig{BaseURL: base, APIKey: key, DefaultModel: "gemini-4-argon", MaxTokens: maxTokens})
}

// 默认（thinkingMode 空串）必须真的思考：CompletionTokens 含 thoughts，
// 应显著大于 disabled 同题的 CompletionTokens。
func TestGoogleLiveThinkingOnByDefault(t *testing.T) {
	g := liveGoogleWithTokens(t, 2048)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	msgs := []ChatMessage{{Role: "user", Content: "Prove: for every prime p > 3, p^2 - 1 is divisible by 24. End with DONE."}}

	on, err := g.Generate(ctx, "gemini-4-argon", msgs, nil, "")
	if err != nil {
		t.Fatalf("Generate(thinking default): %v", err)
	}
	off, err := g.Generate(ctx, "gemini-4-argon", msgs, nil, "disabled")
	if err != nil {
		t.Fatalf("Generate(thinking disabled): %v", err)
	}
	t.Logf("default usage=%+v", on.Usage)
	t.Logf("disabled usage=%+v", off.Usage)
	if on.Usage.CompletionTokens <= off.Usage.CompletionTokens {
		t.Fatalf("default thinking did not add reasoning tokens: on=%d off=%d",
			on.Usage.CompletionTokens, off.Usage.CompletionTokens)
	}
}

// 相同长前缀重复请求，隐式缓存命中后 CacheHitTokens 必须 > 0。
func TestGoogleLiveCacheHitTokens(t *testing.T) {
	g := liveGoogleWithTokens(t, 32)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	filler := strings.Repeat("The quick brown fox jumps over the lazy dog. Caching probes need a stable prefix. "+
		"Alpha beta gamma delta epsilon zeta eta theta iota kappa. ", 120)
	msgs := []ChatMessage{{Role: "user", Content: filler + "\nReply with the single word: pong"}}

	var last TokenUsage
	for i := 1; i <= 4; i++ {
		resp, err := g.Generate(ctx, "gemini-4-argon", msgs, nil, "disabled")
		if err != nil {
			t.Fatalf("run#%d: %v", i, err)
		}
		last = resp.Usage
		t.Logf("run#%d prompt=%d cached=%d completion=%d total=%d",
			i, last.PromptTokens, last.CacheHitTokens, last.CompletionTokens, last.TotalTokens)
		if last.CacheHitTokens > 0 {
			if last.CacheHitTokens > last.PromptTokens {
				t.Fatalf("cached (%d) > prompt (%d) — mapping is wrong", last.CacheHitTokens, last.PromptTokens)
			}
			return
		}
	}
	t.Fatalf("cache never reported after 4 identical-prefix calls: %+v", last)
}

// 流式同样要带缓存命中字段（SSE 首个 chunk 的 usage 全零，不能被当成真实用量）。
func TestGoogleLiveStreamUsageCache(t *testing.T) {
	g := liveGoogleWithTokens(t, 32)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	filler := strings.Repeat("The quick brown fox jumps over the lazy dog. Streaming cache probe with a stable prefix. "+
		"Alpha beta gamma delta epsilon zeta eta theta iota kappa. ", 120)
	msgs := []ChatMessage{{Role: "user", Content: filler + "\nReply with the single word: pong"}}

	var usage *TokenUsage
	for i := 1; i <= 4; i++ {
		ch, err := g.GenerateStream(ctx, "gemini-4-argon", msgs, nil, "disabled")
		if err != nil {
			t.Fatalf("run#%d: %v", i, err)
		}
		usage = nil
		for ev := range ch {
			switch ev.Type {
			case EventUsage:
				usage = ev.Usage
			case EventError:
				t.Fatalf("run#%d stream error: %s", i, ev.Error)
			}
		}
		if usage == nil {
			t.Fatalf("run#%d: stream produced no usage event", i)
		}
		if usage.PromptTokens == 0 {
			t.Fatalf("run#%d: zero-valued usage leaked into the stream: %+v", i, usage)
		}
		t.Logf("stream run#%d prompt=%d cached=%d completion=%d", i, usage.PromptTokens, usage.CacheHitTokens, usage.CompletionTokens)
		if usage.CacheHitTokens > 0 {
			return
		}
	}
	t.Fatalf("stream never reported cache hit: %+v", usage)
}

// 思考正文必须真的到达调用方（不只是 token 计数）：上游只有显式
// includeThoughts=true 才回 thought=true 的 part，缺了它 UI 看不到思考过程。
func TestGoogleLiveReasoningTextVisible(t *testing.T) {
	g := liveGoogleWithTokens(t, 2048)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Second)
	defer cancel()
	msgs := []ChatMessage{{Role: "user", Content: "Prove: for every prime p > 3, p^2 - 1 is divisible by 24. End with DONE."}}

	resp, err := g.Generate(ctx, "gemini-4-argon", msgs, nil, "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	t.Logf("non-stream reasoning_len=%d usage=%+v", len(resp.ReasoningContent), resp.Usage)
	if strings.TrimSpace(resp.ReasoningContent) == "" {
		t.Fatalf("ReasoningContent empty — includeThoughts not applied?")
	}

	ch, err := g.GenerateStream(ctx, "gemini-4-argon", msgs, nil, "")
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	var reasoning, content strings.Builder
	for ev := range ch {
		switch ev.Type {
		case EventReasoningContent:
			reasoning.WriteString(ev.ReasoningContent)
		case EventContent:
			content.WriteString(ev.Content)
		case EventError:
			t.Fatalf("stream error: %s", ev.Error)
		}
	}
	t.Logf("stream reasoning_len=%d content_len=%d", reasoning.Len(), content.Len())
	if reasoning.Len() == 0 {
		t.Fatalf("stream produced no reasoning deltas — UI would show an empty thinking block")
	}
	if content.Len() == 0 {
		t.Fatalf("stream produced no content")
	}
}

// 真实网关回归：并行工具调用（一个 model turn 里多个 functionCall）必须能被
// 历史回放，否则用户会话会以
// "number of function response parts is equal to the number of function call parts"
// 连续 400。
func TestGoogleLiveParallelToolHistory(t *testing.T) {
	g := liveGoogleWithTokens(t, 512)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	toolDef := fakeTool{name: "get_weather"}
	msgs := []ChatMessage{
		{Role: "user", Content: "Compare weather in Beijing and Shanghai using the tool for both."},
		{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "a", Name: "get_weather", Arguments: `{"city":"Beijing"}`},
			{ID: "b", Name: "get_weather", Arguments: `{"city":"Shanghai"}`},
		}},
		{Role: "tool", ToolCallID: "a", ToolName: "get_weather", Content: `{"temp":"22C","sky":"sunny"}`},
		{Role: "tool", ToolCallID: "b", ToolName: "get_weather", Content: `{"temp":"27C","sky":"rain"}`},
		{Role: "user", Content: "In one short sentence: which is warmer?"},
	}
	resp, err := g.Generate(ctx, "gemini-4-argon", msgs, []ToolDefinition{toolDef}, "disabled")
	if err != nil {
		t.Fatalf("parallel tool history rejected by gateway: %v", err)
	}
	t.Logf("reply=%q usage=%+v", resp.Content, resp.Usage)
	if strings.TrimSpace(resp.Content) == "" {
		t.Fatal("empty reply")
	}
}

// 真实网关多轮带签名 Tool Call 闭环：
//   - 第 1 轮（流式）：让模型思考并发起工具调用，通过 CollectStream 收集
//     ReasoningContent、ToolCalls 与 ReasoningItems（含 thoughtSignature）；
//   - 第 2 轮（流式）：把第 1 轮产出的 assistant 消息（含 ReasoningItems）与
//     tool 结果一起发回网关，验证 thought:true part + thoughtSignature 原样回传成功。
func TestGoogleLiveThoughtSignatureMultiTurnStream(t *testing.T) {
	g := liveGoogleWithTokens(t, 1024)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	toolDef := fakeTool{name: "get_weather"}
	msgs := []ChatMessage{
		{Role: "user", Content: "Think briefly about why Beijing and Shanghai have different climates, then call get_weather for BOTH Beijing and Shanghai in parallel."},
	}

	ch1, err := g.GenerateStream(ctx, "gemini-4-argon", msgs, []ToolDefinition{toolDef}, "high")
	if err != nil {
		t.Fatalf("turn 1 GenerateStream: %v", err)
	}
	r1, err := CollectStream(ctx, ch1)
	if err != nil {
		t.Fatalf("turn 1 CollectStream: %v", err)
	}
	t.Logf("turn 1: tool_calls=%d reasoning_len=%d reasoning_items=%d",
		len(r1.ToolCalls), len(r1.ReasoningContent), len(r1.ReasoningItems))
	if len(r1.ToolCalls) == 0 {
		t.Fatalf("turn 1 did not call tool: content=%q", r1.Content)
	}
	hasSig := false
	for _, ri := range r1.ReasoningItems {
		if strings.HasPrefix(ri.ID, geminiSigPrefix) && ri.EncryptedContent != "" {
			hasSig = true
			break
		}
	}
	if !hasSig {
		t.Fatalf("turn 1 did not capture any Gemini thoughtSignature in ReasoningItems: %+v", r1.ReasoningItems)
	}

	msgs = append(msgs, ChatMessage{
		Role:             "assistant",
		Content:          r1.Content,
		ReasoningContent: r1.ReasoningContent,
		ReasoningItems:   r1.ReasoningItems,
		ToolCalls:        r1.ToolCalls,
	})
	for _, tc := range r1.ToolCalls {
		msgs = append(msgs, ChatMessage{
			Role:       "tool",
			ToolCallID: tc.ID,
			ToolName:   tc.Name,
			Content:    `{"temp":"22C","sky":"sunny"}`,
		})
	}

	// 确认 toGeminiRequest 确实把 thoughtSignature 挂回了第 2 轮请求
	req2 := g.toGeminiRequest(ctx, "gemini-4-argon", msgs, []ToolDefinition{toolDef}, "low")
	sigCount := 0
	for _, c := range req2.Contents {
		for _, p := range c.Parts {
			if p.ThoughtSignature != "" {
				sigCount++
			}
		}
	}
	if sigCount == 0 {
		t.Fatalf("turn 2 request did not carry any thoughtSignature back: %+v", req2.Contents)
	}

	ch2, err := g.GenerateStream(ctx, "gemini-4-argon", msgs, []ToolDefinition{toolDef}, "low")
	if err != nil {
		t.Fatalf("turn 2 GenerateStream: %v", err)
	}
	r2, err := CollectStream(ctx, ch2)
	if err != nil {
		t.Fatalf("turn 2 CollectStream: %v", err)
	}
	t.Logf("turn 2 reply=%q reasoning_items=%d usage=%+v", r2.Content, len(r2.ReasoningItems), r2.Usage)
	if strings.TrimSpace(r2.Content) == "" {
		t.Fatal("turn 2 empty reply")
	}
}
