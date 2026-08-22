/**
 * 类型化事件总线（§3.3）——事件名 ↔ 载荷的索引访问。
 *
 * 后端插件/宿主发布类型包扩展 `EventMap`（声明合并）即可让前端插件
 * 订阅自定义事件并自动获得载荷类型。
 */
import type { Disposable } from './manifest'
import type { SafeMessage } from './safe'
import type { ToolProgress } from './progress'

/** 会话摘要（sanitize 副本）。 */
export interface SessionSummary {
  chatID: string
  title: string
  model: string
  busy: boolean
  maxContext: number
  tokenUsage: { prompt: number; completion: number }
}

/** turn 触发器。 */
export type TurnTrigger = 'user' | 'notification' | 'resume'

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

export interface EventsAPI {
  /** 订阅事件；返回 disposable。载荷类型由事件名索引 `EventMap` 自动推导。 */
  on<K extends keyof EventMap>(name: K, handler: (payload: EventMap[K]) => void): Disposable
  /** 一次性订阅。 */
  once<K extends keyof EventMap>(name: K, handler: (payload: EventMap[K]) => void): Disposable
}
