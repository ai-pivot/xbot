---
title: "插件生命周期"
weight: 4
---

> 本文档的英文版本包含完整的代码示例和 API 参考。请参阅 [English version](../../en/plugins/lifecycle/) 获取完整内容。

本文档提供 插件生命周期 的中文概览。详细的 API 参考和代码示例请参阅英文版本。


Understand the plugin lifecycle: discovery, activation, deactivation, and everything in between.

## Lifecycle States

```
Discovered → Activating → Active → Deactivating → Inactive
                ↓                          ↑
              Error ←──────────────────────┘
```

| State | Constant | Description |
|-------|----------|-------------|
| Discovered | `StateDiscovered` | Manifest loaded, not yet activated |
| Activating | `StateActivating` | `Activate()` in progress |
| Active | `StateActive` | Plugin is running and contributing |
| Inactive | `StateInactive` | Disabled by user or config |
| Error | `StateError` | Activation failed or runtime error |

## Discovery

`PluginManager.Discover()` scans plugin directories:

1. Scans `~/.xbot/plugins/` and `~/.xbot/plugins/builtin/`
2. For each subdirectory, loads `plugin.json` via `LoadManifest()`
3. Validates the manifest (ID format, version, runtime type)
4. Creates a runtime instance via `RuntimeFactory.Create()`
5. Resolves dependency activation order (topological sort)

Disabled plugins (listed in `config.json` → `plugins.disabled_plugins`) stay in the entries map as `StateInactive` — they're visible in the plugin panel but not activated.

## Activation Events

The `activation_events` field in the manifest controls when a plugin activates:

| Event | Description |
|-------|-------------|
| `onStart` | Activate when xbot starts (default if empty) |
| `onTool:<name>` | Activate when tool `<name>` is first called |
| `onHook:<event>` | Activate when hook event `<event>` fires |
| `onCommand:<cmd>` | Activate when command `/<cmd>` is invoked |

Lazy activation: plugins with `onTool`/`onHook`/`onCommand` events are only activated when the event first occurs, saving resources.

## Activation Process

When a plugin activates:

1. **State transition**: `StateDiscovered` → `StateActivating`
2. **Context creation**: A `PluginContext` is created with the plugin's declared permissions
3. **`Activate(ctx)` called**: The plugin registers its capabilities:
   - `ctx.RegisterTool(tool)` — Register tools
   - `ctx.OnPreToolUse(matcher, handler)` — Subscribe to hooks
   - `ctx.ContributeUI(widgetID, zone, widget, priority)` — Register widgets
   - `ctx.Subscribe(topic, handler)` — Subscribe to event bus
   - `ctx.ScheduleCron(spec)` — Schedule cron jobs
4. **Wiring**: Capabilities are wired into xbot's registries
5. **State transition**: `StateActivating` → `StateActive`

If `Activate()` returns an error or panics:
- State → `StateError`
- Error is logged
- Auto-retry may attempt reactivation (if enabled)

## Deactivation

Deactivation occurs when:
- xbot shuts down
- User disables the plugin via config
- `WatchConfig` detects the plugin was added to `disabled_plugins`
- Plugin is reloaded

The `Deactivate(ctx)` method is called, which should:
- Clean up resources (goroutines, file handles, network connections)
- Unregister tools, hooks, and widgets (handled automatically by the PluginManager)
- Flush any pending data to storage

## Auto-Retry

Failed plugins can automatically retry activation with exponential backoff:

- Initial delay: 1 second
- Maximum delay: 30 seconds
- Backoff multiplier: 2x
- Enabled via `SetAutoRetry(true, maxRetries)`

`DeactivateAll()` cancels the retry context. If you call `activate()` manually after `DeactivateAll()`, you must re-enable auto-retry.

## Hot Reload

`WatchConfig` polls `config.json` every 30 seconds (configurable, minimum 5s):

1. Compares `plugins.disabled_plugins` lists
2. Reactively deactivates newly disabled plugins
3. Reactively activates newly enabled plugins

Returns a stop channel for graceful shutdown.

## Plugin Entry States

```go
type PluginState int

const (
    StateDiscovered PluginState = iota  // Manifest loaded
    StateActivating                      // Activate() in progress
    StateActive                          // Running
    StateInactive                        // Disabled
    StateError                           // Failed
)
```

## State Transitions in Code

```go
// Discovery → Discovered
entry := pm.newEntry(manifest, pluginDir, plugin)
entry.State = StateDiscovered

// Activation → Activating → Active
entry.State = StateActivating
if err := plugin.Activate(entry.Context); err != nil {
    entry.State = StateError
} else {
    entry.State = StateActive
}

// Deactivation → Inactive
plugin.Deactivate(entry.Context)
entry.State = StateInactive
```

## See Also

- [Plugin Manifest](./manifest/) — Activation events configuration
- [PluginContext API](./plugin-context/) — What plugins can do during activation
- [Auto-retry](./auto-retry/) — Automatic recovery
- [Hot Reload](./hot-reload/) — Configuration-driven reload
