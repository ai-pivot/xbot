package plugin

import "context"

// ---------------------------------------------------------------------------
// MockPlugin — configurable Plugin mock for testing
// ---------------------------------------------------------------------------

// MockPlugin is a configurable mock implementing the Plugin interface.
// Use NewMockPlugin to create an instance, then chain With* methods
// to customize behavior for specific test scenarios.
//
// MockPlugin can be used directly with TestKit:
//
//	tk := NewTestKit(t, NewMockPlugin("com.example.test"))
type MockPlugin struct {
	manifestFn   func() PluginManifest
	activateFn   func(ctx PluginContext) error
	deactivateFn func(ctx PluginContext) error
}

// NewMockPlugin creates a MockPlugin with sensible defaults.
// The manifest is initialized with the given id and standard test fields.
func NewMockPlugin(id string) *MockPlugin {
	return &MockPlugin{
		manifestFn: func() PluginManifest {
			return PluginManifest{
				ID:               id,
				Name:             "Mock Plugin " + id,
				Version:          "0.0.1",
				Description:      "mock plugin for testing",
				Runtime:          RuntimeNative,
				ActivationEvents: []string{"onStart"},
			}
		},
		activateFn:   func(ctx PluginContext) error { return nil },
		deactivateFn: func(ctx PluginContext) error { return nil },
	}
}

// Manifest returns the plugin manifest.
func (m *MockPlugin) Manifest() PluginManifest {
	if m.manifestFn != nil {
		return m.manifestFn()
	}
	return PluginManifest{}
}

// Activate calls the configured activate function. Defaults to nil error.
func (m *MockPlugin) Activate(ctx PluginContext) error {
	if m.activateFn != nil {
		return m.activateFn(ctx)
	}
	return nil
}

// Deactivate calls the configured deactivate function. Defaults to nil error.
func (m *MockPlugin) Deactivate(ctx PluginContext) error {
	if m.deactivateFn != nil {
		return m.deactivateFn(ctx)
	}
	return nil
}

// WithManifest sets a custom manifest function. Returns *MockPlugin for chaining.
func (m *MockPlugin) WithManifest(fn func() PluginManifest) *MockPlugin {
	m.manifestFn = fn
	return m
}

// WithActivate sets a custom activate function. Returns *MockPlugin for chaining.
func (m *MockPlugin) WithActivate(fn func(ctx PluginContext) error) *MockPlugin {
	m.activateFn = fn
	return m
}

// WithDeactivate sets a custom deactivate function. Returns *MockPlugin for chaining.
func (m *MockPlugin) WithDeactivate(fn func(ctx PluginContext) error) *MockPlugin {
	m.deactivateFn = fn
	return m
}

// ---------------------------------------------------------------------------
// MockTool — configurable PluginTool mock for testing
// ---------------------------------------------------------------------------

// MockTool is a configurable mock implementing the PluginTool interface.
// Use NewMockTool to create an instance, then chain With* methods
// to customize behavior for specific test scenarios.
//
// Unlike SimplePluginTool, MockTool provides safe defaults when fields are nil,
// making it suitable for test scenarios where you need a controllable test double.
type MockTool struct {
	definitionFn func() ToolDef
	executeFn    func(ctx context.Context, input string) (*ToolResult, error)
}

// NewMockTool creates a MockTool with sensible defaults.
// The definition is initialized with the given name and a placeholder description.
func NewMockTool(name string) *MockTool {
	return &MockTool{
		definitionFn: func() ToolDef {
			return ToolDef{
				Name:        name,
				Description: "mock tool for testing",
			}
		},
		executeFn: func(ctx context.Context, input string) (*ToolResult, error) {
			return NewToolResult("mock result"), nil
		},
	}
}

// Definition returns the tool definition.
func (m *MockTool) Definition() ToolDef {
	if m.definitionFn != nil {
		return m.definitionFn()
	}
	return ToolDef{}
}

// Execute calls the configured execute function. Defaults to ("mock result", nil).
func (m *MockTool) Execute(ctx context.Context, input string) (*ToolResult, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, input)
	}
	return NewToolResult("mock result"), nil
}

// WithDefinition sets a custom definition function. Returns *MockTool for chaining.
func (m *MockTool) WithDefinition(fn func() ToolDef) *MockTool {
	m.definitionFn = fn
	return m
}

// WithExecute sets a custom execute function. Returns *MockTool for chaining.
func (m *MockTool) WithExecute(fn func(ctx context.Context, input string) (*ToolResult, error)) *MockTool {
	m.executeFn = fn
	return m
}
