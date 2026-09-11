package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	retry "github.com/avast/retry-go/v5"
	log "xbot/logger"
)

// RetryNotifyFunc 重试通知回调。
// attempt: 当前重试次数（从 1 开始），maxAttempts: 最大尝试次数，err: 触发重试的错误。
type RetryNotifyFunc func(attempt, maxAttempts uint, err error)

type retryNotifyKey struct{}

// WithRetryNotify 将重试通知回调注入 context。
// RetryLLM 在每次重试时会调用该回调，调用方可借此向用户推送进度。
func WithRetryNotify(ctx context.Context, fn RetryNotifyFunc) context.Context {
	return context.WithValue(ctx, retryNotifyKey{}, fn)
}

// getRetryNotify 从 context 获取通知回调（可能为 nil）。
func getRetryNotify(ctx context.Context) RetryNotifyFunc {
	fn, _ := ctx.Value(retryNotifyKey{}).(RetryNotifyFunc)
	return fn
}

// RetryConfig 重试配置
type RetryConfig struct {
	Attempts      uint          // 最大尝试次数（含首次），默认 5
	Delay         time.Duration // 初始延迟，默认 1s
	MaxDelay      time.Duration // 最大延迟，默认 30s
	MaxConcurrent int           // 最大并发数（0 表示不限制）
	Timeout       time.Duration // 单次 LLM 调用超时（0 = 不设超时）
}

// DefaultRetryConfig 返回默认重试配置
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		Attempts: 5,
		Delay:    1 * time.Second,
		MaxDelay: 30 * time.Second,
		Timeout:  120 * time.Second, // 单次 LLM 调用超时，确保每次重试有独立窗口
	}
}

// RetryLLM 为任意 LLM 实现提供重试能力的装饰器
type RetryLLM struct {
	inner  LLM
	config RetryConfig
	sem    chan struct{} // 并发信号量，nil 表示不限制
}

// NewRetryLLM 创建重试包装器；inner 可选实现 StreamingLLM
func NewRetryLLM(inner LLM, cfg RetryConfig) *RetryLLM {
	r := &RetryLLM{inner: inner, config: cfg}
	if cfg.MaxConcurrent > 0 {
		r.sem = make(chan struct{}, cfg.MaxConcurrent)
	}
	return r
}

// acquire 获取并发信号量，返回释放函数。
// 如果 sem 为 nil（不限制并发），返回空操作函数。
// 注意：返回的 release 函数仅在成功获取信号量后才执行释放，
// 如果 ctx 已取消，返回空操作函数避免死锁。
func (r *RetryLLM) acquire(ctx context.Context) func() {
	if r.sem == nil {
		return func() {}
	}
	select {
	case r.sem <- struct{}{}:
		return func() { <-r.sem }
	case <-ctx.Done():
		return func() {} // ctx 已取消，返回空操作避免死锁
	}
}

// LoadModelsFromAPI delegates to the inner client if it implements ModelLoader.
// RetryLLM itself does not retry this call — model list fetching has its own
// timeout/error handling in the refresh pipeline.
func (r *RetryLLM) LoadModelsFromAPI(ctx context.Context) error {
	if loader, ok := r.inner.(ModelLoader); ok {
		return loader.LoadModelsFromAPI(ctx)
	}
	return fmt.Errorf("inner LLM does not implement ModelLoader")
}

// IsInputTooLongError detects 400-class errors caused by the input exceeding the
// model's context window. Different providers return this in different formats:
//   - Dashscope: "Range of input length should be [1, 202752]"
//   - OpenAI:    "maximum context length" / "max_tokens"
//   - Anthropic: "prompt is too long"
func IsInputTooLongError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// Input-too-long 指示关键词（足够精确，无需 400 前置）
	indicators := []string{
		"range of input length",
		"maximum context length",
		"exceeds the maximum number of tokens",
		"context_length_exceeded",
		"prompt is too long",
		"input too long",
		"token limit",
		"reduce the length",
		"too many tokens",
		"request too large",
	}
	for _, ind := range indicators {
		if strings.Contains(msg, ind) {
			return true
		}
	}
	// 有精确指示关键词时返回 true
	return false
}

// IsRetryableError 判断错误是否可重试。
//
// 策略（2026-09-11 用户要求「所有失败都要触发指数退避」）：**默认重试**。
// 之前是白名单——只有 429/5xx/网络错误被识别，其它任何失败都直接冒泡、不重试。
// 白名单的先天缺陷：无法穷举 provider/网关/SDK 的错误形态（自定义错误串、
// 代理改写过的响应、新的状态码描述……），漏一个就退化成「一次失败即失败」。
//
// 只有两类例外：
//  1. context.Canceled —— 用户主动取消（/cancel），这不是失败；重试等于对抗
//     用户意图。
//  2. 输入超长（IsInputTooLongError）—— 确定性失败：同样的 payload 重发必然
//     同样失败。调用方（agent.handleInputTooLong）有专门的「压缩上下文 + 重试」
//     恢复路径；在这里空转 Attempts 次只会推迟真正的修复并多烧几次调用。
//
// 其余一切（含 4xx、未知错误、纯字符串错误）都重试：由 retryOptions 的
// 指数退避 + 抖动处理瞬时故障，最终仍失败时错误照常返回。
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}
	// 用户主动取消：不是失败，不重试。
	if errors.Is(err, context.Canceled) {
		return false
	}
	// 确定性失败：重试无意义，且有专门的恢复路径。
	if IsInputTooLongError(err) {
		return false
	}
	return true
}

// isRateLimitError 判断错误是否为 429 Rate Limit 错误
func isRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// OpenAI SDK: `POST "URL": 429 Too Many Requests`
	if strings.Contains(msg, ": 429 ") {
		return true
	}
	// Anthropic SDK: `anthropic API error: status=429, body=...`
	if strings.Contains(msg, "status=429") {
		return true
	}
	return false
}

// retryOptions 构建通用重试选项
func (r *RetryLLM) retryOptions(ctx context.Context, label string) []retry.Option {
	return []retry.Option{
		retry.Attempts(r.config.Attempts),
		retry.Delay(r.config.Delay),
		retry.MaxDelay(r.config.MaxDelay),
		retry.DelayType(retry.CombineDelay(retry.BackOffDelay, retry.RandomDelay)),
		// 不传 retry.Context(ctx) —— 超时后 ctx 已取消会导致 retry 框架跳过重试
		// context.Canceled 由 IsRetryableError 处理（返回 false → 不重试）
		retry.RetryIf(IsRetryableError),
		retry.OnRetry(func(n uint, err error) {
			log.Ctx(ctx).WithFields(log.Fields{
				"attempt": n + 1,
				"max":     r.config.Attempts,
				"error":   err.Error(),
			}).Warn("[LLM] " + label)

			// 通知调用方（如 agent runLoop）以便向用户推送进度
			if notify := getRetryNotify(ctx); notify != nil {
				notify(n+1, r.config.Attempts, err)
			}

			// 429 额外指数退避：避免短时间内重复触发速率限制
			if isRateLimitError(err) {
				extraDelay := time.Duration(2<<min(n, 4)) * time.Second // 2s, 4s, 8s, 16s, 32s
				log.Ctx(ctx).WithField("delay", extraDelay).Warn("[LLM] Rate limited, backing off")
				select {
				case <-time.After(extraDelay):
				case <-ctx.Done():
				}
			}
		}),
	}
}

// perAttemptCtx 为每次重试创建全新的超时上下文。
// 如果调用方 ctx 携带 deadline（如 engine.go 的 context.WithTimeout），
// 提取超时 duration 创建新 ctx，而非复用同一个 deadline。
// 这样每次重试都有完整的超时窗口。
// 父 ctx 的取消信号仍然会被传播。
func (r *RetryLLM) perAttemptCtx(parent context.Context) (context.Context, context.CancelFunc) {
	timeout := r.config.Timeout
	if timeout <= 0 {
		if deadline, ok := parent.Deadline(); ok {
			timeout = time.Until(deadline)
		}
	}
	if timeout <= 0 {
		return parent, func() {}
	}
	// 父 ctx 已取消则不启动
	select {
	case <-parent.Done():
		return parent, func() {}
	default:
	}
	// 创建全新超时上下文（不继承父 ctx 的 deadline）
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	// 传播父 ctx 的取消信号（但不是 deadline）
	go func() {
		select {
		case <-parent.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// Generate 生成 LLM 响应，失败时按配置重试
func (r *RetryLLM) Generate(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (*LLMResponse, error) {
	release := r.acquire(ctx)
	defer release()
	return retry.NewWithData[*LLMResponse](
		r.retryOptions(ctx, "Retrying request")...,
	).Do(func() (*LLMResponse, error) {
		attemptCtx, cancel := r.perAttemptCtx(ctx)
		defer cancel()
		return r.inner.Generate(attemptCtx, model, messages, tools, thinkingMode)
	})
}

// ListModels 获取可用模型列表（直接转发，不重试）
func (r *RetryLLM) ListModels() []string {
	return r.inner.ListModels()
}

// GenerateStream 仅在获取 channel 时重试，流开始后不重试。
// 注意：不使用 perAttemptCtx，因为 GenerateStream 是异步的（启动 goroutine 后立即返回），
// perAttemptCtx 的 defer cancel() 会在 goroutine 仍在运行时过早取消上下文，
// 导致 processStream 检测到 context canceled 并发送 EventError。
// 流的超时/取消由调用方（generateResponse → CollectStream）通过 ctx 管理。
//
// Deprecated: 新代码应使用 GenerateStreamAndCollect，它重试整个 stream 周期
// （连接+收集），而非仅重试连接建立。
func (r *RetryLLM) GenerateStream(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (<-chan StreamEvent, error) {
	release := r.acquire(ctx)
	defer release()
	streaming, ok := r.inner.(StreamingLLM)
	if !ok {
		return nil, fmt.Errorf("underlying LLM does not support streaming")
	}
	return retry.NewWithData[<-chan StreamEvent](
		r.retryOptions(ctx, "Retrying stream connection")...,
	).Do(func() (<-chan StreamEvent, error) {
		return streaming.GenerateStream(ctx, model, messages, tools, thinkingMode)
	})
}

// GenerateStreamAndCollect 重试整个 stream 周期：连接建立 + 事件收集。
// 与 GenerateStream 仅重试 SSE 连接建立不同，此方法同时重试 stream 中途错误
// （断连、服务端 mid-stream 报错）。每次重试通过 perAttemptCtx 获得全新的超时窗口。
//
// streamContentFn / streamReasoningFn / streamToolCallFn 在每个 attempt 中都会被调用。
// 重试过程中中间 attempt 的内容可能短暂出现在 UI 中，但最终只有成功 attempt
// 的完整响应会被返回。
func (r *RetryLLM) GenerateStreamAndCollect(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string, streamContentFn func(string), streamReasoningFn func(string), streamToolCallFn func([]ToolCallDelta), streamUsageFn func(*TokenUsage)) (*LLMResponse, error) {
	release := r.acquire(ctx)
	defer release()

	streaming, ok := r.inner.(StreamingLLM)
	if !ok {
		return nil, fmt.Errorf("underlying LLM does not support streaming")
	}

	return retry.NewWithData[*LLMResponse](
		r.retryOptions(ctx, "Retrying stream request")...,
	).Do(func() (*LLMResponse, error) {
		// Streaming does NOT use perAttemptCtx. A per-attempt deadline would
		// bind to the underlying HTTP connection via ctx, causing active streams
		// to be killed mid-generation when total elapsed time exceeds the deadline
		// (e.g., 120s for a long DeepSeek reasoning response).
		//
		// Instead, we pass the parent ctx directly to GenerateStream. The stream
		// collection phase uses CollectStreamWithCallback's built-in idle timeout
		// (120s without any chunk), which correctly handles:
		//   - Connection hangs (no first chunk within 120s)
		//   - Mid-stream hangs (no chunk for 120s after the last one)
		//   - Active streams of any duration (timer resets on every chunk)
		//
		// Connection establishment (DNS + TCP + TLS) is bounded by the HTTP
		// client's own transport timeout, not by context deadline.

		type genResult struct {
			ch  <-chan StreamEvent
			err error
		}
		// Request-send timestamp — recorded BEFORE initiating the stream
		// request. TTFT must be measured from here: GenerateStream blocks until
		// the SSE connection is established (and the first chunks may already
		// be buffered in eventCh when collection starts), so collecting from
		// "now" at CollectStreamWithCallback time would silently drop the
		// entire request-setup window from TTFT (the "ttft suddenly tiny after
		// tool generation finished" bug: live frames reported the full window,
		// the committed/DB value reported ~0-5ms local latency).
		t0 := time.Now()
		resultCh := make(chan genResult, 1)
		go func() {
			ch, err2 := streaming.GenerateStream(ctx, model, messages, tools, thinkingMode)
			resultCh <- genResult{ch, err2}
		}()

		var eventCh <-chan StreamEvent
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case r := <-resultCh:
			if r.err != nil {
				return nil, r.err
			}
			eventCh = r.ch
		}

		if streamContentFn != nil || streamReasoningFn != nil || streamToolCallFn != nil || streamUsageFn != nil {
			return CollectStreamWithCallbackFrom(ctx, eventCh, t0, streamContentFn, streamReasoningFn, streamToolCallFn, streamUsageFn)
		}
		return CollectStreamWithCallbackFrom(ctx, eventCh, t0, nil, nil, nil, nil)
	})
}
