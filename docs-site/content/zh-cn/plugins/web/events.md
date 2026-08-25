---
title: "类型化事件总线"
weight: 5
---

类型化事件总线使用**事件表上的索引访问**：订阅 `'message.committed'` 时载荷类型自动推导为 `{ turnID: number; message: SafeMessage }`——零 cast。定义于 `web/src/plugin-api/events.ts`。

## EventMap

```ts
/** turn 触发器。 */
export type TurnTrigger = 'user' | 'notification' | 'resume'

/** 会话摘要（sanitize 副本）。 */
export interface SessionSummary {
  chatID: string
  title: string
  model: string
  busy: boolean
  maxContext: number
  tokenUsage: { prompt: number; completion: number }
}

/** 核心事件表：后端/其他插件用声明合并扩展。 */
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
```

## EventsAPI

```ts
export interface EventsAPI {
  /** 订阅事件；返回 disposable。载荷类型由事件名索引 EventMap 自动推导。 */
  on<K extends keyof EventMap>(name: K, handler: (payload: EventMap[K]) => void): Disposable
  /** 一次性订阅。 */
  once<K extends keyof EventMap>(name: K, handler: (payload: EventMap[K]) => void): Disposable
}
```

`SafeMessage`（`web/src/plugin-api/safe.ts`）是 sanitize 后的公共消息类型——插件永远接触不到内部字段（`persisted`/`eventSeq`/`dbID`/…）：

```ts
export interface SafeMessage {
  id: number
  turnID: number
  role: 'user' | 'assistant' | 'system' | 'tool'
  content: string
  createdAt: string
}

export interface SafeAssistantMessage extends SafeMessage {
  role: 'assistant'
  iterations?: readonly SafeIteration[]
  reasoning?: string
}

export interface SafeUserMessage extends SafeMessage {
  role: 'user'
  /** 是否为通知注入（🔔）。 */
  isNotification?: boolean
}
```

## 运行时实现

`PluginEventBus`（`web/src/plugin-runtime/events.ts`）实现 `EventsAPI`：

- **宽类型内部存储**：handler 以 `(payload: never) => void`（擦除形态）存储。泛型只在外层 API 签名上——对外强类型、对内宽松存储，避免协变陷阱。
- **按插件归属**：内部 `subscribe(pluginId, name, handler)` 把订阅绑定到插件；`unsubscribePlugin(pluginId)` 在卸载时移除该插件的全部订阅（热加载要求）。
- **崩溃隔离**：handler 抛错不打断其他订阅者——错误记录后继续投递。投递前拷贝订阅集合，handler 内退订/重入安全。
- **`once`** 包装 `subscribe`，触发时自退订。

## 后端事件桥

后端把 agent 生命周期事件以 `web_plugin_event` WS 消息推给前端。`PluginRuntimeBootstrap`（`web/src/plugin-runtime/usePluginRuntimeHost.ts`）解析 `{ name, payload }` 后经总线重投：

```ts
} else if (msg.type === 'web_plugin_event') {
  const evt = JSON.parse(msg.content ?? '{}') as { name?: string; payload?: unknown }
  if (evt.name) {
    // 后端事件载荷是运行时 JSON，类型在插件订阅侧由 EventMap 保证；
    // 这里以 unknown 擦除后投递。
    runtime.events.emit(evt.name as keyof import('@/plugin-api').EventMap, evt.payload as never)
  }
}
```

插件只能**订阅**——发布是宿主内部行为（`emit` 不在 `EventsAPI` 上暴露）。

## 声明合并扩展

后端插件发布 `.d.ts` 类型包，向 `EventMap` 合并自定义事件；前端插件 import 类型包后自动获得载荷类型：

```ts
// 插件类型扩展包（如 @xbot/plugin-myplugin-types）
declare module '@xbot/plugin-api' {
  interface EventMap {
    'myplugin.data.arrived': { batch: number; rows: readonly string[] }
  }
}
```

## 示例

```ts
export function activate(ctx: PluginContext<typeof manifest.permissions>) {
  const disposables = [
    ctx.events.on('turn.started', (ev) => {
      // ev: { turnID: number; trigger: 'user' | 'notification' | 'resume' }
      console.log(`turn ${ev.turnID} started by ${ev.trigger}`)
    }),
    ctx.events.once('context.compressed', (ev) => {
      // ev: { beforeTokens: number; afterTokens: number }
      console.log(`compressed ${ev.beforeTokens} → ${ev.afterTokens}`)
    }),
  ]
  return () => disposables.forEach((d) => d())
}
```
