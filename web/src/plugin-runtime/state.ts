/**
 * 状态快照实现——只读，结构化克隆返回。
 *
 * 插件永远拿不到内部对象引用（structuredClone），无法窥探后续变化。
 */
import type { StateAPI } from '@/plugin-api'
import type { SafeMessage, SafeMessageFactory } from '@/plugin-api'
import type { SessionSummary } from '@/plugin-api'

export interface StateSources {
  getSession(): SessionSummary | null
  getMessagesRaw(): readonly unknown[]
  toSafeMessage: SafeMessageFactory
  getPlugins(): readonly { id: string; version: string; enabled: boolean }[]
}

export class PluginState implements StateAPI {
  private readonly sources: StateSources

  constructor(sources: StateSources) {
    this.sources = sources
  }

  getSession(): SessionSummary | null {
    const s = this.sources.getSession()
    return s ? (structuredClone(s) as SessionSummary) : null
  }

  getMessages(options?: { limit?: number; before?: number }): readonly SafeMessage[] {
    const raw = this.sources.getMessagesRaw()
    let msgs = raw.map((m) => this.sources.toSafeMessage(m))
    if (options?.before) {
      msgs = msgs.filter((m) => m.id < (options.before as number))
    }
    if (options?.limit) {
      msgs = msgs.slice(-options.limit)
    }
    return structuredClone(msgs) as SafeMessage[]
  }

  getPlugins(): readonly { id: string; version: string; enabled: boolean }[] {
    return structuredClone(this.sources.getPlugins())
  }
}
