---
title: "Logging & Audit"
weight: 17
---

Plugin operational logs and the lifecycle audit trail are kept fully isolated from the main xbot log. Per-plugin logs go to per-plugin files with daily rotation; audit events go to a JSONL audit log. Both live under `~/.xbot/plugins/`.

## Per-Plugin Logging

Plugins get a structured logger through `PluginContext.Logger()`, which implements the `plugin.Logger` interface:

```go
type Logger interface {
    Debug(msg string, fields ...Field)
    Info(msg string, fields ...Field)
    Warn(msg string, fields ...Field)
    Error(msg string, fields ...Field)

    Debugf(format string, args ...any)
    Infof(format string, args ...any)
    Warnf(format string, args ...any)
    Errorf(format string, args ...any)

    WithField(key string, value any) Logger
    WithFields(fields ...Field) Logger
}

type Field struct {
    Key   string
    Value any
}
```

Usage:

```go
ctx.Logger().Info("widget refreshed", plugin.Field{Key: "widget", Value: "git"})
ctx.Logger().WithField("attempt", 3).Warnf("retry failed: %v", err)
```

### Location and Rotation

- Location: `~/.xbot/plugins/<pluginID>/logs/<pluginID>-YYYY-MM-DD.log`
- The writer is `rotateWriter` (`plugin/plog.go`) — a thread-safe `io.Writer` that rotates by date. `Write` checks the current date on every call and opens a new file when the day changes; files are opened with `O_CREATE|O_WRONLY|O_APPEND`, `0644`.
- The plugin ID is sanitized by `sanitizeBaseName` before being used as a file name — any character outside `[a-zA-Z0-9._-]` is replaced with `_`.

### Line Format

```
2006-01-02 15:04:05 [INFO] plugin=my-plugin key=value message
```

### Log Levels

`Debug`, `Info`, `Warn`, `Error` (plus the formatted variants). Per-plugin files do NOT apply a level filter — all levels are written.

### Cleanup

`pluginLogManager` starts a cleanup goroutine that runs once at startup and then every hour (`cleanupLoop`, 1h ticker). `doCleanup` scans every `~/.xbot/plugins/<id>/logs/` directory plus the audit directory and removes `.log`/`.jsonl` files whose modification time is older than `DefaultPluginLogMaxAge` (7 days).

### Fallback

Per-plugin logs are written ONLY to the plugin's own log file — never into the global logrus log. If the per-plugin writer could not be created (`fileOut == nil`), `pluginLogger.emit` falls back to the global logrus logger so no logs are silently lost.

## Audit Logging

`AuditLogger` (`plugin/audit.go`) records an append-only JSONL trail of plugin lifecycle operations. `PluginManager` creates one at `~/.xbot/plugins/audit.jsonl`, rotated daily to `~/.xbot/plugins/audit-YYYY-MM-DD.jsonl`.

### AuditEntry

```go
type AuditEntry struct {
    Timestamp time.Time      `json:"timestamp"`
    PluginID  string         `json:"plugin_id"`
    Action    string         `json:"action"`
    Details   map[string]any `json:"details,omitempty"`
    Error     string         `json:"error,omitempty"`
}
```

### Actions

| Constant | Value | Recorded on |
|----------|-------|-------------|
| `AuditActivate` | `"activate"` | Plugin activation |
| `AuditDeactivate` | `"deactivate"` | Plugin deactivation |
| `AuditInstall` | `"install"` | Plugin installation |
| `AuditUninstall` | `"uninstall"` | Plugin removal |
| `AuditReload` | `"reload"` | Plugin reload |
| `AuditDisable` | `"disable"` | Plugin disabled |

### API

```go
al, err := plugin.NewAuditLogger(path) // path: ~/.xbot/plugins/audit.jsonl
if err != nil {
    // handle
}

al.Log(plugin.AuditEntry{PluginID: "xbot.genui", Action: plugin.AuditReload})

entries := al.Query(plugin.AuditFilter{PluginID: "xbot.genui"})
al.Clear()
al.Close()
```

- `Log` sets `Timestamp` to `time.Now()` when it is zero and silently ignores write errors — audit logging must not block the caller.
- `Query` scans all `audit-*.jsonl` files, applies `AuditFilter` (`PluginID`, `From`, `To`; zero values mean "no filter"), and returns entries sorted by `Timestamp` ascending.
- Rotation uses the same `rotateWriter` with a `.jsonl` suffix. If the rotating writer cannot be created, the logger falls back to a single `audit.jsonl` file (legacy mode, opened `0600`).
- `Clear` truncates today's audit file and recreates the rotating writer (legacy mode truncates and reopens the single file).

## See Also

- [Hot Reload & Monitoring](./hot-reload/) — reload events are audited
- [PluginContext API](./plugin-context/) — `Logger()` accessor
