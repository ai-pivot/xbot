package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// Test Helpers
// ---------------------------------------------------------------------------

func testManifest() PluginManifest {
	return PluginManifest{
		ID:               "com.test.example",
		Name:             "Test Plugin",
		Version:          "1.0.0",
		Description:      "A test plugin",
		Runtime:          RuntimeNative,
		ActivationEvents: []string{"onStart"},
		Permissions:      []string{"tools.register", "hooks.subscribe", "context.enrich", "storage.private"},
	}
}

func testPluginDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return dir
}

func writeTestManifest(t *testing.T, dir string, m *PluginManifest) {
	t.Helper()
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Manifest Tests
// ---------------------------------------------------------------------------

func TestLoadManifest(t *testing.T) {
	dir := testPluginDir(t)
	m := testManifest()
	writeTestManifest(t, dir, &m)

	loaded, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest failed: %v", err)
	}
	if loaded.ID != m.ID {
		t.Errorf("ID mismatch: got %q, want %q", loaded.ID, m.ID)
	}
	if loaded.Name != m.Name {
		t.Errorf("Name mismatch: got %q, want %q", loaded.Name, m.Name)
	}
	if loaded.Runtime != RuntimeNative {
		t.Errorf("Runtime mismatch: got %q, want %q", loaded.Runtime, RuntimeNative)
	}
}

func TestLoadManifestValidation_MissingID(t *testing.T) {
	dir := testPluginDir(t)
	m := testManifest()
	m.ID = ""
	writeTestManifest(t, dir, &m)

	_, err := LoadManifest(dir)
	if err == nil {
		t.Fatal("expected error for missing ID")
	}
}

func TestLoadManifestValidation_InvalidRuntime(t *testing.T) {
	dir := testPluginDir(t)
	m := testManifest()
	m.Runtime = "invalid"
	writeTestManifest(t, dir, &m)

	_, err := LoadManifest(dir)
	if err == nil {
		t.Fatal("expected error for invalid runtime")
	}
}

func TestValidateActivationEvent(t *testing.T) {
	tests := []struct {
		event string
		valid bool
	}{
		{"onStart", true},
		{"onTool:code_review", true},
		{"onTool:", false},
		{"onHook:PreToolUse", true},
		{"onHook:InvalidEvent", false},
		{"onCommand:deploy", true},
		{"onCommand:", false},
		{"invalid", false},
	}
	for _, tt := range tests {
		err := validateActivationEvent(tt.event)
		if (err == nil) != tt.valid {
			t.Errorf("validateActivationEvent(%q): valid=%v, err=%v", tt.event, tt.valid, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Permission Tests
// ---------------------------------------------------------------------------

func TestPermissionChecker(t *testing.T) {
	pc := NewPermissionChecker([]string{"tools.register", "hooks.subscribe"})

	if !pc.Has(PermToolsRegister) {
		t.Error("should have tools.register")
	}
	if !pc.Has(PermHooksSubscribe) {
		t.Error("should have hooks.subscribe")
	}
	if pc.Has(PermStoragePrivate) {
		t.Error("should not have storage.private")
	}
	if !pc.HasAll(PermToolsRegister, PermHooksSubscribe) {
		t.Error("should have both permissions")
	}
	if pc.HasAll(PermToolsRegister, PermStoragePrivate) {
		t.Error("should not have all (missing storage.private)")
	}
	if !pc.HasAny(PermToolsRegister, PermStoragePrivate) {
		t.Error("should have at least one")
	}
}

func TestPermissionChecker_Wildcard(t *testing.T) {
	pc := NewPermissionChecker([]string{"*"})
	if !pc.Has(PermToolsRegister) {
		t.Error("wildcard should grant all permissions")
	}
	if !pc.Has(PermBusWrite) {
		t.Error("wildcard should grant bus.write")
	}
}

func TestIsValidPermission(t *testing.T) {
	for _, perm := range AllPermissions() {
		if !IsValidPermission(perm) {
			t.Errorf("AllPermissions() contains invalid permission %q", perm)
		}
	}
	if IsValidPermission("nonexistent") {
		t.Error("nonexistent permission should be invalid")
	}
}

// ---------------------------------------------------------------------------
// PluginContext Tests
// ---------------------------------------------------------------------------

func TestPluginContext_RegisterTool(t *testing.T) {
	m := testManifest()
	storage := &noopStorage{}
	pc := newPluginContext(&m, storage, newPluginLogger(m.ID), nil)

	tool := &SimplePluginTool{
		Def: ToolDef{
			Name:        "test_tool",
			Description: "A test tool",
		},
		ExecFn: func(ctx context.Context, input string) (*ToolResult, error) {
			return NewToolResult("ok"), nil
		},
	}

	err := pc.RegisterTool(tool)
	if err != nil {
		t.Fatalf("RegisterTool failed: %v", err)
	}
	tools := pc.GetTools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Definition().Name != "test_tool" {
		t.Errorf("tool name mismatch: got %q", tools[0].Definition().Name)
	}
}

func TestPluginContext_RegisterTool_NoPermission(t *testing.T) {
	m := testManifest()
	m.Permissions = []string{"hooks.subscribe"} // no tools.register
	storage := &noopStorage{}
	pc := newPluginContext(&m, storage, newPluginLogger(m.ID), nil)

	tool := &SimplePluginTool{
		Def: ToolDef{Name: "test_tool", Description: "test"},
	}

	err := pc.RegisterTool(tool)
	if err == nil {
		t.Fatal("expected permission error")
	}
	if _, ok := err.(*PermissionError); !ok {
		t.Errorf("expected PermissionError, got %T", err)
	}
}

func TestPluginContext_RegisterHook(t *testing.T) {
	m := testManifest()
	storage := &noopStorage{}
	pc := newPluginContext(&m, storage, newPluginLogger(m.ID), nil)

	called := false
	_ = called
	err := pc.OnPreToolUse("Shell", func(ctx context.Context, payload *HookPayload) (*HookResult, error) {
		called = true
		return &HookResult{Decision: DecisionAllow}, nil
	})
	if err != nil {
		t.Fatalf("OnPreToolUse failed: %v", err)
	}

	hooks := pc.GetHooks()
	if len(hooks) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(hooks))
	}
	if hooks[0].Event != HookPreToolUse {
		t.Errorf("hook event mismatch: got %q", hooks[0].Event)
	}
	if hooks[0].Matcher != "Shell" {
		t.Errorf("hook matcher mismatch: got %q", hooks[0].Matcher)
	}
}

func TestPluginContext_EnrichContext(t *testing.T) {
	m := testManifest()
	storage := &noopStorage{}
	pc := newPluginContext(&m, storage, newPluginLogger(m.ID), nil)

	err := pc.EnrichContext("test_enricher", func(ctx context.Context) (string, error) {
		return "enriched content", nil
	})
	if err != nil {
		t.Fatalf("EnrichContext failed: %v", err)
	}

	enrichers := pc.GetEnrichers()
	if len(enrichers) != 1 {
		t.Fatalf("expected 1 enricher, got %d", len(enrichers))
	}
	if enrichers[0].Name != "test_enricher" {
		t.Errorf("enricher name mismatch: got %q", enrichers[0].Name)
	}
}

// ---------------------------------------------------------------------------
// Storage Tests
// ---------------------------------------------------------------------------

func TestFileStorage(t *testing.T) {
	dir := t.TempDir()
	storage, err := NewFileStorage(dir)
	if err != nil {
		t.Fatalf("NewFileStorage failed: %v", err)
	}

	// Set and Get
	if err := storage.Set("key1", "value1"); err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	v, ok := storage.Get("key1")
	if !ok || v != "value1" {
		t.Errorf("Get key1: got %q, ok=%v, want %q, ok=true", v, ok, "value1")
	}

	// Keys
	keys := storage.Keys()
	if len(keys) != 1 || keys[0] != "key1" {
		t.Errorf("Keys: got %v, want [key1]", keys)
	}

	// Delete
	if err := storage.Delete("key1"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	_, ok = storage.Get("key1")
	if ok {
		t.Error("key1 should be deleted")
	}

	// Clear
	storage.Set("a", "1")
	storage.Set("b", "2")
	if err := storage.Clear(); err != nil {
		t.Fatalf("Clear failed: %v", err)
	}
	if len(storage.Keys()) != 0 {
		t.Error("storage should be empty after Clear")
	}
}

func TestFileStorage_Persistence(t *testing.T) {
	dir := t.TempDir()

	// Create and write
	storage1, err := NewFileStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	storage1.Set("persist", "me")

	// Reopen and verify
	storage2, err := NewFileStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := storage2.Get("persist")
	if !ok || v != "me" {
		t.Errorf("persisted data: got %q, ok=%v", v, ok)
	}
}

// ---------------------------------------------------------------------------
// PluginManager Tests
// ---------------------------------------------------------------------------

type mockPlugin struct {
	manifest    PluginManifest
	activated   bool
	deactivated bool
	activateErr error
}

func (m *mockPlugin) Manifest() PluginManifest { return m.manifest }
func (m *mockPlugin) Activate(ctx PluginContext) error {
	m.activated = true
	if m.activateErr != nil {
		return m.activateErr
	}
	// Register a test tool
	tool := &SimplePluginTool{
		Def: ToolDef{Name: "mock_tool", Description: "Mock tool"},
		ExecFn: func(ctx context.Context, input string) (*ToolResult, error) {
			return NewToolResult("mock result"), nil
		},
	}
	_ = ctx.RegisterTool(tool)
	return nil
}
func (m *mockPlugin) Deactivate(ctx PluginContext) error {
	m.deactivated = true
	return nil
}

// panicPlugin is a test plugin that panics during Activate.
type panicPlugin struct {
	manifest PluginManifest
}

func (p *panicPlugin) Manifest() PluginManifest { return p.manifest }
func (p *panicPlugin) Activate(ctx PluginContext) error {
	panic("boom!")
}
func (p *panicPlugin) Deactivate(ctx PluginContext) error { return nil }

// mockRuntimeFactory is a test RuntimeFactory that creates mockPlugin instances.
type mockRuntimeFactory struct{}

func (f *mockRuntimeFactory) Create(manifest *PluginManifest, dir string) (Plugin, error) {
	return &mockPlugin{manifest: *manifest}, nil
}

func TestPluginManager_Register(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	p := &mockPlugin{manifest: testManifest()}

	err := pm.Register(p)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	entry, ok := pm.GetPlugin("com.test.example")
	if !ok {
		t.Fatal("plugin not found after registration")
	}
	if entry.State != StateDiscovered {
		t.Errorf("expected StateDiscovered, got %q", entry.State)
	}
}

func TestPluginManager_Activate(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	p := &mockPlugin{manifest: testManifest()}
	pm.Register(p)

	ctx := context.Background()
	err := pm.ActivateAll(ctx)
	if err != nil {
		t.Fatalf("ActivateAll failed: %v", err)
	}

	if !p.activated {
		t.Error("plugin should be activated")
	}

	entry, _ := pm.GetPlugin("com.test.example")
	if entry.State != StateActive {
		t.Errorf("expected StateActive, got %q", entry.State)
	}

	if pm.ActiveCount() != 1 {
		t.Errorf("expected 1 active plugin, got %d", pm.ActiveCount())
	}
}

func TestPluginManager_DeactivateAll(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	p := &mockPlugin{manifest: testManifest()}
	pm.Register(p)

	ctx := context.Background()
	pm.ActivateAll(ctx)
	pm.DeactivateAll(ctx)

	if !p.deactivated {
		t.Error("plugin should be deactivated")
	}

	if pm.ActiveCount() != 0 {
		t.Errorf("expected 0 active plugins, got %d", pm.ActiveCount())
	}
}

func TestPluginManager_DuplicateRegistration(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	p1 := &mockPlugin{manifest: testManifest()}
	p2 := &mockPlugin{manifest: testManifest()}

	pm.Register(p1)
	err := pm.Register(p2)
	if err == nil {
		t.Fatal("expected error for duplicate registration")
	}
}

func TestPluginManager_ActivateForEvent(t *testing.T) {
	m := testManifest()
	m.ActivationEvents = []string{"onTool:code_review"}

	pm := NewPluginManager(t.TempDir())
	p := &mockPlugin{manifest: m}
	pm.Register(p)

	ctx := context.Background()

	// Should not activate for unrelated event
	pm.ActivateForEvent(ctx, "onTool:other")
	if p.activated {
		t.Error("should not activate for unrelated event")
	}

	// Should activate for matching event
	pm.ActivateForEvent(ctx, "onTool:code_review")
	if !p.activated {
		t.Error("should activate for matching event")
	}
}

// ---------------------------------------------------------------------------
// Hook Bridge Tests
// ---------------------------------------------------------------------------

func TestHookBridge_Dispatch(t *testing.T) {
	bridge := NewPluginHookBridge()

	// Register a hook that denies Shell commands
	bridge.Register("test-plugin", HookPreToolUse, "Shell", func(ctx context.Context, payload *HookPayload) (*HookResult, error) {
		return &HookResult{Decision: DecisionDeny, Message: "blocked"}, nil
	})

	// Register a hook that allows everything
	bridge.Register("test-plugin2", HookPreToolUse, "", func(ctx context.Context, payload *HookPayload) (*HookResult, error) {
		return &HookResult{Decision: DecisionAllow}, nil
	})

	ctx := context.Background()

	// Test deny takes priority
	result := bridge.Dispatch(ctx, &HookPayload{
		Event:    HookPreToolUse,
		ToolName: "Shell",
	})
	if result.Decision != DecisionDeny {
		t.Errorf("expected Deny, got %q", result.Decision)
	}

	// Test no match falls through to allow
	result2 := bridge.Dispatch(ctx, &HookPayload{
		Event:    HookPreToolUse,
		ToolName: "Read",
	})
	if result2.Decision != DecisionAllow {
		t.Errorf("expected Allow for non-matching tool, got %q", result2.Decision)
	}
}

// ---------------------------------------------------------------------------
// Enricher Registry Tests
// ---------------------------------------------------------------------------

func TestEnricherRegistry(t *testing.T) {
	reg := NewEnricherRegistry()

	reg.Register("plugin1", "status", func(ctx context.Context) (string, error) {
		return "All systems green", nil
	}, 100)

	reg.Register("plugin2", "metrics", func(ctx context.Context) (string, error) {
		return "CPU: 50%", nil
	}, 50)

	ctx := context.Background()
	content := reg.RunAll(ctx)

	if reg.Count() != 2 {
		t.Errorf("expected 2 enrichers, got %d", reg.Count())
	}
	// Metrics (priority 50) should come before status (priority 100)
	if len(content) == 0 {
		t.Error("expected non-empty content")
	}
}

// ---------------------------------------------------------------------------
// PluginToolAdapter Tests
// ---------------------------------------------------------------------------

func TestPluginToolAdapter(t *testing.T) {
	tool := &SimplePluginTool{
		Def: ToolDef{
			Name:        "test_tool",
			Description: "Test description",
		},
		ExecFn: func(ctx context.Context, input string) (*ToolResult, error) {
			return NewToolResult("test output"), nil
		},
	}

	adapter := NewPluginToolAdapter("test-plugin", tool)

	if adapter.Name() != "test_tool" {
		t.Errorf("Name: got %q", adapter.Name())
	}
	if adapter.PluginID() != "test-plugin" {
		t.Errorf("PluginID: got %q", adapter.PluginID())
	}
}

// ---------------------------------------------------------------------------
// Discovery Tests
// ---------------------------------------------------------------------------

func TestDiscoverPlugins(t *testing.T) {
	baseDir := t.TempDir()
	pluginsDir := filepath.Join(baseDir, "plugins")
	os.MkdirAll(pluginsDir, 0755)

	// Create a valid plugin
	pluginDir := filepath.Join(pluginsDir, "test-plugin")
	os.MkdirAll(pluginDir, 0755)
	writeTestManifest(t, pluginDir, &PluginManifest{
		ID:          "com.test.plugin",
		Name:        "Test",
		Version:     "1.0.0",
		Description: "Test",
		Runtime:     RuntimeNative,
	})

	// Create an invalid plugin (missing manifest)
	invalidDir := filepath.Join(pluginsDir, "invalid-plugin")
	os.MkdirAll(invalidDir, 0755)

	manifests := DiscoverPlugins([]string{pluginsDir})
	if len(manifests) != 1 {
		t.Fatalf("expected 1 valid plugin, got %d", len(manifests))
	}
	if manifests[0].ID != "com.test.plugin" {
		t.Errorf("ID mismatch: got %q", manifests[0].ID)
	}
}

func TestDiscoverPlugins_EmptyDir(t *testing.T) {
	manifests := DiscoverPlugins([]string{"/nonexistent"})
	if len(manifests) != 0 {
		t.Errorf("expected 0 plugins from nonexistent dir, got %d", len(manifests))
	}
}

// ---------------------------------------------------------------------------
// matchToolName Tests
// ---------------------------------------------------------------------------

func TestMatchToolName(t *testing.T) {
	tests := []struct {
		pattern string
		name    string
		want    bool
	}{
		// Exact match
		{"Shell", "Shell", true},
		{"Shell", "Read", false},
		// Wildcard all
		{"*", "Shell", true},
		{"*", "", true},
		// Empty pattern matches all
		{"", "Shell", true},
		// Prefix wildcard
		{"Shell*", "Shell", true},
		{"Shell*", "ShellExec", true},
		{"Shell*", "BashShell", false},
		// Suffix wildcard
		{"*Shell", "Shell", true},
		{"*Shell", "BashShell", true},
		{"*Shell", "ShellExec", false},
		// Contains wildcard
		{"*ell*", "Shell", true},
		{"*ell*", "Hello", true},
		{"*ell*", "Bash", false},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.name, func(t *testing.T) {
			got := matchToolName(tt.pattern, tt.name)
			if got != tt.want {
				t.Errorf("matchToolName(%q, %q) = %v, want %v", tt.pattern, tt.name, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ParseToolInputString Tests
// ---------------------------------------------------------------------------

func TestParseToolInputString(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		field   string
		want    string
		wantErr bool
	}{
		{
			name:  "simple field",
			input: `{"name": "Alice"}`,
			field: "name",
			want:  "Alice",
		},
		{
			name:    "field not found",
			input:   `{"name": "Alice"}`,
			field:   "age",
			wantErr: true,
		},
		{
			name:    "not a string",
			input:   `{"count": 42}`,
			field:   "count",
			wantErr: true,
		},
		{
			name:  "escaped quotes in value",
			input: `{"cmd": "echo \"hello\""}`,
			field: "cmd",
			want:  `echo "hello"`,
		},
		{
			name:  "multiple fields",
			input: `{"name": "Bob", "age": 30}`,
			field: "name",
			want:  "Bob",
		},
		{
			name:    "invalid JSON",
			input:   `{invalid}`,
			field:   "name",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseToolInputString(tt.input, tt.field)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseToolInputString() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ParseToolInputString() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// PluginToolAdapter Execute Tests
// ---------------------------------------------------------------------------

func TestPluginToolAdapter_Execute(t *testing.T) {
	tool := &SimplePluginTool{
		Def: ToolDef{
			Name:        "test_exec",
			Description: "Execute test tool",
		},
		ExecFn: func(ctx context.Context, input string) (*ToolResult, error) {
			return NewToolResult("executed: " + input), nil
		},
	}

	adapter := NewPluginToolAdapter("test-plugin", tool)

	// Test Description
	desc := adapter.Description()
	if !strContains(desc, "test-plugin") || !strContains(desc, "Execute test tool") {
		t.Errorf("Description() = %q, should contain plugin attribution", desc)
	}

	// Test Execute
	ctx := context.Background()
	result, err := adapter.Execute(ctx, `{"input": "hello"}`)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Content != "executed: {\"input\": \"hello\"}" {
		t.Errorf("Execute() Content = %q", result.Content)
	}
	if result.IsError {
		t.Error("Execute() should not be error")
	}
}

func TestPluginToolAdapter_ErrorResult(t *testing.T) {
	tool := &SimplePluginTool{
		Def: ToolDef{Name: "err_tool", Description: "error tool"},
		ExecFn: func(ctx context.Context, input string) (*ToolResult, error) {
			return NewToolError("something went wrong"), nil
		},
	}

	adapter := NewPluginToolAdapter("test-plugin", tool)
	ctx := context.Background()
	result, err := adapter.Execute(ctx, "")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.IsError {
		t.Error("result should be an error")
	}
	if result.Content != "something went wrong" {
		t.Errorf("Content = %q", result.Content)
	}
}

func strContains(s, substr string) bool {
	return len(s) >= len(substr) && strSearch(s, substr)
}

func strSearch(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// EnricherRegistry Error Handling Tests
// ---------------------------------------------------------------------------

func TestEnricherRegistry_ErrorHandling(t *testing.T) {
	reg := NewEnricherRegistry()

	reg.Register("plugin1", "failing", func(ctx context.Context) (string, error) {
		return "", fmt.Errorf("enricher failed")
	}, 10)

	reg.Register("plugin2", "working", func(ctx context.Context) (string, error) {
		return "I work fine", nil
	}, 20)

	ctx := context.Background()
	content := reg.RunAll(ctx)

	// Should contain working enricher output but skip failing one
	if !strContains(content, "I work fine") {
		t.Error("RunAll should include output from working enricher")
	}
	if strContains(content, "enricher failed") {
		t.Error("RunAll should not include error messages in output")
	}
}

// ---------------------------------------------------------------------------
// Manifest Validation Executable Tests
// ---------------------------------------------------------------------------

func TestLoadManifest_GRPCExecutable(t *testing.T) {
	dir := testPluginDir(t)
	m := testManifest()
	m.Runtime = RuntimeGRPC
	m.Executable = "/usr/bin/my-plugin"
	m.Args = []string{"--port", "5000"}
	m.Entry = "" // Use Executable instead of Entry
	writeTestManifest(t, dir, &m)

	loaded, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest with Executable: %v", err)
	}
	if loaded.Executable != "/usr/bin/my-plugin" {
		t.Errorf("Executable = %q, want /usr/bin/my-plugin", loaded.Executable)
	}
	if len(loaded.Args) != 2 || loaded.Args[0] != "--port" {
		t.Errorf("Args = %v, want [--port 5000]", loaded.Args)
	}
}

func TestLoadManifest_GRPCNoEntryOrExecutable(t *testing.T) {
	dir := testPluginDir(t)
	m := testManifest()
	m.Runtime = RuntimeGRPC
	m.Entry = ""
	m.Executable = ""
	writeTestManifest(t, dir, &m)

	_, err := LoadManifest(dir)
	if err == nil {
		t.Fatal("expected error for grpc without entry or executable")
	}
}

// ---------------------------------------------------------------------------
// Phase 4 — Additional Coverage Tests
// ---------------------------------------------------------------------------

func TestPluginManager_DisabledPlugin(t *testing.T) {
	// Create a plugin directory
	baseDir := t.TempDir()
	pluginsDir := filepath.Join(baseDir, "plugins")
	os.MkdirAll(pluginsDir, 0755)

	pluginDir := filepath.Join(pluginsDir, "disabled-plugin")
	os.MkdirAll(pluginDir, 0755)
	writeTestManifest(t, pluginDir, &PluginManifest{
		ID:          "com.test.disabled",
		Name:        "Disabled Plugin",
		Version:     "1.0.0",
		Description: "Should be skipped",
		Runtime:     RuntimeNative,
	})

	pm := NewPluginManager(baseDir)
	pm.DisablePlugins([]string{"com.test.disabled"})

	ctx := context.Background()
	count, err := pm.Discover(ctx)
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 discovered plugins (disabled), got %d", count)
	}

	_, found := pm.GetPlugin("com.test.disabled")
	if found {
		t.Error("disabled plugin should not be found")
	}
}

func TestPluginManager_RegisterAndActivate(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	p := &mockPlugin{manifest: testManifest()}

	ctx := context.Background()
	err := pm.RegisterAndActivate(ctx, p)
	if err != nil {
		t.Fatalf("RegisterAndActivate failed: %v", err)
	}

	if !p.activated {
		t.Error("plugin should be activated")
	}
	if !pm.IsPluginActive("com.test.example") {
		t.Error("IsPluginActive should return true")
	}
	if pm.IsPluginActive("nonexistent") {
		t.Error("IsPluginActive should return false for unknown plugin")
	}
}

func TestPluginManager_PanicRecovery(t *testing.T) {
	pm := NewPluginManager(t.TempDir())

	// The activate method already has panic recovery, but let's test with
	// an actual panicking plugin
	panicPluginReal := &panicPlugin{manifest: testManifest()}

	ctx := context.Background()
	err := pm.RegisterAndActivate(ctx, panicPluginReal)
	if err == nil {
		t.Fatal("expected error from panicking plugin")
	}

	// Manager should still be functional
	if pm.IsPluginActive("com.test.example") {
		t.Error("panicking plugin should not be active")
	}

	// Manager state should be consistent
	if pm.ActiveCount() != 0 {
		t.Error("no plugins should be active after panic")
	}
}

func TestPluginManager_ConcurrentActivation(t *testing.T) {
	pm := NewPluginManager(t.TempDir())

	var wg sync.WaitGroup
	const n = 10
	errors := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			m := testManifest()
			m.ID = fmt.Sprintf("com.test.plugin-%d", idx)
			p := &mockPlugin{manifest: m}
			if err := pm.RegisterAndActivate(context.Background(), p); err != nil {
				errors <- err
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("concurrent activation error: %v", err)
	}

	if pm.ActiveCount() != n {
		t.Errorf("expected %d active plugins, got %d", n, pm.ActiveCount())
	}
}

func TestEnricherRegistry_Empty(t *testing.T) {
	reg := NewEnricherRegistry()

	if reg.Count() != 0 {
		t.Errorf("empty registry should have count 0, got %d", reg.Count())
	}

	ctx := context.Background()
	content := reg.RunAll(ctx)
	if content != "" {
		t.Errorf("empty registry should produce empty content, got %q", content)
	}

	list := reg.List()
	if len(list) != 0 {
		t.Errorf("empty registry list should be empty, got %v", list)
	}
}

func TestPluginBridge_NoHandlers(t *testing.T) {
	bridge := NewPluginHookBridge()

	ctx := context.Background()
	result := bridge.Dispatch(ctx, &HookPayload{
		Event:    HookPreToolUse,
		ToolName: "Shell",
	})

	if result.Decision != DecisionDefer {
		t.Errorf("no handlers should return Defer, got %q", result.Decision)
	}
}

func TestPermissionChecker_EmptyPermissions(t *testing.T) {
	pc := NewPermissionChecker(nil)

	if pc.Has(PermToolsRegister) {
		t.Error("empty permissions should deny all")
	}
	if pc.Has(PermHooksSubscribe) {
		t.Error("empty permissions should deny all")
	}
	if pc.HasAll(PermToolsRegister) {
		t.Error("empty permissions should deny HasAll")
	}
	if pc.HasAny(PermToolsRegister) {
		t.Error("empty permissions should deny HasAny")
	}

	// Also test with empty slice (not nil)
	pc2 := NewPermissionChecker([]string{})
	if pc2.Has(PermToolsRegister) {
		t.Error("empty slice permissions should deny all")
	}
}

func TestManifest_IDValidation(t *testing.T) {
	tests := []struct {
		id     string
		wantOK bool
	}{
		// Valid IDs
		{"com.example.plugin", true},
		{"my-plugin", true},
		{"plugin_v1", true},
		{"A", true},
		{"a123", true},
		// Invalid IDs
		{"", false},                       // empty
		{".plugin", false},                // starts with dot
		{"-plugin", false},                // starts with hyphen
		{"_plugin", false},                // starts with underscore
		{"plugin with space", false},      // contains space
		{"plugin/slash", false},           // contains slash
		{"plugin\\backslash", false},      // contains backslash
		{strings.Repeat("a", 129), false}, // too long (129 chars)
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			dir := t.TempDir()
			m := testManifest()
			m.ID = tt.id
			writeTestManifest(t, dir, &m)

			_, err := LoadManifest(dir)
			if (err == nil) != tt.wantOK {
				t.Errorf("LoadManifest(%q): ok=%v, want ok=%v, err=%v", tt.id, err == nil, tt.wantOK, err)
			}
		})
	}
}

func TestPluginContext_SetSessionMetadata(t *testing.T) {
	m := testManifest()
	storage := &noopStorage{}
	pc := newPluginContext(&m, storage, newPluginLogger(m.ID), nil)

	// Before setting, metadata should be empty
	if pc.WorkingDir() != "" {
		t.Errorf("WorkingDir should be empty, got %q", pc.WorkingDir())
	}
	if pc.Channel() != "" {
		t.Errorf("Channel should be empty, got %q", pc.Channel())
	}
	if pc.ChatID() != "" {
		t.Errorf("ChatID should be empty, got %q", pc.ChatID())
	}

	// Set metadata
	pc.SetSessionMetadata("/home/user/project", "cli", "chat-123")

	if pc.WorkingDir() != "/home/user/project" {
		t.Errorf("WorkingDir: got %q, want %q", pc.WorkingDir(), "/home/user/project")
	}
	if pc.Channel() != "cli" {
		t.Errorf("Channel: got %q, want %q", pc.Channel(), "cli")
	}
	if pc.ChatID() != "chat-123" {
		t.Errorf("ChatID: got %q, want %q", pc.ChatID(), "chat-123")
	}
}

// ---------------------------------------------------------------------------
// Phase 5 — PluginToolV2 / ToolCallContext Tests
// ---------------------------------------------------------------------------

func TestSimplePluginTool_ExecuteWithContext_V2(t *testing.T) {
	tool := &SimplePluginTool{
		Def: ToolDef{Name: "v2_tool", Description: "V2 test tool"},
		ExecV2Fn: func(ctx *ToolCallContext, input string) (*ToolResult, error) {
			return NewToolResult("session=" + ctx.SessionID + " channel=" + ctx.Channel + " user=" + ctx.UserID), nil
		},
	}

	// V2 should be used via ExecuteWithContext
	tcc := &ToolCallContext{
		SessionID: "sess-123",
		Channel:   "cli",
		UserID:    "user-456",
		Ctx:       context.Background(),
	}
	result, err := tool.ExecuteWithContext(tcc, `{"q": "test"}`)
	if err != nil {
		t.Fatalf("ExecuteWithContext error: %v", err)
	}
	if result.Content != "session=sess-123 channel=cli user=user-456" {
		t.Errorf("V2 result = %q", result.Content)
	}
}

func TestSimplePluginTool_ExecuteWithContext_Fallback(t *testing.T) {
	tool := &SimplePluginTool{
		Def: ToolDef{Name: "fallback_tool", Description: "fallback test"},
		ExecFn: func(ctx context.Context, input string) (*ToolResult, error) {
			return NewToolResult("v1-fallback"), nil
		},
	}

	// No ExecV2Fn set, should fallback to ExecFn
	tcc := &ToolCallContext{Ctx: context.Background()}
	result, err := tool.ExecuteWithContext(tcc, "")
	if err != nil {
		t.Fatalf("ExecuteWithContext fallback error: %v", err)
	}
	if result.Content != "v1-fallback" {
		t.Errorf("fallback result = %q", result.Content)
	}
}

func TestSimplePluginTool_ExecuteWithContext_NoFunc(t *testing.T) {
	tool := &SimplePluginTool{
		Def: ToolDef{Name: "nofunc_tool", Description: "no func"},
	}

	tcc := &ToolCallContext{Ctx: context.Background()}
	result, err := tool.ExecuteWithContext(tcc, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result when no function set")
	}
}

func TestPluginToolAdapter_V2Detection(t *testing.T) {
	// Create a V2 tool
	tool := &SimplePluginTool{
		Def: ToolDef{Name: "adapter_v2", Description: "adapter v2 test"},
		ExecV2Fn: func(ctx *ToolCallContext, input string) (*ToolResult, error) {
			return NewToolResult("v2-called:" + ctx.SessionID), nil
		},
	}

	adapter := NewPluginToolAdapter("test-plugin", tool)

	// Execute via V1 interface — adapter should detect V2 and use it
	result, err := adapter.Execute(context.Background(), `{"x": 1}`)
	if err != nil {
		t.Fatalf("adapter.Execute error: %v", err)
	}
	if result.Content != "v2-called:" {
		// SessionID is empty since we called via V1 (no ToolCallContext fields)
		t.Errorf("adapter V2 detection result = %q", result.Content)
	}

	// Execute via V2 interface directly
	tcc := &ToolCallContext{SessionID: "sess-abc", Ctx: context.Background()}
	result2, err := adapter.ExecuteWithContext(tcc, "")
	if err != nil {
		t.Fatalf("adapter.ExecuteWithContext error: %v", err)
	}
	if result2.Content != "v2-called:sess-abc" {
		t.Errorf("adapter V2 direct result = %q", result2.Content)
	}
}

func TestPluginToolAdapter_V1Fallback(t *testing.T) {
	// V1-only tool (no ExecV2Fn)
	tool := &SimplePluginTool{
		Def: ToolDef{Name: "v1_only", Description: "V1 only"},
		ExecFn: func(ctx context.Context, input string) (*ToolResult, error) {
			return NewToolResult("v1-ok"), nil
		},
	}

	adapter := NewPluginToolAdapter("test-plugin", tool)

	// V1 call
	result, err := adapter.Execute(context.Background(), "")
	if err != nil {
		t.Fatalf("adapter V1 error: %v", err)
	}
	if result.Content != "v1-ok" {
		t.Errorf("adapter V1 result = %q", result.Content)
	}

	// V2 call with fallback
	tcc := &ToolCallContext{SessionID: "test", Ctx: context.Background()}
	result2, err := adapter.ExecuteWithContext(tcc, "")
	if err != nil {
		t.Fatalf("adapter V2 fallback error: %v", err)
	}
	if result2.Content != "v1-ok" {
		t.Errorf("adapter V2 fallback result = %q", result2.Content)
	}
}

func TestPluginToolV2_InterfaceAssertion(t *testing.T) {
	v2Tool := &SimplePluginTool{
		Def: ToolDef{Name: "interface_check", Description: "check"},
		ExecV2Fn: func(ctx *ToolCallContext, input string) (*ToolResult, error) {
			return NewToolResult("v2"), nil
		},
	}

	// SimplePluginTool with ExecV2Fn should implement PluginToolV2
	var _ PluginToolV2 = v2Tool

	// V1-only should NOT implement PluginToolV2 at the interface level...
	// Actually SimplePluginTool always implements ExecuteWithContext, so
	// it always satisfies PluginToolV2. But let's verify behavior:
	v1Tool := &SimplePluginTool{
		Def: ToolDef{Name: "v1_check", Description: "check"},
		ExecFn: func(ctx context.Context, input string) (*ToolResult, error) {
			return NewToolResult("v1"), nil
		},
	}

	// Both should work with PluginToolV2 interface
	var iface PluginToolV2 = v1Tool
	result, err := iface.ExecuteWithContext(&ToolCallContext{Ctx: context.Background()}, "")
	if err != nil {
		t.Fatalf("v1 as V2 interface error: %v", err)
	}
	if result.Content != "v1" {
		t.Errorf("v1 as V2 result = %q", result.Content)
	}
}

// ---------------------------------------------------------------------------
// Phase 5 — Health Check Tests
// ---------------------------------------------------------------------------

// healthyPlugin implements Plugin + HealthChecker
type healthyPlugin struct {
	manifest PluginManifest
}

func (h *healthyPlugin) Manifest() PluginManifest              { return h.manifest }
func (h *healthyPlugin) Activate(ctx PluginContext) error      { return nil }
func (h *healthyPlugin) Deactivate(ctx PluginContext) error    { return nil }
func (h *healthyPlugin) HealthCheck(ctx context.Context) error { return nil }

// sickPlugin implements Plugin + HealthChecker (always fails)
type sickPlugin struct {
	manifest PluginManifest
}

func (s *sickPlugin) Manifest() PluginManifest           { return s.manifest }
func (s *sickPlugin) Activate(ctx PluginContext) error   { return nil }
func (s *sickPlugin) Deactivate(ctx PluginContext) error { return nil }
func (s *sickPlugin) HealthCheck(ctx context.Context) error {
	return fmt.Errorf("database connection lost")
}

func TestPluginManager_HealthCheck_Healthy(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	p := &healthyPlugin{manifest: testManifest()}
	pm.RegisterAndActivate(context.Background(), p)

	results := pm.HealthCheck(context.Background())
	if len(results) != 1 {
		t.Fatalf("expected 1 health result, got %d", len(results))
	}
	if results["com.test.example"] != nil {
		t.Errorf("expected healthy (nil error), got %v", results["com.test.example"])
	}
}

func TestPluginManager_HealthCheck_Sick(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	m := testManifest()
	m.ID = "com.test.sick"
	p := &sickPlugin{manifest: m}
	pm.RegisterAndActivate(context.Background(), p)

	results := pm.HealthCheck(context.Background())
	if results["com.test.sick"] == nil {
		t.Error("expected error from sick plugin")
	}
	if results["com.test.sick"].Error() != "database connection lost" {
		t.Errorf("sick error = %q", results["com.test.sick"].Error())
	}
}

func TestPluginManager_HealthCheck_NoHealthChecker(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	p := &mockPlugin{manifest: testManifest()}
	pm.RegisterAndActivate(context.Background(), p)

	results := pm.HealthCheck(context.Background())
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	// mockPlugin doesn't implement HealthChecker, should be nil (healthy)
	if results["com.test.example"] != nil {
		t.Errorf("expected nil for plugin without HealthChecker, got %v", results["com.test.example"])
	}
}

func TestPluginManager_HealthCheck_Mixed(t *testing.T) {
	pm := NewPluginManager(t.TempDir())

	// Healthy
	h := &healthyPlugin{manifest: testManifest()}
	pm.RegisterAndActivate(context.Background(), h)

	// Sick
	m := testManifest()
	m.ID = "com.test.sick"
	s := &sickPlugin{manifest: m}
	pm.RegisterAndActivate(context.Background(), s)

	// No health checker
	m2 := testManifest()
	m2.ID = "com.test.plain"
	mp := &mockPlugin{manifest: m2}
	pm.RegisterAndActivate(context.Background(), mp)

	results := pm.HealthCheck(context.Background())
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if results["com.test.example"] != nil {
		t.Error("healthy plugin should be nil")
	}
	if results["com.test.sick"] == nil {
		t.Error("sick plugin should have error")
	}
	if results["com.test.plain"] != nil {
		t.Error("plain plugin should be nil (assumed healthy)")
	}
}

func TestPluginManager_HealthCheck_InactivePlugin(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	p := &healthyPlugin{manifest: testManifest()}
	// Register but don't activate
	pm.Register(p)

	results := pm.HealthCheck(context.Background())
	if len(results) != 0 {
		t.Errorf("inactive plugins should not be health-checked, got %d results", len(results))
	}
}

// ---------------------------------------------------------------------------
// Phase 5 — Metrics Tests
// ---------------------------------------------------------------------------

func TestPluginManager_Metrics_Empty(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	m := pm.Metrics()
	if m.TotalPlugins != 0 {
		t.Errorf("TotalPlugins = %d, want 0", m.TotalPlugins)
	}
	if m.ActivePlugins != 0 {
		t.Errorf("ActivePlugins = %d, want 0", m.ActivePlugins)
	}
}

func TestPluginManager_Metrics_Active(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	p := &mockPlugin{manifest: testManifest()}
	pm.RegisterAndActivate(context.Background(), p)

	m := pm.Metrics()
	if m.TotalPlugins != 1 {
		t.Errorf("TotalPlugins = %d, want 1", m.TotalPlugins)
	}
	if m.ActivePlugins != 1 {
		t.Errorf("ActivePlugins = %d, want 1", m.ActivePlugins)
	}
	// mockPlugin registers 1 tool
	if m.TotalTools != 1 {
		t.Errorf("TotalTools = %d, want 1", m.TotalTools)
	}
}

func TestPluginManager_Metrics_MultiplePlugins(t *testing.T) {
	pm := NewPluginManager(t.TempDir())

	// Register 3 plugins with different capabilities
	m1 := testManifest()
	m1.ID = "com.test.plugin1"
	p1 := &mockPlugin{manifest: m1}
	pm.RegisterAndActivate(context.Background(), p1)

	m2 := testManifest()
	m2.ID = "com.test.plugin2"
	p2 := &mockPlugin{manifest: m2}
	pm.RegisterAndActivate(context.Background(), p2)

	// Register but don't activate
	m3 := testManifest()
	m3.ID = "com.test.plugin3"
	p3 := &mockPlugin{manifest: m3}
	pm.Register(p3)

	metrics := pm.Metrics()
	if metrics.TotalPlugins != 3 {
		t.Errorf("TotalPlugins = %d, want 3", metrics.TotalPlugins)
	}
	if metrics.ActivePlugins != 2 {
		t.Errorf("ActivePlugins = %d, want 2", metrics.ActivePlugins)
	}
	// Each mockPlugin registers 1 tool
	if metrics.TotalTools != 2 {
		t.Errorf("TotalTools = %d, want 2", metrics.TotalTools)
	}
}

func TestPluginManager_Metrics_WithHooks(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	m := testManifest()
	m.Permissions = []string{"tools.register", "hooks.subscribe", "context.enrich", "storage.private"}

	// Create a plugin that registers tools, hooks, and enrichers
	p := &richMockPlugin{manifest: m}
	pm.RegisterAndActivate(context.Background(), p)

	metrics := pm.Metrics()
	if metrics.TotalTools != 1 {
		t.Errorf("TotalTools = %d, want 1", metrics.TotalTools)
	}
	if metrics.TotalHooks != 2 {
		t.Errorf("TotalHooks = %d, want 2", metrics.TotalHooks)
	}
	if metrics.TotalEnrichers != 1 {
		t.Errorf("TotalEnrichers = %d, want 1", metrics.TotalEnrichers)
	}
}

// richMockPlugin registers tools, hooks, and enrichers
type richMockPlugin struct {
	manifest PluginManifest
}

func (r *richMockPlugin) Manifest() PluginManifest { return r.manifest }
func (r *richMockPlugin) Activate(ctx PluginContext) error {
	ctx.RegisterTool(&SimplePluginTool{
		Def: ToolDef{Name: "rich_tool", Description: "Rich tool"},
		ExecFn: func(ctx context.Context, input string) (*ToolResult, error) {
			return NewToolResult("rich"), nil
		},
	})
	ctx.OnPreToolUse("Shell", func(ctx context.Context, payload *HookPayload) (*HookResult, error) {
		return &HookResult{Decision: DecisionAllow}, nil
	})
	ctx.OnPostToolUse("", func(ctx context.Context, payload *HookPayload) (*HookResult, error) {
		return &HookResult{Decision: DecisionAllow}, nil
	})
	ctx.EnrichContext("test_enricher", func(ctx context.Context) (string, error) {
		return "enriched", nil
	})
	return nil
}
func (r *richMockPlugin) Deactivate(ctx PluginContext) error { return nil }

// ---------------------------------------------------------------------------
// Phase 5 — ToolCallContext Fields Test
// ---------------------------------------------------------------------------

func TestToolCallContext_AllFields(t *testing.T) {
	bg := context.Background()
	tcc := &ToolCallContext{
		SessionID: "sess-001",
		Channel:   "feishu",
		ChatID:    "chat-002",
		UserID:    "user-003",
		Ctx:       bg,
	}

	if tcc.SessionID != "sess-001" {
		t.Errorf("SessionID = %q", tcc.SessionID)
	}
	if tcc.Channel != "feishu" {
		t.Errorf("Channel = %q", tcc.Channel)
	}
	if tcc.ChatID != "chat-002" {
		t.Errorf("ChatID = %q", tcc.ChatID)
	}
	if tcc.UserID != "user-003" {
		t.Errorf("UserID = %q", tcc.UserID)
	}
	if tcc.Ctx != bg {
		t.Error("Ctx should match")
	}
}

func TestPluginMetrics_JSON(t *testing.T) {
	m := PluginMetrics{
		TotalPlugins:   5,
		ActivePlugins:  3,
		TotalTools:     10,
		TotalHooks:     7,
		TotalEnrichers: 2,
	}

	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	// Verify JSON tags
	if !strContains(string(data), "totalPlugins") {
		t.Error("JSON should contain totalPlugins")
	}
	if !strContains(string(data), "activePlugins") {
		t.Error("JSON should contain activePlugins")
	}

	// Round-trip
	var m2 PluginMetrics
	if err := json.Unmarshal(data, &m2); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if m2 != m {
		t.Errorf("round-trip mismatch: got %+v, want %+v", m2, m)
	}
}

// ---------------------------------------------------------------------------
// Phase 6 — Boundary Tests
// ---------------------------------------------------------------------------

func TestPluginManager_String(t *testing.T) {
	pm := NewPluginManager(t.TempDir())

	// Empty manager
	s := pm.String()
	if s != "PluginManager{total=0, active=0, error=0, disabled=0}" {
		t.Errorf("empty String() = %q", s)
	}

	// Register and activate one plugin
	p1 := &mockPlugin{manifest: testManifest()}
	pm.RegisterAndActivate(context.Background(), p1)
	s = pm.String()
	if !strContains(s, "total=1") || !strContains(s, "active=1") {
		t.Errorf("after activate String() = %q", s)
	}

	// Register a second plugin but don't activate (discovered state)
	m2 := testManifest()
	m2.ID = "com.test.discovered"
	p2 := &mockPlugin{manifest: m2}
	pm.Register(p2)
	s = pm.String()
	if !strContains(s, "total=2") || !strContains(s, "active=1") {
		t.Errorf("with discovered String() = %q", s)
	}

	// Disable a plugin that is NOT in entries — should count as disabled
	pm.DisablePlugins([]string{"com.test.notloaded"})
	s = pm.String()
	if !strContains(s, "disabled=1") {
		t.Errorf("with disabled String() = %q", s)
	}
}

func TestPluginManager_String_WithErrors(t *testing.T) {
	pm := NewPluginManager(t.TempDir())

	// Activate a plugin that fails
	m := testManifest()
	m.ID = "com.test.failing"
	p := &mockPlugin{manifest: m, activateErr: fmt.Errorf("fail")}
	pm.RegisterAndActivate(context.Background(), p)

	s := pm.String()
	if !strContains(s, "error=1") {
		t.Errorf("with error String() = %q", s)
	}
}

func TestPluginManager_HealthCheck_Empty(t *testing.T) {
	pm := NewPluginManager(t.TempDir())

	// No plugins at all
	results := pm.HealthCheck(context.Background())
	if len(results) != 0 {
		t.Errorf("empty HealthCheck should return empty map, got %d results", len(results))
	}
}

func TestPluginManager_Metrics_AfterActivation(t *testing.T) {
	pm := NewPluginManager(t.TempDir())

	// Before activation
	m := pm.Metrics()
	if m.TotalPlugins != 0 || m.ActivePlugins != 0 {
		t.Fatalf("pre-activation metrics should be zero: %+v", m)
	}

	// Activate one plugin (mockPlugin registers 1 tool)
	p := &mockPlugin{manifest: testManifest()}
	pm.RegisterAndActivate(context.Background(), p)

	m = pm.Metrics()
	if m.TotalPlugins != 1 {
		t.Errorf("TotalPlugins = %d, want 1", m.TotalPlugins)
	}
	if m.ActivePlugins != 1 {
		t.Errorf("ActivePlugins = %d, want 1", m.ActivePlugins)
	}
	if m.TotalTools != 1 {
		t.Errorf("TotalTools = %d, want 1", m.TotalTools)
	}
	if m.TotalHooks != 0 {
		t.Errorf("TotalHooks = %d, want 0", m.TotalHooks)
	}
	if m.TotalEnrichers != 0 {
		t.Errorf("TotalEnrichers = %d, want 0", m.TotalEnrichers)
	}
}

func TestManifest_DependencyValidation(t *testing.T) {
	tests := []struct {
		name    string
		deps    []PluginDependency
		wantErr bool
	}{
		{
			name:    "no dependencies",
			deps:    nil,
			wantErr: false,
		},
		{
			name: "valid dependency",
			deps: []PluginDependency{
				{ID: "com.example.base", Version: "1.0.0"},
			},
			wantErr: false,
		},
		{
			name: "valid with semver range",
			deps: []PluginDependency{
				{ID: "com.example.base", Version: "^1.0.0"},
			},
			wantErr: false,
		},
		{
			name: "valid with wildcard version",
			deps: []PluginDependency{
				{ID: "com.example.base", Version: "*"},
			},
			wantErr: false,
		},
		{
			name: "empty dependency ID",
			deps: []PluginDependency{
				{ID: "", Version: "1.0.0"},
			},
			wantErr: true,
		},
		{
			name: "invalid dependency ID",
			deps: []PluginDependency{
				{ID: "/bad/id", Version: "1.0.0"},
			},
			wantErr: true,
		},
		{
			name: "empty version is ok (optional)",
			deps: []PluginDependency{
				{ID: "com.example.base", Version: ""},
			},
			wantErr: false,
		},
		{
			name: "multiple valid dependencies",
			deps: []PluginDependency{
				{ID: "com.example.base", Version: "1.0.0"},
				{ID: "com.example.utils", Version: ">=2.0.0"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			m := testManifest()
			m.Dependencies = tt.deps
			writeTestManifest(t, dir, &m)

			_, err := LoadManifest(dir)
			if (err != nil) != tt.wantErr {
				t.Errorf("LoadManifest() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestWASMRuntime_Create(t *testing.T) {
	factory := NewWASMRuntime()

	m := &PluginManifest{
		ID:               "com.test.wasm",
		Name:             "WASM Test",
		Version:          "1.0.0",
		Description:      "WASM test plugin",
		Runtime:          RuntimeWASM,
		ActivationEvents: []string{"onStart"},
	}

	plugin, err := factory.Create(m, "/tmp/test-wasm")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if plugin == nil {
		t.Fatal("Create() returned nil plugin")
	}

	// Verify manifest
	loaded := plugin.Manifest()
	if loaded.ID != "com.test.wasm" {
		t.Errorf("Manifest ID = %q, want %q", loaded.ID, "com.test.wasm")
	}
	if loaded.Runtime != RuntimeWASM {
		t.Errorf("Manifest Runtime = %q, want %q", loaded.Runtime, RuntimeWASM)
	}
}

func TestWASMRuntime_Create_WrongRuntime(t *testing.T) {
	factory := NewWASMRuntime()

	m := &PluginManifest{
		ID:      "com.test.native",
		Name:    "Native",
		Version: "1.0.0",
		Runtime: RuntimeNative,
	}

	_, err := factory.Create(m, "/tmp/test")
	if err == nil {
		t.Fatal("expected error for wrong runtime type")
	}
}

func TestWASMRuntime_Activate_NoOp(t *testing.T) {
	factory := NewWASMRuntime()

	m := &PluginManifest{
		ID:               "com.test.wasm",
		Name:             "WASM NoOp Test",
		Version:          "1.0.0",
		Description:      "test",
		Runtime:          RuntimeWASM,
		ActivationEvents: []string{"onStart"},
	}

	plugin, err := factory.Create(m, "/tmp/test-wasm")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	// Activate should succeed (no-op with warning log)
	storage := &noopStorage{}
	ctx := newPluginContext(m, storage, newPluginLogger(m.ID), nil)

	err = plugin.Activate(ctx)
	if err != nil {
		t.Fatalf("Activate() error: %v", err)
	}

	// Deactivate should also succeed
	err = plugin.Deactivate(ctx)
	if err != nil {
		t.Fatalf("Deactivate() error: %v", err)
	}
}

func TestPluginManager_DeactivateAll_NotInitialized(t *testing.T) {
	// nil-safe: calling DeactivateAll on a manager with no active plugins
	pm := NewPluginManager(t.TempDir())

	// Should not panic
	pm.DeactivateAll(context.Background())

	if pm.ActiveCount() != 0 {
		t.Error("expected 0 active plugins")
	}
}

// ---------------------------------------------------------------------------
// Phase 8 — EventBus Tests
// ---------------------------------------------------------------------------

func TestPluginEventBus_SubscribeAndPublish(t *testing.T) {
	bus := NewPluginEventBus()

	var received []string
	handler := func(ctx context.Context, topic string, data any) error {
		received = append(received, fmt.Sprintf("%s:%v", topic, data))
		return nil
	}

	err := bus.Subscribe("test.topic", handler)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	errs := bus.Publish(context.Background(), "test.topic", "hello")
	if len(errs) > 0 {
		t.Fatalf("Publish errors: %v", errs)
	}

	if len(received) != 1 || received[0] != "test.topic:hello" {
		t.Errorf("received = %v, want [test.topic:hello]", received)
	}
}

func TestPluginEventBus_Unsubscribe(t *testing.T) {
	bus := NewPluginEventBus()

	called := 0
	handler := func(ctx context.Context, topic string, data any) error {
		called++
		return nil
	}

	bus.Subscribe("test", handler)
	bus.Publish(context.Background(), "test", nil)
	if called != 1 {
		t.Fatalf("expected 1 call before unsubscribe, got %d", called)
	}

	err := bus.Unsubscribe("test", handler)
	if err != nil {
		t.Fatalf("Unsubscribe failed: %v", err)
	}

	bus.Publish(context.Background(), "test", nil)
	if called != 1 {
		t.Errorf("expected 1 call after unsubscribe, got %d", called)
	}
}

func TestPluginEventBus_NoSubscribers(t *testing.T) {
	bus := NewPluginEventBus()
	errs := bus.Publish(context.Background(), "nonexistent", "data")
	if len(errs) != 0 {
		t.Errorf("expected no errors for no subscribers, got %v", errs)
	}
}

func TestPluginEventBus_PanicRecovery(t *testing.T) {
	bus := NewPluginEventBus()

	bus.Subscribe("panic.topic", func(ctx context.Context, topic string, data any) error {
		panic("handler panic!")
	})

	bus.Subscribe("panic.topic", func(ctx context.Context, topic string, data any) error {
		return nil // this should still run
	})

	errs := bus.Publish(context.Background(), "panic.topic", nil)
	if len(errs) != 1 {
		t.Fatalf("expected 1 error (panic), got %d: %v", len(errs), errs)
	}
	if !strContains(errs[0].Error(), "panic") {
		t.Errorf("error should mention panic, got: %v", errs[0])
	}
}

func TestPluginManager_Reload(t *testing.T) {
	baseDir := t.TempDir()

	// Create plugin directory with manifest
	pluginsDir := filepath.Join(baseDir, "plugins", "com.test.reload")
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		t.Fatal(err)
	}
	m := PluginManifest{
		ID:               "com.test.reload",
		Name:             "Reload Test Plugin",
		Version:          "1.0.0",
		Runtime:          RuntimeNative,
		ActivationEvents: []string{"onStart"},
		Permissions:      []string{"tools.register"},
	}
	writeTestManifest(t, pluginsDir, &m)

	pm := NewPluginManager(baseDir)
	pm.SetRuntimeFactory(&mockRuntimeFactory{})

	ctx := context.Background()
	_, err := pm.Discover(ctx)
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	if err := pm.ActivateAll(ctx); err != nil {
		t.Fatalf("ActivateAll failed: %v", err)
	}

	entry, ok := pm.GetPlugin("com.test.reload")
	if !ok {
		t.Fatal("plugin not found after discover+activate")
	}
	if entry.State != StateActive {
		t.Fatalf("expected active state, got %v", entry.State)
	}

	// Reload
	if err := pm.Reload(ctx, "com.test.reload"); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	entry2, ok := pm.GetPlugin("com.test.reload")
	if !ok {
		t.Fatal("plugin not found after reload")
	}
	if entry2.State != StateActive {
		t.Errorf("expected active state after reload, got %v", entry2.State)
	}
}

func TestPluginManager_Reload_NonExistent(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	err := pm.Reload(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for non-existent plugin")
	}
}

func TestPluginContext_Subscribe_NoPermission(t *testing.T) {
	m := testManifest()
	m.Permissions = []string{"bus.plugin", "bus.write"} // missing bus.read
	storage := &noopStorage{}
	bus := NewPluginEventBus()
	pc := newPluginContext(&m, storage, newPluginLogger(m.ID), bus)

	err := pc.Subscribe("test", func(ctx context.Context, topic string, data any) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected permission error for Subscribe without bus.read")
	}
}

func TestPluginContext_Publish_NoPermission(t *testing.T) {
	m := testManifest()
	m.Permissions = []string{"bus.plugin", "bus.read"} // missing bus.write
	storage := &noopStorage{}
	bus := NewPluginEventBus()
	pc := newPluginContext(&m, storage, newPluginLogger(m.ID), bus)

	err := pc.Publish("test", "data")
	if err == nil {
		t.Fatal("expected permission error for Publish without bus.write")
	}
}

// ---------------------------------------------------------------------------
// E2E Integration Test — Full Lifecycle
// ---------------------------------------------------------------------------

// e2eFullPlugin implements Plugin + HealthChecker.
// On Activate it registers a V2 tool, a PreToolUse hook, a context enricher,
// and subscribes+publishes on the event bus.
type e2eFullPlugin struct {
	manifest   PluginManifest
	hookCalled bool
	enriched   bool
	busSubData string
}

func (p *e2eFullPlugin) Manifest() PluginManifest { return p.manifest }

func (p *e2eFullPlugin) Activate(ctx PluginContext) error {
	// Register a V2 tool
	ctx.RegisterTool(&SimplePluginTool{
		Def: ToolDef{Name: "e2e_tool", Description: "E2E test tool"},
		ExecV2Fn: func(tcc *ToolCallContext, input string) (*ToolResult, error) {
			return NewToolResult("session=" + tcc.SessionID + " input=" + input), nil
		},
	})

	// Subscribe to PreToolUse hook
	ctx.OnPreToolUse("Shell", func(c context.Context, payload *HookPayload) (*HookResult, error) {
		p.hookCalled = true
		return &HookResult{Decision: DecisionAllow}, nil
	})

	// Register a context enricher
	ctx.EnrichContext("e2e_enricher", func(c context.Context) (string, error) {
		p.enriched = true
		return "e2e enriched content", nil
	})

	// Subscribe to a bus topic
	ctx.Subscribe("e2e.topic", func(c context.Context, topic string, data any) error {
		p.busSubData = data.(string)
		return nil
	})

	// Publish to the bus
	ctx.Publish("e2e.topic", "bus-data")

	return nil
}

func (p *e2eFullPlugin) Deactivate(ctx PluginContext) error { return nil }

func (p *e2eFullPlugin) HealthCheck(ctx context.Context) error { return nil }

func TestPluginE2E_FullLifecycle(t *testing.T) {
	// 1. Create PluginManager
	pm := NewPluginManager(t.TempDir())

	// 2. Create a full-featured plugin
	m := PluginManifest{
		ID:               "com.test.e2e",
		Name:             "E2E Test Plugin",
		Version:          "1.0.0",
		Description:      "Full lifecycle E2E test",
		Runtime:          RuntimeNative,
		ActivationEvents: []string{"onStart"},
		Permissions:      []string{"tools.register", "hooks.subscribe", "context.enrich", "bus.plugin", "bus.read", "bus.write"},
	}
	e2ePlugin := &e2eFullPlugin{manifest: m}

	// 3. Register and activate
	ctx := context.Background()
	err := pm.RegisterAndActivate(ctx, e2ePlugin)
	if err != nil {
		t.Fatalf("RegisterAndActivate failed: %v", err)
	}

	// 4. Verify tools registered via GetPlugin
	entry, ok := pm.GetPlugin("com.test.e2e")
	if !ok {
		t.Fatal("plugin not found after activation")
	}
	tools := entry.Context.GetTools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}

	// 5. Execute the tool via adapter (V2)
	tool := tools[0]
	adapter := NewPluginToolAdapter("com.test.e2e", tool)
	result, err := adapter.ExecuteWithContext(&ToolCallContext{
		SessionID: "sess-e2e",
		Channel:   "cli",
		UserID:    "user-e2e",
		Ctx:       context.Background(),
	}, `{"input": "hello"}`)
	if err != nil {
		t.Fatalf("ExecuteWithContext failed: %v", err)
	}
	// V2 result should contain session info
	if !strContains(result.Content, "session=sess-e2e") {
		t.Errorf("V2 result should contain session info, got %q", result.Content)
	}

	// 6. Verify hook received via PluginHookBridge
	bridge := NewPluginHookBridge()
	WirePluginHooks(bridge, pm)
	hookResult := bridge.Dispatch(ctx, &HookPayload{
		Event:    HookPreToolUse,
		ToolName: "Shell",
	})
	if !e2ePlugin.hookCalled {
		t.Error("hook handler should have been called")
	}
	if hookResult.Decision != DecisionAllow {
		t.Errorf("hook decision = %q, want %q", hookResult.Decision, DecisionAllow)
	}

	// 7. Verify enricher works via EnricherRegistry
	enricherReg := NewEnricherRegistry()
	WirePluginEnrichers(enricherReg, pm, 100)
	if enricherReg.Count() != 1 {
		t.Errorf("enricher count = %d, want 1", enricherReg.Count())
	}
	content := enricherReg.RunAll(ctx)
	if !strContains(content, "e2e enriched content") {
		t.Errorf("enricher output should contain enriched content, got %q", content)
	}

	// 8. Verify metrics correct
	metrics := pm.Metrics()
	if metrics.TotalPlugins != 1 {
		t.Errorf("TotalPlugins = %d, want 1", metrics.TotalPlugins)
	}
	if metrics.ActivePlugins != 1 {
		t.Errorf("ActivePlugins = %d, want 1", metrics.ActivePlugins)
	}
	if metrics.TotalTools != 1 {
		t.Errorf("TotalTools = %d, want 1", metrics.TotalTools)
	}
	if metrics.TotalHooks != 1 {
		t.Errorf("TotalHooks = %d, want 1", metrics.TotalHooks)
	}
	if metrics.TotalEnrichers != 1 {
		t.Errorf("TotalEnrichers = %d, want 1", metrics.TotalEnrichers)
	}

	// 9. Verify health check passes
	results := pm.HealthCheck(ctx)
	if results["com.test.e2e"] != nil {
		t.Errorf("expected healthy (nil error), got %v", results["com.test.e2e"])
	}

	// 10. Verify String() output
	s := pm.String()
	if !strContains(s, "total=1") || !strContains(s, "active=1") {
		t.Errorf("String() should contain total=1 and active=1, got %q", s)
	}

	// 11. Verify event bus works
	bus := pm.Bus()
	var busReceived string
	bus.Subscribe("e2e.topic", func(c context.Context, topic string, data any) error {
		busReceived = data.(string)
		return nil
	})
	bus.Publish(context.Background(), "e2e.topic", "bus-data")
	if busReceived != "bus-data" {
		t.Errorf("bus received = %q, want %q", busReceived, "bus-data")
	}

	// 12. Deactivate
	pm.DeactivateAll(ctx)

	// 13. Verify metrics updated
	metrics2 := pm.Metrics()
	if metrics2.TotalPlugins != 1 {
		t.Errorf("TotalPlugins after deactivate = %d, want 1", metrics2.TotalPlugins)
	}
	if metrics2.ActivePlugins != 0 {
		t.Errorf("ActivePlugins after deactivate = %d, want 0", metrics2.ActivePlugins)
	}
	if metrics2.TotalTools != 0 {
		t.Errorf("TotalTools after deactivate = %d, want 0", metrics2.TotalTools)
	}
	if metrics2.TotalHooks != 0 {
		t.Errorf("TotalHooks after deactivate = %d, want 0", metrics2.TotalHooks)
	}
	if metrics2.TotalEnrichers != 0 {
		t.Errorf("TotalEnrichers after deactivate = %d, want 0", metrics2.TotalEnrichers)
	}
}
