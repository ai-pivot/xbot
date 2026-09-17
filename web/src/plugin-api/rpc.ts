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
  // ---- 用户累计用量（所有会话汇总）----
  'get_user_token_usage': {
    params: Record<string, never>
    result: UserTokenUsage
  }
  // ---- 分日期聚合用量（按天 + 模型）----
  'get_daily_token_usage': {
    params: { days?: number; sender_id?: string }
    result: DailyTokenUsage[]
  }
  // ---- 前台 shell 转后台（promote-to-background）----
  // 把当前会话正在前台执行的 shell 命令转入后台（用户在工具卡片上点"转后台"）。
  // tool_call_id 来自 progress 事件的 ActiveTools.call_id（运行中的 Shell 工具）。
  'promote_shell': {
    params: { session_key: string; tool_call_id?: string }
    result: { ok: boolean; task_id: string }
  }
  // ---- xbot.ssh-runner：SSH 纳管远程机器（VS Code Remote 式 SSH 管道）----
  // 插件后端方法（含点号 → 路由到插件进程）。
  // 模型：runner 不常驻远端 —— provision 只安装二进制；connect 由后端发起一条
  // SSH 会话，runner 跑在该会话前台（管道断 = runner 死），重连先杀老 runner
  // 再起新的；默认 tunnel（ssh -R 反向隧道，远端无需能访问 server）。
  'xbot.ssh-runner.probe': {
    params: { ssh: string }
    result: {
      os: string
      arch: string
      user: string
      is_root: boolean
      has_systemd: boolean
      has_curl: boolean
      has_wget: boolean
      installed_version: string
      install_dir: string
    }
  }
  'xbot.ssh-runner.provision': {
    params: {
      ssh: string
      name: string
      download_base: string
      install_dir: string
      dry_run?: boolean
    }
    /** 异步作业——立即返回 job_id，用 job_status 轮询。仅安装二进制，不启动任何服务。 */
    result: { job_id: string }
  }
  'xbot.ssh-runner.connect': {
    params: {
      ssh: string
      name: string
      /** 远端启动参数串（来自 runner_create 的 command，原样透传，不由前端拼 URL）。 */
      connect_cmd: string
      install_dir: string
      /** 'tunnel'（默认）：ssh -R 反向隧道，远端无需能访问 server；'direct'：runner 直连 server。 */
      connection_mode?: 'tunnel' | 'direct'
      /** true：后端持久化该目标，插件重启后自动重新连接（自愈）。 */
      auto_connect?: boolean
    }
    /** 立即返回（supervisor 已武装，SSH 会话在后端后台存活）——用 status 轮询直到 connected。 */
    result: {
      connected: boolean
      mode: string
      remote_port?: number
      restarts: number
      connected_at?: string
      last_error?: string
    }
  }
  'xbot.ssh-runner.disconnect': {
    params: { ssh: string; name: string }
    result: { connected: boolean }
  }
  'xbot.ssh-runner.job_status': {
    params: { job_id: string }
    result: {
      state: 'running' | 'done' | 'failed'
      steps: Array<{ name: string; ok: boolean; detail: string }>
      error: string
    }
  }
  'xbot.ssh-runner.deprovision': {
    params: { ssh: string; name: string; uninstall: boolean }
    result: { job_id: string }
  }
  'xbot.ssh-runner.status': {
    params: { ssh: string; name: string }
    result: {
      installed_version: string
      /** 连接态权威来源：connected / reconnecting（supervisor 在重连）/ disconnected。 */
      service_state: 'connected' | 'reconnecting' | 'disconnected'
      detail: string
      connected: boolean
      connection_mode: string
      restarts: number
      connected_at: string
      remote_port: number
      last_error: string
    }
  }
  'xbot.ssh-runner.logs': {
    params: { ssh: string; name: string; lines: number }
    result: { lines: string[]; source: 'ssh-session' | 'remote-log' }
  }
  // 核心 runner 注册表 / 会话目标（无点号 → 核心 RPC；单用户全局，无用户维度）。
  'runner_create': {
    params: {
      name: string
      mode?: string
      docker_image?: string
      workspace?: string
      /** 既有 runner 的 LLM 配置回传（re-key 时保留，不让面板的连接动作重置机器设置）。 */
      llm_provider?: string
      llm_api_key?: string
      llm_model?: string
      llm_base_url?: string
    }
    /** command = 远端启动参数串（--server ws://… --token …），原样传给 xbot.ssh-runner.connect.connect_cmd。 */
    result: { name: string; token: string; command: string }
  }
  'runner_list': {
    params: Record<string, never>
    result: {
      runners: Array<{
        name: string
        mode: string
        docker_image: string
        workspace: string
        online: boolean
        created_at: string
        version?: string
        // The runner may declare a local LLM (omitempty on the server side).
        llm_provider?: string
        llm_api_key?: string
        llm_model?: string
        llm_base_url?: string
      }>
    }
  }
  'runner_delete': { params: { name: string }; result: Record<string, never> }
  'runner_session_get': {
    params: { channel: string; chat_id: string }
    result: { name: string; online: boolean }
  }
  'runner_session_set': {
    params: { channel: string; chat_id: string; name: string }
    result: Record<string, never>
  }
}

// ---- 会话用量/性能聚合（对应 Go sqlite.TenantUsageStats JSON）----

/** 用户累计 token 用量（所有会话汇总，对应 Go sqlite.UserTokenUsage）。 */
export interface UserTokenUsage {
  sender_id: string
  input_tokens: number
  output_tokens: number
  total_tokens: number
  cached_tokens: number
  conversation_count: number
  llm_call_count: number
}

/** 分日期 token 用量（按天 + 模型，对应 Go sqlite.DailyTokenUsage）。 */
export interface DailyTokenUsage {
  date: string
  sender_id: string
  model: string
  input_tokens: number
  output_tokens: number
  cached_tokens: number
  conversation_count: number
  llm_call_count: number
}

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
