package plugin

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// TestKit — lightweight test harness for plugin authors
// ---------------------------------------------------------------------------

// formatFields formats a slice of Field into a "key=value key=value" string
// for use in test log output.
func formatFields(fields []Field) string {
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = fmt.Sprintf("%s=%v", f.Key, f.Value)
	}
	return strings.Join(parts, " ")
}

// ---------------------------------------------------------------------------
// testLogger — Logger that writes to *testing.T
// ---------------------------------------------------------------------------

// testLogger implements Logger by writing to a *testing.T.
// Error-level messages use t.Errorf; all others use t.Logf.
type testLogger struct {
	t *testing.T
}

func (l *testLogger) Debug(msg string, fields ...Field) {
	l.t.Helper()
	l.t.Logf("[DEBUG] %s %s", msg, formatFields(fields))
}

func (l *testLogger) Info(msg string, fields ...Field) {
	l.t.Helper()
	l.t.Logf("[INFO] %s %s", msg, formatFields(fields))
}

func (l *testLogger) Warn(msg string, fields ...Field) {
	l.t.Helper()
	l.t.Logf("[WARN] %s %s", msg, formatFields(fields))
}

func (l *testLogger) Error(msg string, fields ...Field) {
	l.t.Helper()
	l.t.Errorf("[ERROR] %s %s", msg, formatFields(fields))
}

func (l *testLogger) WithField(key string, value any) Logger {
	return &loggerWithFields{parent: l, fields: []Field{{Key: key, Value: value}}}
}

func (l *testLogger) WithFields(fields ...Field) Logger {
	return &loggerWithFields{parent: l, fields: fields}
}

// ---------------------------------------------------------------------------
// mapStorage — in-memory StorageAccessor for tests
// ---------------------------------------------------------------------------

// mapStorage is a thread-safe in-memory implementation of StorageAccessor.
// All mutating operations always return nil (no I/O errors).
type mapStorage struct {
	mu   sync.RWMutex
	data map[string]string
}

// newMapStorage creates an initialized in-memory storage.
func newMapStorage() *mapStorage {
	return &mapStorage{data: make(map[string]string)}
}

func (s *mapStorage) Get(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.data[key]
	return v, ok
}

func (s *mapStorage) Set(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	return nil
}

func (s *mapStorage) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

func (s *mapStorage) Keys() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]string, 0, len(s.data))
	for k := range s.data {
		result = append(result, k)
	}
	return result
}

func (s *mapStorage) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = make(map[string]string)
	return nil
}

// ---------------------------------------------------------------------------
// testKitContext — test-friendly PluginContext implementation
// ---------------------------------------------------------------------------

// testKitContext wraps pluginContextImpl with a reference to the underlying
// mapStorage so TestKit.SetStorage can pre-populate values before activation.
type testKitContext struct {
	*pluginContextImpl
	storage *mapStorage
}

// newTestKitContext creates a fully wired PluginContext for testing.
// Permissions are forced to ["*"] so all plugin operations are allowed.
func newTestKitContext(t *testing.T, manifest PluginManifest) *testKitContext {
	manifest.Permissions = []string{"*"}
	storage := newMapStorage()
	logger := &testLogger{t: t}
	bus := NewPluginEventBus()
	configStore := (*PluginConfigStore)(nil)

	impl := newPluginContext(&manifest, storage, logger, bus, configStore)

	return &testKitContext{
		pluginContextImpl: impl,
		storage:           storage,
	}
}

// ---------------------------------------------------------------------------
// TestKit — main test harness
// ---------------------------------------------------------------------------

// TestKit provides a lightweight test harness for plugin authors.
// It wires up an in-memory PluginContext with a real event bus and storage,
// allowing plugins to be activated, tools called, and hooks/enrichers verified
// entirely within *testing.T — no server or filesystem required.
type TestKit struct {
	t        *testing.T
	plugin   Plugin
	ctx      *testKitContext
	manifest PluginManifest
}

// NewTestKit creates a TestKit for the given plugin.
// The plugin's Manifest() is called to obtain the manifest.
func NewTestKit(t *testing.T, p Plugin) *TestKit {
	t.Helper()
	return &TestKit{
		t:        t,
		plugin:   p,
		manifest: p.Manifest(),
		ctx:      newTestKitContext(t, p.Manifest()),
	}
}

// Activate calls plugin.Activate with the test PluginContext.
func (tk *TestKit) Activate() error {
	tk.t.Helper()
	return tk.plugin.Activate(tk.ctx)
}

// Deactivate calls plugin.Deactivate with the test PluginContext.
func (tk *TestKit) Deactivate() error {
	tk.t.Helper()
	return tk.plugin.Deactivate(tk.ctx)
}

// CallTool looks up a registered tool by name and executes it with the given
// JSON input string. Returns an error if the tool is not found or execution fails.
func (tk *TestKit) CallTool(toolName, input string) (*ToolResult, error) {
	tk.t.Helper()
	for _, tool := range tk.ctx.GetTools() {
		if tool.Definition().Name == toolName {
			if v2, ok := tool.(PluginToolV2); ok {
				return v2.ExecuteWithContext(&ToolCallContext{Ctx: context.Background()}, input)
			}
			return tool.Execute(context.Background(), input)
		}
	}
	return nil, fmt.Errorf("tool %q not found", toolName)
}

// AssertToolRegistered verifies that a tool with the given name has been
// registered. Panics if not found.
func (tk *TestKit) AssertToolRegistered(name string) {
	tk.t.Helper()
	for _, tool := range tk.ctx.GetTools() {
		if tool.Definition().Name == name {
			return
		}
	}
	panic(fmt.Sprintf("tool %q not registered (available: %v)", name, toolNames(tk.ctx.GetTools())))
}

// AssertHookRegistered verifies that a hook for the given event has been
// registered. Panics if not found.
func (tk *TestKit) AssertHookRegistered(event HookEvent) {
	tk.t.Helper()
	for _, h := range tk.ctx.GetHooks() {
		if h.Event == event {
			return
		}
	}
	panic(fmt.Sprintf("hook %q not registered", event))
}

// AssertEnricherRegistered verifies that an enricher with the given name has
// been registered. Calls t.Fatalf if not found.
func (tk *TestKit) AssertEnricherRegistered(name string) {
	tk.t.Helper()
	for _, e := range tk.ctx.GetEnrichers() {
		if e.Name == name {
			return
		}
	}
	tk.t.Fatalf("enricher %q not registered", name)
}

// GetEnricherOutput runs all registered context enrichers and returns their
// concatenated output. Calls t.Fatalf if any enricher returns an error.
func (tk *TestKit) GetEnricherOutput() string {
	tk.t.Helper()
	var sb strings.Builder
	for _, e := range tk.ctx.GetEnrichers() {
		out, err := e.Enricher(context.Background())
		if err != nil {
			tk.t.Fatalf("enricher %q failed: %v", e.Name, err)
		}
		sb.WriteString(out)
	}
	return sb.String()
}

// SetStorage pre-populates a key-value pair in the test storage.
// Call this before Activate to inject test data for the plugin to read.
func (tk *TestKit) SetStorage(key, value string) {
	tk.t.Helper()
	tk.ctx.storage.Set(key, value)
}

// Context returns the underlying PluginContext, allowing direct access
// to all PluginContext methods during tests.
func (tk *TestKit) Context() PluginContext {
	tk.t.Helper()
	return tk.ctx
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// toolNames extracts tool names from a slice of PluginTool.
func toolNames(tools []PluginTool) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Definition().Name
	}
	return names
}
