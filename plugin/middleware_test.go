package plugin

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// MiddlewareChain Tests
// ---------------------------------------------------------------------------

func TestMiddlewareChain_Empty(t *testing.T) {
	chain := NewMiddlewareChain()
	if chain.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", chain.Len())
	}

	final := func(ctx context.Context, toolName string, input string) (*ToolResult, error) {
		return NewToolResult("ok"), nil
	}

	result, err := chain.Execute(context.Background(), "test_tool", `{"key":"val"}`, final)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.Content != "ok" {
		t.Errorf("Content = %q, want %q", result.Content, "ok")
	}
}

func TestMiddlewareChain_Single(t *testing.T) {
	var beforeCalled, afterCalled bool

	mw := func(ctx context.Context, toolName string, input string, next PluginMiddlewareNext) (*ToolResult, error) {
		beforeCalled = true
		result, err := next(ctx, toolName, input)
		afterCalled = true
		return result, err
	}

	chain := NewMiddlewareChain(mw)
	final := func(ctx context.Context, toolName string, input string) (*ToolResult, error) {
		return NewToolResult("done"), nil
	}

	result, err := chain.Execute(context.Background(), "tool", "{}", final)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !beforeCalled {
		t.Error("before was not called")
	}
	if !afterCalled {
		t.Error("after was not called")
	}
	if result.Content != "done" {
		t.Errorf("Content = %q, want %q", result.Content, "done")
	}
}

func TestMiddlewareChain_Multiple(t *testing.T) {
	var order []string

	makeMW := func(name string) PluginMiddleware {
		return func(ctx context.Context, toolName string, input string, next PluginMiddlewareNext) (*ToolResult, error) {
			order = append(order, name+"-before")
			result, err := next(ctx, toolName, input)
			order = append(order, name+"-after")
			return result, err
		}
	}

	chain := NewMiddlewareChain(makeMW("A"), makeMW("B"), makeMW("C"))
	final := func(ctx context.Context, toolName string, input string) (*ToolResult, error) {
		order = append(order, "final")
		return NewToolResult("ok"), nil
	}

	_, err := chain.Execute(context.Background(), "tool", "{}", final)
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	expected := []string{
		"A-before", "B-before", "C-before",
		"final",
		"C-after", "B-after", "A-after",
	}
	if len(order) != len(expected) {
		t.Fatalf("order = %v, want %v", order, expected)
	}
	for i, v := range expected {
		if order[i] != v {
			t.Errorf("order[%d] = %q, want %q", i, order[i], v)
		}
	}
}

func TestMiddlewareChain_ShortCircuit(t *testing.T) {
	var finalCalled bool

	blockMW := func(ctx context.Context, toolName string, input string, next PluginMiddlewareNext) (*ToolResult, error) {
		// Do NOT call next — short-circuit
		return NewToolError("blocked"), nil
	}

	chain := NewMiddlewareChain(blockMW)
	final := func(ctx context.Context, toolName string, input string) (*ToolResult, error) {
		finalCalled = true
		return NewToolResult("should not reach"), nil
	}

	result, err := chain.Execute(context.Background(), "tool", "{}", final)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if finalCalled {
		t.Error("final handler was called despite short-circuit")
	}
	if !result.IsError {
		t.Error("expected IsError = true")
	}
	if result.Content != "blocked" {
		t.Errorf("Content = %q, want %q", result.Content, "blocked")
	}
}

func TestMiddlewareChain_ShortCircuit_Middle(t *testing.T) {
	// First MW passes, second blocks, third should not be called
	var order []string

	makeMW := func(name string, block bool) PluginMiddleware {
		return func(ctx context.Context, toolName string, input string, next PluginMiddlewareNext) (*ToolResult, error) {
			order = append(order, name+"-before")
			if block {
				return NewToolError(name + " blocked"), nil
			}
			result, err := next(ctx, toolName, input)
			order = append(order, name+"-after")
			return result, err
		}
	}

	chain := NewMiddlewareChain(makeMW("A", false), makeMW("B", true), makeMW("C", false))
	final := func(ctx context.Context, toolName string, input string) (*ToolResult, error) {
		order = append(order, "final")
		return NewToolResult("ok"), nil
	}

	result, err := chain.Execute(context.Background(), "tool", "{}", final)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError = true")
	}
	// A-before, B-before (blocks), then A-after (A wraps B and still runs after)
	expected := []string{"A-before", "B-before", "A-after"}
	if len(order) != len(expected) {
		t.Fatalf("order = %v, want %v", order, expected)
	}
	for i, v := range expected {
		if order[i] != v {
			t.Errorf("order[%d] = %q, want %q", i, order[i], v)
		}
	}
}

func TestMiddlewareChain_Use(t *testing.T) {
	chain := NewMiddlewareChain()
	if chain.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", chain.Len())
	}

	chain.Use(func(ctx context.Context, toolName string, input string, next PluginMiddlewareNext) (*ToolResult, error) {
		return next(ctx, toolName, input)
	})
	if chain.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", chain.Len())
	}

	// nil middleware should be ignored
	chain.Use(nil)
	if chain.Len() != 1 {
		t.Fatalf("Len() = %d, want 1 after nil Use()", chain.Len())
	}
}

func TestMiddlewareChain_NilChain(t *testing.T) {
	var chain *MiddlewareChain
	if chain.Len() != 0 {
		t.Fatalf("Len() = %d, want 0 for nil chain", chain.Len())
	}

	final := func(ctx context.Context, toolName string, input string) (*ToolResult, error) {
		return NewToolResult("direct"), nil
	}

	result, err := chain.Execute(context.Background(), "tool", "{}", final)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if result.Content != "direct" {
		t.Errorf("Content = %q, want %q", result.Content, "direct")
	}
}

func TestMiddlewareChain_InputModification(t *testing.T) {
	// Middleware can modify input before passing to next
	modifyMW := func(ctx context.Context, toolName string, input string, next PluginMiddlewareNext) (*ToolResult, error) {
		return next(ctx, toolName, `{"modified":true}`)
	}

	chain := NewMiddlewareChain(modifyMW)
	final := func(ctx context.Context, toolName string, input string) (*ToolResult, error) {
		return NewToolResult(input), nil
	}

	result, err := chain.Execute(context.Background(), "tool", `{"original":true}`, final)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if result.Content != `{"modified":true}` {
		t.Errorf("Content = %q, want modified input", result.Content)
	}
}

func TestMiddlewareChain_OutputModification(t *testing.T) {
	// Middleware can modify the result after calling next
	modifyMW := func(ctx context.Context, toolName string, input string, next PluginMiddlewareNext) (*ToolResult, error) {
		result, err := next(ctx, toolName, input)
		if err != nil {
			return nil, err
		}
		result.Content = "modified:" + result.Content
		return result, nil
	}

	chain := NewMiddlewareChain(modifyMW)
	final := func(ctx context.Context, toolName string, input string) (*ToolResult, error) {
		return NewToolResult("original"), nil
	}

	result, err := chain.Execute(context.Background(), "tool", "{}", final)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if result.Content != "modified:original" {
		t.Errorf("Content = %q, want %q", result.Content, "modified:original")
	}
}

// ---------------------------------------------------------------------------
// LoggingMiddleware Tests
// ---------------------------------------------------------------------------

func TestLoggingMiddleware(t *testing.T) {
	logger := &captureLogger{}

	mw := LoggingMiddleware(logger)
	chain := NewMiddlewareChain(mw)
	final := func(ctx context.Context, toolName string, input string) (*ToolResult, error) {
		return NewToolResult("hello"), nil
	}

	result, err := chain.Execute(context.Background(), "my_tool", `{"q":"test"}`, final)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if result.Content != "hello" {
		t.Errorf("Content = %q, want %q", result.Content, "hello")
	}

	// Should have logged start + completed
	if len(logger.entries) < 2 {
		t.Fatalf("expected at least 2 log entries, got %d", len(logger.entries))
	}
	if !strings.Contains(logger.entries[0].msg, "started") {
		t.Errorf("first log = %q, want 'started'", logger.entries[0].msg)
	}
	// Last entry should be completed
	last := logger.entries[len(logger.entries)-1]
	if !strings.Contains(last.msg, "completed") {
		t.Errorf("last log = %q, want 'completed'", last.msg)
	}
}

func TestLoggingMiddleware_Error(t *testing.T) {
	logger := &captureLogger{}

	mw := LoggingMiddleware(logger)
	chain := NewMiddlewareChain(mw)
	final := func(ctx context.Context, toolName string, input string) (*ToolResult, error) {
		return nil, fmt.Errorf("tool failed")
	}

	_, err := chain.Execute(context.Background(), "tool", "{}", final)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Should have logged start + failed
	last := logger.entries[len(logger.entries)-1]
	if !strings.Contains(last.msg, "failed") {
		t.Errorf("last log = %q, want 'failed'", last.msg)
	}
}

// ---------------------------------------------------------------------------
// RecoveryMiddleware Tests
// ---------------------------------------------------------------------------

func TestRecoveryMiddleware(t *testing.T) {
	logger := &captureLogger{}

	mw := RecoveryMiddleware(logger)
	chain := NewMiddlewareChain(mw)
	final := func(ctx context.Context, toolName string, input string) (*ToolResult, error) {
		panic("something went wrong")
	}

	result, err := chain.Execute(context.Background(), "panic_tool", "{}", final)
	if err != nil {
		t.Fatalf("error should be nil after recovery, got: %v", err)
	}
	if result == nil {
		t.Fatal("result should not be nil after recovery")
	}
	if !result.IsError {
		t.Error("expected IsError = true")
	}
	if !strings.Contains(result.Content, "panicked") {
		t.Errorf("Content = %q, should contain 'panicked'", result.Content)
	}

	// Should have logged the panic
	hasPanicLog := false
	for _, e := range logger.entries {
		if strings.Contains(e.msg, "panic") {
			hasPanicLog = true
			break
		}
	}
	if !hasPanicLog {
		t.Error("expected panic log entry")
	}
}

func TestRecoveryMiddleware_NoPanic(t *testing.T) {
	logger := &captureLogger{}

	mw := RecoveryMiddleware(logger)
	chain := NewMiddlewareChain(mw)
	final := func(ctx context.Context, toolName string, input string) (*ToolResult, error) {
		return NewToolResult("fine"), nil
	}

	result, err := chain.Execute(context.Background(), "tool", "{}", final)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if result.Content != "fine" {
		t.Errorf("Content = %q, want %q", result.Content, "fine")
	}
	if result.IsError {
		t.Error("expected IsError = false")
	}
}

// ---------------------------------------------------------------------------
// TimeoutMiddleware Tests
// ---------------------------------------------------------------------------

func TestTimeoutMiddleware_Success(t *testing.T) {
	mw := TimeoutMiddleware(5 * time.Second)
	chain := NewMiddlewareChain(mw)
	final := func(ctx context.Context, toolName string, input string) (*ToolResult, error) {
		return NewToolResult("fast"), nil
	}

	result, err := chain.Execute(context.Background(), "tool", "{}", final)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if result.Content != "fast" {
		t.Errorf("Content = %q, want %q", result.Content, "fast")
	}
}

func TestTimeoutMiddleware_Exceeded(t *testing.T) {
	mw := TimeoutMiddleware(50 * time.Millisecond)
	chain := NewMiddlewareChain(mw)
	final := func(ctx context.Context, toolName string, input string) (*ToolResult, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return NewToolResult("slow"), nil
		}
	}

	result, err := chain.Execute(context.Background(), "tool", "{}", final)
	if err != nil {
		t.Fatalf("error should be nil (timeout converts to ToolResult), got: %v", err)
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}
	if !result.IsError {
		t.Error("expected IsError = true for timeout")
	}
	if !strings.Contains(result.Content, "timed out") {
		t.Errorf("Content = %q, should contain 'timed out'", result.Content)
	}
}

func TestTimeoutMiddleware_NoopForZero(t *testing.T) {
	// Zero or negative timeout should be a no-op
	mw := TimeoutMiddleware(0)
	chain := NewMiddlewareChain(mw)
	final := func(ctx context.Context, toolName string, input string) (*ToolResult, error) {
		return NewToolResult("passthrough"), nil
	}

	result, err := chain.Execute(context.Background(), "tool", "{}", final)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if result.Content != "passthrough" {
		t.Errorf("Content = %q, want %q", result.Content, "passthrough")
	}
}

// ---------------------------------------------------------------------------
// RetryMiddleware Tests
// ---------------------------------------------------------------------------

func TestRetryMiddleware_SuccessOnFirstTry(t *testing.T) {
	var calls int32

	mw := RetryMiddleware(3)
	chain := NewMiddlewareChain(mw)
	final := func(ctx context.Context, toolName string, input string) (*ToolResult, error) {
		atomic.AddInt32(&calls, 1)
		return NewToolResult("ok"), nil
	}

	result, err := chain.Execute(context.Background(), "tool", "{}", final)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if result.Content != "ok" {
		t.Errorf("Content = %q, want %q", result.Content, "ok")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("calls = %d, want 1", atomic.LoadInt32(&calls))
	}
}

func TestRetryMiddleware_SucceedsOnRetry(t *testing.T) {
	var calls int32

	mw := RetryMiddleware(3)
	chain := NewMiddlewareChain(mw)
	final := func(ctx context.Context, toolName string, input string) (*ToolResult, error) {
		count := atomic.AddInt32(&calls, 1)
		if count < 3 {
			return nil, fmt.Errorf("attempt %d failed", count)
		}
		return NewToolResult("recovered"), nil
	}

	result, err := chain.Execute(context.Background(), "tool", "{}", final)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if result.Content != "recovered" {
		t.Errorf("Content = %q, want %q", result.Content, "recovered")
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Errorf("calls = %d, want 3", atomic.LoadInt32(&calls))
	}
}

func TestRetryMiddleware_ExhaustsRetries(t *testing.T) {
	var calls int32

	mw := RetryMiddleware(2)
	chain := NewMiddlewareChain(mw)
	final := func(ctx context.Context, toolName string, input string) (*ToolResult, error) {
		atomic.AddInt32(&calls, 1)
		return nil, fmt.Errorf("always fails")
	}

	result, err := chain.Execute(context.Background(), "tool", "{}", final)
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if !strings.Contains(err.Error(), "always fails") {
		t.Errorf("error = %q, should contain 'always fails'", err.Error())
	}
	if atomic.LoadInt32(&calls) != 3 { // 1 initial + 2 retries
		t.Errorf("calls = %d, want 3", atomic.LoadInt32(&calls))
	}
	_ = result
}

func TestRetryMiddleware_NoopForZero(t *testing.T) {
	var calls int32

	mw := RetryMiddleware(0)
	chain := NewMiddlewareChain(mw)
	final := func(ctx context.Context, toolName string, input string) (*ToolResult, error) {
		atomic.AddInt32(&calls, 1)
		return NewToolResult("once"), nil
	}

	result, err := chain.Execute(context.Background(), "tool", "{}", final)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if result.Content != "once" {
		t.Errorf("Content = %q, want %q", result.Content, "once")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("calls = %d, want 1", atomic.LoadInt32(&calls))
	}
}

func TestRetryMiddleware_RespectsContextCancel(t *testing.T) {
	var calls int32

	mw := RetryMiddleware(10)
	chain := NewMiddlewareChain(mw)
	final := func(ctx context.Context, toolName string, input string) (*ToolResult, error) {
		atomic.AddInt32(&calls, 1)
		return nil, fmt.Errorf("fail")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := chain.Execute(ctx, "tool", "{}", final)
	if err == nil {
		t.Fatal("expected error")
	}
	// Should only be called once because context is already cancelled
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("calls = %d, want 1 (cancelled context should stop retries)", atomic.LoadInt32(&calls))
	}
}

// ---------------------------------------------------------------------------
// UseMiddleware + PluginContext Integration
// ---------------------------------------------------------------------------

func TestPluginContext_UseMiddleware(t *testing.T) {
	m := testManifest()
	m.Permissions = []string{"tools.register"}

	pc := newPluginContext(&m, &noopStorage{}, newPluginLogger(m.ID), nil, nil)

	mw := func(ctx context.Context, toolName string, input string, next PluginMiddlewareNext) (*ToolResult, error) {
		return next(ctx, toolName, input)
	}

	err := pc.UseMiddleware(mw)
	if err != nil {
		t.Fatalf("UseMiddleware() error: %v", err)
	}

	middlewares := pc.GetMiddlewares()
	if len(middlewares) != 1 {
		t.Fatalf("GetMiddlewares() = %d, want 1", len(middlewares))
	}
}

func TestPluginContext_UseMiddleware_NoPermission(t *testing.T) {
	m := testManifest()
	m.Permissions = nil // No permissions

	pc := newPluginContext(&m, &noopStorage{}, newPluginLogger(m.ID), nil, nil)

	err := pc.UseMiddleware(func(ctx context.Context, toolName string, input string, next PluginMiddlewareNext) (*ToolResult, error) {
		return next(ctx, toolName, input)
	})
	if err == nil {
		t.Fatal("expected permission error")
	}
	if _, ok := err.(*PermissionError); !ok {
		t.Errorf("error type = %T, want *PermissionError", err)
	}
}

func TestPluginContext_UseMiddleware_Nil(t *testing.T) {
	m := testManifest()
	m.Permissions = []string{"tools.register"}

	pc := newPluginContext(&m, &noopStorage{}, newPluginLogger(m.ID), nil, nil)

	err := pc.UseMiddleware(nil)
	if err != nil {
		t.Fatalf("UseMiddleware(nil) error: %v", err)
	}

	if len(pc.GetMiddlewares()) != 0 {
		t.Error("nil middleware should not be registered")
	}
}

// ---------------------------------------------------------------------------
// Combined Middleware Tests
// ---------------------------------------------------------------------------

func TestMiddlewareChain_RecoveryWithLogging(t *testing.T) {
	logger := &captureLogger{}

	// Recovery wraps Logging (recovery is outermost)
	chain := NewMiddlewareChain(
		RecoveryMiddleware(logger),
		LoggingMiddleware(logger),
	)
	final := func(ctx context.Context, toolName string, input string) (*ToolResult, error) {
		panic("boom")
	}

	result, err := chain.Execute(context.Background(), "tool", "{}", final)
	if err != nil {
		t.Fatalf("error should be nil after recovery: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError = true")
	}
}

func TestMiddlewareChain_AllBuiltins(t *testing.T) {
	logger := &captureLogger{}
	var calls int32

	chain := NewMiddlewareChain(
		RecoveryMiddleware(logger),
		LoggingMiddleware(logger),
		TimeoutMiddleware(5*time.Second),
		RetryMiddleware(2),
	)
	final := func(ctx context.Context, toolName string, input string) (*ToolResult, error) {
		atomic.AddInt32(&calls, 1)
		return NewToolResult("all good"), nil
	}

	result, err := chain.Execute(context.Background(), "tool", "{}", final)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if result.Content != "all good" {
		t.Errorf("Content = %q, want %q", result.Content, "all good")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("calls = %d, want 1", atomic.LoadInt32(&calls))
	}
}

// ---------------------------------------------------------------------------
// Test Helpers
// ---------------------------------------------------------------------------

// captureLogger captures log entries for assertions.
type captureLogger struct {
	entries []captureLogEntry
}

type captureLogEntry struct {
	msg    string
	fields []Field
}

func (l *captureLogger) Debug(msg string, fields ...Field) {
	l.entries = append(l.entries, captureLogEntry{msg: msg, fields: fields})
}
func (l *captureLogger) Info(msg string, fields ...Field) {
	l.entries = append(l.entries, captureLogEntry{msg: msg, fields: fields})
}
func (l *captureLogger) Warn(msg string, fields ...Field) {
	l.entries = append(l.entries, captureLogEntry{msg: msg, fields: fields})
}
func (l *captureLogger) Error(msg string, fields ...Field) {
	l.entries = append(l.entries, captureLogEntry{msg: msg, fields: fields})
}

func (l *captureLogger) WithField(key string, value any) Logger {
	return &loggerWithFields{parent: l, fields: []Field{{Key: key, Value: value}}}
}

func (l *captureLogger) WithFields(fields ...Field) Logger {
	return &loggerWithFields{parent: l, fields: fields}
}

// ---------------------------------------------------------------------------
// ToolTimeout Decorator Tests
// ---------------------------------------------------------------------------

func TestToolTimeout_Success(t *testing.T) {
	inner := &SimplePluginTool{
		Def: ToolDef{Name: "fast_tool", Description: "A fast tool"},
		ExecFn: func(ctx context.Context, input string) (*ToolResult, error) {
			return NewToolResult("fast: " + input), nil
		},
	}

	wrapped := ToolTimeout(inner, 5*time.Second)

	result, err := wrapped.Execute(context.Background(), `{"key":"val"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.Content != `fast: {"key":"val"}` {
		t.Errorf("Content = %q, want %q", result.Content, `fast: {"key":"val"}`)
	}
	if result.IsError {
		t.Error("expected IsError = false")
	}

	// Definition should pass through
	def := wrapped.Definition()
	if def.Name != "fast_tool" {
		t.Errorf("Definition().Name = %q, want %q", def.Name, "fast_tool")
	}
}

func TestToolTimeout_Exceeded(t *testing.T) {
	inner := &SimplePluginTool{
		Def: ToolDef{Name: "slow_tool", Description: "A slow tool"},
		ExecFn: func(ctx context.Context, input string) (*ToolResult, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(5 * time.Second):
				return NewToolResult("should not reach"), nil
			}
		},
	}

	wrapped := ToolTimeout(inner, 50*time.Millisecond)

	result, err := wrapped.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("error should be nil (timeout converts to ToolResult), got: %v", err)
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}
	if !result.IsError {
		t.Error("expected IsError = true for timeout")
	}
	if !strings.Contains(result.Content, "timed out") {
		t.Errorf("Content = %q, should contain 'timed out'", result.Content)
	}
	if !strings.Contains(result.Content, "slow_tool") {
		t.Errorf("Content = %q, should contain tool name 'slow_tool'", result.Content)
	}
}

func TestToolTimeout_V2Success(t *testing.T) {
	inner := &SimplePluginTool{
		Def: ToolDef{Name: "v2_tool", Description: "V2 fast tool"},
		ExecV2Fn: func(ctx *ToolCallContext, input string) (*ToolResult, error) {
			return NewToolResult("v2:session=" + ctx.SessionID), nil
		},
	}

	wrapped := ToolTimeout(inner, 5*time.Second)

	tcc := &ToolCallContext{
		Ctx:       context.Background(),
		SessionID: "sess-42",
	}

	// Verify it implements PluginToolV2
	v2, ok := wrapped.(PluginToolV2)
	if !ok {
		t.Fatal("ToolTimeout wrapper should implement PluginToolV2")
	}

	result, err := v2.ExecuteWithContext(tcc, `{}`)
	if err != nil {
		t.Fatalf("ExecuteWithContext() error: %v", err)
	}
	if result.Content != "v2:session=sess-42" {
		t.Errorf("Content = %q, want %q", result.Content, "v2:session=sess-42")
	}
}

func TestToolTimeout_V2Exceeded(t *testing.T) {
	inner := &SimplePluginTool{
		Def: ToolDef{Name: "v2_slow", Description: "V2 slow tool"},
		ExecV2Fn: func(ctx *ToolCallContext, input string) (*ToolResult, error) {
			select {
			case <-ctx.Ctx.Done():
				return nil, ctx.Ctx.Err()
			case <-time.After(5 * time.Second):
				return NewToolResult("should not reach"), nil
			}
		},
	}

	wrapped := ToolTimeout(inner, 50*time.Millisecond)

	tcc := &ToolCallContext{
		Ctx:       context.Background(),
		SessionID: "sess-99",
	}

	v2 := wrapped.(PluginToolV2)
	result, err := v2.ExecuteWithContext(tcc, `{}`)
	if err != nil {
		t.Fatalf("error should be nil, got: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError = true")
	}
	if !strings.Contains(result.Content, "timed out") {
		t.Errorf("Content = %q, should contain 'timed out'", result.Content)
	}
}

func TestToolTimeout_NonPositiveIsNoop(t *testing.T) {
	inner := &SimplePluginTool{
		Def: ToolDef{Name: "noop_tool", Description: "no timeout"},
		ExecFn: func(ctx context.Context, input string) (*ToolResult, error) {
			return NewToolResult("direct"), nil
		},
	}

	// Zero timeout should return the original tool unchanged
	wrapped := ToolTimeout(inner, 0)
	if wrapped != inner {
		t.Error("ToolTimeout with 0 should return the original tool")
	}

	// Negative timeout should also return the original tool unchanged
	wrapped2 := ToolTimeout(inner, -1*time.Second)
	if wrapped2 != inner {
		t.Error("ToolTimeout with negative duration should return the original tool")
	}
}

// ---------------------------------------------------------------------------
// ToolRetry Decorator Tests
// ---------------------------------------------------------------------------

func TestToolRetry_SuccessFirstTry(t *testing.T) {
	var calls int32

	inner := &SimplePluginTool{
		Def: ToolDef{Name: "lucky_tool", Description: "Always succeeds"},
		ExecFn: func(ctx context.Context, input string) (*ToolResult, error) {
			atomic.AddInt32(&calls, 1)
			return NewToolResult("ok"), nil
		},
	}

	wrapped := ToolRetry(inner, 3, time.Millisecond)

	result, err := wrapped.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.Content != "ok" {
		t.Errorf("Content = %q, want %q", result.Content, "ok")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("calls = %d, want 1", atomic.LoadInt32(&calls))
	}

	// Definition should pass through
	def := wrapped.Definition()
	if def.Name != "lucky_tool" {
		t.Errorf("Definition().Name = %q, want %q", def.Name, "lucky_tool")
	}
}

func TestToolRetry_SuccessAfterRetry(t *testing.T) {
	var calls int32

	inner := &SimplePluginTool{
		Def: ToolDef{Name: "flaky_tool", Description: "Fails then succeeds"},
		ExecFn: func(ctx context.Context, input string) (*ToolResult, error) {
			count := atomic.AddInt32(&calls, 1)
			if count < 3 {
				return nil, fmt.Errorf("attempt %d failed", count)
			}
			return NewToolResult("recovered"), nil
		},
	}

	wrapped := ToolRetry(inner, 3, time.Millisecond)

	result, err := wrapped.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.Content != "recovered" {
		t.Errorf("Content = %q, want %q", result.Content, "recovered")
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Errorf("calls = %d, want 3", atomic.LoadInt32(&calls))
	}
}

func TestToolRetry_AllFail(t *testing.T) {
	var calls int32

	inner := &SimplePluginTool{
		Def: ToolDef{Name: "doom_tool", Description: "Always fails"},
		ExecFn: func(ctx context.Context, input string) (*ToolResult, error) {
			atomic.AddInt32(&calls, 1)
			return nil, fmt.Errorf("always fails")
		},
	}

	wrapped := ToolRetry(inner, 2, time.Millisecond)

	result, err := wrapped.Execute(context.Background(), `{}`)
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if !strings.Contains(err.Error(), "always fails") {
		t.Errorf("error = %q, should contain 'always fails'", err.Error())
	}
	// 1 initial + 2 retries = 3
	if atomic.LoadInt32(&calls) != 3 {
		t.Errorf("calls = %d, want 3", atomic.LoadInt32(&calls))
	}
	_ = result
}

func TestToolRetry_ZeroRetries(t *testing.T) {
	inner := &SimplePluginTool{
		Def: ToolDef{Name: "no_retry_tool", Description: "No retries"},
		ExecFn: func(ctx context.Context, input string) (*ToolResult, error) {
			return NewToolResult("direct"), nil
		},
	}

	// Zero maxRetries should return the original tool unchanged
	wrapped := ToolRetry(inner, 0, time.Millisecond)
	if wrapped != inner {
		t.Error("ToolRetry with maxRetries=0 should return the original tool")
	}

	// Negative maxRetries should also return the original tool unchanged
	wrapped2 := ToolRetry(inner, -1, time.Millisecond)
	if wrapped2 != inner {
		t.Error("ToolRetry with negative maxRetries should return the original tool")
	}
}

func TestToolRetry_ContextCancel(t *testing.T) {
	var calls int32

	inner := &SimplePluginTool{
		Def: ToolDef{Name: "cancel_tool", Description: "Fails on every call"},
		ExecFn: func(ctx context.Context, input string) (*ToolResult, error) {
			atomic.AddInt32(&calls, 1)
			return nil, fmt.Errorf("fail")
		},
	}

	wrapped := ToolRetry(inner, 10, time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := wrapped.Execute(ctx, `{}`)
	if err == nil {
		t.Fatal("expected error")
	}
	// Should only be called once because context is already cancelled
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("calls = %d, want 1 (cancelled context should stop retries)", atomic.LoadInt32(&calls))
	}
}

func TestToolRetry_V2Success(t *testing.T) {
	var calls int32

	inner := &SimplePluginTool{
		Def: ToolDef{Name: "v2_retry_tool", Description: "V2 fast tool"},
		ExecV2Fn: func(ctx *ToolCallContext, input string) (*ToolResult, error) {
			atomic.AddInt32(&calls, 1)
			return NewToolResult("v2:session=" + ctx.SessionID), nil
		},
	}

	wrapped := ToolRetry(inner, 3, time.Millisecond)

	tcc := &ToolCallContext{
		Ctx:       context.Background(),
		SessionID: "sess-retry",
	}

	// Verify it implements PluginToolV2
	v2, ok := wrapped.(PluginToolV2)
	if !ok {
		t.Fatal("ToolRetry wrapper should implement PluginToolV2")
	}

	result, err := v2.ExecuteWithContext(tcc, `{}`)
	if err != nil {
		t.Fatalf("ExecuteWithContext() error: %v", err)
	}
	if result.Content != "v2:session=sess-retry" {
		t.Errorf("Content = %q, want %q", result.Content, "v2:session=sess-retry")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("calls = %d, want 1", atomic.LoadInt32(&calls))
	}
}

// ---------------------------------------------------------------------------
// ToolCache Decorator Tests
// ---------------------------------------------------------------------------

func TestToolCache_Hit(t *testing.T) {
	var calls int32

	inner := &SimplePluginTool{
		Def: ToolDef{Name: "cached_tool", Description: "A cached tool"},
		ExecFn: func(ctx context.Context, input string) (*ToolResult, error) {
			atomic.AddInt32(&calls, 1)
			return NewToolResult("cached: " + input), nil
		},
	}

	wrapped := ToolCache(inner, 5*time.Second)

	// First call — should invoke inner
	result1, err := wrapped.Execute(context.Background(), `{"key":"val"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result1.Content != `cached: {"key":"val"}` {
		t.Errorf("Content = %q, want %q", result1.Content, `cached: {"key":"val"}`)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("calls after first = %d, want 1", atomic.LoadInt32(&calls))
	}

	// Second call with same input — should hit cache
	result2, err := wrapped.Execute(context.Background(), `{"key":"val"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result2.Content != `cached: {"key":"val"}` {
		t.Errorf("Content = %q, want %q", result2.Content, `cached: {"key":"val"}`)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("calls after second = %d, want 1 (cache hit)", atomic.LoadInt32(&calls))
	}

	// Definition should pass through
	def := wrapped.Definition()
	if def.Name != "cached_tool" {
		t.Errorf("Definition().Name = %q, want %q", def.Name, "cached_tool")
	}
}

func TestToolCache_Miss(t *testing.T) {
	var calls int32

	inner := &SimplePluginTool{
		Def: ToolDef{Name: "miss_tool", Description: "Different inputs"},
		ExecFn: func(ctx context.Context, input string) (*ToolResult, error) {
			atomic.AddInt32(&calls, 1)
			return NewToolResult("result: " + input), nil
		},
	}

	wrapped := ToolCache(inner, 5*time.Second)

	// Two different inputs — each should invoke inner
	result1, err := wrapped.Execute(context.Background(), `{"a":1}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result1.Content != `result: {"a":1}` {
		t.Errorf("Content = %q, want %q", result1.Content, `result: {"a":1}`)
	}

	result2, err := wrapped.Execute(context.Background(), `{"b":2}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result2.Content != `result: {"b":2}` {
		t.Errorf("Content = %q, want %q", result2.Content, `result: {"b":2}`)
	}

	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("calls = %d, want 2 (two different inputs)", atomic.LoadInt32(&calls))
	}
}

func TestToolCache_Expired(t *testing.T) {
	var calls int32

	inner := &SimplePluginTool{
		Def: ToolDef{Name: "expire_tool", Description: "TTL expiry test"},
		ExecFn: func(ctx context.Context, input string) (*ToolResult, error) {
			atomic.AddInt32(&calls, 1)
			return NewToolResult("result: " + input), nil
		},
	}

	wrapped := ToolCache(inner, 50*time.Millisecond)

	// First call — invoke inner
	_, err := wrapped.Execute(context.Background(), `{"key":"val"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("calls after first = %d, want 1", atomic.LoadInt32(&calls))
	}

	// Wait for TTL to expire
	time.Sleep(80 * time.Millisecond)

	// Second call — cache should have expired, inner invoked again
	_, err = wrapped.Execute(context.Background(), `{"key":"val"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("calls after expiry = %d, want 2 (cache expired)", atomic.LoadInt32(&calls))
	}
}

func TestToolCache_NonPositiveTTL(t *testing.T) {
	inner := &SimplePluginTool{
		Def: ToolDef{Name: "noop_tool", Description: "No-op test"},
		ExecFn: func(ctx context.Context, input string) (*ToolResult, error) {
			return NewToolResult("direct"), nil
		},
	}

	// Zero TTL — should return inner unchanged
	wrapped := ToolCache(inner, 0)
	if wrapped != inner {
		t.Error("ToolCache with ttl=0 should return inner tool unchanged")
	}

	// Negative TTL — should also return inner unchanged
	wrappedNeg := ToolCache(inner, -1*time.Second)
	if wrappedNeg != inner {
		t.Error("ToolCache with negative ttl should return inner tool unchanged")
	}
}

func TestToolCache_ErrorNotCached(t *testing.T) {
	var calls int32

	inner := &SimplePluginTool{
		Def: ToolDef{Name: "err_tool", Description: "Error caching test"},
		ExecFn: func(ctx context.Context, input string) (*ToolResult, error) {
			atomic.AddInt32(&calls, 1)
			return nil, fmt.Errorf("transient error")
		},
	}

	wrapped := ToolCache(inner, 5*time.Second)

	// First call returns error
	_, err := wrapped.Execute(context.Background(), `{"key":"val"}`)
	if err == nil {
		t.Fatal("expected error from first call")
	}

	// Second call — error should NOT be cached, inner called again
	_, err = wrapped.Execute(context.Background(), `{"key":"val"}`)
	if err == nil {
		t.Fatal("expected error from second call")
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("calls = %d, want 2 (error should not be cached)", atomic.LoadInt32(&calls))
	}
}
