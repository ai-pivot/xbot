package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// MustActivate
// ---------------------------------------------------------------------------

func TestMustActivate(t *testing.T) {
	p := &sdkTestPlugin{manifest: testManifest()}
	ctx := &sdkMockContext{}

	MustActivate(p, ctx)
	if !p.activated {
		t.Error("expected plugin to be activated")
	}
}

func TestMustActivate_Panic(t *testing.T) {
	p := &sdkTestPlugin{
		manifest:    testManifest(),
		activateErr: errors.New("activation failed"),
	}
	ctx := &sdkMockContext{}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected MustActivate to panic on error")
		}
		got, ok := r.(string)
		if !ok {
			t.Fatalf("expected string panic, got %T", r)
		}
		for _, substr := range []string{"activation failed"} {
			found := false
			for i := 0; i <= len(got)-len(substr); i++ {
				if got[i:i+len(substr)] == substr {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("panic message should contain %q, got: %s", substr, got)
			}
		}
	}()

	MustActivate(p, ctx)
}

// ---------------------------------------------------------------------------
// ToolFromFunc
// ---------------------------------------------------------------------------

func TestToolFromFunc(t *testing.T) {
	tool := ToolFromFunc("echo", "echoes input", func(ctx context.Context, input string) (string, error) {
		return "echo: " + input, nil
	})

	def := tool.Definition()
	if def.Name != "echo" {
		t.Errorf("expected name 'echo', got %q", def.Name)
	}
	if def.Description != "echoes input" {
		t.Errorf("expected desc 'echoes input', got %q", def.Description)
	}

	result, err := tool.Execute(context.Background(), `{"text":"hello"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "echo: {\"text\":\"hello\"}" {
		t.Errorf("unexpected content: %s", result.Content)
	}
	if result.IsError {
		t.Error("expected non-error result")
	}
}

func TestToolFromFunc_Error(t *testing.T) {
	tool := ToolFromFunc("fail", "always fails", func(ctx context.Context, input string) (string, error) {
		return "", errors.New("boom")
	})

	_, err := tool.Execute(context.Background(), "")
	if err == nil || err.Error() != "boom" {
		t.Errorf("expected 'boom' error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ToolFromJSONFunc
// ---------------------------------------------------------------------------

func TestToolFromJSONFunc(t *testing.T) {
	params := []ToolParamDef{
		{Name: "name", Type: "string", Description: "name field", Required: true},
	}
	tool := ToolFromJSONFunc("greet", "generates greeting", params,
		func(ctx context.Context, input json.RawMessage) (any, error) {
			var m map[string]string
			if err := json.Unmarshal(input, &m); err != nil {
				return nil, err
			}
			return map[string]string{"greeting": "Hello, " + m["name"]}, nil
		},
	)

	def := tool.Definition()
	if def.Name != "greet" {
		t.Errorf("expected name 'greet', got %q", def.Name)
	}
	if def.InputSchema == nil {
		t.Error("expected InputSchema to be populated")
	}
	if len(def.Parameters) != 1 || def.Parameters[0].Name != "name" {
		t.Error("expected 1 parameter 'name'")
	}

	result, err := tool.Execute(context.Background(), `{"name":"World"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got map[string]string
	if err := json.Unmarshal([]byte(result.Content), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got["greeting"] != "Hello, World" {
		t.Errorf("unexpected greeting: %s", got["greeting"])
	}
}

func TestToolFromJSONFunc_Error(t *testing.T) {
	tool := ToolFromJSONFunc("fail_json", "fails on bad JSON", nil,
		func(ctx context.Context, input json.RawMessage) (any, error) {
			return nil, errors.New("json fail")
		},
	)

	_, err := tool.Execute(context.Background(), "{}")
	if err == nil || err.Error() != "json fail" {
		t.Errorf("expected 'json fail' error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// DenyHook
// ---------------------------------------------------------------------------

func TestDenyHook(t *testing.T) {
	handler := DenyHook("not allowed")
	result, err := handler(context.Background(), &HookPayload{Event: HookPreToolUse})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Decision != DecisionDeny {
		t.Errorf("expected DecisionDeny, got %s", result.Decision)
	}
	if result.Message != "not allowed" {
		t.Errorf("expected message 'not allowed', got %q", result.Message)
	}
}

// ---------------------------------------------------------------------------
// AllowHook
// ---------------------------------------------------------------------------

func TestAllowHook(t *testing.T) {
	handler := AllowHook()
	result, err := handler(context.Background(), &HookPayload{Event: HookPreToolUse})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Decision != DecisionAllow {
		t.Errorf("expected DecisionAllow, got %s", result.Decision)
	}
}

// ---------------------------------------------------------------------------
// LogHook
// ---------------------------------------------------------------------------

func TestLogHook(t *testing.T) {
	logger := &sdkMockLogger{}
	handler := LogHook(logger, "hook fired")

	result, err := handler(context.Background(), &HookPayload{Event: HookPostToolUse})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Decision != DecisionAllow {
		t.Errorf("expected DecisionAllow, got %s", result.Decision)
	}
	if len(logger.entries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(logger.entries))
	}
	if logger.entries[0].msg != "hook fired" {
		t.Errorf("expected log msg 'hook fired', got %q", logger.entries[0].msg)
	}
}

// ---------------------------------------------------------------------------
// StaticEnricher
// ---------------------------------------------------------------------------

func TestStaticEnricher(t *testing.T) {
	enricher := StaticEnricher("system prompt content")
	result, err := enricher(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "system prompt content" {
		t.Errorf("expected 'system prompt content', got %q", result)
	}
}

// ---------------------------------------------------------------------------
// FileEnricher
// ---------------------------------------------------------------------------

func TestFileEnricher(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "enricher.txt")
	if err := os.WriteFile(path, []byte("file content"), 0644); err != nil {
		t.Fatal(err)
	}

	enricher := FileEnricher(path)
	result, err := enricher(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "file content" {
		t.Errorf("expected 'file content', got %q", result)
	}
}

func TestFileEnricher_NotFound(t *testing.T) {
	enricher := FileEnricher("/nonexistent/path")
	_, err := enricher(context.Background())
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

// ---------------------------------------------------------------------------
// QuickManifest
// ---------------------------------------------------------------------------

func TestQuickManifest(t *testing.T) {
	m := QuickManifest("com.test.quick", "Quick", "0.1.0", "test manifest")

	if m.ID != "com.test.quick" {
		t.Errorf("expected ID 'com.test.quick', got %q", m.ID)
	}
	if m.Name != "Quick" {
		t.Errorf("expected Name 'Quick', got %q", m.Name)
	}
	if m.Version != "0.1.0" {
		t.Errorf("expected Version '0.1.0', got %q", m.Version)
	}
	if m.Runtime != RuntimeNative {
		t.Errorf("expected RuntimeNative, got %s", m.Runtime)
	}
	if len(m.ActivationEvents) != 1 || m.ActivationEvents[0] != "onStart" {
		t.Errorf("expected default activation 'onStart', got %v", m.ActivationEvents)
	}
}

func TestQuickManifestWithOptions(t *testing.T) {
	m := QuickManifest("com.test.opts", "Opts", "1.0.0", "test with options",
		WithPermissions("tools.register", "hooks.subscribe"),
		WithActivationEvents("onTool:search"),
		WithRuntime(RuntimeGRPC),
		WithTools(ToolContribution{Name: "search", Description: "search tool"}),
		WithHooks(HookContribution{Event: "PreToolUse", Matcher: "search"}),
		WithEnrichers(EnricherContribution{Name: "project-info", Description: "project context"}),
	)

	// Permissions
	if len(m.Permissions) != 2 {
		t.Errorf("expected 2 permissions, got %d", len(m.Permissions))
	}

	// Activation events
	if len(m.ActivationEvents) != 1 || m.ActivationEvents[0] != "onTool:search" {
		t.Errorf("expected activation 'onTool:search', got %v", m.ActivationEvents)
	}

	// Runtime
	if m.Runtime != RuntimeGRPC {
		t.Errorf("expected RuntimeGRPC, got %s", m.Runtime)
	}

	// Contributes
	if m.Contributes == nil {
		t.Fatal("expected Contributes to be set")
	}
	if len(m.Contributes.Tools) != 1 || m.Contributes.Tools[0].Name != "search" {
		t.Error("expected 1 tool 'search'")
	}
	if len(m.Contributes.Hooks) != 1 || m.Contributes.Hooks[0].Event != "PreToolUse" {
		t.Error("expected 1 hook 'PreToolUse'")
	}
	if len(m.Contributes.ContextEnrichers) != 1 || m.Contributes.ContextEnrichers[0].Name != "project-info" {
		t.Error("expected 1 enricher 'project-info'")
	}
}

// ---------------------------------------------------------------------------
// Test Helpers (sdk-prefixed to avoid conflicts with plugin_test.go)
// ---------------------------------------------------------------------------

type sdkTestPlugin struct {
	manifest    PluginManifest
	activateErr error
	activated   bool
}

func (p *sdkTestPlugin) Manifest() PluginManifest { return p.manifest }
func (p *sdkTestPlugin) Activate(ctx PluginContext) error {
	if p.activateErr != nil {
		return p.activateErr
	}
	p.activated = true
	return nil
}
func (p *sdkTestPlugin) Deactivate(ctx PluginContext) error { return nil }

// sdkMockContext implements PluginContext with no-ops for testing.
type sdkMockContext struct{}

func (c *sdkMockContext) RegisterTool(tool PluginTool) error                      { return nil }
func (c *sdkMockContext) RegisterTools(tools ...PluginTool) error                 { return nil }
func (c *sdkMockContext) UseMiddleware(middleware PluginMiddleware) error         { return nil }
func (c *sdkMockContext) OnPreToolUse(matcher string, handler HookHandler) error  { return nil }
func (c *sdkMockContext) OnPostToolUse(matcher string, handler HookHandler) error { return nil }
func (c *sdkMockContext) OnUserPrompt(handler HookHandler) error                  { return nil }
func (c *sdkMockContext) OnAgentStop(handler HookHandler) error                   { return nil }
func (c *sdkMockContext) OnSessionStart(handler HookHandler) error                { return nil }
func (c *sdkMockContext) OnSessionEnd(handler HookHandler) error                  { return nil }
func (c *sdkMockContext) OnEvent(event HookEvent, matcher string, handler HookHandler) error {
	return nil
}
func (c *sdkMockContext) OnAllToolUse(handler HookHandler) error           { return nil }
func (c *sdkMockContext) OnError(handler HookHandler) error                { return nil }
func (c *sdkMockContext) OnPluginError(callback PluginErrorCallback) error { return nil }
func (c *sdkMockContext) ContributeUI(widgetID, zone string, widget UIWidget, priority int) error {
	return nil
}
func (c *sdkMockContext) UpdateWidget(widgetID string) error                        { return nil }
func (c *sdkMockContext) SetWidgetRegistry(wr *WidgetRegistry)                      {}
func (c *sdkMockContext) EnrichContext(name string, enricher ContextEnricher) error { return nil }
func (c *sdkMockContext) Storage() StorageAccessor                                  { return nil }
func (c *sdkMockContext) StorageInt(key string) (int64, bool)                       { return 0, false }
func (c *sdkMockContext) StorageBool(key string) (bool, bool)                       { return false, false }
func (c *sdkMockContext) StorageJSON(key string, value any) error                   { return nil }
func (c *sdkMockContext) StorageGetJSON(key string, target any) error               { return nil }
func (c *sdkMockContext) PluginID() string                                          { return "" }
func (c *sdkMockContext) WorkingDir() string                                        { return "" }
func (c *sdkMockContext) Channel() string                                           { return "" }
func (c *sdkMockContext) ChatID() string                                            { return "" }
func (c *sdkMockContext) Logger() Logger                                            { return &sdkMockLogger{} }
func (c *sdkMockContext) Config() (map[string]any, error)                           { return make(map[string]any), nil }
func (c *sdkMockContext) SetConfig(key string, value any) error                     { return nil }
func (c *sdkMockContext) Subscribe(topic string, handler PluginEventHandler) error  { return nil }
func (c *sdkMockContext) Publish(topic string, data any) error                      { return nil }
func (c *sdkMockContext) ToolCallCount() int64                                      { return 0 }
func (c *sdkMockContext) HookCallCount() int64                                      { return 0 }
func (c *sdkMockContext) SetValue(key string, value any)                            {}
func (c *sdkMockContext) GetValue(key string) (any, bool)                           { return nil, false }

type sdkMockLogger struct {
	entries []sdkLogEntry
}

type sdkLogEntry struct {
	msg    string
	fields []Field
}

func (l *sdkMockLogger) Debug(msg string, fields ...Field) {
	l.entries = append(l.entries, sdkLogEntry{msg: msg, fields: fields})
}
func (l *sdkMockLogger) Info(msg string, fields ...Field) {
	l.entries = append(l.entries, sdkLogEntry{msg: msg, fields: fields})
}
func (l *sdkMockLogger) Warn(msg string, fields ...Field) {
	l.entries = append(l.entries, sdkLogEntry{msg: msg, fields: fields})
}
func (l *sdkMockLogger) Error(msg string, fields ...Field) {
	l.entries = append(l.entries, sdkLogEntry{msg: msg, fields: fields})
}

func (l *sdkMockLogger) WithField(key string, value any) Logger {
	return &loggerWithFields{parent: l, fields: []Field{{Key: key, Value: value}}}
}

func (l *sdkMockLogger) WithFields(fields ...Field) Logger {
	return &loggerWithFields{parent: l, fields: fields}
}

func TestQuickManifest_WithActivationEvents(t *testing.T) {
	m := QuickManifest("id", "name", "1.0.0", "desc",
		WithActivationEvents("onTool:deploy"))
	if len(m.ActivationEvents) != 1 || m.ActivationEvents[0] != "onTool:deploy" {
		t.Errorf("ActivationEvents = %v, want [onTool:deploy]", m.ActivationEvents)
	}
}

func TestQuickManifest_WithRuntime(t *testing.T) {
	m := QuickManifest("id", "name", "1.0.0", "desc",
		WithRuntime(RuntimeGRPC))
	if m.Runtime != RuntimeGRPC {
		t.Errorf("Runtime = %q, want %q", m.Runtime, RuntimeGRPC)
	}
}

func TestQuickManifest_WithTools(t *testing.T) {
	m := QuickManifest("id", "name", "1.0.0", "desc",
		WithTools(ToolContribution{Name: "tool1", Description: "test tool"}))
	if m.Contributes == nil || len(m.Contributes.Tools) != 1 {
		t.Fatal("expected 1 tool contribution")
	}
	if m.Contributes.Tools[0].Name != "tool1" {
		t.Errorf("tool name = %q, want 'tool1'", m.Contributes.Tools[0].Name)
	}
}

func TestQuickManifest_WithHooks(t *testing.T) {
	m := QuickManifest("id", "name", "1.0.0", "desc",
		WithHooks(HookContribution{Event: "PreToolUse", Matcher: "Shell"}))
	if m.Contributes == nil || len(m.Contributes.Hooks) != 1 {
		t.Fatal("expected 1 hook contribution")
	}
	if m.Contributes.Hooks[0].Event != "PreToolUse" {
		t.Errorf("hook event = %q, want 'PreToolUse'", m.Contributes.Hooks[0].Event)
	}
}

func TestQuickManifest_WithEnrichers(t *testing.T) {
	m := QuickManifest("id", "name", "1.0.0", "desc",
		WithEnrichers(EnricherContribution{Name: "ctx", Description: "context enricher"}))
	if m.Contributes == nil || len(m.Contributes.ContextEnrichers) != 1 {
		t.Fatal("expected 1 enricher contribution")
	}
	if m.Contributes.ContextEnrichers[0].Name != "ctx" {
		t.Errorf("enricher name = %q, want 'ctx'", m.Contributes.ContextEnrichers[0].Name)
	}
}

// ---------------------------------------------------------------------------
// FormatToolResult
// ---------------------------------------------------------------------------

func TestFormatToolResult(t *testing.T) {
	result := FormatToolResult("Server Info", map[string]string{
		"status":  "running",
		"version": "2.0.1",
		"port":    "8080",
	})

	if result.IsError {
		t.Error("expected non-error result")
	}
	expected := "Server Info\nport: 8080\nstatus: running\nversion: 2.0.1"
	if result.Content != expected {
		t.Errorf("expected %q, got %q", expected, result.Content)
	}
}

func TestFormatToolResult_EmptySections(t *testing.T) {
	result := FormatToolResult("No Data", nil)

	if result.IsError {
		t.Error("expected non-error result")
	}
	if result.Content != "No Data" {
		t.Errorf("expected just title, got %q", result.Content)
	}
}

func TestFormatToolResult_SingleSection(t *testing.T) {
	result := FormatToolResult("Status", map[string]string{"key": "value"})

	expected := "Status\nkey: value"
	if result.Content != expected {
		t.Errorf("expected %q, got %q", expected, result.Content)
	}
}

func TestFormatToolResult_EmptyTitle(t *testing.T) {
	result := FormatToolResult("", map[string]string{"a": "1"})

	expected := "\na: 1"
	if result.Content != expected {
		t.Errorf("expected %q, got %q", expected, result.Content)
	}
}

// ---------------------------------------------------------------------------
// FormatListResult
// ---------------------------------------------------------------------------

func TestFormatListResult(t *testing.T) {
	result := FormatListResult([]string{"alpha", "beta", "gamma"})

	if result.IsError {
		t.Error("expected non-error result")
	}
	expected := "1. alpha\n2. beta\n3. gamma"
	if result.Content != expected {
		t.Errorf("expected %q, got %q", expected, result.Content)
	}
}

func TestFormatListResult_Empty(t *testing.T) {
	result := FormatListResult(nil)

	if result.IsError {
		t.Error("expected non-error result")
	}
	if result.Content != "(no items)" {
		t.Errorf("expected '(no items)', got %q", result.Content)
	}
}

func TestFormatListResult_SingleItem(t *testing.T) {
	result := FormatListResult([]string{"only"})

	expected := "1. only"
	if result.Content != expected {
		t.Errorf("expected %q, got %q", expected, result.Content)
	}
}

// ---------------------------------------------------------------------------
// FormatErrorResult
// ---------------------------------------------------------------------------

func TestFormatErrorResult(t *testing.T) {
	result := FormatErrorResult("deploy", fmt.Errorf("connection refused"))

	if !result.IsError {
		t.Error("expected error result")
	}
	expected := "deploy failed: connection refused"
	if result.Content != expected {
		t.Errorf("expected %q, got %q", expected, result.Content)
	}
}

func TestFormatErrorResult_WrappedError(t *testing.T) {
	inner := fmt.Errorf("read failed: %w", errors.New("EOF"))
	result := FormatErrorResult("load config", inner)

	if !result.IsError {
		t.Error("expected error result")
	}
	if result.Content != "load config failed: read failed: EOF" {
		t.Errorf("unexpected content: %q", result.Content)
	}
}

func TestFormatErrorResult_NilError(t *testing.T) {
	result := FormatErrorResult("test", nil)

	if !result.IsError {
		t.Error("expected error result")
	}
	expected := "test failed: unknown error"
	if result.Content != expected {
		t.Errorf("expected %q, got %q", expected, result.Content)
	}
}
