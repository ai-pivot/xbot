/**
 * xbot.iteration-stats 桥接层 —— 插件能力（i18n / rpc / config / events）与
 * 会话身份的唯一持有者。
 *
 * 契约（与 xbot.ssh-runner / xbot.git-fancy 同范式）：
 *   - 本模块**不 import 任何宿主内部模块的运行时值**（类型用 `import type`，编译期擦除）；
 *   - `activate(ctx)` 把 ctx 存进模块级单例（`setPluginCtx`），视图组件从单例读能力；
 *   - **i18n 文案随插件分发**（自己的 `plugin.json#web.i18n`），
 *     命中失败逐级回退到调用点写的中文兜底 —— UI 永不显示裸 key；
 *   - 会话身份来自宿主注入的 `window.__xbot_session__`：**未就绪时绝不伪造身份**
 *     （伪造 chatID 会让后端把读请求落到错误会话上）。
 */
import type { BackendRPC } from '@/plugin-api'

// ── React（宿主注入 window.React —— 独立 bundle 不重复打包 React）────────────

const hostWindow = window as unknown as { React: typeof import('react') }

/** 宿主注入的 React（与 xbot.git-fancy / xbot.ssh-runner 同一范式）。 */
export const React = hostWindow.React

// ── ctx 单例 ───────────────────────────────────────────────────────────────

/** 插件自带文案解析器（宿主注入；见 PluginContext.i18n 的契约）。 */
interface I18nLike {
  readonly locale?: string
  t(key: string, fallback?: string): string
}

type RpcCall = (method: string, params: Record<string, unknown>) => Promise<unknown>

interface PluginCtxLike {
  i18n?: I18nLike
  rpc?: { call: RpcCall }
  config?: {
    get(): Promise<Record<string, unknown>>
    onConfigChange?: (handler: (config: Record<string, unknown>) => void) => () => void
  }
  events?: { on(event: string, handler: (payload: unknown) => void): () => void }
}

let ctxRef: PluginCtxLike | null = null

/** activate(ctx) 调用——注入能力到模块级单例（宿主在视图 mount 前调用）。 */
export function setPluginCtx(ctx: unknown): void {
  ctxRef = (ctx as PluginCtxLike | null | undefined) ?? null
}

/** 当前宿主语言（BCP-47）；ctx 未注入时回落 'en'（不谎称中文）。 */
export function pluginLocale(): string {
  return ctxRef?.i18n?.locale ?? 'en'
}

/**
 * 取插件自己的文案：命中 `plugin.json#web.i18n` 则用表里的值，否则回退
 * `fallback`（调用点写的中文原文）；`{{param}}` 占位符在本函数内插值。
 */
export function t(key: string, fallback: string, params?: Record<string, string | number>): string {
  let text = fallback
  const inst = ctxRef?.i18n
  if (inst) {
    try {
      const resolved = inst.t(key, fallback)
      if (typeof resolved === 'string' && resolved !== '') text = resolved
    } catch {
      /* 解析异常 ⇒ 回退中文原文 */
    }
  }
  if (params) {
    return text.replace(/\{\{(\w+)\}\}/g, (_, k: string) => String(params[k] ?? ''))
  }
  return text
}

// ── 会话身份 ───────────────────────────────────────────────────────────────

export interface SessionIdentity {
  channel: string
  chatID: string
}

/**
 * 从宿主注入的 `window.__xbot_session__` 解析当前会话身份。
 *
 * 未就绪（布局恢复的 tab 可能先于会话加载 mount）返回 **null** —— 调用方必须
 * 延迟到身份就绪（`session.switched` 事件 / 轮询重读）再发 RPC，**不得伪造**。
 */
export function resolveSession(): SessionIdentity | null {
  const s = (window as unknown as { __xbot_session__?: { channel?: string; chatID?: string } })
    .__xbot_session__
  if (s?.chatID) return { channel: s.channel ?? 'web', chatID: s.chatID }
  return null
}

// ── RPC ───────────────────────────────────────────────────────────────────

/** `get_session_usage_stats` 的返回（会话用量聚合 + 最近迭代明细）。 */
export type SessionUsageStats = BackendRPC['get_session_usage_stats']['result']

/** 单次查询返回的明细条数上限（服务端硬钳制到 500 —— 见 storage/sqlite/session.go）。 */
export const USAGE_DETAIL_LIMIT = 500

/**
 * 拉取当前会话的用量统计。
 * 身份未就绪 ⇒ 抛 `SessionNotReadyError`（调用方进入等待态，不是错误重试）。
 */
export async function fetchSessionUsage(limit: number = USAGE_DETAIL_LIMIT): Promise<SessionUsageStats> {
  const chat = resolveSession()
  if (!chat) throw new SessionNotReadyError()
  const rpc = ctxRef?.rpc
  if (!rpc) throw new Error('插件 RPC 未注入（检查 plugin.json permissions 是否含 "rpc"）')
  const res = await rpc.call('get_session_usage_stats', {
    channel: chat.channel,
    chat_id: chat.chatID,
    limit,
  })
  return res as SessionUsageStats
}

/** 会话身份尚未就绪——调用方应进入等待态（订阅 session.switched / 轮询），不是错误重试。 */
export class SessionNotReadyError extends Error {
  constructor() {
    super('session identity not ready (window.__xbot_session__ is empty)')
    this.name = 'SessionNotReadyError'
  }
}

// ── 刷新信号（turn.ended / session.switched → 面板）────────────────────────

const refreshListeners = new Set<() => void>()

/** 面板订阅"数据可能已变化"信号；返回退订函数。 */
export function subscribeRefresh(cb: () => void): () => void {
  refreshListeners.add(cb)
  return () => {
    refreshListeners.delete(cb)
  }
}

function notifyRefresh(): void {
  refreshListeners.forEach((f) => {
    try {
      f()
    } catch (error) {
      console.error('[iteration-stats] refresh listener failed', error)
    }
  })
}

/**
 * 订阅宿主事件：turn 结束（usage 已落库）⇒ 刷新；会话切换 ⇒ 身份变了 ⇒ 刷新。
 * 无 `events` 权限时静默降级（面板仍可手动刷新）。
 */
export function wireRefreshEvents(): void {
  const events = ctxRef?.events
  if (!events) return
  try {
    events.on('turn.ended', () => notifyRefresh())
    events.on('session.switched', () => notifyRefresh())
  } catch (error) {
    console.warn('[iteration-stats] 事件订阅失败（缺 events 权限？）', error)
  }
}

// ── 配置（showTTFT：徽章是否显示首 token 延迟）─────────────────────────────

export interface PluginConfig {
  showTTFT: boolean
}

const DEFAULT_CONFIG: PluginConfig = { showTTFT: true }

let configSnapshot: PluginConfig = DEFAULT_CONFIG
const configListeners = new Set<() => void>()

export function getConfigSnapshot(): PluginConfig {
  return configSnapshot
}

export function subscribeConfig(cb: () => void): () => void {
  configListeners.add(cb)
  return () => {
    configListeners.delete(cb)
  }
}

function applyConfig(raw: Record<string, unknown> | null | undefined): void {
  configSnapshot = { showTTFT: raw?.showTTFT !== false }
  configListeners.forEach((f) => f())
}

/** activate 时调用：读一次配置 + 订阅变更（设置面板改动实时生效，无需重载视图）。 */
export function wireConfig(): void {
  const cfg = ctxRef?.config
  if (!cfg) return
  void cfg
    .get()
    .then(applyConfig)
    .catch(() => {})
  cfg.onConfigChange?.(applyConfig)
}

/** 测试用：重置模块级单例（vitest 每个用例之间隔离）。 */
export function __resetIterationStatsBridgeForTests(): void {
  ctxRef = null
  configSnapshot = DEFAULT_CONFIG
  configListeners.clear()
  refreshListeners.clear()
}
