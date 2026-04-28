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
	ctx      *pluginContextImpl // for resource tracking; may be nil
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

// NewPluginToolAdapterWithContext creates an adapter that tracks tool call counts.
func NewPluginToolAdapterWithContext(pluginID string, tool PluginTool, ctx *pluginContextImpl) *PluginToolAdapter {
	def := tool.Definition()
	return &PluginToolAdapter{
		pluginID: pluginID,
		tool:     tool,
		def:      def,
		ctx:      ctx,
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
// If the underlying tool implements PluginToolV2, it uses ExecuteWithContext
// with a basic ToolCallContext; otherwise falls back to V1 Execute.
func (a *PluginToolAdapter) Execute(ctx context.Context, input string) (*ToolResult, error) {
	if a.ctx != nil {
		a.ctx.incrementToolCallCount()
	}
	if v2, ok := a.tool.(PluginToolV2); ok {
		tcc := &ToolCallContext{Ctx: ctx}
		return v2.ExecuteWithContext(tcc, input)
	}
	return a.tool.Execute(ctx, input)
}

// ExecuteWithContext runs the plugin tool with a rich call context.
func (a *PluginToolAdapter) ExecuteWithContext(ctx *ToolCallContext, input string) (*ToolResult, error) {
	if a.ctx != nil {
		a.ctx.incrementToolCallCount()
	}
	if v2, ok := a.tool.(PluginToolV2); ok {
		return v2.ExecuteWithContext(ctx, input)
	}
	return a.tool.Execute(ctx.Ctx, input)
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
	// Def is the tool's parameter definition.
	Def ToolDef
	// ExecFn is the optional V1 execution function.
	ExecFn func(ctx context.Context, input string) (*ToolResult, error)
	// ExecV2Fn is the optional V2 execution function with rich context.
	ExecV2Fn func(ctx *ToolCallContext, input string) (*ToolResult, error)
}

// Definition returns the tool's definition.
func (t *SimplePluginTool) Definition() ToolDef {
	return t.Def
}

// Execute calls the wrapped function (V1).
func (t *SimplePluginTool) Execute(ctx context.Context, input string) (*ToolResult, error) {
	if t.ExecFn == nil {
		return NewToolError("tool execution function not set"), nil
	}
	return t.ExecFn(ctx, input)
}

// ExecuteWithContext calls the V2 function if set, otherwise falls back to V1.
func (t *SimplePluginTool) ExecuteWithContext(ctx *ToolCallContext, input string) (*ToolResult, error) {
	if t.ExecV2Fn != nil {
		return t.ExecV2Fn(ctx, input)
	}
	// Fallback to V1
	if t.ExecFn == nil {
		return NewToolError("tool execution function not set"), nil
	}
	return t.ExecFn(ctx.Ctx, input)
}

// ---------------------------------------------------------------------------
// Helper: BuildToolDef creates a ToolDef with common parameter patterns.
// ---------------------------------------------------------------------------

// BuildToolDef creates a simple ToolDef with the given name, description,
// and parameter definitions.
func BuildToolDef(name, description string, params ...ToolParamDef) ToolDef {
	tp := make([]llm.ToolParam, 0, len(params))
	properties := make(map[string]any, len(params))
	var required []string

	for _, p := range params {
		tp = append(tp, llm.ToolParam{
			Name:        p.Name,
			Type:        p.Type,
			Description: p.Description,
			Required:    p.Required,
		})

		prop := map[string]any{
			"type":        p.Type,
			"description": p.Description,
		}
		properties[p.Name] = prop

		if p.Required {
			required = append(required, p.Name)
		}
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}

	return ToolDef{
		Name:        name,
		Description: description,
		Parameters:  tp,
		InputSchema: schema,
	}
}

// ToolParamDef is a simplified parameter definition for convenience.
type ToolParamDef struct {
	// Name is the parameter name.
	Name string
	// Type is the JSON schema type (e.g., "string", "number", "boolean").
	Type string
	// Description explains the parameter's purpose.
	Description string
	// Required indicates whether this parameter is mandatory.
	Required bool
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

// ---------------------------------------------------------------------------
// SchemaBuilder — fluent API for building []llm.ToolParam
// ---------------------------------------------------------------------------

// SchemaBuilder provides a fluent API for constructing tool parameter slices.
// The returned Build() slice references the builder's internal state; callers
// should not modify it after building.
type SchemaBuilder struct {
	params []llm.ToolParam
}

// NewSchemaBuilder creates a new SchemaBuilder with a non-nil empty params slice.
func NewSchemaBuilder() *SchemaBuilder {
	return &SchemaBuilder{
		params: make([]llm.ToolParam, 0, 4),
	}
}

// AddStringParam appends a string-typed parameter.
func (sb *SchemaBuilder) AddStringParam(name, desc string, required bool) *SchemaBuilder {
	sb.params = append(sb.params, llm.ToolParam{
		Name:        name,
		Type:        "string",
		Description: desc,
		Required:    required,
	})
	return sb
}

// AddNumberParam appends a number-typed parameter.
func (sb *SchemaBuilder) AddNumberParam(name, desc string, required bool) *SchemaBuilder {
	sb.params = append(sb.params, llm.ToolParam{
		Name:        name,
		Type:        "number",
		Description: desc,
		Required:    required,
	})
	return sb
}

// AddBoolParam appends a boolean-typed parameter.
func (sb *SchemaBuilder) AddBoolParam(name, desc string, required bool) *SchemaBuilder {
	sb.params = append(sb.params, llm.ToolParam{
		Name:        name,
		Type:        "boolean",
		Description: desc,
		Required:    required,
	})
	return sb
}

// AddArrayParam appends an array-typed parameter (without Items; for simple
// arrays only). Use llm.ToolParam directly if you need to specify Items.
func (sb *SchemaBuilder) AddArrayParam(name, desc string, required bool) *SchemaBuilder {
	sb.params = append(sb.params, llm.ToolParam{
		Name:        name,
		Type:        "array",
		Description: desc,
		Required:    required,
	})
	return sb
}

// Build returns the accumulated parameters as a []llm.ToolParam slice.
// The returned slice references the builder's internal state; callers should
// not modify it after building.
func (sb *SchemaBuilder) Build() []llm.ToolParam {
	return sb.params
}

// DescriptionWithPrefix returns description with [plugin_id] prefix.
func (a *PluginToolAdapter) DescriptionWithPrefix() string {
	return fmt.Sprintf("[%s] %s", a.pluginID, a.def.Description)
}
