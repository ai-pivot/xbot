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
  // 核心 RPC（无点号）——插件配置的 schema + 值。注意：绝不能用 'plugin.get_config'
  // 这类含点号的名字，否则 FetchRpcTransport 会把它误路由到 web_plugin_rpc（插件
  // 进程方法），导致 ctx.config.get() 静默失败。
  'plugin_config': {
    params: { id?: string }
    result: {
      plugins: Array<{
        id: string
        name: string
        properties: Record<string, unknown>
        values: Record<string, unknown>
      }>
    }
  }
  'plugin_config_set': {
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
  // ---- 会话用量统计（iteration_history v59 聚合：input/cached tokens + model）----
  'get_session_usage_stats': {
    params: { channel?: string; chat_id: string; limit?: number }
    result: TenantUsageStats
  }
}

// ---- 会话用量/性能聚合（对应 Go sqlite.TenantUsageStats JSON）----

export interface UsageModelRow {
  model: string
  iterations: number
  turns: number
  input_tokens: number
  output_tokens: number
  cached_tokens: number
  avg_ttft_ms: number
  avg_tpot_ms: number
}

export interface UsageIterationRow {
  turn_id: number
  iteration: number
  input_tokens: number
  output_tokens: number
  cached_tokens: number
  ttft_ms: number
  tpot_ms: number
  tokens_per_sec: number
  total_ms: number
  model: string
  created_at: string
}

export interface TenantUsageStats {
  iteration_count: number
  turn_count: number
  input_tokens: number
  output_tokens: number
  cached_tokens: number
  llm_total_ms: number
  avg_ttft_ms: number
  avg_tpot_ms: number
  avg_tokens_per_sec: number
  first_iteration_at: string
  last_iteration_at: string
  /** 当前上下文水位（tenant_state.last_prompt/completion_tokens）。 */
  last_prompt_tokens: number
  last_completion_tokens: number
  current_model: string
  session_created_at: string
  session_last_active: string
  by_model: UsageModelRow[] | null
  recent_iterations: UsageIterationRow[] | null
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

export interface RPCAPI {
  /** 调用后端方法；方法名/参数/返回类型由 `BackendRPC` 驱动。 */
  call<K extends keyof BackendRPC>(
    method: K,
    params: BackendRPC[K]['params'],
  ): Promise<BackendRPC[K]['result']>
  /** 单向通知（不等待结果）。 */
  notify<K extends keyof BackendRPC>(method: K, params: BackendRPC[K]['params']): void
}
