package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xbot/tools"
)

// ---------------------------------------------------------------------------
// Integration Tests — full lifecycle and cross-subsystem verification
// ---------------------------------------------------------------------------

// allPermissionsList includes every recognized permission.
var allPermissionsList = []string{
	PermToolsRegister,
	PermHooksSubscribe,
	PermContextEnrich,
	PermStoragePrivate,
	PermBusRead,
	PermBusWrite,
	PermBusPlugin,
}

// integrationManifest returns a manifest with all permissions for integration tests.
func integrationManifest(id string) PluginManifest {
	return PluginManifest{
		ID:               id,
		Name:             "Integration Test Plugin " + id,
		Version:          "1.0.0",
		Description:      "plugin for integration testing",
		Runtime:          RuntimeNative,
		ActivationEvents: []string{"onStart"},
		Permissions:      allPermissionsList,
	}
}

// fullPlugin is a test plugin that registers a tool, hooks, and enricher during Activate.
type fullPlugin struct {
	manifest    PluginManifest
	toolResult  string
	mu          sync.Mutex
	activated   bool
	deactivated bool
	hookCalled  int32
}

func newFullPlugin(id string) *fullPlugin {
	return &fullPlugin{
		manifest:   integrationManifest(id),
		toolResult: "hello from " + id,
	}
}

func (p *fullPlugin) Manifest() PluginManifest { return p.manifest }

func (p *fullPlugin) Activate(ctx PluginContext) error {
	// Register a tool
	tool := &SimplePluginTool{
		Def: ToolDef{
			Name:        p.manifest.ID + ".greet",
			Description: "A greeting tool",
		},
		ExecFn: func(_ context.Context, input string) (*ToolResult, error) {
			return NewToolResult(p.toolResult + " input=" + input), nil
		},
	}
	if err := ctx.RegisterTool(tool); err != nil {
		return fmt.Errorf("register tool: %w", err)
	}

	// Register a pre-tool-use hook
	if err := ctx.OnPreToolUse("", func(_ context.Context, payload *HookPayload) (*HookResult, error) {
		atomic.AddInt32(&p.hookCalled, 1)
		return &HookResult{Decision: DecisionAllow}, nil
	}); err != nil {
		return fmt.Errorf("register hook: %w", err)
	}

	// Register a context enricher
	if err := ctx.EnrichContext("test-enricher", func(_ context.Context) (string, error) {
		return "enriched content from " + p.manifest.ID, nil
	}); err != nil {
		return fmt.Errorf("register enricher: %w", err)
	}

	p.mu.Lock()
	p.activated = true
	p.mu.Unlock()
	return nil
}

func (p *fullPlugin) Deactivate(ctx PluginContext) error {
	p.mu.Lock()
	p.deactivated = true
	p.mu.Unlock()
	return nil
}

func (p *fullPlugin) isActivated() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.activated
}

func (p *fullPlugin) isDeactivated() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.deactivated
}

// orderPlugin is a minimal plugin for dependency order testing.
type orderPlugin struct {
	manifest   func() PluginManifest
	onActivate func()
}

func (p *orderPlugin) Manifest() PluginManifest { return p.manifest() }
func (p *orderPlugin) Activate(_ PluginContext) error {
	if p.onActivate != nil {
		p.onActivate()
	}
	return nil
}
func (p *orderPlugin) Deactivate(_ PluginContext) error { return nil }

// testRuntimeFactory creates Plugin instances for testing.
type testRuntimeFactory struct {
	createFn func(manifest *PluginManifest, dir string) (Plugin, error)
}

func (f *testRuntimeFactory) Create(manifest *PluginManifest, dir string) (Plugin, error) {
	if f.createFn != nil {
		return f.createFn(manifest, dir)
	}
	return &orderPlugin{
		manifest: func() PluginManifest { return *manifest },
	}, nil
}

// middlewarePlugin is a test plugin that registers a middleware.
type middlewarePlugin struct {
	manifest   PluginManifest
	middleware PluginMiddleware
}

func (p *middlewarePlugin) Manifest() PluginManifest { return p.manifest }

func (p *middlewarePlugin) Activate(ctx PluginContext) error {
	if err := ctx.UseMiddleware(p.middleware); err != nil {
		return err
	}
	tool := &SimplePluginTool{
		Def: ToolDef{
			Name:        "com.middleware.test.echo",
			Description: "Echo tool",
		},
		ExecFn: func(_ context.Context, input string) (*ToolResult, error) {
			return NewToolResult(input), nil
		},
	}
	return ctx.RegisterTool(tool)
}

func (p *middlewarePlugin) Deactivate(_ PluginContext) error { return nil }

// busPlugin is a test plugin with custom activate logic.
type busPlugin struct {
	manifest PluginManifest
	activate func(ctx PluginContext) error
}

func (p *busPlugin) Manifest() PluginManifest         { return p.manifest }
func (p *busPlugin) Activate(ctx PluginContext) error { return p.activate(ctx) }
func (p *busPlugin) Deactivate(_ PluginContext) error { return nil }

// ---------------------------------------------------------------------------
// TestIntegration_FullPluginLifecycle
// ---------------------------------------------------------------------------

func TestIntegration_FullPluginLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	pm := NewPluginManager(tmpDir)

	// 1. Create plugin
	p := newFullPlugin("com.integration.full")

	// 2. Register plugin
	if err := pm.Register(p); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// 3. Activate all
	ctx := context.Background()
	if err := pm.ActivateAll(ctx); err != nil {
		t.Fatalf("ActivateAll failed: %v", err)
	}

	if !p.isActivated() {
		t.Fatal("plugin should be activated")
	}
	if pm.ActiveCount() != 1 {
		t.Fatalf("ActiveCount = %d, want 1", pm.ActiveCount())
	}

	// Wire subsystems
	registry := tools.NewRegistry()
	registry.SetFlatMode(true)
	hookBridge := NewPluginHookBridge()
	enricherRegistry := NewEnricherRegistry()

	if err := WireAll(pm, registry, hookBridge, enricherRegistry); err != nil {
		t.Fatalf("WireAll failed: %v", err)
	}

	// 4. Execute tool via PluginToolBridge
	toolCtx := &tools.ToolContext{Ctx: ctx}
	toolImpl, _ := registry.Get("com.integration.full.greet")
	if toolImpl == nil {
		t.Fatal("tool not found in registry")
	}
	result, err := toolImpl.Execute(toolCtx, `{"name":"world"}`)
	if err != nil {
		t.Fatalf("tool Execute failed: %v", err)
	}
	if result.IsError {
		t.Error("tool result should not be error")
	}
	if result.Summary == "" {
		t.Error("tool result content should not be empty")
	}

	// 5. Dispatch hook via PluginHookBridge
	hookResult := hookBridge.Dispatch(ctx, &HookPayload{
		Event:    HookPreToolUse,
		ToolName: "some.tool",
	})
	if hookResult.Decision != DecisionAllow {
		t.Errorf("hook decision = %s, want allow", hookResult.Decision)
	}
	if atomic.LoadInt32(&p.hookCalled) != 1 {
		t.Errorf("hookCalled = %d, want 1", p.hookCalled)
	}

	// 6. Run enricher via EnricherRegistry
	enriched := enricherRegistry.RunAll(ctx)
	if enriched == "" {
		t.Error("enricher should produce content")
	}
	if enricherRegistry.Count() != 1 {
		t.Errorf("enricher count = %d, want 1", enricherRegistry.Count())
	}

	// 7. Verify Metrics
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

	// 8. Verify Profiler
	profiler := NewProfiler()
	profiler.RecordToolCall("com.integration.full", 10*time.Millisecond)
	profiler.RecordHookCall("com.integration.full", 5*time.Millisecond)
	profiler.RecordEnricherCall("com.integration.full", 2*time.Millisecond)

	profile := profiler.GetProfile("com.integration.full")
	if profile.ToolCalls != 1 {
		t.Errorf("profile.ToolCalls = %d, want 1", profile.ToolCalls)
	}
	if profile.HookCalls != 1 {
		t.Errorf("profile.HookCalls = %d, want 1", profile.HookCalls)
	}
	if profile.EnricherCalls != 1 {
		t.Errorf("profile.EnricherCalls = %d, want 1", profile.EnricherCalls)
	}

	// 9. Verify AuditLog
	if pm.AuditLog() == nil {
		t.Fatal("AuditLog should not be nil")
	}
	auditEntries := pm.AuditLog().Query(AuditFilter{PluginID: "com.integration.full"})
	// Should have at least one activate entry
	found := false
	for _, e := range auditEntries {
		if e.Action == AuditActivate {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected activate audit entry")
	}

	// 10. Verify HealthCheck
	health := pm.HealthCheck(ctx)
	if len(health) != 1 {
		t.Fatalf("HealthCheck returned %d entries, want 1", len(health))
	}
	if health["com.integration.full"] != nil {
		t.Errorf("HealthCheck for plugin = %v, want nil (healthy)", health["com.integration.full"])
	}

	// 11. Export + Import config
	exported, err := pm.ExportConfig()
	if err != nil {
		t.Fatalf("ExportConfig failed: %v", err)
	}
	var exportData ConfigExport
	if err := json.Unmarshal(exported, &exportData); err != nil {
		t.Fatalf("unmarshal export: %v", err)
	}
	if exportData.Version != ConfigExportVersion {
		t.Errorf("export version = %d, want %d", exportData.Version, ConfigExportVersion)
	}
	if len(exportData.Plugins) != 1 {
		t.Errorf("export plugins = %d, want 1", len(exportData.Plugins))
	}

	// Import should succeed on same manager
	if err := pm.ImportConfig(exported); err != nil {
		t.Fatalf("ImportConfig failed: %v", err)
	}

	// 12. DeactivateAll
	pm.DeactivateAll(ctx)
	if pm.ActiveCount() != 0 {
		t.Errorf("ActiveCount after deactivation = %d, want 0", pm.ActiveCount())
	}
	if !p.isDeactivated() {
		t.Error("plugin should be deactivated")
	}

	// 13. Verify final metrics
	finalMetrics := pm.Metrics()
	if finalMetrics.ActivePlugins != 0 {
		t.Errorf("final ActivePlugins = %d, want 0", finalMetrics.ActivePlugins)
	}
}

// ---------------------------------------------------------------------------
// TestIntegration_DependencyOrder
// ---------------------------------------------------------------------------

func TestIntegration_DependencyOrder(t *testing.T) {
	// Test that plugins with dependencies are activated in correct
	// topological order when discovered via Discover() with a RuntimeFactory.

	var activationOrder []string
	var orderMu sync.Mutex

	recordActivation := func(id string) {
		orderMu.Lock()
		activationOrder = append(activationOrder, id)
		orderMu.Unlock()
	}

	// Step 1: Verify DependencyResolver gives correct topological order
	dr := NewDependencyResolver()
	mA := integrationManifest("com.dep.a")
	mB := integrationManifest("com.dep.b")
	mB.Dependencies = []PluginDependency{{ID: "com.dep.a", Version: "1.0.0"}}
	mC := integrationManifest("com.dep.c")
	mC.Dependencies = []PluginDependency{{ID: "com.dep.b", Version: "1.0.0"}}
	// Add in reverse order to verify sorting
	dr.AddManifest(&mC)
	dr.AddManifest(&mA)
	dr.AddManifest(&mB)

	order, err := dr.Resolve()
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if len(order) != 3 {
		t.Fatalf("order length = %d, want 3", len(order))
	}
	assertBefore := func(a, b string) {
		idxA, idxB := -1, -1
		for i, id := range order {
			if id == a {
				idxA = i
			}
			if id == b {
				idxB = i
			}
		}
		if idxA > idxB {
			t.Errorf("resolver: %s (idx=%d) should come before %s (idx=%d)", a, idxA, b, idxB)
		}
	}
	assertBefore("com.dep.a", "com.dep.b")
	assertBefore("com.dep.b", "com.dep.c")

	// Step 2: Verify actual activation order through Discover + ActivateAll
	baseDir := t.TempDir()
	pm := NewPluginManager(baseDir)

	// Create plugin directories with manifests
	manifests := []PluginManifest{mA, mB, mC}
	for _, m := range manifests {
		pluginDir := filepath.Join(baseDir, "plugins", m.ID)
		if err := os.MkdirAll(pluginDir, 0755); err != nil {
			t.Fatal(err)
		}
		writeTestManifest(t, pluginDir, &m)
	}

	// Set a runtime factory that creates plugins recording activation order
	pm.SetRuntimeFactory(&testRuntimeFactory{
		createFn: func(manifest *PluginManifest, dir string) (Plugin, error) {
			id := manifest.ID
			return &orderPlugin{
				manifest:   func() PluginManifest { return *manifest },
				onActivate: func() { recordActivation(id) },
			}, nil
		},
	})

	pm.AddSearchDirs([]string{filepath.Join(baseDir, "plugins")})

	ctx := context.Background()
	count, err := pm.Discover(ctx)
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}
	if count != 3 {
		t.Fatalf("discovered %d plugins, want 3", count)
	}

	if err := pm.ActivateAll(ctx); err != nil {
		t.Fatalf("ActivateAll failed: %v", err)
	}

	if pm.ActiveCount() != 3 {
		t.Fatalf("ActiveCount = %d, want 3", pm.ActiveCount())
	}

	orderMu.Lock()
	defer orderMu.Unlock()
	if len(activationOrder) != 3 {
		t.Fatalf("activation order length = %d, want 3: %v", len(activationOrder), activationOrder)
	}

	assertActivationBefore := func(a, b string) {
		idxA, idxB := -1, -1
		for i, id := range activationOrder {
			if id == a {
				idxA = i
			}
			if id == b {
				idxB = i
			}
		}
		if idxA < 0 || idxB < 0 {
			t.Fatalf("missing activations: %s=%d %s=%d", a, idxA, b, idxB)
		}
		if idxA > idxB {
			t.Errorf("activation: %s (idx=%d) should activate before %s (idx=%d)", a, idxA, b, idxB)
		}
	}
	assertActivationBefore("com.dep.a", "com.dep.b")
	assertActivationBefore("com.dep.b", "com.dep.c")
}

// ---------------------------------------------------------------------------
// TestIntegration_RateLimiting
// ---------------------------------------------------------------------------

func TestIntegration_RateLimiting(t *testing.T) {
	pm := NewPluginManager(t.TempDir())
	pm.SetRateLimiter(NewPluginRateLimiter(map[string]RateLimit{
		"com.ratelimit.test": {MaxCalls: 2, Window: time.Minute},
	}))

	p := newFullPlugin("com.ratelimit.test")
	if err := pm.Register(p); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	if err := pm.ActivateAll(ctx); err != nil {
		t.Fatalf("ActivateAll failed: %v", err)
	}

	registry := tools.NewRegistry()
	registry.SetFlatMode(true)
	if err := WirePluginTools(pm, registry); err != nil {
		t.Fatalf("WirePluginTools failed: %v", err)
	}

	toolImpl, _ := registry.Get("com.ratelimit.test.greet")
	if toolImpl == nil {
		t.Fatal("tool not found in registry")
	}

	tCtx := &tools.ToolContext{Ctx: ctx}

	// First two calls should succeed
	for i := 0; i < 2; i++ {
		result, err := toolImpl.Execute(tCtx, "")
		if err != nil {
			t.Fatalf("call %d failed: %v", i+1, err)
		}
		if result.IsError {
			t.Errorf("call %d should not be rate limited", i+1)
		}
	}

	// Third call should be rate limited
	result, _ := toolImpl.Execute(tCtx, "")
	if !result.IsError {
		t.Error("third call should be rate limited")
	}
}

// ---------------------------------------------------------------------------
// TestIntegration_MiddlewareChain
// ---------------------------------------------------------------------------

func TestIntegration_MiddlewareChain(t *testing.T) {
	pm := NewPluginManager(t.TempDir())

	var middlewareCalled int32

	// Plugin with middleware that modifies input
	p := &middlewarePlugin{
		manifest: integrationManifest("com.middleware.test"),
		middleware: func(ctx context.Context, toolName string, input string, next PluginMiddlewareNext) (*ToolResult, error) {
			atomic.AddInt32(&middlewareCalled, 1)
			// Modify input before passing to next
			return next(ctx, toolName, "[mw]"+input)
		},
	}

	if err := pm.Register(p); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	if err := pm.ActivateAll(ctx); err != nil {
		t.Fatalf("ActivateAll failed: %v", err)
	}

	registry := tools.NewRegistry()
	registry.SetFlatMode(true)
	if err := WirePluginTools(pm, registry); err != nil {
		t.Fatalf("WirePluginTools failed: %v", err)
	}

	toolImpl, _ := registry.Get("com.middleware.test.echo")
	if toolImpl == nil {
		t.Fatal("tool not found in registry")
	}

	tCtx := &tools.ToolContext{Ctx: ctx}
	result, err := toolImpl.Execute(tCtx, "hello")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if result.IsError {
		t.Error("result should not be error")
	}
	if atomic.LoadInt32(&middlewareCalled) != 1 {
		t.Errorf("middleware called %d times, want 1", middlewareCalled)
	}
	// The tool echoes input back, middleware should have prepended "[mw]"
	if result.Summary != "[mw]hello" {
		t.Errorf("result = %q, want [mw]hello", result.Summary)
	}
}

// ---------------------------------------------------------------------------
// TestIntegration_EventBus_PubSub
// ---------------------------------------------------------------------------

func TestIntegration_EventBus_PubSub(t *testing.T) {
	pm := NewPluginManager(t.TempDir())

	var received atomic.Value
	var subscriberCtx PluginContext

	// Plugin A subscribes to a topic
	pluginA := &busPlugin{
		manifest: integrationManifest("com.bus.subscriber"),
		activate: func(ctx PluginContext) error {
			subscriberCtx = ctx
			return ctx.Subscribe("test.topic", func(_ context.Context, topic string, data any) error {
				received.Store(data)
				return nil
			})
		},
	}

	// Plugin B only activates (no publish in activate)
	pluginB := &busPlugin{
		manifest: integrationManifest("com.bus.publisher"),
		activate: func(ctx PluginContext) error {
			return nil
		},
	}

	if err := pm.Register(pluginA); err != nil {
		t.Fatalf("Register A failed: %v", err)
	}
	if err := pm.Register(pluginB); err != nil {
		t.Fatalf("Register B failed: %v", err)
	}

	ctx := context.Background()
	if err := pm.ActivateAll(ctx); err != nil {
		t.Fatalf("ActivateAll failed: %v", err)
	}

	// Now publish after both are activated
	if err := subscriberCtx.Publish("test.topic", map[string]string{"msg": "hello from publisher"}); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	data := received.Load()
	if data == nil {
		t.Fatal("subscriber should have received data")
	}
	m, ok := data.(map[string]string)
	if !ok {
		t.Fatalf("received data type = %T, want map[string]string", data)
	}
	if m["msg"] != "hello from publisher" {
		t.Errorf("received msg = %q, want 'hello from publisher'", m["msg"])
	}
}
