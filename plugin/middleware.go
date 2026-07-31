package plugin

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// Plugin Middleware Chain — intercepts tool execution calls
//
// Middleware follows the classic Gin/Chi nested-closure pattern.
// Each middleware receives (ctx, toolName, input, next) and must call next()
// to continue the chain. Not calling next() short-circuits execution.
//
// Execution order for middlewares [A, B, C]:
//
//	A.before → B.before → C.before → final handler → C.after → B.after → A.after
// ---------------------------------------------------------------------------

// PluginMiddleware intercepts tool execution calls.
// Call next() to continue the chain, or return a ToolResult to short-circuit.
type PluginMiddleware func(ctx context.Context, toolName string, input string, next PluginMiddlewareNext) (*ToolResult, error)

// PluginMiddlewareNext calls the next middleware (or the final handler) in the chain.
type PluginMiddlewareNext func(ctx context.Context, toolName string, input string) (*ToolResult, error)

// MiddlewareChain executes an ordered chain of plugin middleware.
// The chain is built once during wiring and is read-only at execution time,
// so no locking is required.
type MiddlewareChain struct {
	middlewares []PluginMiddleware
}

// NewMiddlewareChain creates a MiddlewareChain with the given middlewares.
func NewMiddlewareChain(middlewares ...PluginMiddleware) *MiddlewareChain {
	mws := make([]PluginMiddleware, 0, len(middlewares))
	mws = append(mws, middlewares...)
	return &MiddlewareChain{middlewares: mws}
}

// Execute runs the middleware chain and calls the final handler.
//
// Middlewares are executed in registration order (first registered = outermost).
// The final PluginMiddlewareNext is called after all middlewares have run.
// If the chain is empty, final is called directly.
func (mc *MiddlewareChain) Execute(ctx context.Context, toolName, input string, final PluginMiddlewareNext) (*ToolResult, error) {
	if mc == nil || len(mc.middlewares) == 0 {
		return final(ctx, toolName, input)
	}

	// Build the chain from the inside out: last middleware wraps final,
	// second-to-last wraps that, and so on.
	next := final
	for i := len(mc.middlewares) - 1; i >= 0; i-- {
		mw := mc.middlewares[i]
		prev := next
		next = func(ctx context.Context, toolName string, input string) (*ToolResult, error) {
			return mw(ctx, toolName, input, prev)
		}
	}
	return next(ctx, toolName, input)
}

// Use appends a middleware to the chain.
// NOT concurrent-safe — must only be called during chain construction (WirePluginTools),
// never during active tool execution.
func (mc *MiddlewareChain) Use(middleware PluginMiddleware) {
	if middleware == nil {
		return
	}
	mc.middlewares = append(mc.middlewares, middleware)
}

// Len returns the number of middlewares in the chain.
func (mc *MiddlewareChain) Len() int {
	if mc == nil {
		return 0
	}
	return len(mc.middlewares)
}

// ---------------------------------------------------------------------------
// Built-in Middleware
// ---------------------------------------------------------------------------

// LoggingMiddleware logs tool call details before and after execution.
// It is a pure observer — it does not modify the result or error.
func LoggingMiddleware(logger Logger) PluginMiddleware {
	return func(ctx context.Context, toolName string, input string, next PluginMiddlewareNext) (*ToolResult, error) {
		start := time.Now()
		logger.Info("tool call started",
			Field{Key: "tool", Value: toolName},
			Field{Key: "input_len", Value: len(input)},
		)

		result, err := next(ctx, toolName, input)

		elapsed := time.Since(start)
		if err != nil {
			logger.Error("tool call failed",
				Field{Key: "tool", Value: toolName},
				Field{Key: "error", Value: err.Error()},
				Field{Key: "duration", Value: elapsed.String()},
			)
		} else if result != nil && result.IsError {
			logger.Warn("tool call returned error result",
				Field{Key: "tool", Value: toolName},
				Field{Key: "duration", Value: elapsed.String()},
			)
		} else {
			logger.Info("tool call completed",
				Field{Key: "tool", Value: toolName},
				Field{Key: "duration", Value: elapsed.String()},
			)
		}
		return result, err
	}
}

// RecoveryMiddleware recovers from panics inside tool execution and converts
// them to error ToolResults. It uses named return values so the deferred
// recover can properly set the return values.
func RecoveryMiddleware(logger Logger) PluginMiddleware {
	return func(ctx context.Context, toolName string, input string, next PluginMiddlewareNext) (result *ToolResult, err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("tool panic recovered",
					Field{Key: "tool", Value: toolName},
					Field{Key: "panic", Value: fmt.Sprintf("%v", r)},
					Field{Key: "stack", Value: string(debug.Stack())},
				)
				result = NewToolError(fmt.Sprintf("tool %s panicked: %v", toolName, r))
				err = nil
			}
		}()
		return next(ctx, toolName, input)
	}
}

// TimeoutMiddleware enforces a maximum execution duration.
// It derives a child context with the given timeout and passes it to next().
// If the timeout is exceeded, an error ToolResult is returned.
func TimeoutMiddleware(timeout time.Duration) PluginMiddleware {
	if timeout <= 0 {
		// No-op for non-positive timeout
		return func(ctx context.Context, toolName string, input string, next PluginMiddlewareNext) (*ToolResult, error) {
			return next(ctx, toolName, input)
		}
	}
	return func(ctx context.Context, toolName string, input string, next PluginMiddlewareNext) (*ToolResult, error) {
		childCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		result, err := next(childCtx, toolName, input)
		if err != nil {
			if ctx.Err() == nil && childCtx.Err() == context.DeadlineExceeded {
				return NewToolError(fmt.Sprintf("tool %s timed out after %s", toolName, timeout)), nil
			}
			return nil, err
		}
		if result == nil && childCtx.Err() == context.DeadlineExceeded {
			return NewToolError(fmt.Sprintf("tool %s timed out after %s", toolName, timeout)), nil
		}
		return result, nil
	}
}

// defaultRetryBackoff is the fixed delay between retry attempts.
const defaultRetryBackoff = 100 * time.Millisecond

// RetryMiddleware retries tool execution on error (Go error only, not ToolResult.IsError).
// It performs up to maxRetries additional attempts with fixed 100ms backoff.
// maxRetries <= 0 means no retries.
func RetryMiddleware(maxRetries int) PluginMiddleware {
	if maxRetries <= 0 {
		return func(ctx context.Context, toolName string, input string, next PluginMiddlewareNext) (*ToolResult, error) {
			return next(ctx, toolName, input)
		}
	}
	return func(ctx context.Context, toolName string, input string, next PluginMiddlewareNext) (*ToolResult, error) {
		var result *ToolResult
		var err error
		for attempt := 0; attempt <= maxRetries; attempt++ {
			result, err = next(ctx, toolName, input)
			if err == nil {
				return result, nil
			}
			// Don't retry if context is cancelled
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// Last attempt — don't sleep
			if attempt < maxRetries {
				time.Sleep(defaultRetryBackoff)
			}
		}
		return result, err
	}
}

// ---------------------------------------------------------------------------
// Tool-level Timeout Decorator
// ---------------------------------------------------------------------------

// timeoutTool wraps a PluginTool with an execution deadline.
// It implements both PluginTool and PluginToolV2 so that V2 callers
// also benefit from the timeout.
type timeoutTool struct {
	inner   PluginTool
	timeout time.Duration
}

// ToolTimeout wraps a PluginTool with a timeout.
// If the tool's Execute (or ExecuteWithContext) does not return within the
// given duration, an error ToolResult is returned instead.
// A non-positive timeout disables the timeout (returns the tool unchanged).
func ToolTimeout(tool PluginTool, timeout time.Duration) PluginTool {
	if timeout <= 0 {
		return tool
	}
	return &timeoutTool{inner: tool, timeout: timeout}
}

// Definition returns the wrapped tool's definition.
func (t *timeoutTool) Definition() ToolDef {
	return t.inner.Definition()
}

// executeFunc is the unified signature for tool execution, used by the
// shared decorator cores to avoid duplicating logic between V1 and V2 paths.
type executeFunc func(ctx context.Context, input string) (*ToolResult, error)

// executeWithTimeout is the shared core for both V1 and V2 timeout paths.
func (t *timeoutTool) executeWithTimeout(fn executeFunc, ctx context.Context, input string) (*ToolResult, error) {
	childCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	type outcome struct {
		result *ToolResult
		err    error
	}
	ch := make(chan outcome, 1)
	go func() {
		r, e := fn(childCtx, input)
		ch <- outcome{result: r, err: e}
	}()

	select {
	case o := <-ch:
		return o.result, o.err
	case <-childCtx.Done():
		name := t.inner.Definition().Name
		return NewToolError(fmt.Sprintf("tool %s timed out after %s", name, t.timeout)), nil
	}
}

// Execute runs the wrapped tool with a timeout-derivative context.
func (t *timeoutTool) Execute(ctx context.Context, input string) (*ToolResult, error) {
	return t.executeWithTimeout(t.inner.Execute, ctx, input)
}

// ExecuteWithContext runs the wrapped tool's V2 method with a timeout.
// If the inner tool does not implement PluginToolV2, it falls back to V1 Execute.
func (t *timeoutTool) ExecuteWithContext(ctx *ToolCallContext, input string) (*ToolResult, error) {
	v2, ok := t.inner.(PluginToolV2)
	if !ok {
		return t.Execute(ctx.Ctx, input)
	}
	fn := func(childCtx context.Context, input string) (*ToolResult, error) {
		childTCC := &ToolCallContext{
			SessionID: ctx.SessionID,
			Channel:   ctx.Channel,
			ChatID:    ctx.ChatID,
			UserID:    ctx.UserID,
			Ctx:       childCtx,
		}
		return v2.ExecuteWithContext(childTCC, input)
	}
	return t.executeWithTimeout(fn, ctx.Ctx, input)
}

// ---------------------------------------------------------------------------
// Tool-level Retry Decorator
// ---------------------------------------------------------------------------

// retryTool wraps a PluginTool with retry logic for transient Go errors.
// It mirrors the timeoutTool pattern and implements both PluginTool and
// PluginToolV2 so that V2 callers also benefit from retries.
type retryTool struct {
	inner      PluginTool
	maxRetries int
	delay      time.Duration
}

// ToolRetry wraps a PluginTool with retry logic.
// On Go error (not ToolResult.IsError), it retries up to maxRetries
// additional attempts with a fixed delay between attempts.
// A non-positive maxRetries disables retries (returns the tool unchanged).
func ToolRetry(tool PluginTool, maxRetries int, delay time.Duration) PluginTool {
	if maxRetries <= 0 {
		return tool
	}
	return &retryTool{inner: tool, maxRetries: maxRetries, delay: delay}
}

// Definition returns the wrapped tool's definition.
func (r *retryTool) Definition() ToolDef {
	return r.inner.Definition()
}

// executeWithRetry is the shared core for both V1 and V2 retry paths.
func (r *retryTool) executeWithRetry(fn executeFunc, ctx context.Context, input string) (*ToolResult, error) {
	var result *ToolResult
	var err error
	for attempt := 0; attempt <= r.maxRetries; attempt++ {
		result, err = fn(ctx, input)
		if err == nil {
			return result, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt < r.maxRetries {
			time.Sleep(r.delay)
		}
	}
	return result, err
}

// Execute runs the wrapped tool, retrying on Go error.
func (r *retryTool) Execute(ctx context.Context, input string) (*ToolResult, error) {
	return r.executeWithRetry(r.inner.Execute, ctx, input)
}

// ExecuteWithContext runs the wrapped tool's V2 method with retry logic.
// If the inner tool does not implement PluginToolV2, it falls back to V1 Execute.
func (r *retryTool) ExecuteWithContext(ctx *ToolCallContext, input string) (*ToolResult, error) {
	v2, ok := r.inner.(PluginToolV2)
	if !ok {
		return r.Execute(ctx.Ctx, input)
	}
	fn := func(_ context.Context, input string) (*ToolResult, error) {
		return v2.ExecuteWithContext(ctx, input)
	}
	return r.executeWithRetry(fn, ctx.Ctx, input)
}

// ---------------------------------------------------------------------------
// Tool-level Cache Decorator
// ---------------------------------------------------------------------------

// cacheEntry holds a cached ToolResult and its expiration time.
type cacheEntry struct {
	result *ToolResult
	expiry time.Time
}

// cacheTool wraps a PluginTool with an in-memory TTL cache.
// It implements both PluginTool and PluginToolV2 so that V2 callers
// also benefit from caching.
type cacheTool struct {
	inner PluginTool
	ttl   time.Duration
	mu    sync.RWMutex
	cache map[string]*cacheEntry
}

// ToolCache wraps a PluginTool with an in-memory TTL cache.
// Successful results (err == nil, result != nil, !result.IsError) are cached
// by input string and served from cache until the TTL expires.
// The cache key is the input string alone; results are shared across sessions
// and users for the same input.
// A non-positive TTL disables caching (returns the tool unchanged).
func ToolCache(tool PluginTool, ttl time.Duration) PluginTool {
	if ttl <= 0 {
		return tool
	}
	return &cacheTool{inner: tool, ttl: ttl, cache: make(map[string]*cacheEntry)}
}

// Definition returns the wrapped tool's definition.
func (c *cacheTool) Definition() ToolDef {
	return c.inner.Definition()
}

// Execute runs the wrapped tool, caching successful results.
func (c *cacheTool) Execute(ctx context.Context, input string) (*ToolResult, error) {
	// Check cache
	c.mu.RLock()
	entry, hit := c.cache[input]
	c.mu.RUnlock()

	if hit && time.Now().Before(entry.expiry) {
		return entry.result, nil
	}

	// Cache miss or expired — call inner
	result, err := c.inner.Execute(ctx, input)

	// Only cache successful results
	if err == nil && result != nil && !result.IsError {
		c.mu.Lock()
		c.cache[input] = &cacheEntry{result: result, expiry: time.Now().Add(c.ttl)}
		c.mu.Unlock()
	}

	return result, err
}

// ExecuteWithContext runs the wrapped tool's V2 method with caching.
// If the inner tool does not implement PluginToolV2, it falls back to V1 Execute.
func (c *cacheTool) ExecuteWithContext(ctx *ToolCallContext, input string) (*ToolResult, error) {
	v2, ok := c.inner.(PluginToolV2)
	if !ok {
		// Not V2 — delegate to V1 path
		return c.Execute(ctx.Ctx, input)
	}

	// Check cache
	c.mu.RLock()
	entry, hit := c.cache[input]
	c.mu.RUnlock()

	if hit && time.Now().Before(entry.expiry) {
		return entry.result, nil
	}

	// Cache miss or expired — call inner V2
	result, err := v2.ExecuteWithContext(ctx, input)

	// Only cache successful results
	if err == nil && result != nil && !result.IsError {
		c.mu.Lock()
		c.cache[input] = &cacheEntry{result: result, expiry: time.Now().Add(c.ttl)}
		c.mu.Unlock()
	}

	return result, err
}

// ---------------------------------------------------------------------------
// Tool-level Logging Decorator
// ---------------------------------------------------------------------------

// loggingTool wraps a PluginTool with structured logging.
// It logs tool execution lifecycle events (start, success, error result, Go error)
// using the same format as LoggingMiddleware, but at the tool-level decorator
// layer. It implements both PluginTool and PluginToolV2.
type loggingTool struct {
	inner  PluginTool
	logger Logger
}

// ToolLogging wraps a PluginTool with structured logging.
// Logs are emitted at Info level for start/success, Warn for error results,
// and Error for Go errors. A nil logger disables logging (returns the tool unchanged).
func ToolLogging(tool PluginTool, logger Logger) PluginTool {
	if logger == nil {
		return tool
	}
	return &loggingTool{inner: tool, logger: logger}
}

// Definition returns the wrapped tool's definition.
func (l *loggingTool) Definition() ToolDef {
	return l.inner.Definition()
}

// Execute runs the wrapped tool, logging lifecycle events.
func (l *loggingTool) Execute(ctx context.Context, input string) (*ToolResult, error) {
	name := l.inner.Definition().Name
	start := time.Now()

	l.logger.Info("tool execution started",
		Field{Key: "tool", Value: name},
		Field{Key: "input_len", Value: len(input)},
	)

	result, err := l.inner.Execute(ctx, input)

	elapsed := time.Since(start)
	if err != nil {
		l.logger.Error("tool execution failed",
			Field{Key: "tool", Value: name},
			Field{Key: "error", Value: err.Error()},
			Field{Key: "duration", Value: elapsed.String()},
		)
	} else if result != nil && result.IsError {
		l.logger.Warn("tool execution returned error result",
			Field{Key: "tool", Value: name},
			Field{Key: "duration", Value: elapsed.String()},
		)
	} else {
		l.logger.Info("tool execution completed",
			Field{Key: "tool", Value: name},
			Field{Key: "duration", Value: elapsed.String()},
		)
	}

	return result, err
}

// ExecuteWithContext runs the wrapped tool's V2 method with logging.
// If the inner tool does not implement PluginToolV2, it falls back to V1 Execute.
func (l *loggingTool) ExecuteWithContext(ctx *ToolCallContext, input string) (*ToolResult, error) {
	v2, ok := l.inner.(PluginToolV2)
	if !ok {
		return l.Execute(ctx.Ctx, input)
	}

	name := l.inner.Definition().Name
	start := time.Now()

	l.logger.Info("tool execution started",
		Field{Key: "tool", Value: name},
		Field{Key: "input_len", Value: len(input)},
	)

	result, err := v2.ExecuteWithContext(ctx, input)

	elapsed := time.Since(start)
	if err != nil {
		l.logger.Error("tool execution failed",
			Field{Key: "tool", Value: name},
			Field{Key: "error", Value: err.Error()},
			Field{Key: "duration", Value: elapsed.String()},
		)
	} else if result != nil && result.IsError {
		l.logger.Warn("tool execution returned error result",
			Field{Key: "tool", Value: name},
			Field{Key: "duration", Value: elapsed.String()},
		)
	} else {
		l.logger.Info("tool execution completed",
			Field{Key: "tool", Value: name},
			Field{Key: "duration", Value: elapsed.String()},
		)
	}

	return result, err
}

// ---------------------------------------------------------------------------
// LatencyHistogram — latency bucket counter for ToolMetrics
// ---------------------------------------------------------------------------

// LatencyHistogram tracks counts in named latency buckets.
// It is safe for concurrent use via a sync.Mutex.
type LatencyHistogram struct {
	mu     sync.Mutex
	counts map[string]int64
}

// NewLatencyHistogram creates a LatencyHistogram with an initialized map.
func NewLatencyHistogram() *LatencyHistogram {
	return &LatencyHistogram{counts: make(map[string]int64)}
}

// Observe increments the count for the given bucket.
func (h *LatencyHistogram) Observe(bucket string) {
	h.mu.Lock()
	h.counts[bucket]++
	h.mu.Unlock()
}

// Snapshot returns a copy of the current bucket counts.
func (h *LatencyHistogram) Snapshot() map[string]int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	cp := make(map[string]int64, len(h.counts))
	for k, v := range h.counts {
		cp[k] = v
	}
	return cp
}

// ---------------------------------------------------------------------------
// Tool-level Metrics Decorator
// ---------------------------------------------------------------------------

// metricsTool wraps a PluginTool with call counting and latency histogram.
type metricsTool struct {
	inner     PluginTool
	counter   *atomic.Int64
	histogram *LatencyHistogram
}

// ToolMetrics wraps a PluginTool with call count and latency tracking.
// A nil counter or nil histogram disables the corresponding metric.
// If both are nil, the tool is returned unchanged.
func ToolMetrics(tool PluginTool, counter *atomic.Int64, histogram *LatencyHistogram) PluginTool {
	if counter == nil && histogram == nil {
		return tool
	}
	return &metricsTool{inner: tool, counter: counter, histogram: histogram}
}

// Definition returns the wrapped tool's definition.
func (m *metricsTool) Definition() ToolDef {
	return m.inner.Definition()
}

// Execute runs the wrapped tool, recording call count and latency.
func (m *metricsTool) Execute(ctx context.Context, input string) (*ToolResult, error) {
	start := time.Now()
	result, err := m.inner.Execute(ctx, input)
	m.record(time.Since(start))
	return result, err
}

// ExecuteWithContext runs the wrapped tool's V2 method with metrics.
// If the inner tool does not implement PluginToolV2, it falls back to V1 Execute.
func (m *metricsTool) ExecuteWithContext(ctx *ToolCallContext, input string) (*ToolResult, error) {
	v2, ok := m.inner.(PluginToolV2)
	if !ok {
		return m.Execute(ctx.Ctx, input)
	}
	start := time.Now()
	result, err := v2.ExecuteWithContext(ctx, input)
	m.record(time.Since(start))
	return result, err
}

// record increments the call counter (if set) and records latency in the histogram (if set).
func (m *metricsTool) record(elapsed time.Duration) {
	if m.counter != nil {
		m.counter.Add(1)
	}
	if m.histogram != nil {
		m.histogram.Observe(latencyBucket(elapsed))
	}
}

// latencyBucket maps a duration to an exponential-scale bucket string.
func latencyBucket(d time.Duration) string {
	ms := d.Milliseconds()
	switch {
	case ms < 1:
		return "<1ms"
	case ms < 10:
		return "1-10ms"
	case ms < 100:
		return "10-100ms"
	case ms < 1000:
		return "100ms-1s"
	default:
		return ">1s"
	}
}
