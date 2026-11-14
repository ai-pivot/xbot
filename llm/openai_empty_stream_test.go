package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

// Issue #282 — 0-chunk 空流被当成功。
//
// 事故形态：provider / 代理对 POST /v1/chat/completions 回 200 +
// Content-Type: text/event-stream，然后**一个事件都不发就关掉 body**。
// processStream 的 `for stream.Next()` 一次都不执行 → chunkCount==0 →
// 截断保护（显式要求 chunkCount>0）被跳过 → 无条件发 EventDone →
// CollectStreamWithCallback 看到 gotDone 返回 (resp, nil) = 成功。
// 上层于是把「模型什么都没说」记成了一次正常回答（issue #282）。
//
// 本文件钉死修复后的契约：
//   1. 0-chunk 空流 = 可重试错误（错误串含 "empty stream"）；
//   2. 错误串带上被捕获的响应体尾（streamBodyTail 不再"只写不读"）；
//   3. near-empty 日志字段带受长度限制的单行 body tail，且 chunkCount==0 时
//      ttft 不是 int64 溢出值；
//   4. 既有 RetryLLM 链真的接上了（2 次空流 + 1 次正常 = 成功；3 次全空 = 报错）；
//   5. 用户取消不会被当成空流错误（否则触发重试风暴）。

// sseHeaders 写 SSE 响应头并立刻 flush —— 客户端拿到 200 之后 body 里什么都没有。
func sseHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// emptySSEStreamServer 复刻空流：刷完 SSE 头，零事件干净关闭（stream.Err()==nil）。
func emptySSEStreamServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseHeaders(w)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestOpenAILLM(baseURL string) *OpenAILLM {
	return NewOpenAILLM(OpenAIConfig{BaseURL: baseURL, APIKey: "test-key", MaxTokens: 10})
}

// ---------------------------------------------------------------------------
// AC1: 0-chunk 空流必须返回 error != nil 且含 "empty stream"
// ---------------------------------------------------------------------------

func TestGenerateStream_EmptyStreamIsError(t *testing.T) {
	srv := emptySSEStreamServer(t)
	o := newTestOpenAILLM(srv.URL)
	ctx := context.Background()

	eventCh, err := o.GenerateStream(ctx, "test-model", []ChatMessage{{Role: "user", Content: "hi"}}, nil, "")
	if err != nil {
		t.Fatalf("建流不应失败（HTTP 200 + SSE 头是合法的）：%v", err)
	}

	resp, err := CollectStream(ctx, eventCh)
	if err == nil {
		t.Fatalf("0-chunk 空流必须返回错误，实际 err=nil（issue #282 修复前形态：空响应被当成功），resp=%+v", resp)
	}
	if !strings.Contains(err.Error(), "empty stream") {
		t.Fatalf("错误串必须含 \"empty stream\"，实际：%v", err)
	}
}

// ---------------------------------------------------------------------------
// AC2: 空流时把 provider 的响应体尾读出来（streamBodyTail 之前只写不读）
// ---------------------------------------------------------------------------

func TestGenerateStream_EmptyStreamCarriesBodyTail(t *testing.T) {
	// 非 SSE 的错误体：0 个可解析事件，但 body 里写着真实原因。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseHeaders(w)
		_, _ = io.WriteString(w, `{"error":"content filter"}`)
	}))
	defer srv.Close()

	o := newTestOpenAILLM(srv.URL)
	ctx := context.Background()

	eventCh, err := o.GenerateStream(ctx, "test-model", []ChatMessage{{Role: "user", Content: "hi"}}, nil, "")
	if err != nil {
		t.Fatalf("建流不应失败：%v", err)
	}
	_, err = CollectStream(ctx, eventCh)
	if err == nil {
		t.Fatal("空流必须返回错误")
	}
	if !strings.Contains(err.Error(), "empty stream") {
		t.Fatalf("错误串必须含 \"empty stream\"，实际：%v", err)
	}
	if !strings.Contains(err.Error(), "content filter") {
		t.Fatalf("错误串必须带响应体尾（证明 streamBodyTail 被真的读出来），实际：%v", err)
	}
}

// ---------------------------------------------------------------------------
// AC3 + AC4: near-empty 日志字段（body tail 有界、ttft 不溢出）
// ---------------------------------------------------------------------------

func TestNearEmptyStreamFields_BodyTailAndTTFT(t *testing.T) {
	// tailReader 捕获到的原始字节：大量 SSE keepalive（会让日志爆炸）+ 真正的
	// 错误体 + 换行/控制字符/非法 UTF-8。
	raw := strings.Repeat("data: keepalive \n\n", 400) + "<html>502 Bad Gateway</html>\x00\x1b\xff\n"

	o := &OpenAILLM{}
	o.streamBodyTail = []byte(raw) // 模拟 tailReader 已写入（含锁内语义）

	tail, size := o.streamBodyTailSnapshot()
	if size != len(raw) {
		t.Fatalf("stream_body_bytes = %d, want %d", size, len(raw))
	}
	if tail == "" {
		t.Fatal("stream_body_tail 不能为空——空流时响应体是唯一证据")
	}

	fields := nearEmptyStreamFields(
		[]ChatMessage{{Role: "user", Content: "hi"}}, "test-model", nil, "enabled",
		0, time.Now(), time.Time{}, "", tail, size)

	got, _ := fields["stream_body_tail"].(string)
	if got == "" {
		t.Fatal("fields[stream_body_tail] 必须非空")
	}
	if n := utf8.RuneCountInString(got); n > streamBodyTailLogRuneLimit {
		t.Fatalf("stream_body_tail 长度 %d rune 超过上限 %d", n, streamBodyTailLogRuneLimit)
	}
	if strings.ContainsAny(got, "\n\r\t") || strings.ContainsAny(got, "\x00\x1b") {
		t.Fatalf("stream_body_tail 必须单行化（换行/控制字符要转成可见转义），实际 %q", got)
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("stream_body_tail 不应残留非法 UTF-8：%q", got)
	}
	if !strings.Contains(got, "502 Bad Gateway") {
		t.Fatalf("截断必须保留尾部（真实错误体在后半段），实际 %q", got)
	}

	// AC4：chunkCount==0 时 firstChunkTime 是零值，Sub() 会 int64 溢出成
	// "-2562047h47m16.854775808s"（读起来像几百年前的超时，误导排障）。
	if fields["ttft"] != "n/a" {
		t.Fatalf("chunkCount==0 时 ttft 必须是 \"n/a\"，实际 %v", fields["ttft"])
	}
	if s := fmt.Sprint(fields["ttft"]); strings.Contains(s, "-2562047") {
		t.Fatalf("ttft 不得是 int64 溢出值：%v", s)
	}

	// 现有诊断字段必须还在（ops 现在靠它们定位空响应）。
	for _, k := range []string{
		"provider", "chunk_count", "total_duration", "finish_reason",
		"stream_body_bytes", "stream_body_tail",
		"msg_count", "tools_count", "model", "thinking_mode", "last_user_msg_preview",
	} {
		if _, ok := fields[k]; !ok {
			t.Fatalf("日志字段缺失：%s（现有 %v）", k, keysOf(fields))
		}
	}
	if fields["chunk_count"] != 0 {
		t.Fatalf("chunk_count = %v, want 0", fields["chunk_count"])
	}

	// 对照面：有首帧时 ttft 仍然是正常时长字符串，不能被 "n/a" 吃掉。
	start := time.Now().Add(-1500 * time.Millisecond)
	f2 := nearEmptyStreamFields(nil, "m", nil, "", 1, start, start.Add(1500*time.Millisecond), FinishReasonStop, "", 0)
	if f2["ttft"] == "n/a" {
		t.Fatal("有首帧时 ttft 不能是 \"n/a\"")
	}
	if s := fmt.Sprint(f2["ttft"]); !strings.Contains(s, "1.5") {
		t.Fatalf("有首帧时 ttft 应是真实时长（约 1.5s），实际 %v", s)
	}
}

func keysOf(fields map[string]any) []string {
	out := make([]string, 0, len(fields))
	for k := range fields {
		out = append(out, k)
	}
	return out
}

// ---------------------------------------------------------------------------
// 判别力对照：只有 reasoning（没有正文）不是空流，不能被误杀
// ---------------------------------------------------------------------------

func TestGenerateStream_ReasoningOnlyStreamIsNotEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseHeaders(w)
		_, _ = io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","created":1,"model":"test-model","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"thinking..."},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","created":1,"model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	o := newTestOpenAILLM(srv.URL)
	ctx := context.Background()
	eventCh, err := o.GenerateStream(ctx, "test-model", []ChatMessage{{Role: "user", Content: "hi"}}, nil, "enabled")
	if err != nil {
		t.Fatalf("建流不应失败：%v", err)
	}
	resp, err := CollectStream(ctx, eventCh)
	if err != nil {
		t.Fatalf("只有 reasoning 的流不是空流，不得报错：%v", err)
	}
	if resp.ReasoningContent != "thinking..." {
		t.Fatalf("reasoning 内容丢失：%+v", resp)
	}
}

// ---------------------------------------------------------------------------
// AC5: 空流错误真的接上既有 RetryLLM 链
// ---------------------------------------------------------------------------

// countingSSEServer 前 emptyAttempts 次 /chat/completions 返回 0-chunk 空流，
// 之后返回一段正常内容（content "hello" + finish_reason stop + [DONE]）。
func countingSSEServer(t *testing.T, emptyAttempts int32) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		n := atomic.AddInt32(&calls, 1)
		sseHeaders(w)
		if n <= emptyAttempts {
			return // 空流：0 事件干净关闭
		}
		_, _ = io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","created":1,"model":"test-model","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","created":1,"model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestRetryLLM_EmptyStreamRetriedThenSucceeds(t *testing.T) {
	srv, calls := countingSSEServer(t, 2)
	o := newTestOpenAILLM(srv.URL)
	r := NewRetryLLM(o, RetryConfig{Attempts: 3, Delay: time.Millisecond, MaxDelay: time.Millisecond})

	resp, err := r.GenerateStreamAndCollect(context.Background(), "test-model",
		[]ChatMessage{{Role: "user", Content: "hi"}}, nil, "", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("前两次空流、第三次成功，最终不应报错：%v", err)
	}
	if resp == nil || resp.Content != "hello" {
		t.Fatalf("最终内容错误：%+v", resp)
	}
	if got := atomic.LoadInt32(calls); got != 3 {
		t.Fatalf("上游被调用 %d 次，want 3（空流必须触发重试）", got)
	}
}

func TestRetryLLM_EmptyStreamExhausted(t *testing.T) {
	srv, calls := countingSSEServer(t, 100)
	o := newTestOpenAILLM(srv.URL)
	r := NewRetryLLM(o, RetryConfig{Attempts: 3, Delay: time.Millisecond, MaxDelay: time.Millisecond})

	resp, err := r.GenerateStreamAndCollect(context.Background(), "test-model",
		[]ChatMessage{{Role: "user", Content: "hi"}}, nil, "", nil, nil, nil, nil)
	if err == nil {
		t.Fatalf("3 次全空流最终必须报错（不能返回 nil error + 空响应），resp=%+v", resp)
	}
	if !strings.Contains(err.Error(), "empty stream") {
		t.Fatalf("最终错误串应含 \"empty stream\"，实际：%v", err)
	}
	if got := atomic.LoadInt32(calls); got != 3 {
		t.Fatalf("上游被调用 %d 次，want 3（Attempts:3 耗尽）", got)
	}
}

// ---------------------------------------------------------------------------
// AC6: 用户取消不能被当成空流错误（否则引发重试风暴）
// ---------------------------------------------------------------------------

func TestGenerateStream_CancelIsNotAnEmptyStreamError(t *testing.T) {
	// 用户取消的真实形态：头已刷、零事件、连接保持（上游还没吐第一个 chunk）。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseHeaders(w)
		<-r.Context().Done() // 挂到客户端断开
	}))
	defer srv.Close()

	o := newTestOpenAILLM(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	eventCh, err := o.GenerateStream(ctx, "test-model", []ChatMessage{{Role: "user", Content: "hi"}}, nil, "")
	if err != nil {
		t.Fatalf("建流不应失败：%v", err)
	}
	cancel() // 用户在第一个 chunk 之前取消

	_, err = CollectStream(ctx, eventCh)
	if err == nil {
		t.Fatal("取消后必须返回错误（context.Canceled），不能当成成功")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应以 context.Canceled 冒泡，实际：%v", err)
	}
	if strings.Contains(err.Error(), "empty stream") {
		t.Fatalf("用户取消不能被当成空流错误（会触发整条重试链对抗用户意图）：%v", err)
	}
	if IsRetryableError(err) {
		t.Fatalf("context.Canceled 必须不可重试，实际 IsRetryableError(%v)=true", err)
	}
}

// TestClassifyFinishedStream 是 AC6 的确定性版本：真实链路上「取消」与「body
// 干净 EOF」的先后顺序不可控（ctx cancel 会让 watcher 先 close stream，错误从
// 既有 stream.Err() 分支冒泡），所以「空流 + ctx 已结束 ⇒ 静默、不报错」这条
// 判定必须在纯函数层面钉死，否则删掉它也不会有测试变红。
func TestClassifyFinishedStream(t *testing.T) {
	cases := []struct {
		name                                   string
		hasContent, hasToolCalls, hasReasoning bool
		ctxErr                                 error
		want                                   emptyStreamOutcome
	}{
		{"content", true, false, false, nil, streamHasPayload},
		{"tool_call_only", false, true, false, nil, streamHasPayload},
		{"reasoning_only", false, false, true, nil, streamHasPayload},
		{"reasoning_only_and_canceled", false, false, true, context.Canceled, streamHasPayload},
		{"empty_and_canceled", false, false, false, context.Canceled, streamEmptyCanceled},
		{"empty_and_deadline", false, false, false, context.DeadlineExceeded, streamEmptyCanceled},
		{"empty_while_alive", false, false, false, nil, streamEmptyFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyFinishedStream(c.hasContent, c.hasToolCalls, c.hasReasoning, c.ctxErr)
			if got != c.want {
				t.Fatalf("classifyFinishedStream = %v, want %v", got, c.want)
			}
		})
	}
}

// TestGenerateStreamResponses_EmptyStreamIsError —— Responses API 侧的同款修复
// （llm/openai_responses.go 的 processResponsesStream）。结构与 chat completions
// 一致，所以复用同一套 classifyFinishedStream / emptyStreamMessage；这里只需要
// 一条端到端用例确认它真的接上了、并且响应体尾也被读出来。
func TestGenerateStreamResponses_EmptyStreamIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseHeaders(w)
		_, _ = io.WriteString(w, `{"error":"quota exceeded"}`)
	}))
	defer srv.Close()

	o := NewOpenAILLM(OpenAIConfig{BaseURL: srv.URL, APIKey: "k", DefaultModel: "resp-model", APIType: APITypeResponses})
	ctx := context.Background()

	eventCh, err := o.GenerateStream(ctx, "resp-model", []ChatMessage{{Role: "user", Content: "hi"}}, nil, "")
	if err != nil {
		t.Fatalf("建流不应失败（HTTP 200 + SSE 头）：%v", err)
	}
	_, err = CollectStream(ctx, eventCh)
	if err == nil {
		t.Fatal("Responses API 的 0 事件空流必须返回错误")
	}
	if !strings.Contains(err.Error(), "empty stream") {
		t.Fatalf("错误串必须含 \"empty stream\"，实际：%v", err)
	}
	if !strings.Contains(err.Error(), "quota exceeded") {
		t.Fatalf("错误串必须带响应体尾，实际：%v", err)
	}
}

// TestGenerateStreamResponses_NonEmptyStreamStillSucceeds 是对照面：加了空流判定
// 之后，正常带文本的 Responses 流必须照旧成功（否则会把好流误判成空流）。
func TestGenerateStreamResponses_NonEmptyStreamStillSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseHeaders(w)
		_, _ = io.WriteString(w, `data: {"type":"response.output_text.delta","item_id":"msg_1","delta":"hello"}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"r1","status":"completed","usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7},"output":[]}}`+"\n\n")
	}))
	defer srv.Close()

	o := NewOpenAILLM(OpenAIConfig{BaseURL: srv.URL, APIKey: "k", DefaultModel: "resp-model", APIType: APITypeResponses})
	ctx := context.Background()

	eventCh, err := o.GenerateStream(ctx, "resp-model", []ChatMessage{{Role: "user", Content: "hi"}}, nil, "")
	if err != nil {
		t.Fatalf("建流不应失败：%v", err)
	}
	resp, err := CollectStream(ctx, eventCh)
	if err != nil {
		t.Fatalf("正常 Responses 流不得报错：%v", err)
	}
	if resp.Content != "hello" {
		t.Fatalf("内容丢失：%+v", resp)
	}
	if resp.Usage.TotalTokens != 7 {
		t.Fatalf("usage 丢失：%+v", resp.Usage)
	}
}
