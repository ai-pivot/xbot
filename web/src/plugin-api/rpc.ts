/**
 * 类型化 RPC（§3.4）——方法表驱动，参数/返回值编译期校验。
 *
 * 后端插件发布类型包用声明合并扩展 `BackendRPC`，前端插件即可
 * `ctx.rpc.call('plugin-id.method', …)` 并获得精确的返回类型。
 */
export interface BackendRPC {
  'session.get': { params: { chatID: string }; result: SessionDetail }
  'session.list': { params: Record<string, never>; result: SessionSummary[] }
  'agent.send': {
    params: { chatID: string; content: string }
    result: { turnID: number; queued: boolean }
  }
  'agent.cancel': { params: { chatID: string }; result: Record<string, never> }
  'plugin.list': { params: Record<string, never>; result: PluginInfo[] }
  'plugin.get_config': {
    params: { id: string }
    result: { configuration: PluginConfigSchema; values: Record<string, unknown> }
  }
  'plugin.set_config': {
    params: { id: string; key: string; value: unknown }
    result: { status: string; key: string }
  }
  // ---- xbot.git-fancy：fancy Git 插件数据源 ----
  'git.status': {
    params: { channel: string; chatID: string }
    result: {
      branch: string
      repo_name: string
      changes: Array<{ path: string; status: string; added: number; deleted: number }>
      ahead: number
      behind: number
      commit_hash: string
      commit_msg: string
      is_repo: boolean
    }
  }
  'git.log': {
    params: { channel: string; chatID: string; limit?: number }
    result: { commits: Array<{ hash: string; author: string; when: string; subject: string }> }
  }
  'git.diff': {
    params: { channel: string; chatID: string; path: string }
    result: { path: string; content: string }
  }
  'git.branches': {
    params: { channel: string; chatID: string }
    result: { current: string; branches: string[] }
  }
}

export interface SessionDetail {
  chatID: string
  title: string
  model: string
  busy: boolean
  maxContext: number
  maxOutput: number
  tokenUsage: { prompt: number; completion: number }
  createdAt: string
}

export interface PluginInfo {
  id: string
  name: string
  version: string
  enabled: boolean
}

// 复用 events.ts 的 SessionSummary 类型（避免循环依赖）。
import type { SessionSummary } from './events'
import type { PluginConfigSchema } from './config'

export interface RPCAPI {
  /** 调用后端方法；方法名/参数/返回类型由 `BackendRPC` 驱动。 */
  call<K extends keyof BackendRPC>(
    method: K,
    params: BackendRPC[K]['params'],
  ): Promise<BackendRPC[K]['result']>
  /** 单向通知（不等待结果）。 */
  notify<K extends keyof BackendRPC>(method: K, params: BackendRPC[K]['params']): void
}
