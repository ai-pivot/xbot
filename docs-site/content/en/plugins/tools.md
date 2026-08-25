---
title: "Plugin Tools"
weight: 11
---

Plugins can register custom tools that the LLM can call during conversations.

## PluginTool Interface

```go
type PluginTool interface {
    Definition() ToolDef
    Execute(ctx context.Context, input string) (*ToolResult, error)
}

type ToolDef struct {
    Name        string
    Description string
    Parameters  []ToolParamDef
}

type ToolResult struct {
    Output string
    IsError bool
}
```

## PluginToolV2 (Enhanced)

V2 tools receive `ToolCallContext` with additional metadata:

```go
type PluginToolV2 interface {
    PluginTool
    ExecuteV2(ctx context.Context, input string, callCtx ToolCallContext) (*ToolResult, error)
}
```

`PluginToolBridge` auto-detects V2. If a plugin implements V2, the bridge passes `ToolCallContext`; otherwise it falls back to V1 `Execute`.

## Registration

**Permission required**: `tools.register`

```go
func (p *MyPlugin) Activate(ctx plugin.PluginContext) error {
    tool := &MyTool{}
    return ctx.RegisterTool(tool)
}
```

## SDK Helpers

### Simple function tool

```go
tool := plugin.ToolFromFunc("greet", "Greet someone by name", func(ctx context.Context, input string) (string, error) {
    return "Hello, " + input + "!", nil
})
ctx.RegisterTool(tool)
```

### JSON parameter tool

```go
tool := plugin.ToolFromJSONFunc("search", "Search for items", []plugin.ToolParamDef{
    {Name: "query", Type: "string", Description: "Search query", Required: true},
    {Name: "limit", Type: "number", Description: "Max results", Required: false},
}, func(ctx context.Context, input json.RawMessage) (any, error) {
    var params struct {
        Query string `json:"query"`
        Limit int    `json:"limit"`
    }
    json.Unmarshal(input, &params)
    return searchResults(params.Query, params.Limit), nil
})
ctx.RegisterTool(tool)
```

## Tool Definition

```go
type ToolDef struct {
    Name        string         // Tool name (unique)
    Description string         // Description for the LLM
    Parameters  []ToolParamDef // Parameter schema
}

type ToolParamDef struct {
    Name        string   // Parameter name
    Type        string   // "string", "number", "boolean"
    Description string   // Parameter description
    Required    bool     // Is this parameter required?
    Enum        []string // Optional enum values
}
```

## Tool Result

```go
type ToolResult struct {
    Output   string // The tool output (shown to LLM)
    IsError  bool   // If true, output is treated as an error
}
```

## Manifest Declaration

Tools can be declared in `plugin.json` for documentation:

```json
{
  "contributes": {
    "tools": [
      {
        "name": "my-tool",
        "description": "Does something useful",
        "input_schema": {
          "type": "object",
          "properties": {
            "input": {"type": "string"}
          }
        }
      }
    ]
  }
}
```

## Middleware

Plugins can register tool middleware that wraps all tool executions:

```go
type PluginMiddleware interface {
    Wrap(next ToolExecutor) ToolExecutor
}

func (p *MyPlugin) Activate(ctx plugin.PluginContext) error {
    ctx.UseMiddleware(&LoggingMiddleware{})
    return nil
}

type LoggingMiddleware struct{}
func (m *LoggingMiddleware) Wrap(next plugin.ToolExecutor) plugin.ToolExecutor {
    return func(ctx context.Context, tool string, input string) (*plugin.ToolResult, error) {
        start := time.Now()
        result, err := next(ctx, tool, input)
        log.Printf("Tool %s took %v", tool, time.Since(start))
        return result, err
    }
}
```

## Channel-Scoped Tools

Tools can be registered for specific channels only:

```go
// Register a tool only for the "feishu" channel
registry.RegisterForChannel("feishu", tool)
```

Channel-scoped tools are only visible when the active channel matches.

## See Also

- [PluginContext API](./plugin-context/) — ToolRegistrar interface
- [Permissions](./permissions/) — `tools.register` permission
- [Cookbook: Tool Registration](./cookbook/tool-registration/) — Step-by-step guide
- [API Reference: PluginTool API](./api-reference/plugin-tool-api/) — Complete API reference
