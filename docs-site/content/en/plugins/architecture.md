---
title: "Plugin Architecture"
weight: 2
---

Understand how xbot's plugin system works under the hood.

## Overview

xbot's plugin system follows a VSCode-like extension model. Plugins are discovered via `plugin.json` manifests, activated lazily based on events, and sandboxed through the `PluginContext` interface.

```
┌─────────────────────────────────────────────────────────┐
│                     xbot Agent                           │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐     │
│  │  Tool Registry│  │ Hook Manager│  │WidgetRegistry│    │
│  └──────┬───────┘  └──────┬──────┘  └──────┬──────┘    │
│         │                  │                │            │
│  ┌──────┴──────────────────┴────────────────┴──────┐   │
│  │              PluginManager                        │   │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────┐          │   │
│  │  │ Native   │ │ Stdio    │ │ Script   │          │   │
│  │  │ Runtime  │ │ Runtime  │ │ Runtime  │          │   │
│  │  └────┬─────┘ └────┬─────┘ └────┬─────┘          │   │
│  └───────┼────────────┼────────────┼────────────────┘   │
│          │            │            │                      │
│  ┌───────┴────────────┴────────────┴────────────────┐   │
│  │              PluginContext                        │   │
│  │  (Permission-filtered API surface)                │   │
│  └───────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────┘
```

## Core Components

### PluginManager

The central orchestrator (`plugin/manager.go`). Responsibilities:

- **Discovery**: Scans `~/.xbot/plugins/` and `~/.xbot/plugins/builtin/` for `plugin.json` files
- **Activation**: Creates runtime instances and calls `Activate()` based on activation events
- **Lifecycle**: Manages plugin states (Discovered → Active → Inactive → Error)
- **Dependency Resolution**: Topological sort with cycle detection (Kahn's algorithm)
- **Hot Reload**: `WatchConfig` polls `config.json` every 30s for plugin enable/disable changes
- **Auto-retry**: Exponential backoff (1s → 30s cap) for failed plugins

### Plugin Interface

Every plugin implements three methods (`plugin/plugin.go`):

```go
type Plugin interface {
    Manifest() PluginManifest
    Activate(ctx PluginContext) error
    Deactivate(ctx PluginContext) error
}
```

- `Manifest()` — Returns metadata (called once during discovery)
- `Activate()` — Registers capabilities (tools, hooks, widgets) via PluginContext
- `Deactivate()` — Cleans up resources (called on shutdown or unload)

### Runtime Factory

Three runtime types (`plugin/runtime_factory.go`):

| Runtime | Description | Use Case |
|---------|-------------|----------|
| `native` | In-process Go plugin | Maximum performance, direct API access |
| `stdio` | External process via JSON-RPC over stdin/stdout | Any language, isolation |
| `script` | External script execution | Simple widgets, bash/Python scripts |

`grpc` is a historical alias for `stdio`. WASM runtime is skeleton-only (planned).

### PluginContext

The **only** interface plugins should use to interact with xbot (`plugin/context.go`). It's a composite of sub-interfaces:

- `ToolRegistrar` — Register tools and middleware
- `HookSubscriber` — Subscribe to lifecycle hooks
- `StorageProvider` — Per-plugin KV storage
- `SessionMetadata` — Read-only session info (working dir, channel, chat ID)
- `EventBusPublisher` — Plugin-to-plugin events
- `UIContributor` — Register widgets, themes, overlays
- `CronScheduler` — Schedule cron jobs

Access is filtered by declared permissions — plugins can only use what they declare.

### Permission System

Permissions are declared in `plugin.json` and enforced at runtime (`plugin/permissions.go`):

```json
{
  "permissions": ["tools.register", "ui.contribute", "bus.read"]
}
```

The `PermissionChecker` validates every `PluginContext` method call. Undeclared permissions are denied.

## Plugin States

```
Discovered → Activating → Active → Deactivating → Inactive
                ↓                          ↑
              Error ←──────────────────────┘
```

| State | Description |
|-------|-------------|
| `StateDiscovered` | Manifest loaded, not yet activated |
| `StateActivating` | `Activate()` in progress |
| `StateActive` | Plugin is running and contributing |
| `StateInactive` | Disabled by user or config |
| `StateError` | Activation failed or runtime error |

## Discovery Flow

1. `PluginManager.Discover()` scans `DefaultPluginDirs()`:
   - `~/.xbot/plugins/` — user-installed plugins
   - `~/.xbot/plugins/builtin/` — built-in plugin packages
2. Each directory is scanned for `plugin.json`
3. Manifests are validated (ID format, version, runtime type)
4. Runtime instances are created via `RuntimeFactory.Create()`
5. Dependency activation order is resolved (topological sort)

## Activation Flow

1. `ActivateAll()` iterates plugins in dependency order
2. For each plugin, `Activate(ctx)` is called
3. The plugin registers capabilities via `PluginContext`
4. Capabilities are wired into xbot's registries (tools, hooks, widgets)
5. Channel providers are wired via `WireChannelProviders()`

## File Layout

```
~/.xbot/
├── plugins/
│   ├── my-plugin/
│   │   ├── plugin.json        # Manifest
│   │   ├── main.sh            # Entry point
│   │   ├── data/
│   │   │   └── storage.json   # KV storage
│   │   ├── config.json        # User overrides
│   │   └── logs/               # Per-plugin logs
│   └── builtin/                # Built-in plugins
├── config.json                 # Global config (plugins.enabled, disabled_plugins)
```

## See Also

- [Plugin Manifest](./manifest/) — Complete manifest specification
- [PluginContext API](./plugin-context/) — The plugin's gateway to xbot
- [Permissions](./permissions/) — Capability-based security model
- [Stdio Runtime](./stdio-runtime/) — JSON-RPC protocol for any language
