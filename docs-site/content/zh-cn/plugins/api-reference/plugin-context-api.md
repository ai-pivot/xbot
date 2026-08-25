---
title: "PluginContext API"
weight: 3
---

`PluginContext` 接口（`plugin/context.go`）参考——插件在 `Activate` 与 `Deactivate` 期间可用的、经权限过滤的安全 API 面。

## 组合结构

`PluginContext` 由七个子接口组合而成（接口隔离原则）。新代码在只需部分能力时可以直接接受更窄的子接口。

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
    RegisterTool(tool PluginTool) error       // 需要 "tools.register"
    RegisterTools(tools ...PluginTool) error  // 需要 "tools.register"
    UseMiddleware(middleware PluginMiddleware) error // 需要 "tools.register"
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
    OnAllToolUse(handler HookHandler) error  // 同时订阅 PreToolUse 和 PostToolUse
    OnError(handler HookHandler) error       // = OnEvent(HookPostToolUseError, "", ...)
}
```

全部需要 `"hooks.subscribe"` 权限。`matcher` 是工具名模式；`""` 匹配所有工具。另有 `OnGlobalEvent(event, matcher, handler)` 注册**会话无关** hook（绕过会话隔离，供自行管理 per-workDir 状态的 script 插件触发器使用）。

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

`Storage()` 需要 `"storage.private"` 权限；无权限时返回 `deniedStorage`——所有写操作以 `PermissionError` 拒绝。类型化助手（`StorageInt`/`StorageBool`/`StorageJSON`/`StorageGetJSON`）经由 `Storage()` 实现，自动继承权限门禁。`StorageInt`/`StorageBool` 在 key 缺失或解析失败时返回 `(零值, false)`；`StorageJSON` 将值序列化为 JSON 字符串存储；`StorageGetJSON` 反序列化到目标指针（target 为 nil 或 key 缺失时报错）。

## SessionMetadata

```go
type SessionMetadata interface {
    PluginID() string
    WorkingDir() string
    Channel() string   // 如 "cli"、"feishu"、"web"
    ChatID() string
    TenantID() int64
    Logger() Logger
}
```

只读会话信息，由 `SetSessionMetadata(workingDir, channel, chatID, tenantID)` 更新。

## EventBusPublisher

```go
type EventBusPublisher interface {
    Subscribe(topic string, handler PluginEventHandler) error // 需要 "bus.plugin" + "bus.read"
    Publish(topic string, data any) error                     // 需要 "bus.plugin" + "bus.write"
}
```

两个权限必须同时具备（`HasAll`）——将插件间事件与核心消息总线分离。

## UIContributor

```go
type UIContributor interface {
    ContributeUI(widgetID, zone string, widget UIWidget, priority int) error // 需要 "ui.contribute"
    UpdateWidget(widgetID string) error
    SetWidgetRegistry(wr *WidgetRegistry)
    ContributeTheme(id string, themeData []byte) error          // 需要 "ui.themes"
    RegisterOverlay(id string, provider OverlayProvider) error  // 需要 "ui.overlay"
    ShowOverlay(id string) error                                // 需要 "ui.overlay"
    HideOverlay() error                                         // 需要 "ui.overlay"
    RegisterWebActionHandler(widgetID string, handler WebActionHandler) error
}

type WebActionHandler func(action string, data string) (string, error)
```

`ContributeUI` 要求 widgetID 与 manifest 中声明的 `UISlotContribution` 匹配。`UpdateWidget` 触发异步重渲染（宽度 0 = 不限制；TUI 在 resize 时以真实宽度刷新）。

## CronScheduler

```go
type CronScheduler interface {
    ScheduleCron(spec CronContribution) (string, error) // 需要 "cron.schedule"
    CancelCron(jobID string) error                      // 需要 "cron.schedule"
}
```

Job ID 生成格式：`plugin:<pluginID>:<index>`。

## 直接方法

| 方法 | 权限 | 说明 |
|------|------|------|
| `EnrichContext(name, enricher)` | `context.enrich` | 注册动态系统提示词内容注入器。 |
| `OnPluginError(callback)` | `hooks.subscribe` | 插件生命周期错误回调（激活失败、运行时崩溃）——与处理工具执行失败的 `OnError` 不同。 |
| `SetValue` / `GetValue` | — | 会话级内存键值存储，用于插件内跨 handler 共享数据。 |
| `ToolCallCount()` / `HookCallCount()` | — | 原子运行时计数器（工具执行总次数 / hook 分发总次数）。 |
| `RegisterChannelProvider(provider)` | `channels.register` | 注册自定义 channel provider。provider 必须实现 `Name() string`（外加 `CreateChannel`、`ConfigSchema`、`IsEnabled`）。内置名称（`feishu`、`qq`、`napcat`、`web`、`cli`）禁止覆盖。 |
| `RegisterCommand(name, description, handler)` | `commands.register` | 注册斜杠命令。Handler：`func(ctx context.Context, args string, pctx PluginContext) (string, error)`——`args` 为命令名之后的内容（已 trim）。 |
| `Notify(level, title, message)` | `notifications.send` | 发送用户通知。级别：`info`、`success`、`warning`、`error`。 |
| `PlaySound(sound)` | `notifications.send` | 播放音效：`beep`、`chime`、`complete`、`error`、`achievement`。 |
| `Config()` | — | 合并后的插件配置：manifest 默认值叠加用户值（`~/.xbot/plugins/<id>/config.json`）。 |
| `SetConfig(key, value)` | — | 设置单个配置键、持久化并通知 `OnConfigChanged` 订阅者。 |
| `OnConfigChanged(callback)` | — | 订阅配置变更；插件停用时自动释放。 |

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

`WithField`/`WithFields` 返回新的预绑定 logger；同 key 时单次调用字段优先于预绑定字段。默认实现**只写入插件专属日志文件**（`~/.xbot/plugins/<id>/logs/`，按日滚动）——绝不写入主 xbot 日志。若插件专属 writer 创建失败，回退到全局 logrus，确保日志不丢失。

## 权限错误

违反权限的调用返回 `*PermissionError`，含 `PluginID`、`Permission`、`Action` 字段。通知/音效方法在无权限时静默警告而非报错。
