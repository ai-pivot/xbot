---
title: "Hot Reload & Monitoring"
weight: 18
---

`PluginManager` supports runtime reload, configuration-driven enable/disable, automatic recovery of failed plugins, health checks, and aggregate metrics.

## Reloading a Single Plugin

`Reload(ctx, pluginID)` re-loads one plugin from disk without restarting xbot:

1. Deactivates the plugin if it is active (`StateDeactivating` → `Deactivate` → `StateInactive`).
2. Releases `OnConfigChanged` subscriptions bound to the old plugin context.
3. Removes the old entry and unregisters all its widgets (`widgetRegistry.UnregisterAll`).
4. Re-scans the plugin's directory (`findPluginDir` over `DefaultPluginDirs` + extra dirs) and reloads the manifest (`LoadManifest`).
5. Recreates storage (`NewFileStorage`; falls back to `noopStorage` on failure) and invalidates the plugin's config cache.
6. Builds a fresh `PluginEntry` (new `PluginContext`, logger, widget registry) and recreates the runtime via `RuntimeFactory.Create`.
7. Re-activates automatically if the manifest declares the `onStart` activation event.
8. Emits `PluginEventReloaded` and writes an `AuditReload` audit entry.

```go
if err := pm.Reload(ctx, "xbot.genui"); err != nil {
    // manifest / runtime / activation errors
}
```

## Reloading Everything

`ReloadAll(ctx)` deactivates all plugins, clears the entry map, re-discovers from disk, and re-activates:

1. Suppresses widget updates for the duration (`widgetRegistry.SuppressUpdates`) to avoid flooding WebSocket push buffers.
2. `DeactivateAll(ctx)` — note this also stops the auto-retry goroutine.
3. Unregisters all widgets, then replaces the entry map with a fresh one.
4. `Discover(ctx)` + `ActivateAll(ctx)`.
5. Calls registered `OnReload` callbacks asynchronously (in a goroutine) so slow listeners (e.g. WebSocket widget pushes) cannot block the RPC handler.

```go
pm.OnReload(func() { /* runs after ReloadAll */ })
if err := pm.ReloadAll(ctx); err != nil { /* discover/activate errors */ }
```

## Config Watching

`WatchConfig(configPath, interval)` polls `config.json` and reacts to changes in `plugins.disabled_plugins`:

```go
stop := pm.WatchConfig("/home/user/.xbot/config.json", 30*time.Second)
// ...
close(stop)
```

- The interval is clamped to a minimum of 5 seconds.
- Each tick compares the config file's modification time; on change it re-reads the file and diffs the `plugins.disabled_plugins` list against the previous snapshot.
- **Newly disabled** plugins are deactivated (`StateDeactivating` → `Deactivate` → `StateInactive`) and added to the `disabled` set.
- **Newly enabled** plugins are removed from the disabled set, then either re-activated in place (entry exists, state `StateInactive`, has `onStart`) or discovered from disk and activated.

## Auto-Retry

`SetAutoRetry(enabled, maxRetries)` runs a background retry loop for plugins stuck in the error state:

```go
pm.SetAutoRetry(true, 5) // retry up to 5 times per plugin; 0 = unlimited
```

- A goroutine (`retryLoop`) ticks at `retryInterval` (default 5s; `SetRetryInterval` exists for tests and is not intended for production).
- Each tick, `retryErrorPlugins` scans all entries; error-state plugins whose `retryCount` is below `maxRetries` are retried with exponential backoff: `1s * 2^(attempt-1)`, capped at 30s (`retryInitialDelay` / `retryMaxDelay`).
- A retry sets the entry to `StateDiscovered` and calls `activate`. On success the retry counter and `lastError` are reset, and `PluginEventActivated` is emitted with `{"recovered": true, "attempt": n}`; on failure `lastError`/`lastErrorAt` are recorded and the plugin's error callback is invoked via `notifyPluginError`.

> **Important**: `DeactivateAll` (and therefore `ReloadAll`) stops the auto-retry goroutine and sets `autoRetry = false`. If you activate plugins manually after `DeactivateAll`, call `SetAutoRetry` again to restore automatic recovery.

## Health Checks

Plugins can implement the optional `HealthChecker` interface:

```go
type HealthChecker interface {
    HealthCheck(ctx context.Context) error
}
```

```go
results := pm.HealthCheck(ctx) // map[pluginID]error — nil means healthy
```

Only ACTIVE plugins are checked; plugins that do not implement `HealthChecker` are reported as healthy (`nil` error).

## Metrics

`Metrics()` returns aggregate plugin-system counters:

```go
type PluginMetrics struct {
    TotalPlugins   int   `json:"total_plugins"`
    ActivePlugins  int   `json:"active_plugins"`
    TotalTools     int   `json:"total_tools"`
    TotalHooks     int   `json:"total_hooks"`
    TotalEnrichers int   `json:"total_enrichers"`
    ToolCallCount  int64 `json:"tool_call_count"` // runtime cumulative tool executions
    HookCallCount  int64 `json:"hook_call_count"` // runtime cumulative hook dispatches
}
```

Tool/hook counts and call counters are aggregated from the `PluginContext` of ACTIVE plugins only. `String()` prints a compact summary: `PluginManager{total=5, active=3, error=1, disabled=1}`.

## See Also

- [Plugin Lifecycle](./lifecycle/) — activation states and events
- [Logging & Audit](./logging/) — reload operations are audited
- [Configuration](./configuration/) — hot reload of plugin config
