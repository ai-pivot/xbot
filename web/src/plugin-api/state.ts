/**
 * 只读状态快照（§8.2）——结构化克隆返回，插件无法保留引用窥探后续变化。
 */
import type { SafeMessage } from './safe'
import type { SessionSummary } from './events'

export interface StateAPI {
  /** 当前会话摘要（只读快照）。 */
  getSession(): SessionSummary | null
  /** 分页只读消息列表（sanitize 副本）。 */
  getMessages(options?: { limit?: number; before?: number }): readonly SafeMessage[]
  /** 所有插件状态。 */
  getPlugins(): readonly { id: string; version: string; enabled: boolean }[]
}
