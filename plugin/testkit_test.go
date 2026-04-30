package plugin

import (
	"context"
	"testing"
)

// ---------------------------------------------------------------------------
// Test Plugins — minimal Plugin implementations for TestKit verification
// ---------------------------------------------------------------------------

// simpleTestPlugin is the most minimal Plugin implementation.
// It tracks activate/deactivate state for lifecycle testing.
type simpleTestPlugin struct {
	manifest    PluginManifest
	activated   bool
	deactivated bool
}

func (p *simpleTestPlugin) Manifest() PluginManifest { return p.manifest }
func (p *simpleTestPlugin) Activate(ctx PluginContext) error {
	p.activated = true
	return nil
}
func (p *simpleTestPlugin) Deactivate(ctx PluginContext) error {
	p.deactivated = true
	return nil
}

// toolTestPlugin registers an "echo" tool during activation.
func newToolTestPlugin(manifest PluginManifest) *toolTestPlugin {
	return &toolTestPlugin{manifest: manifest}
}

type toolTestPlugin struct {
	manifest PluginManifest
}

func (p *toolTestPlugin) Manifest() PluginManifest { return p.manifest }

func (p *toolTestPlugin) Activate(ctx PluginContext) error {
	tool := &SimplePluginTool{
		Def: BuildToolDef("echo", "echoes the input text",
			ToolParamDef{Name: "text", Type: "string", Description: "text to echo", Required: true},
		),
		ExecFn: func(ctx context.Context, input string) (*ToolResult, error) {
			text, err := ParseToolInputString(input, "text")
			if err != nil {
				return nil, err
			}
			return NewToolResult("echo: " + text), nil
		},
	}
	return ctx.RegisterTool(tool)
}

func (p *toolTestPlugin) Deactivate(ctx PluginContext) error { return nil }

// hookTestPlugin registers a PreToolUse hook during activation.
type hookTestPlugin struct {
	manifest PluginManifest
}

func (p *hookTestPlugin) Manifest() PluginManifest { return p.manifest }

func (p *hookTestPlugin) Activate(ctx PluginContext) error {
	return ctx.OnPreToolUse("*", func(ctx context.Context, payload *HookPayload) (*HookResult, error) {
		return &HookResult{Decision: DecisionAllow}, nil
	})
}

func (p *hookTestPlugin) Deactivate(ctx PluginContext) error { return nil }

// enricherTestPlugin registers a greeting context enricher during activation.
type enricherTestPlugin struct {
	manifest PluginManifest
}

func (p *enricherTestPlugin) Manifest() PluginManifest { return p.manifest }

func (p *enricherTestPlugin) Activate(ctx PluginContext) error {
	return ctx.EnrichContext("greeting", StaticEnricher("Hello from enricher!"))
}

func (p *enricherTestPlugin) Deactivate(ctx PluginContext) error { return nil }

// storageTestPlugin reads a pre-set value from storage during activation.
type storageTestPlugin struct {
	manifest PluginManifest
	gotValue string
	gotOK    bool
}

func (p *storageTestPlugin) Manifest() PluginManifest { return p.manifest }

func (p *storageTestPlugin) Activate(ctx PluginContext) error {
	val, ok := ctx.Storage().Get("testKey")
	p.gotValue = val
	p.gotOK = ok
	return nil
}

func (p *storageTestPlugin) Deactivate(ctx PluginContext) error { return nil }

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// --- TestTestKit_ActivateDeactivate ---

func TestTestKit_ActivateDeactivate(t *testing.T) {
	p := &simpleTestPlugin{
		manifest: QuickManifest("com.test.simple", "Simple", "1.0.0", "test"),
	}
	tk := NewTestKit(t, p)

	if err := tk.Activate(); err != nil {
		t.Fatalf("Activate failed: %v", err)
	}
	if !p.activated {
		t.Fatal("expected plugin to be activated")
	}

	if err := tk.Deactivate(); err != nil {
		t.Fatalf("Deactivate failed: %v", err)
	}
	if !p.deactivated {
		t.Fatal("expected plugin to be deactivated")
	}
}

// --- TestTestKit_CallTool ---

func TestTestKit_CallTool(t *testing.T) {
	tk := NewTestKit(t, newToolTestPlugin(
		QuickManifest("com.test.tool", "Tool", "1.0.0", "test"),
	))

	if err := tk.Activate(); err != nil {
		t.Fatalf("Activate failed: %v", err)
	}

	// Successful tool call
	result, err := tk.CallTool("echo", `{"text":"hello"}`)
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	if result.Content != "echo: hello" {
		t.Fatalf("expected content %q, got %q", "echo: hello", result.Content)
	}

	// Nonexistent tool should return error
	_, err = tk.CallTool("nonexistent", "")
	if err == nil {
		t.Fatal("expected error for nonexistent tool")
	}
}

// --- TestTestKit_AssertToolRegistered ---

func TestTestKit_AssertToolRegistered(t *testing.T) {
	tk := NewTestKit(t, newToolTestPlugin(
		QuickManifest("com.test.tool", "Tool", "1.0.0", "test"),
	))

	if err := tk.Activate(); err != nil {
		t.Fatalf("Activate failed: %v", err)
	}

	// Registered tool — should not panic
	tk.AssertToolRegistered("echo")

	// Nonexistent tool — should panic
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for unregistered tool assertion")
		}
	}()
	tk.AssertToolRegistered("nonexistent")
}

// --- TestTestKit_AssertHookRegistered ---

func TestTestKit_AssertHookRegistered(t *testing.T) {
	tk := NewTestKit(t, &hookTestPlugin{
		manifest: QuickManifest("com.test.hook", "Hook", "1.0.0", "test"),
	})

	if err := tk.Activate(); err != nil {
		t.Fatalf("Activate failed: %v", err)
	}

	// Registered hook — should not panic
	tk.AssertHookRegistered(HookPreToolUse)

	// Nonexistent hook — should panic
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for unregistered hook assertion")
		}
	}()
	tk.AssertHookRegistered(HookPostToolUse)
}

// --- TestTestKit_GetEnricherOutput ---

func TestTestKit_GetEnricherOutput(t *testing.T) {
	tk := NewTestKit(t, &enricherTestPlugin{
		manifest: QuickManifest("com.test.enricher", "Enricher", "1.0.0", "test"),
	})

	if err := tk.Activate(); err != nil {
		t.Fatalf("Activate failed: %v", err)
	}

	output := tk.GetEnricherOutput()
	if output != "Hello from enricher!" {
		t.Fatalf("expected %q, got %q", "Hello from enricher!", output)
	}
}

// --- TestTestKit_SetStorage ---

func TestTestKit_SetStorage(t *testing.T) {
	p := &storageTestPlugin{
		manifest: QuickManifest("com.test.storage", "Storage", "1.0.0", "test"),
	}
	tk := NewTestKit(t, p)
	tk.SetStorage("testKey", "testValue")

	if err := tk.Activate(); err != nil {
		t.Fatalf("Activate failed: %v", err)
	}

	if !p.gotOK {
		t.Fatal("expected storage key to exist")
	}
	if p.gotValue != "testValue" {
		t.Fatalf("expected %q, got %q", "testValue", p.gotValue)
	}
}
