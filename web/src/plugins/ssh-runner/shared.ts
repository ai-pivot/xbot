/**
 * xbot.ssh-runner 共享模块 —— 类型 + RPC/配置桥 + 会话身份解析 + 作业/连接轮询。
 *
 * 范式与 xbot.git-fancy 一致：本模块不 import 任何宿主内部模块 —— React 从
 * window 获取（宿主 plugin-runtime 在加载插件产物前注入 window.React /
 * window.__xbot_i18n__），rpc/config 能力在 activate(ctx) 时注入模块级单例。
 * 类型经 `import type` 引用宿主 BackendRPC 声明（类型擦除，产物零运行时依赖）。
 *
 * 模型（VS Code Remote 式 SSH 管道）：
 *   runner 不常驻远端。每次 connect 由后端发起一条 SSH 会话，runner 跑在该会话
 *   的前台（管道断 = runner 死）；重连先杀老 runner 再起新的。默认 tunnel 模式
 *   用 ssh -R 反向隧道把 server 端口转发到远端（远端无需能访问 server）；
 *   direct 模式才是 runner 直连 server（显式选择，无静默回退）。
 *
 * 构建：esbuild --bundle --format=esm --jsx=transform（React external）。
 */
import type { BackendRPC } from '@/plugin-api'

const w = window as unknown as {
  React: typeof import('react')
}

export const React = w.React

// ---------- i18n 桥（独立 bundle 无法 import 宿主 '@/i18n'） ----------

/**
 * 翻译 helper：**优先用插件自己的文案表**（`ctx.i18n`，表来自插件清单的 `web.i18n`），
 * 命中后在本函数内插值 `{{x}}` 占位符；ctx 尚未注入或 key 缺失时回退第二参数
 *（调用点写的是中文原文）⇒ UI 永不显示裸 key。
 *
 * ⛔ 2026-09-19（用户要求「插件 i18n 应该是插件通用功能」）：**不再借用宿主的
 * `window.__xbot_i18n__` / `plugins.sshRunner.*` 命名空间** —— 插件文案随插件分发，
 * 否则插件无法独立安装/卸载，且会污染宿主的 i18n 命名空间。
 */
export function t(key: string, fallback: string, params?: Record<string, string | number>): string {
  let text = fallback
  const inst = ctxRef?.i18n
  if (inst) {
    try {
      text = inst.t(key, fallback)
    } catch {
      /* 解析异常 ⇒ 回退中文原文 */
    }
  }
  if (params) {
    return text.replace(/\{\{(\w+)\}\}/g, (_, k: string) => String(params[k] ?? ''))
  }
  return text
}

// ---------- 类型（BackendRPC 声明的别名——契约单一来源） ----------

/** 远端环境报告（probe）。 */
export type ProbeResult = BackendRPC['xbot.ssh-runner.probe']['result']
/** 作业步骤（provision/deprovision 共用）。 */
export type JobStep = BackendRPC['xbot.ssh-runner.job_status']['result']['steps'][number]
/** 异步作业状态；state !== 'running' 即终态——轮询必须停止。 */
export type JobStatus = BackendRPC['xbot.ssh-runner.job_status']['result']
/** runner_list 单项（不含 token）。 */
export type RunnerInfo = BackendRPC['runner_list']['result']['runners'][number]
/** 远端连接状态（诊断 / 连接徽章的唯一权威来源）。 */
export type RemoteStatus = BackendRPC['xbot.ssh-runner.status']['result']

/** 连接方式：tunnel（默认，ssh -R 反向隧道）| direct（runner 直连 server）。 */
export type ConnectionMode = 'tunnel' | 'direct'

/** 连接徽章三态（来自 status.service_state）。 */
export type ConnectionState = 'connected' | 'reconnecting' | 'disconnected'

/** 规范化连接方式（未知值一律回落 tunnel——不猜、不静默选 direct）。 */
export function normalizeConnectionMode(raw: unknown): ConnectionMode {
  return raw === 'direct' ? 'direct' : 'tunnel'
}

/**
 * 连接徽章状态：以 connected 布尔为权威，service_state 作补充。
 * 后端在 supervisor 未武装（从未连接/已断开）时返回 service_state='disconnected'、
 * connection_mode=''，因此空 status 也归入 disconnected。
 */
export function connectionStateOf(status: RemoteStatus | null | undefined): ConnectionState {
  if (!status) return 'disconnected'
  if (status.connected === true || status.service_state === 'connected') return 'connected'
  if (status.service_state === 'reconnecting') return 'reconnecting'
  return 'disconnected'
}

// ---------- 面板配置 / 目标 ----------

/**
 * 已纳管目标（plugin config `targets` JSON 数组的元素）。
 *
 * 字段与既有条目同风格（snake_case）；`parseTargets` 同时接受 camelCase 别名
 * （connectionMode / autoConnect），写入统一用 `serializeTargets` 的规范形态。
 */
export interface MachineTarget {
  name: string
  ssh: string
  install_dir: string
  connection_mode: ConnectionMode
  /** true：面板挂载后自动连接（并且 connect 时透传给后端做插件重启自愈）。 */
  auto_connect: boolean
  added_at: string
}

export interface RunnerConfigValues {
  downloadBase: string
  installDir: string
  /** 新增机器表单/连接的默认连接方式（`serviceMode` 键已废弃）。 */
  connectionMode: ConnectionMode
  targets: string
}

/** manifest 声明的默认值（config.get() 缺键时兜底——与 plugin.json 一致）。 */
export const DEFAULT_CONFIG_VALUES: RunnerConfigValues = {
  downloadBase: 'https://github.com/ai-pivot/xbot/releases/latest/download',
  installDir: '/usr/local/bin',
  connectionMode: 'tunnel',
  targets: '[]',
}

const CONFIG_KEYS = ['downloadBase', 'installDir', 'targets'] as const

/** 合并 config.get() 的原始值 + manifest 默认值（空串/非字符串视为缺省）。 */
export function mergeConfigValues(raw: Record<string, unknown> | null | undefined): RunnerConfigValues {
  const values: RunnerConfigValues = { ...DEFAULT_CONFIG_VALUES }
  for (const key of CONFIG_KEYS) {
    const v = raw?.[key]
    if (typeof v === 'string' && v.trim() !== '') values[key] = v
  }
  values.connectionMode = normalizeConnectionMode(raw?.['connectionMode'])
  return values
}

function truthyFlag(v: unknown): boolean {
  return v === true || v === 'true'
}

/** 解析 targets JSON；损坏条目安全跳过（配置可被用户手改，不能崩面板）。 */
export function parseTargets(raw: string): MachineTarget[] {
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    return []
  }
  if (!Array.isArray(parsed)) return []
  const out: MachineTarget[] = []
  for (const item of parsed) {
    if (!item || typeof item !== 'object') continue
    const rec = item as Record<string, unknown>
    if (typeof rec.name !== 'string' || rec.name === '') continue
    if (typeof rec.ssh !== 'string' || rec.ssh === '') continue
    out.push({
      name: rec.name,
      ssh: rec.ssh,
      install_dir: typeof rec.install_dir === 'string' ? rec.install_dir : '',
      connection_mode: normalizeConnectionMode(rec.connection_mode ?? rec.connectionMode),
      auto_connect: truthyFlag(rec.auto_connect ?? rec.autoConnect),
      added_at: typeof rec.added_at === 'string' ? rec.added_at : '',
    })
  }
  return out
}

/** 序列化 targets（只写已知字段——用户手加的杂键不落盘，字段形态保持规范）。 */
export function serializeTargets(targets: MachineTarget[]): string {
  return JSON.stringify(
    targets.map((t) => ({
      name: t.name,
      ssh: t.ssh,
      install_dir: t.install_dir,
      connection_mode: t.connection_mode,
      auto_connect: t.auto_connect,
      added_at: t.added_at,
    })),
  )
}

// ---------- RPC / 配置桥（activate 注入） ----------

type RpcCall = (method: string, params: Record<string, unknown>) => Promise<unknown>

/** activate(ctx) 注入的 ctx 形状——仅取本面板用到的能力（rpc / config）。 */
export interface SshRunnerCtx {
  rpc?: { call: RpcCall }
  /** 插件自带文案解析器（宿主注入；见 PluginContext.i18n 的契约）。 */
  i18n?: { t(key: string, fallback?: string): string }
  config?: {
    get(): Promise<Record<string, unknown>>
    set(key: string, value: unknown): Promise<void>
    onConfigChange(handler: (config: Record<string, unknown>) => void): () => void
  }
}

let ctxRef: SshRunnerCtx | null = null

/** activate(ctx) 调用——注入能力到模块级单例（宿主在视图 mount 前调用）。 */
export function setCtx(ctx: unknown): void {
  ctxRef = (ctx as SshRunnerCtx | null | undefined) ?? null
}

export function getCtx(): SshRunnerCtx | null {
  return ctxRef
}

function getRpc(): RpcCall | null {
  const c = ctxRef
  if (!c?.rpc) return null
  return (method, params) => c.rpc!.call(method, params)
}

/** 调用 RPC —— 方法名/参数/返回类型由 BackendRPC 声明驱动。 */
export async function callRpc<K extends keyof BackendRPC>(
  method: K,
  params: BackendRPC[K]['params'],
): Promise<BackendRPC[K]['result']> {
  const rpc = getRpc()
  if (!rpc) throw new Error(t('notInitialized', 'Remote Machines 插件尚未初始化（ctx 未注入）'))
  return (await rpc(method as string, params as Record<string, unknown>)) as BackendRPC[K]['result']
}

// ---------- 会话身份 ----------

export interface SessionIdentity {
  channel: string
  chatID: string
  /** 身份是否就绪（__xbot_session__ 已注入且 chatID 非空）。未就绪时禁止会话相关 RPC。 */
  ready: boolean
}

/**
 * 从当前会话解析 channel/chatID —— 宿主注入 window.__xbot_session__（若有）。
 *
 * 身份未知时返回 { channel: 'cli', chatID: '', ready: false }，绝不伪造身份：
 * 布局恢复的 tab 可能先于会话加载 mount，伪造 chatID 会让后端把切换写到错误
 * 的目标上。调用方必须检查 ready 后才发会话相关 RPC（会话未就绪时切换按钮
 * 一律禁用）。
 */
export function resolveSession(): SessionIdentity {
  const s = (window as unknown as { __xbot_session__?: { channel?: string; chatID?: string } }).__xbot_session__
  if (s?.chatID) return { channel: s.channel ?? 'web', chatID: s.chatID, ready: true }
  return { channel: 'cli', chatID: '', ready: false }
}

/** 会话身份尚未就绪（resolveChat 等价的守卫）——调用方应禁用操作，不是重试场景。 */
export class SessionNotReadyError extends Error {
  constructor() {
    super(t('sessionNotReady', '会话身份未就绪（__xbot_session__ 未设置）——切换功能暂不可用'))
    this.name = 'SessionNotReadyError'
  }
}

/** 取会话身份的强制版本——未就绪时抛 SessionNotReadyError（调用方据此拒绝发 RPC）。 */
export function requireSession(): SessionIdentity {
  const s = resolveSession()
  if (!s.ready) throw new SessionNotReadyError()
  return s
}

// ---------- 轮询 ----------

/** 轮询句柄——cancel() 幂等；取消后不再回调、不再排期（组件卸载必须调用）。 */
export interface JobPoller {
  cancel(): void
}

export const JOB_POLL_INTERVAL_MS = 1500

/**
 * 轮询 job_status 直到终态（done/failed）：
 * - 立即发起第一次查询（无初始延迟）；
 * - 仅当 state === 'running' 时排下一次（1.5s），终态不再排——轮询必然停止；
 * - cancel() 之后即使 in-flight 响应返回也不再回调、不再排期（组件卸载语义）。
 */
export function pollJobStatus(
  jobId: string,
  fetch: (jobId: string) => Promise<JobStatus>,
  onUpdate: (status: JobStatus) => void,
  onError: (message: string) => void,
): JobPoller {
  let cancelled = false
  let timer: ReturnType<typeof setTimeout> | null = null

  const tick = (): void => {
    void fetch(jobId)
      .then((status) => {
        if (cancelled) return
        onUpdate(status)
        if (status.state === 'running') {
          timer = setTimeout(tick, JOB_POLL_INTERVAL_MS)
        }
      })
      .catch((err: unknown) => {
        if (cancelled) return
        onError(errMessage(err))
      })
  }

  tick()
  return {
    cancel(): void {
      cancelled = true
      if (timer !== null) {
        clearTimeout(timer)
        timer = null
      }
    },
  }
}

/** 连接等待轮询间隔（connect 立即返回，实际连接由后端 supervisor 异步建立）。 */
export const CONNECT_POLL_INTERVAL_MS = 1500
/** 连接等待上限：约 36s；超时后停止等待（后端仍在后台重连），由用户决定重试）。 */
export const CONNECT_POLL_MAX_ATTEMPTS = 24

export interface ConnectPollHandlers {
  /** 每次成功查询（connected 之前）——用于刷新行内状态。 */
  onUpdate(status: RemoteStatus): void
  /** connected=true —— 等待结束（唯一成功终态）。 */
  onConnected(status: RemoteStatus): void
  /** 达到 attempts 上限仍未连接 —— 等待结束（后端可能仍在后台重连）。 */
  onExhausted(last: RemoteStatus | null): void
  /** 单次查询失败（ssh 抖动等）——轮询继续，不中止等待。 */
  onError(message: string): void
}

/**
 * 轮询 status 直到 connected=true（带上限/可取消）：
 * - 立即查询一次，之后每 interval 一次；
 * - connected 或 attempts 到达上限即停止（终态不会再排期）；
 * - 单次查询错误只上报（onError），继续轮询——ssh 抖动不应让等待提前结束；
 * - cancel() 幂等：取消后 in-flight 响应也不再回调/排期。
 */
export function pollConnectStatus(
  fetch: () => Promise<RemoteStatus>,
  handlers: ConnectPollHandlers,
  opts?: { maxAttempts?: number; intervalMs?: number },
): JobPoller {
  const maxAttempts = opts?.maxAttempts ?? CONNECT_POLL_MAX_ATTEMPTS
  const intervalMs = opts?.intervalMs ?? CONNECT_POLL_INTERVAL_MS
  let cancelled = false
  let attempts = 0
  let timer: ReturnType<typeof setTimeout> | null = null
  let last: RemoteStatus | null = null

  const tick = (): void => {
    if (cancelled) return
    attempts += 1
    void fetch()
      .then((status) => {
        if (cancelled) return
        last = status
        if (status.connected === true) {
          handlers.onConnected(status)
          return
        }
        handlers.onUpdate(status)
        if (attempts >= maxAttempts) {
          handlers.onExhausted(status)
          return
        }
        timer = setTimeout(tick, intervalMs)
      })
      .catch((err: unknown) => {
        if (cancelled) return
        handlers.onError(errMessage(err))
        if (attempts >= maxAttempts) {
          handlers.onExhausted(last)
          return
        }
        timer = setTimeout(tick, intervalMs)
      })
  }

  tick()
  return {
    cancel(): void {
      cancelled = true
      if (timer !== null) {
        clearTimeout(timer)
        timer = null
      }
    },
  }
}

// ---------- 杂项 ----------

/** 可读错误文案（Error / 字符串 / 任意值统一）。 */
export function errMessage(e: unknown): string {
  if (e instanceof Error) return e.message
  if (typeof e === 'string') return e
  try {
    return JSON.stringify(e)
  } catch {
    return String(e)
  }
}

/** SSH 命令脱敏展示：优先抽 `user@host`（列表行只展示这一部分）。 */
export function maskSSH(ssh: string): string {
  const m = /([A-Za-z0-9._-]+@[A-Za-z0-9._:-]+)/.exec(ssh)
  if (m) return m[1]
  const tokens = ssh.trim().split(/\s+/).filter((tok) => tok !== '' && !tok.startsWith('-'))
  if (tokens[0] === 'ssh') return tokens[1] ?? ssh.trim()
  return tokens[0] ?? ssh.trim()
}

/** 诊断探针（临时）：面板里直接显示 i18n 链路状态，用来定位"宿主英文仍中文"。 */
export function __pluginI18nDebug(): {
  hasCtx: boolean
  hasI18n: boolean
  locale: string | null
  probe: string
} {
  const inst = ctxRef?.i18n
  let probe = '<no-i18n>'
  if (inst) {
    try {
      probe = String(inst.t('stateDisconnected', '<fallback-used>'))
    } catch (e) {
      probe = 'throw:' + String(e)
    }
  }
  const out = {
    hasCtx: !!ctxRef,
    hasI18n: !!inst,
    locale: ((inst as unknown as { locale?: string } | undefined)?.locale) ?? null,
    probe,
  }
  console.log('[plugin-i18n] ssh-runner debug', out)
  return out
}
