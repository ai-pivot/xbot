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
	pc := newPluginContext(&m, storage, newPluginLogger(m.ID))

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
	pc := newPluginContext(&m, storage, newPluginLogger(m.ID))

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
	pc := newPluginContext(&m, storage, newPluginLogger(m.ID))

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
	pc := newPluginContext(&m, storage, newPluginLogger(m.ID))

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
	pc := newPluginContext(&m, storage, newPluginLogger(m.ID))

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
