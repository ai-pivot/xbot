package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// IsRetryableError 测试
// ---------------------------------------------------------------------------

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		// nil
		{"nil error", nil, false},

		// 唯一例外的 context 错误：用户主动取消不是失败
		{"context.Canceled", context.Canceled, false},
		{"wrapped context.Canceled", fmt.Errorf("call failed: %w", context.Canceled), false},

		// 输入超长：确定性失败 + 上层有专门恢复路径（压缩后重试）
		{"input too long (dashscope)", errors.New("Range of input length should be [1, 202752]"), false},
		{"input too long (openai)", errors.New("maximum context length exceeded"), false},
		{"input too long (anthropic)", errors.New("prompt is too long: 210000 tokens"), false},

		// 以下全部可重试（默认策略）
		{"context.DeadlineExceeded", context.DeadlineExceeded, true},
		{"wrapped context.DeadlineExceeded", fmt.Errorf("timeout: %w", context.DeadlineExceeded), true},
		{"string context canceled", errors.New("something context canceled here"), true},
		{"string context deadline exceeded", errors.New("context deadline exceeded"), true},

		// 网络错误
		{"net.DNSError timeout", &net.DNSError{Err: "timeout", IsTimeout: true}, true},
		{"net.OpError", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, true},

		// HTTP 状态码 — OpenAI 格式: `POST "url": NNN StatusText`
		{"429 OpenAI", errors.New(`POST "https://api.openai.com/v1/chat": 429 Too Many Requests`), true},
		{"500 OpenAI", errors.New(`POST "https://api.openai.com/v1/chat": 500 Internal Server Error`), true},
		{"502 OpenAI", errors.New(`POST "https://api.openai.com/v1/chat": 502 Bad Gateway`), true},
		{"503 OpenAI", errors.New(`POST "https://api.openai.com/v1/chat": 503 Service Unavailable`), true},
		{"504 OpenAI", errors.New(`POST "https://api.openai.com/v1/chat": 504 Gateway Timeout`), true},

		// 4xx 现在也重试：无法预先区分「客户端错误」与网关/代理改写的瞬时故障，
		// 且重试被拒绝的请求不消耗配额，退避成本可接受。
		{"400 OpenAI", errors.New(`POST "url": 400 Bad Request`), true},
		{"401 OpenAI", errors.New(`POST "url": 401 Unauthorized`), true},
		{"403 OpenAI", errors.New(`POST "url": 403 Forbidden`), true},
		{"404 OpenAI", errors.New(`POST "url": 404 Not Found`), true},

		// 普通/未知错误 —— 白名单时代它们直接失败，正是用户报告的问题
		{"generic error", errors.New("something went wrong"), true},
		{"unknown provider error", errors.New("upstream returned garbage (xyz-123)"), true},
		{"EOF", errors.New("unexpected EOF"), true},

		// Anthropic SDK 错误格式: `anthropic API error: status=NNN, body=...`
		{"429 Anthropic", errors.New("anthropic API error: status=429, body={\"type\":\"error\"}"), true},
		{"500 Anthropic", errors.New("anthropic API error: status=500, body=internal error"), true},
		{"502 Anthropic", errors.New("anthropic API error: status=502, body=bad gateway"), true},
		{"503 Anthropic", errors.New("anthropic API error: status=503, body=overloaded"), true},
		{"400 Anthropic", errors.New("anthropic API error: status=400, body=bad request"), true},
		{"401 Anthropic", errors.New("anthropic API error: status=401, body=unauthorized"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsRetryableError(tt.err)
			if got != tt.want {
				t.Errorf("IsRetryableError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// failNLLM — 前 N 次调用失败，之后成功的 mock
// ---------------------------------------------------------------------------

type failNLLM struct {
	failCount int          // 前 N 次返回错误
	failErr   error        // 返回的错误
	calls     atomic.Int32 // 实际调用次数
	response  *LLMResponse // 成功时返回的响应
}

func newFailNLLM(failCount int, err error) *failNLLM {
	return &failNLLM{
		failCount: failCount,
		failErr:   err,
		response: &LLMResponse{
			Content:      "ok",
			FinishReason: FinishReasonStop,
			Usage:        TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		},
	}
}

func (m *failNLLM) Generate(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (*LLMResponse, error) {
	n := int(m.calls.Add(1))
	if n <= m.failCount {
		return nil, m.failErr
	}
	return m.response, nil
}

func (m *failNLLM) ListModels() []string {
	return []string{"fail-n-mock"}
}

func (m *failNLLM) GenerateStream(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (
	<-chan StreamEvent, error) {
	n := int(m.calls.Add(1))
	if n <= m.failCount {
		return nil, m.failErr
	}
	ch := make(chan StreamEvent, 2)
	ch <- StreamEvent{Type: EventContent, Content: "ok"}
	ch <- StreamEvent{Type: EventDone, FinishReason: FinishReasonStop}
	close(ch)
	return ch, nil
}

// ---------------------------------------------------------------------------
// Generate 重试测试
// ---------------------------------------------------------------------------

func TestRetryLLM_Generate_SuccessOnFirstTry(t *testing.T) {
	inner := newFailNLLM(0, nil)
	r := NewRetryLLM(inner, DefaultRetryConfig())

	resp, err := r.Generate(context.Background(), "test", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q, want %q", resp.Content, "ok")
	}
	if inner.calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", inner.calls.Load())
	}
}

func TestRetryLLM_Generate_RetryThenSuccess(t *testing.T) {
	// 前 2 次返回 502，第 3 次成功
	retryableErr := errors.New(`POST "url": 502 Bad Gateway`)
	inner := newFailNLLM(2, retryableErr)
	cfg := RetryConfig{Attempts: 3, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	resp, err := r.Generate(context.Background(), "test", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q, want %q", resp.Content, "ok")
	}
	if inner.calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", inner.calls.Load())
	}
}

func TestRetryLLM_Generate_ExhaustedRetries(t *testing.T) {
	// 始终返回 429，3 次尝试全部失败
	retryableErr := errors.New(`POST "url": 429 Too Many Requests`)
	inner := newFailNLLM(100, retryableErr)
	cfg := RetryConfig{Attempts: 3, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	_, err := r.Generate(context.Background(), "test", nil, nil, "")
	if err == nil {
		t.Fatal("expected error, got nil")
		return
	}
	if inner.calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", inner.calls.Load())
	}
}

func TestRetryLLM_Generate_NonRetryableError(t *testing.T) {
	// 输入超长是确定性失败 → 不重试，只调用 1 次（由上层压缩上下文后重试）。
	// 注意：401 之类的 4xx 现在**会**重试（见 TestIsRetryableError），
	// 所以这里不能再拿它当「不可重试」的例子。
	nonRetryableErr := errors.New("maximum context length exceeded")
	inner := newFailNLLM(100, nonRetryableErr)
	cfg := RetryConfig{Attempts: 3, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	_, err := r.Generate(context.Background(), "test", nil, nil, "")
	if err == nil {
		t.Fatal("expected error, got nil")
		return
	}
	if inner.calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (input-too-long is deterministic, must not retry)", inner.calls.Load())
	}
}

// TestRetryLLM_Generate_RetriesUnknownError 是用户报告问题的回归测试：
// 以前只有白名单里的错误（429/5xx/网络错误）才会重试，任何未被识别的失败
// （这里是一个 provider 自定义错误串）都会在第一次失败时直接冒泡，不触发
// 指数退避。现在所有失败都要走退避重试。
func TestRetryLLM_Generate_RetriesUnknownError(t *testing.T) {
	unknown := errors.New("upstream returned garbage (xyz-123)")
	inner := newFailNLLM(2, unknown) // 前两次失败，第三次成功
	cfg := RetryConfig{Attempts: 5, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	resp, err := r.Generate(context.Background(), "test", nil, nil, "")
	if err != nil {
		t.Fatalf("unknown error must be retried and eventually succeed, got: %v", err)
	}
	if resp == nil || resp.Content != "ok" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if got := inner.calls.Load(); got != 3 {
		t.Errorf("calls = %d, want 3 (2 failures + 1 success)", got)
	}
}

// TestRetryLLM_GenerateStreamAndCollect_RetriesUnknownStreamError 覆盖流式路径：
// 流中途的未识别错误同样必须重试。
func TestRetryLLM_GenerateStreamAndCollect_RetriesUnknownStreamError(t *testing.T) {
	unknown := errors.New("stream error: upstream glitch (code 7)")
	inner := newFailNLLM(1, unknown) // 第一次失败，第二次成功
	cfg := RetryConfig{Attempts: 5, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	resp, err := r.GenerateStreamAndCollect(context.Background(), "test", nil, nil, "", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unknown stream error must be retried, got: %v", err)
	}
	if resp == nil || resp.Content != "ok" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if got := inner.calls.Load(); got != 2 {
		t.Errorf("calls = %d, want 2 (1 failure + 1 success)", got)
	}
}

func TestRetryLLM_Generate_ContextCanceled(t *testing.T) {
	// context.Canceled 应停止重试（IsRetryableError 返回 false）
	inner := newFailNLLM(100, context.Canceled)
	cfg := RetryConfig{Attempts: 5, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}

	r := NewRetryLLM(inner, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	_, err := r.Generate(ctx, "test", nil, nil, "")
	if err == nil {
		t.Fatal("expected error after context cancel")
		return
	}
	// context.Canceled 不可重试，应只调用 1 次
	if inner.calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (context.Canceled is not retryable)", inner.calls.Load())
	}
}

func TestRetryLLM_Generate_NetworkError(t *testing.T) {
	// 网络错误可重试
	netErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	inner := newFailNLLM(1, netErr)
	cfg := RetryConfig{Attempts: 3, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	resp, err := r.Generate(context.Background(), "test", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q, want %q", resp.Content, "ok")
	}
	if inner.calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", inner.calls.Load())
	}
}

// ---------------------------------------------------------------------------
// GenerateStream 重试测试
// ---------------------------------------------------------------------------

func TestRetryLLM_GenerateStream_SuccessOnFirstTry(t *testing.T) {
	inner := newFailNLLM(0, nil)
	r := NewRetryLLM(inner, DefaultRetryConfig())

	ch, err := r.GenerateStream(context.Background(), "test", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var events []StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	if len(events) == 0 {
		t.Fatal("expected events, got none")
	}
}

func TestRetryLLM_GenerateStream_RetryConnection(t *testing.T) {
	// 前 1 次连接失败（返回 error），第 2 次成功
	retryableErr := errors.New(`POST "url": 503 Service Unavailable`)
	inner := newFailNLLM(1, retryableErr)
	cfg := RetryConfig{Attempts: 3, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	ch, err := r.GenerateStream(context.Background(), "test", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var gotContent bool
	for ev := range ch {
		if ev.Type == EventContent && ev.Content == "ok" {
			gotContent = true
		}
	}
	if !gotContent {
		t.Error("expected content event with 'ok'")
	}
}

func TestRetryLLM_GenerateStream_NonStreamingInner(t *testing.T) {
	// inner 不实现 StreamingLLM 时应返回错误
	inner := &nonStreamingLLM{}
	r := NewRetryLLM(inner, DefaultRetryConfig())

	_, err := r.GenerateStream(context.Background(), "test", nil, nil, "")
	if err == nil {
		t.Fatal("expected error for non-streaming LLM")
		return
	}
	if err.Error() != "underlying LLM does not support streaming" {
		t.Errorf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ListModels 测试（直接转发，不重试）
// ---------------------------------------------------------------------------

func TestRetryLLM_ListModels(t *testing.T) {
	inner := newFailNLLM(0, nil)
	r := NewRetryLLM(inner, DefaultRetryConfig())

	models := r.ListModels()
	if len(models) != 1 || models[0] != "fail-n-mock" {
		t.Errorf("ListModels() = %v, want [fail-n-mock]", models)
	}
}

// ---------------------------------------------------------------------------
// DefaultRetryConfig 测试
// ---------------------------------------------------------------------------

func TestDefaultRetryConfig(t *testing.T) {
	cfg := DefaultRetryConfig()
	if cfg.Attempts != 5 {
		t.Errorf("Attempts = %d, want 5", cfg.Attempts)
	}
	if cfg.Delay != 1*time.Second {
		t.Errorf("Delay = %v, want 1s", cfg.Delay)
	}
	if cfg.MaxDelay != 30*time.Second {
		t.Errorf("MaxDelay = %v, want 30s", cfg.MaxDelay)
	}
	if cfg.Timeout != 120*time.Second {
		t.Errorf("Timeout = %v, want 120s", cfg.Timeout)
	}
}

// ---------------------------------------------------------------------------
// 辅助类型
// ---------------------------------------------------------------------------

// nonStreamingLLM 只实现 LLM 接口，不实现 StreamingLLM
type nonStreamingLLM struct{}

func (n *nonStreamingLLM) Generate(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (*LLMResponse, error) {
	return &LLMResponse{Content: "ok", FinishReason: FinishReasonStop}, nil
}

func (n *nonStreamingLLM) ListModels() []string {
	return []string{"non-streaming"}
}

// ---------------------------------------------------------------------------
// WithRetryNotify 回调测试
// ---------------------------------------------------------------------------

func TestRetryLLM_Generate_NotifiesOnRetry(t *testing.T) {
	// 前 2 次返回 502，第 3 次成功
	retryableErr := errors.New(`POST "url": 502 Bad Gateway`)
	inner := newFailNLLM(2, retryableErr)
	cfg := RetryConfig{Attempts: 3, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	var notifications []struct {
		attempt, max uint
		err          error
	}
	ctx := WithRetryNotify(context.Background(), func(attempt, max uint, err error) {
		notifications = append(notifications, struct {
			attempt, max uint
			err          error
		}{attempt, max, err})
	})

	resp, err := r.Generate(ctx, "test", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q, want %q", resp.Content, "ok")
	}

	// 应该收到 2 次通知（第 1 次和第 2 次失败后各一次）
	if len(notifications) != 2 {
		t.Fatalf("notifications count = %d, want 2", len(notifications))
	}
	if notifications[0].attempt != 1 || notifications[0].max != 3 {
		t.Errorf("notification[0]: attempt=%d, max=%d, want 1, 3", notifications[0].attempt, notifications[0].max)
	}
	if notifications[1].attempt != 2 || notifications[1].max != 3 {
		t.Errorf("notification[1]: attempt=%d, max=%d, want 2, 3", notifications[1].attempt, notifications[1].max)
	}
}

func TestRetryLLM_Generate_NoNotifyWithoutCallback(t *testing.T) {
	// 没有注入回调时不应 panic
	retryableErr := errors.New(`POST "url": 502 Bad Gateway`)
	inner := newFailNLLM(1, retryableErr)
	cfg := RetryConfig{Attempts: 3, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	resp, err := r.Generate(context.Background(), "test", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q, want %q", resp.Content, "ok")
	}
}

func TestRetryLLM_GenerateStream_NotifiesOnRetry(t *testing.T) {
	retryableErr := errors.New(`POST "url": 503 Service Unavailable`)
	inner := newFailNLLM(1, retryableErr)
	cfg := RetryConfig{Attempts: 3, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	var notified atomic.Int32
	ctx := WithRetryNotify(context.Background(), func(attempt, max uint, err error) {
		notified.Add(1)
	})

	ch, err := r.GenerateStream(ctx, "test", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// drain channel
	for range ch {
	}

	if notified.Load() != 1 {
		t.Errorf("notified = %d, want 1", notified.Load())
	}
}

// ---------------------------------------------------------------------------
// 超时重试测试
// ---------------------------------------------------------------------------

func TestRetryLLM_Generate_TimeoutRetry(t *testing.T) {
	// 前 2 次返回 context.DeadlineExceeded，第 3 次成功
	inner := newFailNLLM(2, context.DeadlineExceeded)
	cfg := RetryConfig{
		Attempts: 5,
		Delay:    10 * time.Millisecond,
		MaxDelay: 50 * time.Millisecond,
		Timeout:  50 * time.Millisecond,
	}

	r := NewRetryLLM(inner, cfg)

	// 使用 context.WithTimeout 模拟调用方设置的超时
	parentCtx, parentCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer parentCancel()

	resp, err := r.Generate(parentCtx, "test", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q, want %q", resp.Content, "ok")
	}
	// 应该调用了 3 次（前 2 次超时，第 3 次成功）
	if inner.calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", inner.calls.Load())
	}
}

func TestRetryLLM_perAttemptCtx(t *testing.T) {
	t.Run("parent has deadline", func(t *testing.T) {
		r := NewRetryLLM(&nonStreamingLLM{}, RetryConfig{})
		parent, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		child, childCancel := r.perAttemptCtx(parent)
		defer childCancel()

		// child 应该有自己的 deadline（不继承 parent 的）
		if child == parent {
			t.Error("perAttemptCtx should create a new context")
		}
		childDeadline, ok := child.Deadline()
		if !ok {
			t.Fatal("child context should have a deadline")
		}
		parentDeadline, _ := parent.Deadline()
		// On Windows, time.Until() and WithTimeout can produce identical
		// deadlines due to lower timer precision. Only verify that the child
		// deadline is not before the parent (i.e. >=, not strictly >).
		if childDeadline.Before(parentDeadline) {
			t.Errorf("child deadline %v should not be before parent deadline %v", childDeadline, parentDeadline)
		}
	})

	t.Run("parent has no deadline", func(t *testing.T) {
		r := NewRetryLLM(&nonStreamingLLM{}, RetryConfig{})

		child, childCancel := r.perAttemptCtx(context.Background())
		defer childCancel()

		// 没有配置 Timeout 且 parent 无 deadline，应返回原 ctx
		if _, ok := child.Deadline(); ok {
			t.Error("child should not have a deadline when parent has none and Timeout is 0")
		}
	})

	t.Run("config Timeout takes priority", func(t *testing.T) {
		r := NewRetryLLM(&nonStreamingLLM{}, RetryConfig{
			Timeout: 2 * time.Second,
		})

		// parent 有 10 秒 deadline，但 config.Timeout = 2s 应优先
		parent, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		child, childCancel := r.perAttemptCtx(parent)
		defer childCancel()

		childDeadline, ok := child.Deadline()
		if !ok {
			t.Fatal("child should have a deadline")
		}
		remaining := time.Until(childDeadline)
		if remaining > 3*time.Second || remaining < time.Second {
			t.Errorf("child deadline should be ~2s, got %v", remaining)
		}
	})

	t.Run("parent already canceled", func(t *testing.T) {
		r := NewRetryLLM(&nonStreamingLLM{}, RetryConfig{})
		parent, cancel := context.WithCancel(context.Background())
		cancel()

		child, childCancel := r.perAttemptCtx(parent)
		defer childCancel()

		// 父 ctx 已取消，应返回父 ctx 本身
		if child != parent {
			t.Error("should return parent context when already canceled")
		}
	})
}

// ---------------------------------------------------------------------------
// GenerateStreamAndCollect 测试（全周期 stream 重试）
// ---------------------------------------------------------------------------

// failMidStreamLLM 的 GenerateStream 总是成功（返回 channel），
// 但 channel 可以发出 EventError 来模拟 mid-stream 失败。
type failMidStreamLLM struct {
	// failStreamCount: 前 N 次 stream attempt 发出 EventError
	failStreamCount int
	streamAttempts  atomic.Int32
	// failMsg 用于 EventError 的错误消息
	failMsg string
}

func newFailMidStreamLLM(failStreamCount int, failMsg string) *failMidStreamLLM {
	return &failMidStreamLLM{
		failStreamCount: failStreamCount,
		failMsg:         failMsg,
	}
}

func (m *failMidStreamLLM) Generate(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (*LLMResponse, error) {
	return &LLMResponse{Content: "ok", FinishReason: FinishReasonStop, Usage: TokenUsage{PromptTokens: 10, CompletionTokens: 5}}, nil
}

func (m *failMidStreamLLM) ListModels() []string {
	return []string{"fail-mid-stream"}
}

func (m *failMidStreamLLM) GenerateStream(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (<-chan StreamEvent, error) {
	n := int(m.streamAttempts.Add(1))
	ch := make(chan StreamEvent, 2)
	if n <= m.failStreamCount {
		ch <- StreamEvent{Type: EventError, Error: m.failMsg}
		close(ch)
	} else {
		ch <- StreamEvent{Type: EventContent, Content: "stream-ok"}
		ch <- StreamEvent{Type: EventDone, FinishReason: FinishReasonStop}
		close(ch)
	}
	return ch, nil
}

func TestRetryLLM_GenerateStreamAndCollect_SuccessFirstTry(t *testing.T) {
	inner := newFailMidStreamLLM(0, "")
	r := NewRetryLLM(inner, DefaultRetryConfig())

	resp, err := r.GenerateStreamAndCollect(context.Background(), "test", nil, nil, "", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "stream-ok" {
		t.Errorf("content = %q, want %q", resp.Content, "stream-ok")
	}
	if inner.streamAttempts.Load() != 1 {
		t.Errorf("streamAttempts = %d, want 1", inner.streamAttempts.Load())
	}
}

func TestRetryLLM_GenerateStreamAndCollect_StreamErrorRetryThenSuccess(t *testing.T) {
	// 第 1 次 stream 发出 503 错误，第 2 次成功
	inner := newFailMidStreamLLM(1, "status code: 503 Service Unavailable")
	cfg := RetryConfig{Attempts: 3, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	resp, err := r.GenerateStreamAndCollect(context.Background(), "test", nil, nil, "", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "stream-ok" {
		t.Errorf("content = %q, want %q", resp.Content, "stream-ok")
	}
	if inner.streamAttempts.Load() != 2 {
		t.Errorf("streamAttempts = %d, want 2", inner.streamAttempts.Load())
	}
}

func TestRetryLLM_GenerateStreamAndCollect_StreamTruncationRetry(t *testing.T) {
	// Stream truncation (no finish_reason) should be retryable.
	// First attempt truncates, second succeeds.
	inner := newFailMidStreamLLM(1, "stream ended without finish_reason (possible truncation)")
	cfg := RetryConfig{Attempts: 3, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	resp, err := r.GenerateStreamAndCollect(context.Background(), "test", nil, nil, "", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "stream-ok" {
		t.Errorf("content = %q, want %q", resp.Content, "stream-ok")
	}
	if inner.streamAttempts.Load() != 2 {
		t.Errorf("streamAttempts = %d, want 2 (truncation should retry)", inner.streamAttempts.Load())
	}
}

func TestRetryLLM_GenerateStreamAndCollect_UnexpectedEOFRetry(t *testing.T) {
	// unexpected EOF (proxy closed connection mid-stream) should be retryable.
	inner := newFailMidStreamLLM(1, "unexpected EOF")
	cfg := RetryConfig{Attempts: 3, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	resp, err := r.GenerateStreamAndCollect(context.Background(), "test", nil, nil, "", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "stream-ok" {
		t.Errorf("content = %q, want %q", resp.Content, "stream-ok")
	}
	if inner.streamAttempts.Load() != 2 {
		t.Errorf("streamAttempts = %d, want 2 (unexpected EOF should retry)", inner.streamAttempts.Load())
	}
}

func TestRetryLLM_GenerateStreamAndCollect_Exhausted(t *testing.T) {
	inner := newFailMidStreamLLM(100, "status code: 500 Internal Server Error")
	cfg := RetryConfig{Attempts: 3, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	_, err := r.GenerateStreamAndCollect(context.Background(), "test", nil, nil, "", nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// 应该尝试 3 次后耗尽
	if inner.streamAttempts.Load() != 3 {
		t.Errorf("streamAttempts = %d, want 3", inner.streamAttempts.Load())
	}
}

func TestRetryLLM_GenerateStreamAndCollect_NonRetryableStreamError(t *testing.T) {
	// 确定性失败才不重试：输入超长（由上层压缩后重试）。
	// 注意 401 之类的 4xx 现在也会重试，不能再当反例。
	inner := newFailMidStreamLLM(100, "maximum context length exceeded")
	cfg := RetryConfig{Attempts: 3, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	_, err := r.GenerateStreamAndCollect(context.Background(), "test", nil, nil, "", nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// 确定性失败，只应该调用 1 次
	if inner.streamAttempts.Load() != 1 {
		t.Errorf("streamAttempts = %d, want 1 (deterministic failure must not retry)", inner.streamAttempts.Load())
	}
}

func TestRetryLLM_GenerateStreamAndCollect_ContextCanceled(t *testing.T) {
	inner := newFailMidStreamLLM(100, "")
	cfg := RetryConfig{Attempts: 5, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	_, err := r.GenerateStreamAndCollect(ctx, "test", nil, nil, "", nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// context.Canceled 不应重试
	if inner.streamAttempts.Load() > 1 {
		t.Errorf("streamAttempts = %d, want <= 1 (no retry on cancel)", inner.streamAttempts.Load())
	}
}

func TestRetryLLM_GenerateStreamAndCollect_NotifiesOnRetry(t *testing.T) {
	inner := newFailMidStreamLLM(2, "status code: 502 Bad Gateway")
	cfg := RetryConfig{Attempts: 3, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	var notifications []struct {
		attempt, max uint
		err          error
	}
	ctx := WithRetryNotify(context.Background(), func(attempt, max uint, err error) {
		notifications = append(notifications, struct {
			attempt, max uint
			err          error
		}{attempt, max, err})
	})

	resp, err := r.GenerateStreamAndCollect(ctx, "test", nil, nil, "", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "stream-ok" {
		t.Errorf("content = %q, want %q", resp.Content, "stream-ok")
	}

	// 应该收到 2 次通知（前 2 次 stream 失败后各一次）
	if len(notifications) != 2 {
		t.Fatalf("notifications count = %d, want 2", len(notifications))
	}
	if notifications[0].attempt != 1 || notifications[0].max != 3 {
		t.Errorf("notification[0]: attempt=%d, max=%d, want 1, 3", notifications[0].attempt, notifications[0].max)
	}
	if notifications[1].attempt != 2 || notifications[1].max != 3 {
		t.Errorf("notification[1]: attempt=%d, max=%d, want 2, 3", notifications[1].attempt, notifications[1].max)
	}
}

func TestRetryLLM_GenerateStreamAndCollect_WithCallbacks(t *testing.T) {
	inner := newFailMidStreamLLM(1, "status code: 503")
	cfg := RetryConfig{Attempts: 3, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	var contentCalls []string
	var reasoningCalls []string

	resp, err := r.GenerateStreamAndCollect(context.Background(), "test", nil, nil, "",
		func(s string) { contentCalls = append(contentCalls, s) },
		func(s string) { reasoningCalls = append(reasoningCalls, s) },
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "stream-ok" {
		t.Errorf("content = %q, want %q", resp.Content, "stream-ok")
	}
	// 回调应该被调用了（至少最后成功那次）
	if len(contentCalls) == 0 {
		t.Error("expected at least one content callback call")
	}
}

func TestRetryLLM_GenerateStreamAndCollect_NetworkErrorRetry(t *testing.T) {
	// 前 1 次 stream 发出网络错误，第 2 次成功
	inner := newFailMidStreamLLM(1, "connection reset by peer")
	cfg := RetryConfig{Attempts: 3, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	resp, err := r.GenerateStreamAndCollect(context.Background(), "test", nil, nil, "", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "stream-ok" {
		t.Errorf("content = %q, want %q", resp.Content, "stream-ok")
	}
	if inner.streamAttempts.Load() != 2 {
		t.Errorf("streamAttempts = %d, want 2", inner.streamAttempts.Load())
	}
}

// slowSetupLLM simulates a provider whose stream connection takes time to
// establish (DNS+TLS+request+response headers) BEFORE the first chunk becomes
// available — and buffers the first chunk into the channel before the caller
// starts collecting (mirrors RetryLLM.GenerateStreamAndCollect's structure:
// GenerateStream blocks, CollectStreamWithCallback starts afterwards).
type slowSetupLLM struct {
	setupDelay time.Duration
}

func (m *slowSetupLLM) Generate(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (*LLMResponse, error) {
	return &LLMResponse{Content: "ok", FinishReason: FinishReasonStop, Usage: TokenUsage{PromptTokens: 10, CompletionTokens: 5}}, nil
}

func (m *slowSetupLLM) ListModels() []string {
	return []string{"slow-setup"}
}

func (m *slowSetupLLM) GenerateStream(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (<-chan StreamEvent, error) {
	// Simulate the connection-setup window (request sent → SSE established).
	// During this window the request is in flight — real TTFT must include it.
	time.Sleep(m.setupDelay)
	// First chunk is already buffered when the caller starts collecting:
	// CollectStreamWithCallback's requestStart must still predate the setup
	// delay for TTFT to be real.
	ch := make(chan StreamEvent, 2)
	ch <- StreamEvent{Type: EventContent, Content: "ok"}
	ch <- StreamEvent{Type: EventDone, FinishReason: FinishReasonStop}
	close(ch)
	return ch, nil
}

// TestRetryLLM_GenerateStreamAndCollect_TTFTIncludesRequestSetup reproduces
// the TTFT bug: CollectStreamWithCallback measured requestStart from the
// moment collection started (AFTER GenerateStream returned — i.e. after the
// connection was established and the first chunk possibly already buffered),
// silently dropping the entire request-setup window from TTFT. The agent's
// live TTFT (baseline = iteration start) and the committed/DB TTFT then
// disagreed — the live value dropped sharply the moment the LLM call
// returned ("tool 生成完毕后 ttft 变成很小的数值").
func TestRetryLLM_GenerateStreamAndCollect_TTFTIncludesRequestSetup(t *testing.T) {
	inner := &slowSetupLLM{setupDelay: 120 * time.Millisecond}
	r := NewRetryLLM(inner, DefaultRetryConfig())

	resp, err := r.GenerateStreamAndCollect(context.Background(), "test", nil, nil, "", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StreamStats == nil {
		t.Fatalf("StreamStats is nil")
	}
	// TTFT must cover the request-setup window (>= setupDelay), not just the
	// local collection latency. Pre-fix: requestStart = Collect start → TTFT
	// ≈ 0-5ms because the first chunk was already buffered in the channel.
	if resp.StreamStats.TTFTMs < 110 {
		t.Errorf("TTFTMs = %d, want >= 110 (request-setup window must be included in TTFT)", resp.StreamStats.TTFTMs)
	}
}
