---
title: "PluginTool API"
weight: 4
---

Reference for the plugin tool interfaces (`plugin/plugin.go`), covering tool definition, execution, and result construction.

## Interfaces

### PluginTool

The plugin-side interface for a tool provided by a plugin:

```go
type PluginTool interface {
    // Definition returns the tool's JSON schema definition for LLM consumption.
    Definition() ToolDef

    // Execute runs the tool with the given input and returns a result.
    // The input is a JSON string matching the tool's input schema.
    Execute(ctx context.Context, input string) (*ToolResult, error)
}
```

### PluginToolV2

Extended interface receiving a rich call context instead of a bare `context.Context`. The `PluginToolBridge` checks for V2 first and falls back to V1.

```go
type PluginToolV2 interface {
    PluginTool
    // ExecuteWithContext runs the tool with a rich call context.
    ExecuteWithContext(ctx *ToolCallContext, input string) (*ToolResult, error)
}
```

### ToolCallContext

```go
type ToolCallContext struct {
    SessionID string          // current conversation session
    Channel   string          // message channel ("cli", "feishu", "web", ...)
    ChatID    string          // chat/conversation ID within the channel
    UserID    string          // user who triggered the tool call
    TenantID  int64           // tenant for multi-tenancy
    Ctx       context.Context // cancellation and deadline information
}
```

## ToolDef

```go
type ToolDef struct {
    Name        string          `json:"name"`
    Description string          `json:"description"`
    Parameters  []llm.ToolParam `json:"parameters"`
    Version     string          `json:"version,omitempty"`     // semver; included in ToJSONSchema() when set
    InputSchema map[string]any  `json:"input_schema,omitempty"` // auto-generated JSON Schema; nil for manual ToolDef construction
}
```

### ToJSONSchema()

Returns the tool definition in OpenAI function calling format:

```json
{
  "type": "function",
  "function": {
    "name": "...",
    "description": "...",
    "parameters": { "type": "object", "properties": {...}, "required": [...] },
    "version": "..." 
  }
}
```

If `InputSchema` is populated (e.g. from `BuildToolDef`), it is used directly as the parameters value; otherwise the schema is reconstructed from the `Parameters` slice (note: nested `ToolParam.Items` structures are not yet supported in the fallback path).

## ToolResult

```go
type ToolResult struct {
    Content  string            `json:"content"`              // primary output sent back to the LLM
    IsError  bool              `json:"is_error,omitempty"`   // execution failed (but the plugin itself ran correctly)
    Metadata map[string]string `json:"metadata,omitempty"`   // optional key-value pairs for downstream processing
}
```

Constructors:

```go
func NewToolResult(content string) *ToolResult // success
func NewToolError(content string) *ToolResult  // error result
```

## ToolResultBuilder

Fluent API for constructing `ToolResult`:

```go
result := NewResultBuilder().
    Content("hello").
    Metadata("format", "json").
    Build()
```

| Method | Signature | Description |
|--------|-----------|-------------|
| `NewResultBuilder` | `func NewResultBuilder() *ToolResultBuilder` | Create a builder with default values. |
| `Content` | `func (b *ToolResultBuilder) Content(content string) *ToolResultBuilder` | Set primary output content. |
| `Error` | `func (b *ToolResultBuilder) Error(content string) *ToolResultBuilder` | Set content AND mark as error. |
| `IsError` | `func (b *ToolResultBuilder) IsError(isError bool) *ToolResultBuilder` | Explicitly set the error flag. |
| `Metadata` | `func (b *ToolResultBuilder) Metadata(key, value string) *ToolResultBuilder` | Add a key-value pair (lazily initializes the map). |
| `Build` | `func (b *ToolResultBuilder) Build() *ToolResult` | Return the constructed result. |

## SDK Formatting Helpers

From `plugin/sdk.go`:

| Helper | Signature | Output |
|--------|-----------|--------|
| `FormatToolResult` | `func FormatToolResult(title string, sections map[string]string) *ToolResult` | `"title\nkey: value\nkey2: value2"` — keys sorted for deterministic output; empty sections → title only. |
| `FormatListResult` | `func FormatListResult(items []string) *ToolResult` | Numbered list `"1. alpha\n2. beta"`; empty/nil → `"(no items)"`. |
| `FormatErrorResult` | `func FormatErrorResult(operation string, err error) *ToolResult` | `"<operation> failed: <msg>"` with `IsError: true`; nil err → `"unknown error"`. |

## Quick Tool Factories

From `plugin/sdk.go`:

```go
// Plain string in / string out.
func ToolFromFunc(name, desc string, fn func(ctx context.Context, input string) (string, error)) PluginTool

// JSON input / structured output (auto-marshaled to JSON).
func ToolFromJSONFunc(name, desc string, params []ToolParamDef,
    fn func(ctx context.Context, input json.RawMessage) (any, error)) PluginTool
```

`ToolFromJSONFunc` uses `BuildToolDef` to generate the JSON Schema automatically.
