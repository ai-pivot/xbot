/**
 * xbot.ssh-runner 共享模块 —— 类型 + RPC/配置桥 + 会话身份解析 + 作业轮询。
 *
 * 范式与 xbot.git-fancy 一致：本模块不 import 任何宿主内部模块 —— React 从
 * window 获取（宿主 plugin-runtime 在加载插件产物前注入 window.React /
 * window.__xbot_i18n__），rpc/config 能力在 activate(ctx) 时注入模块级单例。
 * 类型经 `import type` 引用宿主 BackendRPC 声明（类型擦除，产物零运行时依赖）。
 *
 * 构建：esbuild --bundle --format=esm --jsx=transform（React external）。
 */
import type { BackendRPC } from '@/plugin-api'

const w = window as unknown as {
  React: typeof import('react')
  /** 宿主 iteration-render.tsx 挂载的 i18next 实例（独立 bundle 的 i18n 桥）。 */
  __xbot_i18n__?: { t: (key: string, opts?: Record<string, unknown>) => string }
}

export const React = w.React

// ---------- i18n 桥（独立 bundle 无法 import 宿主 '@/i18n'） ----------

/**
 * 翻译 helper：优先走宿主 i18next（window.__xbot_i18n__，命中 key 时插值
 * {{x}} 占位符）；key 缺失或桥未挂载时回退中文原文（defaultValue 同样插值）。
 * 插件产物与主 bundle 的语言包可能不同步，fallback 保证 UI 永不显示裸 key。
 */
export function t(key: string, fallback: string, params?: Record<string, string | number>): string {
  const inst = w.__xbot_i18n__
  if (inst) {
    try {
      return inst.t(key, { ...params, defaultValue: fallback })
    } catch {
      /* 桥异常时回退中文原文 */
    }
  }
  if (params) {
    return fallback.replace(/\{\{(\w+)\}\}/g, (_, k: string) => String(params[k] ?? ''))
  }
  return fallback
}

// ---------- 类型（BackendRPC 声明的别名——契约单一来源） ----------

/** 远端环境报告（probe）。 */
export type ProbeResult = BackendRPC['xbot.ssh-runner.probe']['result']
/** 作业步骤（provision/deprovision 共用）。 */
export type JobStep = BackendRPC['xbot.ssh-runner.job_status']['result']['steps'][number]
/** 异步作业状态；state !== 'running' 即终态——轮询必须停止。 */
export type JobStatus = BackendRPC['xbot.ssh-runner.job_status']['result']
/** runner_list 单项（在线状态来源；不含 token）。 */
export type RunnerInfo = BackendRPC['runner_list']['result']['runners'][number]
/** 远端服务状态（诊断）。 */
export type RemoteStatus = BackendRPC['xbot.ssh-runner.status']['result']

// ---------- 面板配置 / 目标 ----------

/** 已纳管目标（plugin config `targets` JSON 数组的元素）。 */
export interface MachineTarget {
  name: string
  ssh: string
  install_dir: string
  service_mode: string
  added_at: string
}

export interface RunnerConfigValues {
  downloadBase: string
  installDir: string
  serviceMode: string
  targets: string
}

/** manifest 声明的默认值（config.get() 缺键时兜底——与 plugin.json 一致）。 */
export const DEFAULT_CONFIG_VALUES: RunnerConfigValues = {
  downloadBase: 'https://github.com/ai-pivot/xbot/releases/latest/download',
  installDir: '/usr/local/bin',
  serviceMode: 'systemd',
  targets: '[]',
}

const CONFIG_KEYS: ReadonlyArray<keyof RunnerConfigValues> = [
  'downloadBase',
  'installDir',
  'serviceMode',
  'targets',
]

/** 合并 config.get() 的原始值 + manifest 默认值（空串/非字符串视为缺省）。 */
export function mergeConfigValues(raw: Record<string, unknown> | null | undefined): RunnerConfigValues {
  const values: RunnerConfigValues = { ...DEFAULT_CONFIG_VALUES }
  for (const key of CONFIG_KEYS) {
    const v = raw?.[key]
    if (typeof v === 'string' && v.trim() !== '') values[key] = v
  }
  return values
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
      service_mode: typeof rec.service_mode === 'string' ? rec.service_mode : '',
      added_at: typeof rec.added_at === 'string' ? rec.added_at : '',
    })
  }
  return out
}

// ---------- RPC / 配置桥（activate 注入） ----------

type RpcCall = (method: string, params: Record<string, unknown>) => Promise<unknown>

/** activate(ctx) 注入的 ctx 形状——仅取本面板用到的能力（rpc / config）。 */
export interface SshRunnerCtx {
  rpc?: { call: RpcCall }
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
  if (!rpc) throw new Error(t('plugins.sshRunner.notInitialized', 'Remote Machines 插件尚未初始化（ctx 未注入）'))
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
    super(t('plugins.sshRunner.sessionNotReady', '会话身份未就绪（__xbot_session__ 未设置）——切换功能暂不可用'))
    this.name = 'SessionNotReadyError'
  }
}

/** 取会话身份的强制版本——未就绪时抛 SessionNotReadyError（调用方据此拒绝发 RPC）。 */
export function requireSession(): SessionIdentity {
  const s = resolveSession()
  if (!s.ready) throw new SessionNotReadyError()
  return s
}

// ---------- 作业轮询 ----------

export const JOB_POLL_INTERVAL_MS = 1500

/** 轮询句柄——cancel() 幂等；组件卸载必须调用。 */
export interface JobPoller {
  cancel(): void
}

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
