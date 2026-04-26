package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	retry "github.com/avast/retry-go/v5"
	log "xbot/logger"
)

// RetryNotifyFunc is the retry notification callback.
// attempt: current retry count (1-based), maxAttempts: max attempts, err: the error that triggered the retry.
type RetryNotifyFunc func(attempt, maxAttempts uint, err error)

type retryNotifyKey struct{}

// WithRetryNotify injects a retry notification callback into the context.
// RetryLLM calls this callback on each retry; callers can use it to push progress to users.
func WithRetryNotify(ctx context.Context, fn RetryNotifyFunc) context.Context {
	return context.WithValue(ctx, retryNotifyKey{}, fn)
}

// getRetryNotify retrieves the notification callback from context (may be nil).
func getRetryNotify(ctx context.Context) RetryNotifyFunc {
	fn, _ := ctx.Value(retryNotifyKey{}).(RetryNotifyFunc)
	return fn
}

// RetryConfig holds retry configuration
type RetryConfig struct {
	Attempts      uint          // Max attempts (including first), default 5
	Delay         time.Duration // Initial delay, default 1s
	MaxDelay      time.Duration // Max delay, default 30s
	MaxConcurrent int           // Max concurrency (0 = unlimited)
	Timeout       time.Duration // Per-attempt LLM call timeout (0 = no timeout)
}

// DefaultRetryConfig returns the default retry configuration
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		Attempts: 5,
		Delay:    1 * time.Second,
		MaxDelay: 30 * time.Second,
		Timeout:  120 * time.Second, // Per-attempt LLM timeout, ensuring each retry has an independent window
	}
}

// RetryLLM is a decorator that adds retry capability to any LLM implementation
type RetryLLM struct {
	inner  LLM
	config RetryConfig
	sem    chan struct{} // Concurrency semaphore, nil means unlimited
}

// NewRetryLLM creates a retry wrapper; inner may optionally implement StreamingLLM
func NewRetryLLM(inner LLM, cfg RetryConfig) *RetryLLM {
	r := &RetryLLM{inner: inner, config: cfg}
	if cfg.MaxConcurrent > 0 {
		r.sem = make(chan struct{}, cfg.MaxConcurrent)
	}
	return r
}

// acquire acquires the concurrency semaphore and returns a release function.
// If sem is nil (unlimited concurrency), returns a no-op function.
// Note: the returned release function only releases if the semaphore was successfully acquired;
// if ctx is already cancelled, returns a no-op to avoid deadlock.
func (r *RetryLLM) acquire(ctx context.Context) func() {
	if r.sem == nil {
		return func() {}
	}
	select {
	case r.sem <- struct{}{}:
		return func() { <-r.sem }
	case <-ctx.Done():
		return func() {} // ctx cancelled, return no-op to avoid deadlock
	}
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
	// Input-too-long indicator keywords (precise enough, no 400 prefix needed)
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
	// Return true when precise indicator keywords are found
	return false
}

// isRetryableError determines if an error is retryable.
// Retryable: 429, 5xx, network errors, context timeout
// Not retryable: context cancellation (user /cancel), other 4xx
//
// Note: since retryOptions no longer passes retry.Context(ctx), timeout retries now work correctly.
// Each retry creates a fresh timeout context via perAttemptCtx.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	// context.Canceled: user-initiated cancel (/cancel etc.), do not retry
	if errors.Is(err, context.Canceled) {
		return false
	}
	// context.DeadlineExceeded: timeout is a transient error, allow retry
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := err.Error()
	// Network-level errors are retryable
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	// OpenAI SDK error format: `POST "URL": NNN StatusText ...`
	for _, code := range []string{"429", "500", "502", "503", "504"} {
		if strings.Contains(msg, ": "+code+" ") { // OpenAI
			return true
		}
	}
	// B-05 fix: Anthropic SDK error format: `anthropic API error: status=NNN, body=...`
	// The existing OpenAI format matcher can't match this, so handle separately
	if strings.Contains(msg, "anthropic API error: status=") {
		if idx := strings.Index(msg, "status="); idx != -1 {
			codeStr := msg[idx+7:]
			// Find end of status value (comma, space, or end of string)
			for i, c := range codeStr {
				if c == ',' || c == ' ' || c == ')' {
					codeStr = codeStr[:i]
					break
				}
			}
			// 429 and 5xx are retryable
			if codeStr == "429" || strings.HasPrefix(codeStr, "5") {
				return true
			}
		}
	}
	return false
}

// isRateLimitError checks if the error is a 429 Rate Limit error
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

// retryOptions builds common retry options
func (r *RetryLLM) retryOptions(ctx context.Context, label string) []retry.Option {
	return []retry.Option{
		retry.Attempts(r.config.Attempts),
		retry.Delay(r.config.Delay),
		retry.MaxDelay(r.config.MaxDelay),
		retry.DelayType(retry.CombineDelay(retry.BackOffDelay, retry.RandomDelay)),
		// Don't pass retry.Context(ctx) — a cancelled ctx after timeout would cause the retry framework to skip retries
		// context.Canceled is handled by isRetryableError (returns false → no retry)
		retry.RetryIf(isRetryableError),
		retry.OnRetry(func(n uint, err error) {
			log.Ctx(ctx).WithFields(log.Fields{
				"attempt": n + 1,
				"max":     r.config.Attempts,
				"error":   err.Error(),
			}).Warn("[LLM] " + label)

			// Notify caller (e.g. agent runLoop) to push progress to user
			if notify := getRetryNotify(ctx); notify != nil {
				notify(n+1, r.config.Attempts, err)
			}

			// Extra exponential backoff for 429: avoid repeatedly triggering rate limits
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

// perAttemptCtx creates a fresh timeout context for each retry attempt.
// If the caller's ctx carries a deadline (e.g. engine.go's context.WithTimeout),
// extract the timeout duration and create a new ctx instead of reusing the same deadline.
// This gives each retry a full timeout window.
// The parent ctx's cancellation signal is still propagated.
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
	// Don't start if parent ctx is already cancelled
	select {
	case <-parent.Done():
		return parent, func() {}
	default:
	}
	// Create fresh timeout context (don't inherit parent ctx's deadline)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	// Propagate parent ctx's cancellation signal (but not its deadline)
	go func() {
		select {
		case <-parent.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// Generate produces an LLM response，失败时按配置重试
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

// ListModels returns the available model list（直接转发，不重试）
func (r *RetryLLM) ListModels() []string {
	return r.inner.ListModels()
}

// GenerateStream retries only when acquiring the channel; no retry once streaming starts.
// Note: perAttemptCtx is not used because GenerateStream is asynchronous (starts goroutine then returns immediately);
// perAttemptCtx's defer cancel() would cancel the context prematurely while the goroutine is still running,
// causing processStream to detect context canceled and send EventError.
// Stream timeout/cancellation is managed by the caller (generateResponse → CollectStream) via ctx.
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
