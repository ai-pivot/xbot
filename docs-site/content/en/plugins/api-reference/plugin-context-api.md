---
title: "PluginContext API"
weight: 3
---

Reference for the `PluginContext` interface (`plugin/context.go`) — the safe, permission-filtered API surface available to plugins during `Activate` and `Deactivate`.

## Composition

`PluginContext` composes seven sub-interfaces (Interface Segregation Principle). New code can accept narrower sub-interfaces where only a subset of capabilities is needed.

```go
type PluginContext interface {
    ToolRegistrar
    HookSubscriber
    StorageProvider
    SessionMetadata
    EventBusPublisher
    UIContributor
    CronScheduler

    EnrichContext(name string, enricher ContextEnricher) error
    OnPluginError(callback PluginErrorCallback) error
    SetValue(key string, value any)
    GetValue(key string) (any, bool)
    ToolCallCount() int64
    HookCallCount() int64
    RegisterChannelProvider(provider any) error
    RegisterCommand(name string, description string, handler PluginCommandHandler) error
    Notify(level NotificationLevel, title, message string)
    PlaySound(sound SoundID)
    Config() (map[string]any, error)
    SetConfig(key string, value any) error
    OnConfigChanged(callback func(config map[string]any)) error
}
```

## ToolRegistrar

```go
type ToolRegistrar interface {
    RegisterTool(tool PluginTool) error      // requires "tools.register"
    RegisterTools(tools ...PluginTool) error // requires "tools.register"
    UseMiddleware(middleware PluginMiddleware) error // requires "tools.register"
}
```

## HookSubscriber

```go
type HookSubscriber interface {
    OnPreToolUse(matcher string, handler HookHandler) error
    OnPostToolUse(matcher string, handler HookHandler) error
    OnUserPrompt(handler HookHandler) error
    OnAgentStop(handler HookHandler) error
    OnSessionStart(handler HookHandler) error
    OnSessionEnd(handler HookHandler) error
    OnEvent(event HookEvent, matcher string, handler HookHandler) error
    OnAllToolUse(handler HookHandler) error  // subscribes to BOTH PreToolUse and PostToolUse
    OnError(handler HookHandler) error       // = OnEvent(HookPostToolUseError, "", ...)
}
```

All require the `"hooks.subscribe"` permission. `matcher` is a tool name pattern; `""` matches all tools. An additional `OnGlobalEvent(event, matcher, handler)` registers a session-agnostic hook that bypasses session isolation (used by script plugin triggers that manage per-workDir state).

## StorageProvider

```go
type StorageProvider interface {
    Storage() StorageAccessor
    StorageInt(key string) (int64, bool)
    StorageBool(key string) (bool, bool)
    StorageJSON(key string, value any) error
    StorageGetJSON(key string, target any) error
}
```

`Storage()` requires the `"storage.private"` permission; without it a `deniedStorage` is returned that rejects all writes with `PermissionError`. Typed helpers (`StorageInt`/`StorageBool`/`StorageJSON`/`StorageGetJSON`) go through `Storage()` so they inherit the permission gate. `StorageInt`/`StorageBool` return `(zero, false)` on missing key or parse failure; `StorageJSON` marshals the value and stores it as a string; `StorageGetJSON` unmarshals into the target pointer (errors on nil target or missing key).

## SessionMetadata

```go
type SessionMetadata interface {
    PluginID() string
    WorkingDir() string
    Channel() string   // e.g. "cli", "feishu", "web"
    ChatID() string
    TenantID() int64
    Logger() Logger
}
```

Read-only session information, updated by `SetSessionMetadata(workingDir, channel, chatID, tenantID)`.

## EventBusPublisher

```go
type EventBusPublisher interface {
    Subscribe(topic string, handler PluginEventHandler) error // requires "bus.plugin" + "bus.read"
    Publish(topic string, data any) error                     // requires "bus.plugin" + "bus.write"
}
```

Both permissions are required together (`HasAll`) — this separates plugin-to-plugin events from the core message bus.

## UIContributor

```go
type UIContributor interface {
    ContributeUI(widgetID, zone string, widget UIWidget, priority int) error // requires "ui.contribute"
    UpdateWidget(widgetID string) error
    SetWidgetRegistry(wr *WidgetRegistry)
    ContributeTheme(id string, themeData []byte) error          // requires "ui.themes"
    RegisterOverlay(id string, provider OverlayProvider) error  // requires "ui.overlay"
    ShowOverlay(id string) error                                // requires "ui.overlay"
    HideOverlay() error                                         // requires "ui.overlay"
    RegisterWebActionHandler(widgetID string, handler WebActionHandler) error
}

type WebActionHandler func(action string, data string) (string, error)
```

`ContributeUI` requires the widgetID to match a declared `UISlotContribution` in the manifest. `UpdateWidget` triggers an async re-render (width 0 = unbounded; the TUI refreshes with real width on resize).

## CronScheduler

```go
type CronScheduler interface {
    ScheduleCron(spec CronContribution) (string, error) // requires "cron.schedule"
    CancelCron(jobID string) error                      // requires "cron.schedule"
}
```

Job IDs are generated as `plugin:<pluginID>:<index>`.

## Direct Methods

| Method | Permission | Description |
|--------|-----------|-------------|
| `EnrichContext(name, enricher)` | `context.enrich` | Register a dynamic system-prompt content injector. |
| `OnPluginError(callback)` | `hooks.subscribe` | Callback for plugin lifecycle errors (activation failure, runtime crash) — distinct from `OnError` which handles tool execution failures. |
| `SetValue` / `GetValue` | — | Session-scoped in-memory key-value store for cross-handler data sharing within a plugin. |
| `ToolCallCount()` / `HookCallCount()` | — | Atomic runtime counters (total tool executions / hook dispatches). |
| `RegisterChannelProvider(provider)` | `channels.register` | Register a custom channel provider. Provider must implement `Name() string` (plus `CreateChannel`, `ConfigSchema`, `IsEnabled`). Built-in names (`feishu`, `qq`, `napcat`, `web`, `cli`) cannot be overridden. |
| `RegisterCommand(name, description, handler)` | `commands.register` | Register a slash command. Handler: `func(ctx context.Context, args string, pctx PluginContext) (string, error)` — `args` is everything after the command name (trimmed). |
| `Notify(level, title, message)` | `notifications.send` | Send a user notification. Levels: `info`, `success`, `warning`, `error`. |
| `PlaySound(sound)` | `notifications.send` | Play a sound: `beep`, `chime`, `complete`, `error`, `achievement`. |
| `Config()` | — | Merged plugin config: manifest defaults overlaid with user values from `~/.xbot/plugins/<id>/config.json`. |
| `SetConfig(key, value)` | — | Set one config key, persist, and notify `OnConfigChanged` subscribers. |
| `OnConfigChanged(callback)` | — | Subscribe to config changes; released automatically on deactivation. |

## Logger

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

`WithField`/`WithFields` return new pre-bound loggers; per-call fields take precedence over pre-bound fields for the same key. The default implementation writes ONLY to the per-plugin log file (`~/.xbot/plugins/<id>/logs/`, daily rotating) — never to the main xbot log. If the per-plugin writer cannot be created, it falls back to the global logrus logger so logs are never lost.

## Permission Errors

Permission-violating calls return `*PermissionError` with `PluginID`, `Permission`, and `Action` fields. Notification/sound methods silently warn instead of erroring.
