---
title: "Tools"
weight: 14
---

Tools are functions the agent can call during its loop. Plugins register tools through `PluginContext.RegisterTool` (Go), the `activate` response (stdio), or `channel_tools` (channel plugins). The core types: `PluginTool`, `ToolDef`, `ToolResult` (`plugin/plugin.go`).

## The Go way

### Option 1: SimplePluginTool with BuildToolDef

`plugin/examples/hello-world/hello.go`:

```go
pctx.RegisterTool(&plugin.SimplePluginTool{
	Def: plugin.BuildToolDef("hello", "Greet someone by name. Returns a friendly greeting message.",
		plugin.ToolParamDef{Name: "name", Type: "string", Description: "The person to greet"},
	),
	ExecFn: func(ctx context.Context, input string) (*plugin.ToolResult, error) {
		name, err := plugin.ParseToolInputString(input, "name")
		if err != nil {
			name = "World"
		}
		return plugin.NewToolResult(fmt.Sprintf("Hello, %s! 👋", name)), nil
	},
})
```

`BuildToolDef(name, desc, params...)` generates the JSON Schema automatically (`ToJSONSchema`/`buildParameters`). The agent sees the schema in its tool list; the LLM decides when to call.

### Option 2: SDK func adapters

```go
pctx.RegisterTool(plugin.ToolFromFunc("double", "Doubles a number",
	func(ctx context.Context, input string) (string, error) {
		return input + input, nil
	}))

pctx.RegisterTool(plugin.ToolFromJSONFunc("weather", "Get weather",
	[]plugin.ToolParamDef{{Name: "city", Type: "string"}},
	func(ctx context.Context, input json.RawMessage) (any, error) {
		var req struct{ City string `json:"city"` }
		json.Unmarshal(input, &req)
		return map[string]any{"temp": 21, "city": req.City}, nil // auto-marshalled
	}))
```

### Option 3: PluginToolV2 (full context)

```go
type PluginToolV2 interface {
	PluginTool
	ExecuteV2(ctx context.Context, tc ToolCallContext) (*ToolResult, error)
}
```

`PluginToolBridge` auto-detects V2 and passes `ToolCallContext` (session metadata, tenant, sandbox access). Falls back to V1 `Execute(ctx, input)` otherwise.

## ToolResult — structure your output

```go
// Simple
plugin.NewToolResult("done")
plugin.NewToolError("file not found")   // IsError() = true → error rendering

// Builder with metadata
plugin.NewResultBuilder().
	Content("Server Info\nstatus: running").
	Metadata("kind", "server-info").
	Build()

// Deterministic formatting
plugin.FormatToolResult("Server Info", map[string]string{
	"status": "running", "version": "2.0.1",
})
// → "Server Info\nstatus: running\nversion: 2.0.1"  (keys sorted)

plugin.FormatListResult([]string{"alpha", "beta"})
// → "1. alpha\n2. beta"     (empty → "(no items)")
```

`Metadata` flows through to renderers — e.g. the Edit tool stores a unified diff under `Metadata["diff"]` for fancy rendering.

## The stdio way

The `activate` response declares tools; `execute_tool` runs them:

```json
{ "tools": [ {
    "name": "python_greet",
    "description": "Greet someone by name.",
    "parameters": [ {"name": "name", "type": "string", "description": "The person to greet", "required": true} ],
    "inputSchema": { "type": "object", "properties": {"name": {"type": "string"}}, "required": ["name"] }
} ] }
```

```json
{ "result": "{\"english\": \"Hello, Bob!\"}" }
{ "error": "Unknown tool: xyz" }
```

`protocol.ExecuteToolParams.Input` is the raw JSON string — parse it yourself (`plugin/examples/grpc-python/main.py handle_execute_tool`).

## The channel way

Channel plugins send a `channel_tools` type-message after `channel_config` (see [Channel Plugins](../channel-plugins/)). Tools may declare `channels: ["web"]` and a `ui` block (`tools.UIDecl` — mode/param/libs/surface) so the frontend can render results specially (GenUI panels, etc.).

## Naming and visibility

- Tool names should be snake_case and collision-aware — the global namespace is shared with built-in tools. A plugin tool named `read` shadows the built-in. Prefix with the plugin domain when in doubt (`git_status`).
- Channel-scoped tools (`RegisterForChannel`) only appear for that channel's sessions — `AsDefinitionsForSession` merges `channelTools[channel]` + tenant tools + global tools.
- The manifest `contributes.tools[]` declaration is **documentation + discovery**; the real registration happens in `Activate`.

## Timeouts and failures

- Tool execution is bounded by the manifest `timeout` (default 30s). A timed-out stdio call **kills the process** and marks it not-running (prevents goroutine leaks).
- Return `plugin.NewToolError(msg)` for expected failures — the agent sees an error result and can adapt. Returning Go errors marks the tool failed too; prefer structured error results for recoverable conditions.
