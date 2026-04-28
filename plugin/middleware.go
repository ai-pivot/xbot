package plugin

import (
	"context"
	"fmt"
	"runtime/debug"
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

// Execute runs the wrapped tool with a timeout-derivative context.
func (t *timeoutTool) Execute(ctx context.Context, input string) (*ToolResult, error) {
	childCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	type outcome struct {
		result *ToolResult
		err    error
	}
	ch := make(chan outcome, 1)
	go func() {
		r, e := t.inner.Execute(childCtx, input)
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

// ExecuteWithContext runs the wrapped tool's V2 method with a timeout.
// If the inner tool does not implement PluginToolV2, it falls back to V1 Execute.
func (t *timeoutTool) ExecuteWithContext(ctx *ToolCallContext, input string) (*ToolResult, error) {
	v2, ok := t.inner.(PluginToolV2)
	if !ok {
		// Not V2 — delegate to V1 path
		return t.Execute(ctx.Ctx, input)
	}

	childCtx, cancel := context.WithTimeout(ctx.Ctx, t.timeout)
	defer cancel()

	type outcome struct {
		result *ToolResult
		err    error
	}
	ch := make(chan outcome, 1)
	go func() {
		// Build a child ToolCallContext with the deadline context.
		childTCC := &ToolCallContext{
			SessionID: ctx.SessionID,
			Channel:   ctx.Channel,
			ChatID:    ctx.ChatID,
			UserID:    ctx.UserID,
			Ctx:       childCtx,
		}
		r, e := v2.ExecuteWithContext(childTCC, input)
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
