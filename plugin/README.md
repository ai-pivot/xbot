# xbot Plugin System

A VSCode-inspired plugin system for xbot, providing extensible tool registration, lifecycle hooks, context enrichment, event bus, and isolated storage.

## Quick Start

### 1. Create a Plugin

```go
package myplugin

import (
    "context"
    "fmt"
    "xbot/plugin"
)

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
            // Optional: enable plugin-to-plugin event bus
            // "bus.plugin", "bus.read", "bus.write",
        },
    }
}

func (p *MyPlugin) Activate(ctx plugin.PluginContext) error {
    // Register a tool using V2 (ExecV2Fn) with rich call context
    ctx.RegisterTool(&plugin.SimplePluginTool{
        Def: plugin.BuildToolDef("my_tool", "Does something useful",
            plugin.ToolParamDef{Name: "input", Type: "string", Description: "Input param"},
        ),
        ExecV2Fn: func(ctx *plugin.ToolCallContext, input string) (*plugin.ToolResult, error) {
            // Access session metadata from ToolCallContext
            sessionID := ctx.SessionID
            channel := ctx.Channel
            _ = sessionID // use as needed
            _ = channel
            return plugin.NewToolResult("Done!"), nil
        },
    })

    // Subscribe to events on the plugin event bus (requires bus.plugin + bus.read)
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
        ]
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
| `tools.call` | Call other registered tools |
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
- `Publish` returns a slice of errors from all handlers (including recovered panics).

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

## Architecture

See [DESIGN.md](./DESIGN.md) for the full architecture document.

## Example

See [examples/hello-world/](./examples/hello-world/) for a complete working plugin.
