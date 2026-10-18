package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// rawSSEServer serves a fixed SSE body verbatim (no chunk re-encoding) — used to
// reproduce the EXACT byte shape a real gateway streams for PARALLEL tool calls
// (name/id in one chunk, arguments deltas in separate chunks with no id/name).
func rawSSEServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		f := w.(http.Flusher)
		_, _ = w.Write([]byte(body))
		f.Flush()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestParallelToolCalls_RealGatewayShape reproduces the production bug
// "一个迭代调用多个工具 ⇒ 全部工具 parse args: unexpected end of JSON input".
//
// The raw body below is byte-for-byte what the production gateway
// (mint-alpha.macaron.xin) streams for three parallel tool calls — captured by a
// live curl. Each tool's id/name arrives in its OWN chunk, and its arguments
// arrive in a SEPARATE chunk whose tool_call object carries ONLY {index,
// function:{arguments}}. A correct assembler must produce three tool calls with
// their full arguments.
func TestParallelToolCalls_RealGatewayShape(t *testing.T) {
	const body = "data: {\"id\":\"resp_1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-6-sol\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"resp_1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-6-sol\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_a\",\"type\":\"function\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"\"}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"resp_1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-6-sol\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"city\\\":\\\"Beijing\\\"}\"}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"resp_1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-6-sol\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":1,\"id\":\"call_b\",\"type\":\"function\",\"function\":{\"name\":\"get_time\",\"arguments\":\"\"}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"resp_1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-6-sol\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":1,\"function\":{\"arguments\":\"{\\\"city\\\":\\\"Tokyo\\\"}\"}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"resp_1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-6-sol\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":2,\"id\":\"call_c\",\"type\":\"function\",\"function\":{\"name\":\"get_news\",\"arguments\":\"\"}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"resp_1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-6-sol\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":2,\"function\":{\"arguments\":\"{\\\"city\\\":\\\"London\\\"}\"}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"resp_1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-6-sol\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"\"},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := rawSSEServer(t, body)
	client := NewOpenAILLM(OpenAIConfig{
		BaseURL:      srv.URL,
		APIKey:       "test-key",
		DefaultModel: "gpt-6-sol",
		APIType:      APITypeChatCompletions,
	})

	ctx := context.Background()
	stream, err := client.newStreamingWithRetry(ctx, "gpt-6-sol", []ChatMessage{{Role: "user", Content: "hi"}}, nil, "", nil)
	if err != nil {
		t.Fatalf("newStreamingWithRetry: %v", err)
	}
	eventChan := make(chan StreamEvent, 100)
	go client.processStream(ctx, stream, eventChan, time.Now(), nil, "gpt-6-sol", nil, "",
		&firstChunkGuard{arrived: &atomic.Bool{}, timer: time.AfterFunc(time.Hour, func() {}), cancel: func() {}})

	resp, err := CollectStream(ctx, eventChan)
	if err != nil {
		t.Fatalf("CollectStream: %v", err)
	}

	if len(resp.ToolCalls) != 3 {
		t.Fatalf("got %d tool calls, want 3: %+v", len(resp.ToolCalls), resp.ToolCalls)
	}
	want := map[string]string{
		"get_weather": `{"city":"Beijing"}`,
		"get_time":    `{"city":"Tokyo"}`,
		"get_news":    `{"city":"London"}`,
	}
	for _, tc := range resp.ToolCalls {
		if w, ok := want[tc.Name]; ok {
			if strings.TrimSpace(tc.Arguments) != w {
				t.Errorf("tool %s arguments = %q, want %q (EMPTY = the production bug)", tc.Name, tc.Arguments, w)
			}
		} else {
			t.Errorf("unexpected tool %q", tc.Name)
		}
	}
	fmt.Printf("assembled tool calls: %+v\n", resp.ToolCalls)
}
