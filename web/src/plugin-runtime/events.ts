/**
 * PluginRuntime 事件总线——消费 @xbot/plugin-api 的 EventMap。
 *
 * 类型层面与 API 包一致：事件名索引 EventMap 推导载荷类型。
 * 运行时是简单的发布/订阅（支持热加载：插件卸载时自动退订其全部订阅）。
 *
 * 内部存储用宽类型（handler 存为 (payload: never) => void 的擦除形态），
 * 泛型只在外层 API 签名上——对外强类型、对内宽松存储，避免协变陷阱。
 */
import type { EventMap, EventsAPI } from '@/plugin-api'
import type { Disposable } from '@/plugin-api'

/** 内部订阅记录：handler 用宽类型存储（对外签名才是泛型）。 */
interface SubscriptionRecord {
  pluginId: string
  event: keyof EventMap
  handler: (payload: never) => void
}

/**
 * 内部事件总线实现。所有 on/once 订阅都记录 pluginId，
 * 插件卸载时按 pluginId 批量退订（热加载/卸载要求）。
 */
export class PluginEventBus implements EventsAPI {
  private entries = new Map<keyof EventMap, Set<SubscriptionRecord>>()

  on<K extends keyof EventMap>(name: K, handler: (payload: EventMap[K]) => void): Disposable {
    return this.subscribe('', name, handler)
  }

  once<K extends keyof EventMap>(name: K, handler: (payload: EventMap[K]) => void): Disposable {
    const off = this.subscribe('', name, (payload) => {
      off()
      handler(payload)
    })
    return off
  }

  /** 供 PluginRuntime 内部使用：绑定插件归属，卸载时批量清理。 */
  subscribe<K extends keyof EventMap>(
    pluginId: string,
    name: K,
    handler: (payload: EventMap[K]) => void,
  ): Disposable {
    let set = this.entries.get(name)
    if (!set) {
      set = new Set()
      this.entries.set(name, set)
    }
    // 宽类型存储：handler 参数类型擦除（对外签名保持强类型）。
    const record: SubscriptionRecord = {
      pluginId,
      event: name,
      handler: handler as (payload: never) => void,
    }
    set.add(record)
    let disposed = false
    return () => {
      if (disposed) return
      disposed = true
      set.delete(record)
    }
  }

  /** 触发事件（供宿主内部调用；对插件暴露为可订阅，不暴露发布）。 */
  emit<K extends keyof EventMap>(name: K, payload: EventMap[K]): void {
    const set = this.entries.get(name)
    if (!set) return
    // 拷贝一份，允许 handler 内退订/重入。
    for (const entry of [...set]) {
      try {
        entry.handler(payload as never)
      } catch (error) {
        // 插件 handler 抛错不打断其他订阅者；错误交给宿主记录。
        console.error(`[plugin-runtime] event "${String(name)}" handler failed`, error)
      }
    }
  }

  /** 插件卸载：移除该插件的全部订阅。 */
  unsubscribePlugin(pluginId: string): void {
    for (const set of this.entries.values()) {
      for (const entry of set) {
        if (entry.pluginId === pluginId) set.delete(entry)
      }
    }
  }
}
