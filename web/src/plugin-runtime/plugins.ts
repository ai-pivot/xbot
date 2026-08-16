/**
 * PluginsAPI 实现——插件互操作（§3.7）。
 *
 * get/require 从 registry 取已激活插件的 exports；onActivated/onDeactivated
 * 订阅依赖插件的动态上下线（热加载时降级/恢复）。
 */
import type { Disposable, PluginsAPI, PluginExportsMap } from '@/plugin-api'
import type { ContributionRegistry } from './registry'

export class PluginInterop implements PluginsAPI {
  private listeners = new Map<string, Set<(api: unknown) => void>>()
  private deactivatedListeners = new Map<string, Set<() => void>>()
  private readonly registry: ContributionRegistry

  constructor(registry: ContributionRegistry) {
    this.registry = registry
  }

  get<K extends keyof PluginExportsMap>(id: K): PluginExportsMap[K] | undefined {
    const exp = this.registry.getExports(id as string)
    return exp as PluginExportsMap[K] | undefined
  }

  async require<K extends keyof PluginExportsMap>(id: K): Promise<PluginExportsMap[K]> {
    const existing = this.get(id)
    if (existing) return existing
    // 依赖未激活：交给宿主按需激活（懒加载入口）。
    const activator = (this.registry as unknown as { ensureActive?: (id: string) => Promise<void> }).ensureActive
    if (activator) {
      await activator(id as string)
      const after = this.get(id)
      if (after) return after
    }
    throw new Error(`插件 ${String(id)} 未激活且无法按需激活`)
  }

  onActivated<K extends keyof PluginExportsMap>(id: K, h: (api: PluginExportsMap[K]) => void): Disposable {
    let set = this.listeners.get(id as string)
    if (!set) {
      set = new Set()
      this.listeners.set(id as string, set)
    }
    const fn = h as (api: unknown) => void
    set.add(fn)
    // 若已激活，立即通知一次（订阅者拿到当前 API）。
    const cur = this.get(id)
    if (cur !== undefined) fn(cur)
    return () => {
      set.delete(fn)
    }
  }

  onDeactivated<K extends keyof PluginExportsMap>(id: K, h: () => void): Disposable {
    let set = this.deactivatedListeners.get(id as string)
    if (!set) {
      set = new Set()
      this.deactivatedListeners.set(id as string, set)
    }
    set.add(h)
    return () => {
      set.delete(h)
    }
  }

  /** 宿主在 registry 状态变化时调用。 */
  notifyActivated(id: string): void {
    const cur = this.get(id as keyof PluginExportsMap)
    if (cur === undefined) return
    for (const h of this.listeners.get(id) ?? []) h(cur)
  }

  notifyDeactivated(id: string): void {
    for (const h of this.deactivatedListeners.get(id) ?? []) h()
  }
}
