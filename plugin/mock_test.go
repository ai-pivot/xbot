package plugin

import (
	"context"
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// MockPlugin Tests
// ---------------------------------------------------------------------------

// TestMockPlugin_Basic verifies that NewMockPlugin creates a mock satisfying
// the Plugin interface with correct default behavior.
func TestMockPlugin_Basic(t *testing.T) {
	p := NewMockPlugin("com.test.mock")

	// Verify interface satisfaction
	var _ Plugin = p

	// Manifest should return default values
	m := p.Manifest()
	if m.ID != "com.test.mock" {
		t.Errorf("Manifest.ID = %q, want %q", m.ID, "com.test.mock")
	}
	if m.Name != "Mock Plugin com.test.mock" {
		t.Errorf("Manifest.Name = %q, want %q", m.Name, "Mock Plugin com.test.mock")
	}
	if m.Version != "0.0.1" {
		t.Errorf("Manifest.Version = %q, want %q", m.Version, "0.0.1")
	}
	if m.Runtime != RuntimeNative {
		t.Errorf("Manifest.Runtime = %q, want %q", m.Runtime, RuntimeNative)
	}

	// Activate/Deactivate should return nil by default
	if err := p.Activate(nil); err != nil {
		t.Errorf("Activate() returned unexpected error: %v", err)
	}
	if err := p.Deactivate(nil); err != nil {
		t.Errorf("Deactivate() returned unexpected error: %v", err)
	}
}

// TestMockPlugin_WithActivate verifies that With* methods override
// the default behavior correctly.
func TestMockPlugin_WithActivate(t *testing.T) {
	activateCalled := false
	deactivateCalled := false

	p := NewMockPlugin("com.test.custom").
		WithActivate(func(ctx PluginContext) error {
			activateCalled = true
			return nil
		}).
		WithDeactivate(func(ctx PluginContext) error {
			deactivateCalled = true
			return errors.New("deactivate failed")
		})

	// Custom manifest
	p.WithManifest(func() PluginManifest {
		return PluginManifest{
			ID:      "com.test.overridden",
			Name:    "Overridden",
			Version: "2.0.0",
		}
	})
	m := p.Manifest()
	if m.ID != "com.test.overridden" {
		t.Errorf("Manifest.ID = %q, want %q", m.ID, "com.test.overridden")
	}

	// Custom activate
	if err := p.Activate(nil); err != nil {
		t.Errorf("Activate() returned unexpected error: %v", err)
	}
	if !activateCalled {
		t.Error("custom activate function was not called")
	}

	// Custom deactivate with error
	err := p.Deactivate(nil)
	if err == nil || err.Error() != "deactivate failed" {
		t.Errorf("Deactivate() error = %v, want 'deactivate failed'", err)
	}
	if !deactivateCalled {
		t.Error("custom deactivate function was not called")
	}
}

// TestMockPlugin_WithTestKit verifies that MockPlugin works with TestKit.
func TestMockPlugin_WithTestKit(t *testing.T) {
	tk := NewTestKit(t, NewMockPlugin("com.test.tk"))

	if err := tk.Activate(); err != nil {
		t.Fatalf("TestKit.Activate() failed: %v", err)
	}
	if err := tk.Deactivate(); err != nil {
		t.Fatalf("TestKit.Deactivate() failed: %v", err)
	}
}

// TestMockPlugin_ZeroValue verifies nil safety when MockPlugin is
// constructed without NewMockPlugin.
func TestMockPlugin_ZeroValue(t *testing.T) {
	m := &MockPlugin{}

	mf := m.Manifest()
	if mf.ID != "" {
		t.Errorf("zero-value Manifest.ID = %q, want empty", mf.ID)
	}
	if err := m.Activate(nil); err != nil {
		t.Errorf("zero-value Activate() = %v, want nil", err)
	}
	if err := m.Deactivate(nil); err != nil {
		t.Errorf("zero-value Deactivate() = %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// MockTool Tests
// ---------------------------------------------------------------------------

// TestMockTool_Basic verifies that NewMockTool creates a mock satisfying
// the PluginTool interface with correct default behavior.
func TestMockTool_Basic(t *testing.T) {
	tool := NewMockTool("test_tool")

	// Verify interface satisfaction
	var _ PluginTool = tool

	// Definition should return default values
	def := tool.Definition()
	if def.Name != "test_tool" {
		t.Errorf("Definition.Name = %q, want %q", def.Name, "test_tool")
	}
	if def.Description != "mock tool for testing" {
		t.Errorf("Definition.Description = %q, want %q", def.Description, "mock tool for testing")
	}

	// Execute should return default "mock result"
	result, err := tool.Execute(context.Background(), "")
	if err != nil {
		t.Errorf("Execute() returned unexpected error: %v", err)
	}
	if result.Content != "mock result" {
		t.Errorf("Execute() Content = %q, want %q", result.Content, "mock result")
	}
}

// TestMockTool_WithExecute verifies that With* methods override
// the default behavior correctly.
func TestMockTool_WithExecute(t *testing.T) {
	executeCalled := false

	tool := NewMockTool("custom_tool").
		WithDefinition(func() ToolDef {
			return ToolDef{
				Name:        "custom_tool",
				Description: "custom description",
			}
		}).
		WithExecute(func(ctx context.Context, input string) (*ToolResult, error) {
			executeCalled = true
			return NewToolResult("received: " + input), nil
		})

	// Custom definition
	def := tool.Definition()
	if def.Description != "custom description" {
		t.Errorf("Definition.Description = %q, want %q", def.Description, "custom description")
	}

	// Custom execute
	result, err := tool.Execute(context.Background(), "hello")
	if err != nil {
		t.Errorf("Execute() returned unexpected error: %v", err)
	}
	if !executeCalled {
		t.Error("custom execute function was not called")
	}
	if result.Content != "received: hello" {
		t.Errorf("Execute() Content = %q, want %q", result.Content, "received: hello")
	}
}

// TestMockTool_ZeroValue verifies nil safety when MockTool is
// constructed without NewMockTool.
func TestMockTool_ZeroValue(t *testing.T) {
	m := &MockTool{}

	def := m.Definition()
	if def.Name != "" {
		t.Errorf("zero-value Definition.Name = %q, want empty", def.Name)
	}
	result, err := m.Execute(context.Background(), "")
	if err != nil {
		t.Errorf("zero-value Execute() error = %v, want nil", err)
	}
	if result.Content != "mock result" {
		t.Errorf("Execute() Content = %q, want %q", result.Content, "mock result")
	}
}
