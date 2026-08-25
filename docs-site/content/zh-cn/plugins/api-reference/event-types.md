---
title: "事件类型"
weight: 12
---

插件交互涉及的三种事件系统参考：插件生命周期事件（Go）、类型化事件总线（`EventMap`，Web）、生命周期 hook 事件（见 [Hook 事件](hook-events/)）。

## 插件生命周期事件（Go）

`plugin/events.go` — `PluginEventNotifier` 是轻量、无 topic 的机制，供外部消费者（CLI、channel）获取生命周期通知。区别于用于插件间通信的基于 topic 的 `PluginEventBus`。

```go
type PluginEventType string

const (
    PluginEventActivated     PluginEventType = "activated"
    PluginEventDeactivated   PluginEventType = "deactivated"
    PluginEventInstalled     PluginEventType = "installed"
    PluginEventUninstalled   PluginEventType = "uninstalled"
    PluginEventReloaded      PluginEventType = "reloaded"
    PluginEventError         PluginEventType = "error"
    PluginEventConfigChanged PluginEventType = "config_changed"
)
```

```go
type PluginEvent struct {
    Type      PluginEventType
    PluginID  string
    Timestamp time.Time
    Error     error
    Data      any // 可选；推荐 map[string]any
}

type PluginEventCallback func(event PluginEvent)
```

### PluginEventNotifier

```go
func NewPluginEventNotifier() *PluginEventNotifier
func (n *PluginEventNotifier) Subscribe(callback PluginEventCallback) error
func (n *PluginEventNotifier) Unsubscribe(callback PluginEventCallback) error
func (n *PluginEventNotifier) Notify(event PluginEvent)
```

- `Subscribe` 拒绝 nil 回调。
- `Unsubscribe` 按函数指针比较匹配；未找到时报错。
- `Notify` 采用读时复制模式，回调可在迭代期间安全订阅/退订；每个回调包裹 panic 恢复——单个回调 panic 不影响其他回调或调用方。

## 类型化事件总线（Web，`ctx.events`）

`web/src/plugin-api/events.ts` — 核心事件表。后端插件与宿主发布类型包经声明合并扩展 `EventMap`，前端插件订阅自定义事件时自动获得载荷类型。

```ts
export interface EventMap {
  'message.committed': { turnID: number; message: SafeMessage }
  'message.streaming': { turnID: number; iteration: number; content: string }
  'turn.started': { turnID: number; trigger: TurnTrigger }
  'turn.ended': { turnID: number; outcome: 'ok' | 'cancelled' | 'error' }
  'session.switched': { session: SessionSummary }
  'progress.iteration': { iteration: number; tools: readonly ToolProgress[] }
  'context.compressed': { beforeTokens: number; afterTokens: number }
  'command.executed': { commandId: string; args: unknown }
}

export interface EventsAPI {
  on<K extends keyof EventMap>(name: K, handler: (payload: EventMap[K]) => void): Disposable
  once<K extends keyof EventMap>(name: K, handler: (payload: EventMap[K]) => void): Disposable
}
```

### 支撑类型

```ts
export interface SessionSummary {
  chatID: string
  title: string
  model: string
  busy: boolean
  maxContext: number
  tokenUsage: { prompt: number; completion: number }
}

export type TurnTrigger = 'user' | 'notification' | 'resume'
```

需要 `events` 权限（前端）——仅当声明了 `events` 时 `ctx.events` 才可用。

## 插件间事件总线（Go）

`PluginContext` 上的 `EventBusPublisher`：

```go
Subscribe(topic string, handler PluginEventHandler) error // "bus.plugin" + "bus.read"
Publish(topic string, data any) error                     // "bus.plugin" + "bus.write"
```

Topic 为字符串；无 schema 强制。总线按租户隔离（会话元数据设置后经 `PluginManager.EventBusFor(tenantID)` 解析）。

## 生命周期 Hook 事件

13 个 `HookEvent` 值（`PreToolUse`、`PostToolUse`、`PostToolUseFailure`、`UserPromptSubmit`、`AgentStop`、`SessionStart`、`SessionEnd`、`SubAgentStart`、`SubAgentStop`、`PreCompact`、`PostCompact`、`CronFired`、`WebhookReceived`）的完整说明见 [Hook 事件](hook-events/)。
