package llm

import (
	"context"
	"io"
	"strings"
	"testing"
)

// 用户报障（2026-11-17）："gemini 的会话老是自己终止，前端看不到报错"。
// 根因是 pumpStream 在没收到 finishReason 时无条件补一个 EventDone{Stop}，
// 把"空流/截断"伪装成正常完成；另外 SSE 里的 {"error":{...}} 帧（Gemini 在
// 流式中途出错时用这种形式，而不是 HTTP 状态码）以前完全没解析。
//
// 下面三条把这两个静默路径钉死。
func drainGoogleStream(t *testing.T, sse string) []StreamEvent {
	t.Helper()
	g := NewGoogleLLM(GoogleConfig{BaseURL: "http://x", APIKey: "k", DefaultModel: "m"})
	ch := make(chan StreamEvent, 64)
	go g.pumpStream(context.Background(), "m", io.NopCloser(strings.NewReader(sse)), ch)
	var evs []StreamEvent
	for ev := range ch {
		evs = append(evs, ev)
	}
	return evs
}

func TestPumpStreamEmitsErrorForErrorFrame(t *testing.T) {
	sse := "data: {\"error\":{\"code\":503,\"message\":\"The model is overloaded.\",\"status\":\"UNAVAILABLE\"}}\n\n"
	evs := drainGoogleStream(t, sse)
	if len(evs) != 1 || evs[0].Type != EventError {
		t.Fatalf("want a single EventError, got %+v", evs)
	}
	if !strings.Contains(evs[0].Error, "overloaded") {
		t.Fatalf("error text lost: %q", evs[0].Error)
	}
}

func TestPumpStreamEmptyStreamIsErrorNotDone(t *testing.T) {
	// 上游建立连接后什么都没推就断开：必须是错误，否则上层会把这一轮当成
	// "正常完成但空回复"，用户侧表现为会话自己终止且没有任何报错。
	for _, sse := range []string{"", "\n\n", ": keep-alive\n\n"} {
		evs := drainGoogleStream(t, sse)
		if len(evs) != 1 || evs[0].Type != EventError {
			t.Fatalf("empty stream %q: want single EventError, got %+v", sse, evs)
		}
	}
}

func TestPumpStreamPartialOutputWithoutFinishIsDone(t *testing.T) {
	// 有真实产出但上游没给 finishReason：仍按完成处理（避免误报），
	// 但要保证不再产生错误、内容完整。
	sse := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]}}]}\n\n"
	evs := drainGoogleStream(t, sse)
	var content string
	done := false
	for _, ev := range evs {
		switch ev.Type {
		case EventContent:
			content += ev.Content
		case EventDone:
			done = true
		case EventError:
			t.Fatalf("unexpected error for partial output: %s", ev.Error)
		}
	}
	if content != "hello" || !done {
		t.Fatalf("content=%q done=%v, want hello/true", content, done)
	}
}

func TestPumpStreamUsageAndThought(t *testing.T) {
	sse := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"thought\":true,\"text\":\"reasoning\"}]}}]}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"answer\"}]},\"finishReason\":\"STOP\"}]," +
		"\"usageMetadata\":{\"promptTokenCount\":100,\"candidatesTokenCount\":5,\"totalTokenCount\":120," +
		"\"cachedContentTokenCount\":80,\"thoughtsTokenCount\":15}}\n\n"
	evs := drainGoogleStream(t, sse)
	var reasoning, content string
	var usage *TokenUsage
	done := false
	for _, ev := range evs {
		switch ev.Type {
		case EventReasoningContent:
			reasoning += ev.ReasoningContent
		case EventContent:
			content += ev.Content
		case EventUsage:
			usage = ev.Usage
		case EventDone:
			done = true
		}
	}
	if reasoning != "reasoning" || content != "answer" || !done {
		t.Fatalf("reasoning=%q content=%q done=%v", reasoning, content, done)
	}
	if usage == nil || usage.CacheHitTokens != 80 || usage.CompletionTokens != 20 || usage.PromptTokens != 100 {
		t.Fatalf("usage mapping wrong: %+v", usage)
	}
}
