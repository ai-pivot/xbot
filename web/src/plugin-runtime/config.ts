/**
 * ConfigService —— 前端插件配置能力（§3.8）。
 *
 * 单例持有 per-pluginId 的变更监听器；`forPlugin()` 为每个激活插件构建
 * 绑定其 pluginId 的 ConfigAPI。宿主收到 web_plugin_config_changed 消息时
 * 调 `notifyChanged()` 分发到对应插件。
 */
import type { ConfigAPI } from '@/plugin-api'
import type { RPCAPI } from '@/plugin-api'
import type { Disposable } from '@/plugin-api'

export class PluginConfigService {
  private listeners = new Map<string, Set<(config: Record<string, unknown>) => void>>()
  private rpc: RPCAPI

  constructor(rpc: RPCAPI) {
    this.rpc = rpc
  }

  /** 为指定插件构建绑定其 pluginId 的 ConfigAPI。 */
  forPlugin(pluginId: string): ConfigAPI {
    let set = this.listeners.get(pluginId)
    if (!set) {
      set = new Set()
      this.listeners.set(pluginId, set)
    }
    const handlers = set
    return {
      get: () =>
        this.rpc.call('plugin.get_config', { id: pluginId }).then((res) => res.values),
      set: (key, value) =>
        this.rpc.call('plugin.set_config', { id: pluginId, key, value }).then(() => undefined),
      onConfigChange: (handler) => {
        handlers.add(handler)
        return () => {
          handlers.delete(handler)
        }
      },
    } satisfies ConfigAPI
  }

  /** 后端推送配置变更（web_plugin_config_changed）时分发到对应插件。 */
  notifyChanged(pluginId: string, config: Record<string, unknown>): void {
    for (const h of this.listeners.get(pluginId) ?? []) h(config)
  }
}

export type { Disposable }
