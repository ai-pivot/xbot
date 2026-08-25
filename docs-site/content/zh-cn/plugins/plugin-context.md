---
title: "PluginContext API"
weight: 5
---

> 本文档的英文版本包含完整的代码示例和 API 参考。请参阅 [English version](../../en/plugins/plugin-context/) 获取完整内容。

本文档提供 PluginContext API 的中文概览。详细的 API 参考和代码示例请参阅英文版本。


`PluginContext` is the **only** interface plugins use to interact with xbot. It provides controlled, permission-filtered access to xbot's capabilities.

## Overview

`PluginContext` is a composite interface combining multiple sub-interfaces:

```go
type PluginContext interface {
    ToolRegistrar      // Register tools and middleware
    HookSubscriber     // Subscribe to lifecycle hooks
    StorageProvider    // Per-plugin KV storage
    SessionMetadata    // Read-only session info
    EventBusPublisher  // Plugin-to-plugin events
    UIContributor      // Widgets, themes, overlays
    CronScheduler      // Schedule cron jobs
    // ... plus configuration and channel provider methods
}
```

Access is filtered by declared permissions — plugins can only use what they declare in `plugin.json`.

## Sub-Interfaces

### ToolRegistrar

Register tools for the LLM to use:

```go
type ToolRegistrar interface {
    RegisterTool(tool PluginTool) error
    RegisterTools(tools ...PluginTool) error
    UseMiddleware(middleware PluginMiddleware) error
}
```

**Permission required**: `tools.register`

```go
func (p *MyPlugin) Activate(ctx plugin.PluginContext) error {
    tool := plugin.ToolFromFunc("greet", "Greet someone", func(ctx context.Context, input string) (string, error) {
        return "Hello!", nil
    })
    return ctx.RegisterTool(tool)
}
```

### HookSubscriber

Subscribe to lifecycle hooks:

```go
type HookSubscriber interface {
    OnPreToolUse(matcher string, handler HookHandler) error
    OnPostToolUse(matcher string, handler HookHandler) error
    OnUserPrompt(handler HookHandler) error
    OnAgentStop(handler HookHandler) error
    OnSessionStart(handler HookHandler) error
    OnSessionEnd(handler HookHandler) error
    OnEvent(event HookEvent, matcher string, handler HookHandler) error
    OnAllToolUse(handler HookHandler) error
    OnError(handler HookHandler) error
}
```

**Permission required**: `hooks.register`

```go
func (p *MyPlugin) Activate(ctx plugin.PluginContext) error {
    return ctx.OnPreToolUse("Shell", func(ctx context.Context, payload *plugin.HookPayload) (*plugin.HookResult, error) {
        // Intercept Shell tool calls before execution
        return &plugin.HookResult{Decision: plugin.DecisionAllow}, nil
    })
}
```

### StorageProvider

Per-plugin persistent key-value storage:

```go
type StorageProvider interface {
    Storage() StorageAccessor
    StorageInt(key string) (int64, bool)
    StorageBool(key string) (bool, bool)
    StorageJSON(key string, value any) error
    StorageGetJSON(key string, target any) error
}
```

**Permission required**: `storage`

Storage location: `~/.xbot/plugins/<id>/data/storage.json`

```go
func (p *MyPlugin) Activate(ctx plugin.PluginContext) error {
    // Store a value
    ctx.Storage().Set("counter", "42")
    
    // Retrieve typed values
    count, ok := ctx.StorageInt("counter")  // int64(42), true
    
    // Store JSON
    ctx.StorageJSON("config", map[string]any{"theme": "dark"})
    
    // Retrieve JSON
    var cfg map[string]any
    ctx.StorageGetJSON("config", &cfg)
    
    return nil
}
```

### SessionMetadata

Read-only session information:

```go
type SessionMetadata interface {
    PluginID() string
    WorkingDir() string
    Channel() string    // "cli", "web", "feishu", etc.
    ChatID() string
    TenantID() int64
    Logger() Logger
}
```

Available to all plugins (no permission required).

```go
func (p *MyPlugin) Activate(ctx plugin.PluginContext) error {
    logger := ctx.Logger()
    logger.Info("Plugin activated", 
        plugin.Field{Key: "workDir", Value: ctx.WorkingDir()},
        plugin.Field{Key: "channel", Value: ctx.Channel()},
    )
    return nil
}
```

### EventBusPublisher

Plugin-to-plugin pub/sub communication:

```go
type EventBusPublisher interface {
    Subscribe(topic string, handler PluginEventHandler) error
    Publish(topic string, data any) error
}
```

**Permissions required**: `bus.read` (subscribe), `bus.write` (publish), `bus.plugin` (both)

```go
func (p *MyPlugin) Activate(ctx plugin.PluginContext) error {
    // Subscribe to events from other plugins
    ctx.Subscribe("xbot.git-fancy:commit", func(ctx context.Context, topic string, data any) error {
        logger := ctx.Logger()
        logger.Info("Received commit event")
        return nil
    })
    
    // Publish events to other plugins
    ctx.Publish("xbot.my-plugin:ready", map[string]any{"version": "1.0.0"})
    
    return nil
}
```

### UIContributor

Register UI widgets, themes, and overlays:

```go
type UIContributor interface {
    ContributeUI(widgetID, zone string, widget UIWidget, priority int) error
    UpdateWidget(widgetID string) error
    SetWidgetRegistry(wr *WidgetRegistry)
    ContributeTheme(id string, themeData []byte) error
    RegisterOverlay(id string, provider OverlayProvider) error
    ShowOverlay(id string) error
    HideOverlay() error
    RegisterWebActionHandler(widgetID string, handler WebActionHandler) error
}
```

**Permission required**: `ui.contribute` (widgets), `ui.themes` (themes)

```go
func (p *MyPlugin) Activate(ctx plugin.PluginContext) error {
    widget := &MyWidget{}
    return ctx.ContributeUI("my-widget", "statusBarRight", widget, 100)
}
```

### CronScheduler

Schedule cron jobs:

```go
type CronScheduler interface {
    ScheduleCron(spec CronContribution) (string, error)
}
```

**Permission required**: `cron`

```go
func (p *MyPlugin) Activate(ctx plugin.PluginContext) error {
    _, err := ctx.ScheduleCron(plugin.CronContribution{
        Message: "Check for updates",
        EverySeconds: 3600,
    })
    return err
}
```

## Permission Enforcement

Every `PluginContext` method call is checked against the plugin's declared permissions. If a plugin tries to use a capability it didn't declare, the call returns an error:

```go
// plugin.json declares: ["tools.register"]
// This works:
ctx.RegisterTool(tool)

// This returns an error:
ctx.Subscribe("topic", handler)  // bus.read not declared
```

## SDK Helpers

The `plugin/sdk.go` file provides convenience functions:

```go
// Create a simple tool from a function
tool := plugin.ToolFromFunc("name", "desc", func(ctx context.Context, input string) (string, error) {
    return "result", nil
})

// Create a tool with JSON input
tool := plugin.ToolFromJSONFunc("name", "desc", params, func(ctx context.Context, input json.RawMessage) (any, error) {
    return result, nil
})

// Pre-built hook handlers
plugin.DenyHook("blocked")   // Always deny
plugin.AllowHook()            // Always allow
plugin.LogHook(logger, "msg") // Log and allow
```

## See Also

- [Permissions](./permissions/) — Permission system details
- [Tools](./tools/) — Plugin tool registration
- [Hooks](./hooks/) — Hook system
- [Widgets](./widgets/) — Widget system
- [Storage](./storage/) — Storage system
- [Event Bus](./event-bus/) — Event bus
