package plugin

import (
	"context"
	"encoding/json"
	"fmt"

	"xbot/llm"
)

// ---------------------------------------------------------------------------
// PluginToolAdapter — wraps a PluginTool for the internal tool system.
//
// The actual tools.Tool adapter lives in plugin/integration.go to avoid
// circular imports with the tools package. This file only contains
// the plugin-internal types and helpers.
// ---------------------------------------------------------------------------

// PluginToolAdapter wraps a PluginTool for registration with xbot's tool system.
type PluginToolAdapter struct {
	pluginID string
	tool     PluginTool
	def      ToolDef
}

// NewPluginToolAdapter creates an adapter from a PluginTool.
func NewPluginToolAdapter(pluginID string, tool PluginTool) *PluginToolAdapter {
	def := tool.Definition()
	return &PluginToolAdapter{
		pluginID: pluginID,
		tool:     tool,
		def:      def,
	}
}

// Name returns the tool name.
func (a *PluginToolAdapter) Name() string {
	return a.def.Name
}

// Description returns the tool description with plugin attribution.
func (a *PluginToolAdapter) Description() string {
	return fmt.Sprintf("[%s plugin] %s", a.pluginID, a.def.Description)
}

// Parameters returns the tool's parameter definitions.
func (a *PluginToolAdapter) Parameters() []llm.ToolParam {
	return a.def.Parameters
}

// Execute runs the plugin tool with the given input.
func (a *PluginToolAdapter) Execute(ctx context.Context, input string) (*ToolResult, error) {
	return a.tool.Execute(ctx, input)
}

// PluginID returns the owning plugin's ID.
func (a *PluginToolAdapter) PluginID() string {
	return a.pluginID
}

// ---------------------------------------------------------------------------
// SimplePluginTool — helper for creating tools from functions
// ---------------------------------------------------------------------------

// SimplePluginTool is a convenience struct for creating PluginTool instances
// without implementing the full interface.
type SimplePluginTool struct {
	Def    ToolDef
	ExecFn func(ctx context.Context, input string) (*ToolResult, error)
}

// Definition returns the tool's definition.
func (t *SimplePluginTool) Definition() ToolDef {
	return t.Def
}

// Execute calls the wrapped function.
func (t *SimplePluginTool) Execute(ctx context.Context, input string) (*ToolResult, error) {
	if t.ExecFn == nil {
		return NewToolError("tool execution function not set"), nil
	}
	return t.ExecFn(ctx, input)
}

// ---------------------------------------------------------------------------
// Helper: BuildToolDef creates a ToolDef with common parameter patterns.
// ---------------------------------------------------------------------------

// BuildToolDef creates a simple ToolDef with the given name, description,
// and parameter definitions.
func BuildToolDef(name, description string, params ...ToolParamDef) ToolDef {
	tp := make([]llm.ToolParam, 0, len(params))
	for _, p := range params {
		tp = append(tp, llm.ToolParam{
			Name:        p.Name,
			Type:        p.Type,
			Description: p.Description,
			Required:    p.Required,
		})
	}
	return ToolDef{
		Name:        name,
		Description: description,
		Parameters:  tp,
	}
}

// ToolParamDef is a simplified parameter definition for convenience.
type ToolParamDef struct {
	Name        string
	Type        string // "string", "number", "boolean", "array", "object"
	Description string
	Required    bool
}

// ---------------------------------------------------------------------------
// Helper: ParseToolInput extracts a field from JSON tool input.
// ---------------------------------------------------------------------------

// ParseToolInputString extracts a string field from JSON tool input.
func ParseToolInputString(input string, field string) (string, error) {
	var m map[string]any
	if err := json.Unmarshal([]byte(input), &m); err != nil {
		return "", fmt.Errorf("parse tool input: %w", err)
	}
	v, ok := m[field].(string)
	if !ok {
		return "", fmt.Errorf("field %q not found or not a string", field)
	}
	return v, nil
}
