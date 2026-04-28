# xbot Plugin System

A VSCode-inspired plugin system for xbot, providing extensible tool registration, lifecycle hooks, context enrichment, and isolated storage.

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
        Permissions:      []string{"tools.register", "hooks.subscribe", "storage.private"},
    }
}

func (p *MyPlugin) Activate(ctx plugin.PluginContext) error {
    // Register a tool
    ctx.RegisterTool(&plugin.SimplePluginTool{
        Def: plugin.BuildToolDef("my_tool", "Does something useful",
            plugin.ToolParamDef{Name: "input", Type: "string", Description: "Input param"},
        ),
        ExecFn: func(ctx context.Context, input string) (*plugin.ToolResult, error) {
            return plugin.NewToolResult("Done!"), nil
        },
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
| `bus.read` | Read from message bus (reserved) |
| `bus.write` | Write to message bus (reserved) |
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

## Architecture

See [DESIGN.md](./DESIGN.md) for the full architecture document.

## Example

See [examples/hello-world/](./examples/hello-world/) for a complete working plugin.
