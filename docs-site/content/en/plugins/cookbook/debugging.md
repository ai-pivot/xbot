---
title: "Debugging"
weight: 19
---

Tools and techniques for debugging plugins: per-plugin logs, the profiler, hot reload, state inspection, and common failure modes.

## Per-plugin log files

`pluginLogger` (`plugin/plog.go`) writes **only to the per-plugin log file** — plugin operational logs never pollute the main xbot log:

```
~/.xbot/plugins/<id>/logs/plugin.log
```

- `rotateWriter` rotates the file at size/age thresholds; `pluginLogManager` cleans files older than `DefaultPluginLogMaxAge` via a cleanup loop.
- Only framework-level lifecycle events (discovered/activated/deactivated/failed) go to the global log.
- If the writer can't be created, `pluginLogger.emit()` falls back to global logrus — logs are never lost.

Read logs:

```bash
tail -f ~/.xbot/plugins/xbot.git-fancy/logs/plugin.log
```

## Logging from your plugin

```go
ctx.Logger().Info("activated", plugin.Field{Key: "widgets", Value: len(widgets)})
ctx.Logger().Warnf("trigger %q subscribe failed: %v", trigger, err)
ctx.Logger().WithField("plugin", id).Error("activation failed: ", err)
```

`Logger` (`plugin/context.go:157`) supports structured fields, formatted variants, and `WithField`/`WithFields` chains (per-call fields override pre-bound ones).

- **Script plugins**: log to stderr — it's captured into the xbot log. Stdout is protocol/output-only.
- **Stdio plugins**: same rule — `print(..., file=sys.stderr)` (see `grpc-python/main.py`).

## Profiler

`plugin/profile.go` aggregates per-plugin metrics: tool/hook/enricher call counts, total + last call times.

```go
profiler := plugin.NewProfiler()
profiler.RecordToolCall(pluginID, duration)
profiler.RecordHookCall(pluginID, duration)
profile := profiler.GetProfile(pluginID)  // safe copy — mutate freely
```

Unprofiled plugins return a zero-value `PluginProfile`. The profiler is concurrency-safe (`sync.Mutex`).

## Plugin state machine

`PluginState` (`plugin/plugin.go:704`): `discovered → activating → active`, with `deactivating → inactive` on unload and `error` on failure. Inspect via `PluginManager.ListPlugins()` — each `PluginEntry` carries `State`, `retryCount`, `lastError`, `lastErrorAt`, `Dir`.

## Hot reload

- Agent command `/plugin reload-all` re-activates plugins without restarting the server.
- `WatchConfig` (`plugin/manager.go`) polls `config.json` every 30s and diffs `plugins.disabled_plugins` — but ⚠️ **it is never called in production wiring** (grep serverapp/agent shows no invocation). Changing config.json does **not** trigger a reload; use `/plugin reload-all` or restart.
- **stdio plugin binary updates require reload**: the spawned process binary is cached per activation. Rebuild → `/plugin reload-all`.

## Auto-retry

`PluginManager.SetAutoRetry(true, maxRetries)` starts a background retry loop (`retryLoop`, 5s scan interval) that reactivates error-state plugins with exponential backoff (1s → 30s cap). ⚠️ `DeactivateAll()` cancels the retry context — after manual `activate()`, re-enable auto-retry or failed plugins never recover.

## Common failure modes

| Symptom | Likely cause | Where to look |
|---|---|---|
| Plugin discovered but never activates | `activation_events` mismatch; disabled list | `~/.xbot/config.json` `plugins.disabled_plugins`; manifest `activation_events` |
| Stdio plugin times out on every call | No stdout flush; wrong field names | stderr for protocol noise; `plugin/protocol/protocol.go` field tags |
| Channel plugin tools invisible | `channels.<name>.enabled` missing from config.json | `IsEnabled(nil) → false` (`serverapp/channel_plugin.go`) |
| Widget shows stale content | Script error swallowed; cache not invalidated | plugin log `runScript(...) failed` |
| Hook never fires | Matcher mismatch; session-scoped vs global | `OnEvent` vs `OnGlobalEvent`; matcher pattern |
| Tool args arrive empty | Input JSON string not parsed | `ParseToolInputString` / `json.loads` |
| Native plugin "not registered" | `init()` registration not linked; ID mismatch | `NativeRuntime.registry`; manifest `id` vs `p.Manifest().ID` |

## Audit log

`PluginManager.AuditLog()` returns an `AuditLogger` writing `~/.xbot/plugins/audit.jsonl` — entries record install/uninstall/disable/config-change actions with plugin ID, action, details, and error. Useful for post-mortems ("who disabled the plugin and when").

## Verifying a clean install

```bash
# fresh eyes on a plugin directory
ls -la ~/.xbot/plugins/<id>/
cat ~/.xbot/plugins/<id>/plugin.json | python3 -m json.tool   # manifest parse check
tail -50 ~/.xbot/plugins/<id>/logs/plugin.log                  # last lifecycle events
```
