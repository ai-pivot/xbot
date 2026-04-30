# xbot Plugin System

A VSCode-inspired plugin system for xbot, providing extensible tool registration, lifecycle hooks, context enrichment, event bus, isolated storage, middleware chains, rate limiting, audit trails, and more.

## Quick Start

### 1. Create a Plugin

The fastest way to create a plugin is using the SDK helpers:

```go
package myplugin

import (
    "context"
    "xbot/plugin"
)

type MyPlugin struct{}

func (p *MyPlugin) Manifest() plugin.PluginManifest {
    return plugin.QuickManifest("com.example.my-plugin", "My Plugin", "1.0.0",
        "Does something useful",
        plugin.WithPermissions("tools.register", "storage.private"),
        plugin.WithActivationEvents("onStart"),
    )
}

func (p *MyPlugin) Activate(ctx plugin.PluginContext) error {
    // Create and register a tool with one-liner
    ctx.RegisterTool(plugin.ToolFromFunc("echo", "Echo input",
        func(ctx context.Context, input string) (string, error) {
            return "Echo: " + input, nil
        },
    ))
    return nil
}

func (p *MyPlugin) Deactivate(ctx plugin.PluginContext) error { return nil }
```

For more control, implement the `Plugin` interface directly:

```go
type MyPlugin struct{}

func (p *MyPlugin) Manifest() plugin.PluginManifest {
    return plugin.PluginManifest{
        ID:               "com.example.my-plugin",
        Name:             "My Plugin",
        Version:          "1.0.0",
        Description:      "Does something useful",
        Runtime:          plugin.RuntimeNative,
        ActivationEvents: []string{"onStart"},
        Permissions:      []string{
            "tools.register",
            "hooks.subscribe",
            "storage.private",
        },
    }
}

func (p *MyPlugin) Activate(ctx plugin.PluginContext) error {
    ctx.RegisterTool(&plugin.SimplePluginTool{
        Def: plugin.BuildToolDef("my_tool", "Does something useful",
            plugin.ToolParamDef{Name: "input", Type: "string", Description: "Input param"},
        ),
        ExecV2Fn: func(ctx *plugin.ToolCallContext, input string) (*plugin.ToolResult, error) {
            sessionID := ctx.SessionID
            channel := ctx.Channel
            _ = sessionID
            _ = channel
            return plugin.NewToolResult("Done!"), nil
        },
    })

    ctx.Subscribe("my-topic", func(ctx context.Context, topic string, data any) error {
        fmt.Printf("Received event on %s: %v\n", topic, data)
        return nil
    })

    return nil
}

func (p *MyPlugin) Deactivate(ctx plugin.PluginContext) error {
    return nil
}
```

### 2. Create plugin.json

```json
{
    "id": "com.example.my-plugin",
    "name": "My Plugin",
    "version": "1.0.0",
    "description": "Does something useful",
    "runtime": "native",
    "activationEvents": ["onStart"],
    "permissions": ["tools.register", "hooks.subscribe", "storage.private"],
    "contributes": {
        "tools": [
            {
                "name": "my_tool",
                "description": "Does something useful"
            }
        ],
        "configuration": {
            "properties": {
                "max_retries": {
                    "type": "number",
                    "default": 3,
                    "description": "Maximum retry attempts"
                }
            }
        }
    }
}
```

### 3. Install

Place the plugin directory in `~/.xbot/plugins/`:

```
~/.xbot/plugins/
└── my-plugin/
    ├── plugin.json
    └── (compiled Go binary or source)
```

## Activation Events

| Event | Trigger |
|-------|---------|
| `onStart` | xbot startup (immediate activation) |
| `onTool:<name>` | First call to the named tool |
| `onHook:<event>` | First occurrence of the named hook event |
| `onCommand:<cmd>` | User types the specified slash command |

## Permissions

| Permission | Description |
|-----------|-------------|
| `tools.register` | Register new tools for LLM to use |
| `tools.call` | Call other registered tools *(reserved for future use)* |
| `hooks.subscribe` | Subscribe to lifecycle hooks |
| `context.enrich` | Inject content into system prompt |
| `storage.private` | Per-plugin isolated key-value storage |
| `storage.shared` | Cross-plugin shared storage |
| `network.outbound` | Make outbound network requests |
| `bus.plugin` | Enable plugin-to-plugin event bus (requires bus.read/bus.write) |
| `bus.read` | Read from message bus |
| `bus.write` | Write to message bus |
| `*` | Grant all permissions |

## Hook Events

| Event | Description |
|-------|-------------|
| `PreToolUse` | Before a tool executes (can deny) |
| `PostToolUse` | After a tool executes successfully |
| `PostToolUseFailure` | After a tool fails |
| `UserPromptSubmit` | User sends a message |
| `AgentStop` | Agent finishes processing |
| `SessionStart` | New session created |
| `SessionEnd` | Session ended |
| `SubAgentStart` | SubAgent spawned |
| `SubAgentStop` | SubAgent completed |
| `PreCompact` | Before context compression |
| `PostCompact` | After context compression |
| `CronFired` | Scheduled task triggered |
| `WebhookReceived` | External webhook received |

## Runtime Types

| Runtime | Isolation | Latency | Languages | Status |
|---------|-----------|---------|-----------|--------|
| `native` | Interface boundary | ~μs | Go | ✅ Phase 1 |
| `grpc` | Process isolation | ~1-5ms | Any (JSON/stdio) | ✅ Phase 1 |
| `wasm` | Sandbox isolation | ~0.5-2ms | WASM languages | 🔮 Phase 2 |

## ToolCallContext V2

PluginToolV2 is an extended version of PluginTool that receives a `ToolCallContext` instead of a bare `context.Context`, giving plugins access to session metadata (session ID, channel, user ID, etc.) without requiring a full context.Context.

The adapter automatically detects V2 implementations and falls back to V1.

### ToolCallContext Fields

| Field | Description |
|-------|-------------|
| `SessionID` | Identifies the current conversation session |
| `Channel` | Message channel (e.g., "cli", "feishu", "web") |
| `ChatID` | Chat or conversation ID within the channel |
| `UserID` | Identifies the user who triggered the tool call |
| `Ctx` | Standard `context.Context` for cancellation/deadline |

### Usage with SimplePluginTool

```go
ctx.RegisterTool(&plugin.SimplePluginTool{
    Def: plugin.BuildToolDef("my_tool", "Does something useful",
        plugin.ToolParamDef{Name: "input", Type: "string", Description: "Input param"},
    ),
    // Use ExecV2Fn for rich call context (preferred)
    ExecV2Fn: func(ctx *plugin.ToolCallContext, input string) (*plugin.ToolResult, error) {
        fmt.Printf("Session: %s, Channel: %s, User: %s\n",
            ctx.SessionID, ctx.Channel, ctx.UserID)
        return plugin.NewToolResult("Done!"), nil
    },
})
```

### Implement PluginToolV2 Directly

```go
type MyV2Tool struct{}

func (t *MyV2Tool) Definition() plugin.ToolDef { /* ... */ }
func (t *MyV2Tool) Execute(ctx context.Context, input string) (*plugin.ToolResult, error) {
    // V1 fallback — called only when ToolCallContext is unavailable
    return plugin.NewToolResult("done"), nil
}
func (t *MyV2Tool) ExecuteWithContext(ctx *plugin.ToolCallContext, input string) (*plugin.ToolResult, error) {
    // V2 — receives rich call context
    return plugin.NewToolResult("done for session " + ctx.SessionID), nil
}
```

## Event Bus

The plugin event bus provides a pub/sub mechanism for plugin-to-plugin communication via topics. It is an in-process event bus with panic recovery per handler.

### Permissions

Using the event bus requires three permissions:
- `bus.plugin` — enables access to the event bus
- `bus.read` — allows subscribing to topics
- `bus.write` — allows publishing events

All three are required for full event bus usage.

### Subscribe & Publish

```go
func (p *MyPlugin) Activate(ctx plugin.PluginContext) error {
    // Subscribe to a topic
    err := ctx.Subscribe("user.activity", func(ctx context.Context, topic string, data any) error {
        fmt.Printf("Event on %s: %v\n", topic, data)
        return nil
    })
    if err != nil {
        return err
    }

    // Publish an event
    err = ctx.Publish("user.activity", map[string]string{
        "action": "login",
        "user":   "alice",
    })
    return err
}
```

### Handler Safety

- Each handler invocation is wrapped in panic recovery — a panicking handler will not crash the bus.
- Handlers can safely subscribe/unsubscribe during iteration (copy-on-read pattern).
- `Publish` returns an `error` (the first error from any handler, including recovered panics). The underlying `PluginEventBus.Publish` returns `[]error` for all handler errors.

## Health Check & Metrics

### HealthChecker Interface

Plugins can optionally implement the `HealthChecker` interface to report their health status. Plugins that don't implement it are assumed healthy.

```go
type HealthChecker interface {
    HealthCheck(ctx context.Context) error
}
```

```go
// Example: implement HealthChecker on your plugin
func (p *MyPlugin) HealthCheck(ctx context.Context) error {
    if !p.isConnected() {
        return fmt.Errorf("database connection lost")
    }
    return nil
}
```

Call `PluginManager.HealthCheck(ctx)` to check all active plugins — returns a `map[string]error` (nil = healthy).

### PluginMetrics

`PluginManager.Metrics()` returns aggregate metrics:

```go
type PluginMetrics struct {
    TotalPlugins   int `json:"totalPlugins"`
    ActivePlugins  int `json:"activePlugins"`
    TotalTools     int `json:"totalTools"`
    TotalHooks     int `json:"totalHooks"`
    TotalEnrichers int `json:"totalEnrichers"`
}
```

### String()

`PluginManager.String()` returns a compact status summary:

```
PluginManager{total=5, active=3, error=1, disabled=1}
```

## Hot Reload

The plugin manager supports hot reloading of plugins without restarting xbot.

### Reload a Single Plugin

```go
err := pm.Reload(ctx, "com.example.my-plugin")
```

This deactivates the plugin, re-scans its directory for an updated manifest, recreates the runtime, and re-activates it if it has `onStart`.

### Reload All Plugins

```go
err := pm.ReloadAll(ctx)
```

Deactivates all plugins, clears entries, re-discovers from plugin directories, and re-activates all `onStart` plugins.

## Install & Uninstall

The plugin manager supports runtime installation and uninstallation of plugins.

```go
// Install a plugin from a source directory
entry, err := pm.InstallPlugin(ctx, "/path/to/my-plugin")

// Uninstall a plugin by ID
err := pm.UninstallPlugin(ctx, "com.example.my-plugin")
```

- **InstallPlugin**: Validates the source manifest → acquires write lock (TOCTOU prevention) → checks for ID conflicts → copies to `~/.xbot/plugins/<id>/` → re-validates manifest → creates entry → auto-activates `onStart` plugins
- **UninstallPlugin**: Acquires write lock → deactivates active plugin → removes entries → releases lock → deletes disk directory (with `EvalSymlinks` path traversal protection, only deletes within `xbotHome`)
- The write lock is held during the in-memory operations; disk I/O (directory deletion) happens after lock release

## CLI Commands

The `/plugin` command provides plugin management from the CLI:

| Command | Description |
|---------|-------------|
| `/plugin` | Show plugin status summary (total, active count) |
| `/plugin list` | List all plugins with details (ID, version, status, tool count) |
| `/plugin reload <id>` | Reload a specific plugin |
| `/plugin reload-all` | Reload all plugins |
| `/plugin health` | Health check all active plugins |
| `/plugin metrics` | Show aggregate metrics |
| `/plugin install <dir>` | Install plugin from directory |
| `/plugin uninstall <id>` | Uninstall a plugin |

Implemented in `channel/cli_helpers.go` (`handlePluginCommand`), injected via `CLIChannel.SetPluginManager`. Status display uses `lipgloss` styling.

## Config Hot-Reload

`WatchConfig` monitors the config file for changes to `plugins.disabled_plugins` and automatically enables/disables plugins:

```go
// Start watching config for plugin changes
stop := pm.WatchConfig("config.json", 10*time.Second)
// ... later
close(stop) // stop watching
```

- A background goroutine polls the config file's mtime at the specified interval (minimum 5s)
- When mtime changes, it reads the `plugins.disabled_plugins` field and applies the diff:
  - **Newly disabled**: deactivate plugin + add to disabled map
  - **Removed from disabled**: remove from disabled map → re-discover + activate
- Config read failures keep the last known config (no accidental plugin disruption)

## Plugin Dependencies

Plugins can declare dependencies on other plugins in their manifest. Dependencies are format-validated during manifest loading. Actual version resolution and availability checking will be added in a future iteration.

### PluginDependency Struct

| Field | Description |
|-------|-------------|
| `ID` | Unique identifier of the required plugin |
| `Version` | Semver range constraint (e.g., `"^1.0.0"`, `">=2.0.0"`, `"*"`) |

### Manifest Example

```json
{
    "id": "com.example.my-plugin",
    "name": "My Plugin",
    "version": "1.0.0",
    "dependencies": [
        { "id": "com.example.base-lib", "version": "^1.0.0" },
        { "id": "com.example.auth", "version": ">=2.0.0" }
    ]
}
```

> **Note:** Currently only format validation is performed on dependency declarations. Actual version resolution and ordering will be added in a future iteration.

## Middleware Chain

Middleware chain for plugin tool execution — onion-style composition. Middlewares wrap the final handler and can intercept, modify, or short-circuit tool calls.

```go
chain := plugin.NewMiddlewareChain(
    plugin.LoggingMiddleware(logger),
    plugin.RecoveryMiddleware(logger),
    plugin.TimeoutMiddleware(10 * time.Second),
    plugin.RetryMiddleware(3),
)

result, err := chain.Execute(ctx, "my_tool", input, func(ctx context.Context, toolName, input string) (*plugin.ToolResult, error) {
    // final handler
    return plugin.NewToolResult("done"), nil
})
```

### Built-in Middleware

| Middleware | Description |
|------------|-------------|
| `LoggingMiddleware(logger)` | Logs tool name, duration, and error |
| `RecoveryMiddleware(logger)` | Recovers from panics, returns error instead |
| `TimeoutMiddleware(timeout)` | Enforces context deadline |
| `RetryMiddleware(maxRetries)` | Automatic retry on error |

## Plugin Configuration

User-level plugin configuration stored at `~/.xbot/plugins/<id>/config.json`, independent of the plugin installation directory. This allows plugins to be updated without losing user settings.

### Reading Config

```go
// Read config within Activate
config, err := ctx.Config() // returns (map[string]any, error)
maxRetries := 3 // default
if v, ok := config["max_retries"]; ok {
    maxRetries = int(v.(float64))
}
```

### Declaring Config Schema

Add a `configuration` section to your manifest's `contributes`:

```json
"contributes": {
    "configuration": {
        "properties": {
            "max_retries": {
                "type": "number",
                "default": 3,
                "description": "Maximum retry attempts"
            }
        }
    }
}
```

### ConfigStore API

| Method | Description |
|--------|-------------|
| `PluginConfigStore.Load(pluginID)` | Load config as `map[string]any` |
| `PluginConfigStore.Save(pluginID, config)` | Save config (atomic write) |
| `PluginConfigStore.Update(pluginID, key, value)` | Update a single key |
| `GetDefaultConfig(manifest)` | Extract defaults from schema |

## Rate Limiting & Quota

Per-plugin rate limiting and resource quotas to prevent any single plugin from overwhelming the system.

### Rate Limiter

```go
rl := plugin.NewPluginRateLimiter(map[string]plugin.RateLimit{
    "com.example.plugin": {MaxCalls: 60, Window: time.Minute},
})
```

### Quota Manager

```go
qm := plugin.NewPluginQuotaManager(map[string]plugin.PluginQuota{
    "com.example.plugin": {MaxToolCallsPerDay: 1000, MaxStorageMB: 50},
})
```

### Integrated Bridge

```go
// Rate limiting and quotas are enforced automatically via PluginToolBridgeWithLimits
bridge := plugin.NewPluginToolBridgeWithLimits(adapter, pluginID, rl, qm)
```

### Features

- Sliding window rate limiting per plugin
- Tool call count + storage byte quotas
- Automatic daily reset (midnight UTC)
- Integrated with PluginToolBridge

## Audit Trail

All key plugin lifecycle events are automatically logged to an append-only audit trail.

```go
// Audit log is created automatically by PluginManager
// Default path: ~/.xbot/plugins/audit.jsonl

// Query audit entries
entries := pm.AuditLog().Query(plugin.AuditFilter{
    PluginID: "com.example.plugin",
    From:     time.Now().Add(-24 * time.Hour),
})
```

### AuditEntry

| Field | Description |
|-------|-------------|
| `Timestamp` | Event time |
| `PluginID` | Plugin identifier |
| `Action` | One of: `activate`, `deactivate`, `install`, `uninstall`, `reload`, `disable` |
| `Details` | Optional additional data |
| `Error` | Error message if the action failed |

### Features

- Append-only JSONL format
- Silent writes (errors don't block caller)
- Query with time range + plugin ID filter
- Auto-logged on key lifecycle events

## SDK Helpers

### Quick Tool Creation

```go
// Simple function → PluginTool
tool := plugin.ToolFromFunc("echo", "Echo input", func(ctx context.Context, input string) (string, error) {
    return "Echo: " + input, nil
})

// JSON input/output tool
jsonTool := plugin.ToolFromJSONFunc("search", "Search items",
    []plugin.ToolParamDef{{Name: "query", Type: "string", Description: "Search query", Required: true}},
    func(ctx context.Context, input json.RawMessage) (any, error) {
        return map[string]any{"results": []string{}}, nil
    },
)
```

### Quick Hook Creation

```go
// Deny matching tools
ctx.OnPreToolUse("Shell*", plugin.DenyHook("Shell commands are blocked"))

// Log all tool calls
ctx.OnPreToolUse("*", plugin.LogHook(logger, "Tool call intercepted"))
```

### Quick Manifest Creation

```go
manifest := plugin.QuickManifest("com.example.plugin", "My Plugin", "1.0.0", "Does things",
    plugin.WithPermissions("tools.register", "storage.private"),
    plugin.WithActivationEvents("onStart"),
    plugin.WithTools(toolContribution),
)
```

### Quick Context Enrichers

```go
// Static text enricher
ctx.EnrichContext("static-info", plugin.StaticEnricher("Additional context here"))

// File-based enricher
ctx.EnrichContext("file-data", plugin.FileEnricher("/path/to/data.txt"))
```

## Auto-Retry

Automatic retry for plugins that enter an error state during activation.

```go
pm.SetAutoRetry(true, 5)             // Enable with max 5 retries per plugin
pm.SetRetryInterval(10 * time.Second) // Custom scan interval
```

### Behavior

- A background goroutine periodically scans plugins in error state
- Exponential backoff: `1s → 2s → 4s → ... → 30s` max
- `maxRetries=0` means unlimited retries
- Activation timeout is read from the manifest `timeout` field (default 30s)
- Auto-stopped when `DeactivateAll` is called

## ToolResultBuilder

Fluent API for constructing `ToolResult` instances:

```go
// Successful result with metadata
result := plugin.NewResultBuilder().
    Content("hello world").
    Metadata("format", "json").
    Build()

// Error result
result := plugin.NewResultBuilder().
    Error("invalid input: missing required field").
    Build()
```

## SchemaBuilder

Fluent API for building tool parameter schemas:

```go
params := plugin.NewSchemaBuilder().
    AddStringParam("query", "Search query", true).
    AddNumberParam("limit", "Max results", false).
    AddBoolParam("verbose", "Verbose output", false).
    AddArrayParam("tags", "Filter tags", false).
    Build()
```

## Call Tracer

Ring-buffered tool call audit trail with fixed memory footprint.

```go
// Create a tracer (100-entry ring buffer)
tracer := plugin.NewCallTracer(100)

// Attach to bridge
bridge.SetCallTracer(tracer)

// Query recent calls
traces := tracer.Recent(10)          // newest 10 traces
traces = tracer.ByPlugin("my-plugin") // filter by plugin
tracer.Clear()                        // reset
```

Each `CallTrace` records: `PluginID`, `ToolName`, `StartTime`, `EndTime`, `Duration`, `InputLen`, `OutputLen`, `IsError`.

Features:
- Fixed-capacity ring buffer (O(1) write, no GC pressure)
- Thread-safe (`sync.Mutex`)
- Optional — zero overhead when not attached
- Default capacity: 100 entries

## Standard Errors

Centralized error types for consistent error handling:

| Error | Usage |
|-------|-------|
| `ErrPluginNotFound` | `errors.Is(err, plugin.ErrPluginNotFound)` |
| `ErrPluginAlreadyRegistered` | `errors.Is(err, plugin.ErrPluginAlreadyRegistered)` |
| `ErrPluginNotActive` | `errors.Is(err, plugin.ErrPluginNotActive)` |
| `ErrPluginActivationFailed` | `errors.As(err, &e)` → `e.PluginID`, `e.Err` |
| `ErrRateLimitExceeded` | `errors.As(err, &e)` → `e.PluginID`, `e.RetryAfter` |
| `PermissionError` | `errors.As(err, &e)` → `e.PluginID`, `e.Permission`, `e.Action` |

## Architecture

See [DESIGN.md](./DESIGN.md) for the full architecture document.

## Example

See [examples/hello-world/](./examples/hello-world/) for a complete working plugin.
