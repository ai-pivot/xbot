/**
 * xbot.ssh-runner —— 远程机器管理面板（右侧栏视图）。
 *
 * 模型：VS Code Remote 式 SSH 管道 —— runner 不再常驻远端。每次「连接」由后端
 * 发起一条 SSH 会话，runner 跑在该会话的前台（管道断 = runner 死）；重连先杀
 * 老 runner 再起新的。默认 tunnel 模式用 ssh -R 反向隧道让远端访问 server
 * （远端无需能访问 server）；direct 才是 runner 直连 server（显式选择）。
 *
 * 新增机器流程：runner_create（拿 command）→ probe（环境报告）→ provision
 * （只安装二进制，轮询 job_status）→ connect（connect_cmd=command、install_dir、
 * connection_mode）→ 轮询 status 直到 connected → 写入配置 targets。
 *
 * 行操作：连接 / 断开（disconnect）/ 重连（disconnect → connect）/ 诊断
 * （status + logs）/ 删除（disconnect → deprovision → runner_delete → 移出 targets）。
 * targets 条目 auto_connect=true 时面板挂载后自动连接（容错：失败只提示，不阻塞面板）。
 *
 * 本模块由 PluginRuntime 通过 `/plugins/xbot.ssh-runner/web/index.js` 动态
 * import（既是插件主模块——activate(ctx) 注入 rpc/config，也是侧边栏视图——
 * default export SshRunnerPanel）。不 import 任何宿主内部模块——React 从
 * window 获取（见 shared.ts）。
 *
 * 构建：esbuild --bundle --format=esm --jsx=transform（React external）。
 */
import {
  React,
  t,
  getCtx,
  setCtx,
  callRpc,
  resolveSession,
  requireSession,
  mergeConfigValues,
  parseTargets,
  serializeTargets,
  pollJobStatus,
  pollConnectStatus,
  connectionStateOf,
  errMessage,
  maskSSH,
  type ConnectionMode,
  type ConnectionState,
  type JobPoller,
  type JobStatus,
  type MachineTarget,
  type ProbeResult,
  type RemoteStatus,
  type RunnerConfigValues,
  type RunnerInfo,
  type SessionIdentity,
} from './shared'

const { useState, useEffect, useCallback, useRef, useMemo } = React

/** 激活时注入 ctx（PluginRuntime 调用 mod.activate(ctx)）——能力存入 shared 单例。 */
export function activate(ctx: unknown): void {
  setCtx(ctx)
}

// ---------- 内联 SVG 图标（禁止 emoji——缺字体会成方框） ----------

type IconProps = { className?: string }

function IconHome({ className }: IconProps) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" className={className ?? 'h-3 w-3'}>
      <path d="M3 9.5 12 3l9 6.5V20a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1Z" />
      <path d="M9 21v-8h6v8" />
    </svg>
  )
}

function IconRefresh({ className }: IconProps) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" className={className ?? 'h-3.5 w-3.5'}>
      <path d="M21 12a9 9 0 1 1-2.64-6.36" />
      <path d="M21 3v6h-6" />
    </svg>
  )
}

function IconPlus({ className }: IconProps) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" className={className ?? 'h-3 w-3'}>
      <path d="M12 5v14M5 12h14" />
    </svg>
  )
}

function IconTrash({ className }: IconProps) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" className={className ?? 'h-3 w-3'}>
      <path d="M4 7h16" />
      <path d="M9 7V5a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v2" />
      <path d="M6 7l1 13a1 1 0 0 0 1 1h8a1 1 0 0 0 1-1l1-13" />
      <path d="M10 11v6M14 11v6" />
    </svg>
  )
}

function IconServer({ className }: IconProps) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" className={className ?? 'h-3.5 w-3.5'}>
      <rect x="3" y="3.5" width="18" height="7" rx="1.5" />
      <rect x="3" y="13.5" width="18" height="7" rx="1.5" />
      <path d="M7 7h.01M7 17h.01" />
    </svg>
  )
}

function IconLink({ className }: IconProps) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" className={className ?? 'h-3 w-3'}>
      <path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71" />
      <path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71" />
    </svg>
  )
}

function IconUnlink({ className }: IconProps) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" className={className ?? 'h-3 w-3'}>
      <path d="m18.84 12.25 1.72-1.71a5 5 0 0 0-7.07-7.07l-1.72 1.71" />
      <path d="m5.17 11.75-1.71 1.71a5 5 0 0 0 7.07 7.07l1.71-1.71" />
      <path d="m8 2 1.88 1.88M14.12 3.88 16 2M8 22l1.88-1.88M14.12 20.12 16 22" />
    </svg>
  )
}

function IconRotate({ className }: IconProps) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" className={className ?? 'h-3 w-3'}>
      <path d="M3 12a9 9 0 1 0 3-6.7" />
      <path d="M3 4v5h5" />
    </svg>
  )
}

function Spinner({ className }: IconProps) {
  return (
    <span
      data-testid="ssh-spinner"
      className={`inline-block animate-spin rounded-full border border-current border-t-transparent ${className ?? 'h-2.5 w-2.5'}`}
    />
  )
}

/** 作业步骤状态图标：成功用 SVG 勾，未成功用 SVG 横线（禁止 emoji/符号字形）。 */
function StepMark({ ok }: { ok: boolean }) {
  return ok ? (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2.5} strokeLinecap="round" strokeLinejoin="round" className="h-2.5 w-2.5 shrink-0 text-emerald-600 dark:text-emerald-400">
      <path d="M20 6 9 17l-5-5" />
    </svg>
  ) : (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2.5} strokeLinecap="round" strokeLinejoin="round" className="h-2.5 w-2.5 shrink-0 text-text-muted">
      <path d="M6 12h12" />
    </svg>
  )
}

// ---------- 面板状态类型 ----------

interface FlowCommon {
  name: string
  ssh: string
  connectionMode: ConnectionMode
  autoConnect: boolean
}

type AddFlow =
  | (FlowCommon & { phase: 'form'; error: string | null })
  | (FlowCommon & { phase: 'probing' })
  | (FlowCommon & { phase: 'confirm'; command: string; probe: ProbeResult; error: string | null })
  | (FlowCommon & { phase: 'provisioning'; command: string; jobId: string; job: JobStatus | null; error: string | null })
  | (FlowCommon & { phase: 'connecting'; command: string; status: RemoteStatus | null; note: string | null; error: string | null })
  | (FlowCommon & { phase: 'completed'; connected: boolean })

interface RowStatus {
  loading: boolean
  status: RemoteStatus | null
  error: string | null
}

const EMPTY_ROW_STATUS: RowStatus = { loading: false, status: null, error: null }

interface DiagState {
  loading: boolean
  status: RemoteStatus | null
  logs: string[] | null
  logsSource: string | null
  logsLoading: boolean
  error: string | null
}

interface DeleteState {
  job: JobStatus | null
  error: string | null
}

const EMPTY_DIAG: DiagState = { loading: false, status: null, logs: null, logsSource: null, logsLoading: false, error: null }

/** 连接徽章的三态样式（颜色只表达状态；文案由 i18n 提供）。 */
const CONN_DOT: Record<ConnectionState, string> = {
  connected: 'bg-emerald-500',
  reconnecting: 'bg-amber-500',
  disconnected: 'bg-slate-400 dark:bg-slate-500',
}

const CONN_TEXT: Record<ConnectionState, string> = {
  connected: 'text-emerald-600 dark:text-emerald-400',
  reconnecting: 'text-amber-600 dark:text-amber-400',
  disconnected: 'text-text-muted',
}

function connectionStateLabel(state: ConnectionState): string {
  switch (state) {
    case 'connected':
      return t('plugins.sshRunner.stateConnected', '已连接')
    case 'reconnecting':
      return t('plugins.sshRunner.stateReconnecting', '重连中')
    default:
      return t('plugins.sshRunner.stateDisconnected', '未连接')
  }
}

function initialForm(connectionMode: ConnectionMode = 'tunnel'): AddFlow {
  return { phase: 'form', name: '', ssh: '', connectionMode, autoConnect: false, error: null }
}

function omitKey<T>(rec: Record<string, T>, key: string): Record<string, T> {
  if (!(key in rec)) return rec
  const next = { ...rec }
  delete next[key]
  return next
}

// ---------- 面板 ----------

export default function SshRunnerPanel() {
  const [config, setConfig] = useState<RunnerConfigValues | null>(null)
  const [targets, setTargets] = useState<MachineTarget[]>([])
  const [loading, setLoading] = useState(true)
  const [listError, setListError] = useState<string | null>(null)
  const [session, setSession] = useState<SessionIdentity>(resolveSession)
  const [sessionTarget, setSessionTarget] = useState('')
  const [sessionError, setSessionError] = useState<string | null>(null)
  const [switchLocalBusy, setSwitchLocalBusy] = useState(false)
  const [switchBusy, setSwitchBusy] = useState<Record<string, boolean>>({})
  const [addOpen, setAddOpen] = useState(false)
  const [flow, setFlow] = useState<AddFlow>(initialForm)
  const [rowStatus, setRowStatus] = useState<Record<string, RowStatus>>({})
  const [connectBusy, setConnectBusy] = useState<Record<string, boolean>>({})
  const [expanded, setExpanded] = useState<string | null>(null)
  const [diag, setDiag] = useState<Record<string, DiagState>>({})
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null)
  const [deleteState, setDeleteState] = useState<Record<string, DeleteState>>({})
  const [rowError, setRowError] = useState<Record<string, string>>({})

  const targetsRef = useRef<MachineTarget[]>([])
  const configRef = useRef<RunnerConfigValues>(mergeConfigValues(null))
  /** runner 注册表快照——connect 前 re-key（runner_create）时回传既有设置，避免重置。 */
  const runnersRef = useRef<RunnerInfo[]>([])
  const jobPollerRef = useRef<JobPoller | null>(null)
  const connectPollersRef = useRef<Record<string, JobPoller>>({})
  const sessionKeyRef = useRef('')
  const mountedRef = useRef(true)
  const autoConnectTriedRef = useRef<Set<string>>(new Set())
  const connectBusyRef = useRef<Set<string>>(new Set())
  const flowRef = useRef<AddFlow>(initialForm())

  useEffect(() => {
    flowRef.current = flow
  }, [flow])

  // ---- 数据加载 ----

  const applyConfig = useCallback((merged: RunnerConfigValues) => {
    configRef.current = merged
    setConfig(merged)
    const parsed = parseTargets(merged.targets)
    targetsRef.current = parsed
    setTargets(parsed)
  }, [])

  const reload = useCallback(async () => {
    const c = getCtx()
    if (!c) return
    if (!c.config) {
      setListError(t('plugins.sshRunner.configUnavailable', '插件配置能力不可用（permissions 需含 config）'))
      setLoading(false)
      return
    }
    setLoading(true)
    try {
      const [rawCfg, list] = await Promise.all([c.config.get(), callRpc('runner_list', {})])
      applyConfig(mergeConfigValues(rawCfg))
      runnersRef.current = list.runners ?? []
      setListError(null)
    } catch (e) {
      setListError(errMessage(e))
    } finally {
      setLoading(false)
    }
  }, [applyConfig])

  const loadSessionTarget = useCallback(async () => {
    const sess = resolveSession()
    setSession(sess)
    if (!sess.ready) {
      setSessionTarget('')
      return
    }
    try {
      const cur = await callRpc('runner_session_get', { channel: sess.channel, chat_id: sess.chatID })
      setSessionTarget(cur.name || '')
      setSessionError(null)
    } catch (e) {
      setSessionError(errMessage(e))
    }
  }, [])

  useEffect(() => {
    void reload()
  }, [reload])

  // 会话身份同步：manifest 无 events 权限（只有 rpc/ui/config），身份只能经
  // window.__xbot_session__ 轮询重读；仅在 key 变化时重查当前目标（不刷屏）。
  useEffect(() => {
    const sync = (): void => {
      const next = resolveSession()
      const key = next.ready ? `${next.channel}:${next.chatID}` : ''
      if (key === sessionKeyRef.current) return
      sessionKeyRef.current = key
      void loadSessionTarget()
    }
    sync()
    const timer = setInterval(sync, 3000)
    return () => clearInterval(timer)
  }, [loadSessionTarget])

  // 配置外部变更（设置面板 / 其它客户端改 targets）→ 列表实时更新。
  useEffect(() => {
    const c = getCtx()
    if (!c?.config) return
    return c.config.onConfigChange((raw) => {
      applyConfig(mergeConfigValues(raw))
    })
  }, [applyConfig])

  // 卸载：必须停止作业/连接轮询（随组件停止，避免对已卸载面板 setState）。
  useEffect(() => {
    mountedRef.current = true
    const pollers = connectPollersRef.current
    return () => {
      mountedRef.current = false
      jobPollerRef.current?.cancel()
      jobPollerRef.current = null
      for (const poller of Object.values(pollers)) poller.cancel()
      connectPollersRef.current = {}
    }
  }, [])

  // ---- 每行连接状态（status 是连接态的权威来源） ----

  const loadRowStatus = useCallback(async (target: MachineTarget): Promise<RemoteStatus | null> => {
    setRowStatus((prev) => ({ ...prev, [target.name]: { ...(prev[target.name] ?? EMPTY_ROW_STATUS), loading: true } }))
    try {
      const status = await callRpc('xbot.ssh-runner.status', { ssh: target.ssh, name: target.name })
      if (mountedRef.current) {
        setRowStatus((prev) => ({ ...prev, [target.name]: { loading: false, status, error: null } }))
      }
      return status
    } catch (e) {
      const message = errMessage(e)
      if (mountedRef.current) {
        // 保留上一次已知状态（瞬时查询失败不该把徽章打回「未连接」），错误单独展示。
        setRowStatus((prev) => ({
          ...prev,
          [target.name]: { loading: false, status: prev[target.name]?.status ?? null, error: message },
        }))
      }
      return null
    }
  }, [])

  // 列表（含外部改动）变化 → 逐行刷新连接状态（并行，不阻塞渲染）。
  const targetsKey = useMemo(() => targets.map((tg) => `${tg.name}\u0000${tg.ssh}`).join('\u0001'), [targets])
  useEffect(() => {
    if (loading) return
    const list = targetsRef.current
    if (list.length === 0) return
    void Promise.all(list.map((tg) => loadRowStatus(tg)))
  }, [targetsKey, loading, loadRowStatus])

  // ---- 连接 / 断开 / 重连（VS Code Remote 式 SSH 管道） ----

  /**
   * 取回远端启动参数串（command）——唯一来源是核心 runner_create。
   * 对已注册的 runner 回传其既有 mode/workspace/LLM 设置：重连动作只轮换 token，
   * 不重置用户在其它面板配置过的机器设置。
   */
  const mintConnectCommand = useCallback(async (name: string): Promise<string> => {
    const known = runnersRef.current.find((r) => r.name === name)
    const created = known
      ? await callRpc('runner_create', {
          name,
          mode: known.mode,
          docker_image: known.docker_image,
          workspace: known.workspace,
          llm_provider: known.llm_provider,
          llm_api_key: known.llm_api_key,
          llm_model: known.llm_model,
          llm_base_url: known.llm_base_url,
        })
      : await callRpc('runner_create', { name })
    return created.command
  }, [])

  /** connect 立即返回（supervisor 已武装）——轮询 status 直到 connected（带上限/可取消）。 */
  const startConnectPoll = useCallback((target: MachineTarget): void => {
    connectPollersRef.current[target.name]?.cancel()
    const name = target.name
    const finishBusy = (): void => {
      connectBusyRef.current.delete(name)
      if (mountedRef.current) setConnectBusy((prev) => omitKey(prev, name))
    }
    const poller = pollConnectStatus(
      () => callRpc('xbot.ssh-runner.status', { ssh: target.ssh, name }),
      {
        onUpdate: (status) => {
          if (!mountedRef.current) return
          setRowStatus((prev) => ({ ...prev, [name]: { loading: false, status, error: null } }))
        },
        onConnected: (status) => {
          delete connectPollersRef.current[name]
          finishBusy()
          if (!mountedRef.current) return
          setRowStatus((prev) => ({ ...prev, [name]: { loading: false, status, error: null } }))
          setRowError((prev) => omitKey(prev, name))
        },
        onExhausted: (last) => {
          delete connectPollersRef.current[name]
          finishBusy()
          if (!mountedRef.current) return
          if (last !== null) {
            setRowStatus((prev) => ({ ...prev, [name]: { loading: false, status: last, error: null } }))
          }
          setRowError((prev) => ({
            ...prev,
            [name]: t(
              'plugins.sshRunner.connectTimeout',
              '等待连接超时（后端仍在后台重连）——可重试，或稍后刷新状态。',
            ),
          }))
        },
        onError: (message) => {
          if (!mountedRef.current) return
          setRowStatus((prev) => ({
            ...prev,
            [name]: { ...(prev[name] ?? EMPTY_ROW_STATUS), loading: false, error: message },
          }))
        },
      },
    )
    connectPollersRef.current[name] = poller
  }, [])

  /** 连接目标：runner_create 拿 command → connect（后端会先杀老 runner）→ 轮询 status。 */
  const connectTarget = useCallback(async (target: MachineTarget): Promise<void> => {
    if (connectBusyRef.current.has(target.name)) return
    connectBusyRef.current.add(target.name)
    setConnectBusy((prev) => ({ ...prev, [target.name]: true }))
    setRowError((prev) => omitKey(prev, target.name))
    try {
      const command = await mintConnectCommand(target.name)
      await callRpc('xbot.ssh-runner.connect', {
        ssh: target.ssh,
        name: target.name,
        connect_cmd: command,
        install_dir: target.install_dir || configRef.current.installDir,
        connection_mode: target.connection_mode,
        auto_connect: target.auto_connect,
      })
    } catch (e) {
      connectBusyRef.current.delete(target.name)
      setConnectBusy((prev) => omitKey(prev, target.name))
      throw e
    }
    if (!mountedRef.current) return
    startConnectPoll(target)
  }, [mintConnectCommand, startConnectPoll])

  const onConnectRow = useCallback(
    (target: MachineTarget) => {
      void connectTarget(target).catch((e: unknown) => {
        if (!mountedRef.current) return
        setRowError((prev) => ({ ...prev, [target.name]: errMessage(e) }))
      })
    },
    [connectTarget],
  )

  const onDisconnectRow = useCallback(
    (target: MachineTarget) => {
      void (async () => {
        connectPollersRef.current[target.name]?.cancel()
        delete connectPollersRef.current[target.name]
        setConnectBusy((prev) => ({ ...prev, [target.name]: true }))
        try {
          await callRpc('xbot.ssh-runner.disconnect', { ssh: target.ssh, name: target.name })
          await loadRowStatus(target)
        } catch (e) {
          if (mountedRef.current) setRowError((prev) => ({ ...prev, [target.name]: errMessage(e) }))
        } finally {
          connectBusyRef.current.delete(target.name)
          if (mountedRef.current) setConnectBusy((prev) => omitKey(prev, target.name))
        }
      })()
    },
    [loadRowStatus],
  )

  const onReconnectRow = useCallback(
    (target: MachineTarget) => {
      void (async () => {
        // 重连 = 先显式断开，再连接（后端 connect 本身也会先杀老 runner）。
        let disconnectError: string | null = null
        try {
          await callRpc('xbot.ssh-runner.disconnect', { ssh: target.ssh, name: target.name })
        } catch (e) {
          // 断开失败不阻断重连（connect 是 kill-old-first）；错误并入最终提示。
          disconnectError = errMessage(e)
        }
        try {
          await connectTarget(target)
        } catch (e) {
          if (!mountedRef.current) return
          const message = errMessage(e)
          setRowError((prev) => ({
            ...prev,
            [target.name]: disconnectError === null ? message : `${message}（断开旧连接也失败：${disconnectError}）`,
          }))
        }
      })()
    },
    [connectTarget],
  )

  // ---- autoConnect：targets 条目 auto_connect=true → 挂载后自动连接（容错） ----

  const autoConnectTarget = useCallback(
    async (target: MachineTarget): Promise<void> => {
      try {
        const status = await callRpc('xbot.ssh-runner.status', { ssh: target.ssh, name: target.name })
        if (!mountedRef.current) return
        setRowStatus((prev) => ({ ...prev, [target.name]: { loading: false, status, error: null } }))
        if (status.connected === true) return // 已在连接——绝不重复 connect
        await connectTarget(target)
      } catch (e) {
        // 容错：自动连接失败只提示（面板与其它的目标操作照常）。
        if (!mountedRef.current) return
        setRowError((prev) => ({
          ...prev,
          [target.name]: t('plugins.sshRunner.autoConnectFailed', '自动连接失败：{{msg}}', { msg: errMessage(e) }),
        }))
      }
    },
    [connectTarget],
  )

  useEffect(() => {
    if (loading) return
    for (const target of targets) {
      if (!target.auto_connect) continue
      if (autoConnectTriedRef.current.has(target.name)) continue
      autoConnectTriedRef.current.add(target.name)
      void autoConnectTarget(target)
    }
  }, [loading, targets, autoConnectTarget])

  // ---- 新增机器流程 ----

  const finishAdd = useCallback(
    async (name: string, ssh: string, connectionMode: ConnectionMode, autoConnect: boolean, connected: boolean) => {
      const cfg = configRef.current
      const target: MachineTarget = {
        name,
        ssh,
        install_dir: cfg.installDir,
        connection_mode: connectionMode,
        auto_connect: autoConnect,
        added_at: new Date().toISOString(),
      }
      const next = [...targetsRef.current.filter((x) => x.name !== name), target]
      try {
        const c = getCtx()
        if (!c?.config) {
          throw new Error(t('plugins.sshRunner.configUnavailable', '插件配置能力不可用（permissions 需含 config）'))
        }
        await c.config.set('targets', serializeTargets(next))
        targetsRef.current = next
        setTargets(next)
        setFlow((prev) => (prev.phase === 'connecting' ? { phase: 'completed', name, ssh, connectionMode, autoConnect, connected } : prev))
        void reload()
      } catch (e) {
        setFlow((prev) => (prev.phase === 'connecting' ? { ...prev, error: errMessage(e) } : prev))
      }
    },
    [reload],
  )

  const startConnectPhase = useCallback(
    (name: string, ssh: string, command: string, connectionMode: ConnectionMode, autoConnect: boolean) => {
      setFlow({ phase: 'connecting', name, ssh, command, connectionMode, autoConnect, status: null, note: null, error: null })
      void (async () => {
        try {
          await callRpc('xbot.ssh-runner.connect', {
            ssh,
            name,
            connect_cmd: command,
            install_dir: configRef.current.installDir,
            connection_mode: connectionMode,
            auto_connect: autoConnect,
          })
        } catch (e) {
          if (!mountedRef.current) return
          setFlow((prev) => (prev.phase === 'connecting' ? { ...prev, error: errMessage(e) } : prev))
          return
        }
        if (!mountedRef.current) return
        connectPollersRef.current[name]?.cancel()
        connectPollersRef.current[name] = pollConnectStatus(
          () => callRpc('xbot.ssh-runner.status', { ssh, name }),
          {
            onUpdate: (status) => {
              setFlow((prev) => (prev.phase === 'connecting' ? { ...prev, status } : prev))
            },
            onConnected: (status) => {
              delete connectPollersRef.current[name]
              setFlow((prev) => (prev.phase === 'connecting' ? { ...prev, status } : prev))
              void finishAdd(name, ssh, connectionMode, autoConnect, true)
            },
            onExhausted: (last) => {
              delete connectPollersRef.current[name]
              setFlow((prev) =>
                prev.phase === 'connecting'
                  ? {
                      ...prev,
                      status: last ?? prev.status,
                      error: t(
                        'plugins.sshRunner.connectTimeout',
                        '等待连接超时（后端仍在后台重连）——可重试，或稍后刷新状态。',
                      ),
                    }
                  : prev,
              )
            },
            onError: (message) => {
              setFlow((prev) => (prev.phase === 'connecting' ? { ...prev, note: message } : prev))
            },
          },
        )
      })()
    },
    [finishAdd],
  )

  const startProvision = useCallback(
    (name: string, ssh: string, command: string, connectionMode: ConnectionMode, autoConnect: boolean) => {
      const cfg = configRef.current
      jobPollerRef.current?.cancel()
      setFlow({ phase: 'provisioning', name, ssh, command, connectionMode, autoConnect, jobId: '', job: null, error: null })
      void (async () => {
        try {
          // 只安装二进制（不启动任何常驻服务）；连接由后续 connect 建立。
          const res = await callRpc('xbot.ssh-runner.provision', {
            ssh,
            name,
            download_base: cfg.downloadBase,
            install_dir: cfg.installDir,
          })
          if (!mountedRef.current) return
          setFlow((prev) => (prev.phase === 'provisioning' ? { ...prev, jobId: res.job_id } : prev))
          jobPollerRef.current = pollJobStatus(
            res.job_id,
            (id) => callRpc('xbot.ssh-runner.job_status', { job_id: id }),
            (job) => {
              setFlow((prev) =>
                prev.phase === 'provisioning'
                  ? {
                      ...prev,
                      job,
                      error: job.state === 'failed' ? job.error || t('plugins.sshRunner.installFailed', '安装失败') : prev.error,
                    }
                  : prev,
              )
              if (job.state === 'done') startConnectPhase(name, ssh, command, connectionMode, autoConnect)
            },
            (message) => {
              setFlow((prev) => (prev.phase === 'provisioning' ? { ...prev, error: message } : prev))
            },
          )
        } catch (e) {
          setFlow((prev) => (prev.phase === 'provisioning' ? { ...prev, error: errMessage(e) } : prev))
        }
      })()
    },
    [startConnectPhase],
  )

  const setName = useCallback((name: string) => {
    setFlow((prev) => (prev.phase === 'form' ? { ...prev, name } : prev))
  }, [])

  const setSsh = useCallback((ssh: string) => {
    setFlow((prev) => (prev.phase === 'form' ? { ...prev, ssh } : prev))
  }, [])

  const setConnectionMode = useCallback((connectionMode: ConnectionMode) => {
    setFlow((prev) => (prev.phase === 'form' ? { ...prev, connectionMode } : prev))
  }, [])

  const setAutoConnect = useCallback((autoConnect: boolean) => {
    setFlow((prev) => (prev.phase === 'form' ? { ...prev, autoConnect } : prev))
  }, [])

  const submitProbe = useCallback(() => {
    if (flow.phase !== 'form') return
    const name = flow.name.trim()
    const ssh = flow.ssh.trim()
    if (!name) {
      setFlow({ ...flow, error: t('plugins.sshRunner.errorNameRequired', '请填写名称') })
      return
    }
    if (!ssh) {
      setFlow({ ...flow, error: t('plugins.sshRunner.errorSshRequired', '请填写 SSH 命令（例如 ssh user@host -p 22）') })
      return
    }
    if (targetsRef.current.some((x) => x.name === name)) {
      setFlow({ ...flow, error: t('plugins.sshRunner.errorDuplicate', '名称已存在：{{name}}', { name }) })
      return
    }
    const { connectionMode, autoConnect } = flow
    setFlow({ phase: 'probing', name, ssh, connectionMode, autoConnect })
    void (async () => {
      try {
        const created = await callRpc('runner_create', { name })
        const probe = await callRpc('xbot.ssh-runner.probe', { ssh })
        if (!mountedRef.current) return
        setFlow({ phase: 'confirm', name, ssh, connectionMode, autoConnect, command: created.command, probe, error: null })
      } catch (e) {
        if (!mountedRef.current) return
        setFlow({ phase: 'form', name, ssh, connectionMode, autoConnect, error: errMessage(e) })
      }
    })()
  }, [flow])

  const confirmProvision = useCallback(() => {
    if (flow.phase !== 'confirm') return
    startProvision(flow.name, flow.ssh, flow.command, flow.connectionMode, flow.autoConnect)
  }, [flow, startProvision])

  const retryProvision = useCallback(() => {
    if (flow.phase !== 'provisioning') return
    startProvision(flow.name, flow.ssh, flow.command, flow.connectionMode, flow.autoConnect)
  }, [flow, startProvision])

  const retryConnect = useCallback(() => {
    if (flow.phase !== 'connecting') return
    startConnectPhase(flow.name, flow.ssh, flow.command, flow.connectionMode, flow.autoConnect)
  }, [flow, startConnectPhase])

  const cancelConnectWait = useCallback(() => {
    const current = flowRef.current
    if (current.phase !== 'connecting') return
    connectPollersRef.current[current.name]?.cancel()
    delete connectPollersRef.current[current.name]
    setFlow({ ...current, error: t('plugins.sshRunner.connectCancelled', '已停止等待连接（后端仍在后台尝试重连）。') })
  }, [])

  const saveOnly = useCallback(() => {
    const current = flowRef.current
    if (current.phase !== 'connecting') return
    connectPollersRef.current[current.name]?.cancel()
    delete connectPollersRef.current[current.name]
    void finishAdd(current.name, current.ssh, current.connectionMode, current.autoConnect, false)
  }, [finishAdd])

  const closeAdd = useCallback(() => {
    const current = flowRef.current
    if (current.phase === 'connecting' || current.phase === 'provisioning') {
      connectPollersRef.current[current.name]?.cancel()
      delete connectPollersRef.current[current.name]
    }
    setAddOpen(false)
    setFlow(initialForm(configRef.current.connectionMode))
  }, [])

  const openAdd = useCallback(() => {
    setAddOpen(true)
    setFlow(initialForm(configRef.current.connectionMode))
  }, [])

  // ---- 会话切换（runner_session_set） ----

  const switchTo = useCallback(async (target: MachineTarget) => {
    let sess: SessionIdentity
    try {
      sess = requireSession()
    } catch (e) {
      setRowError((prev) => ({ ...prev, [target.name]: errMessage(e) }))
      return
    }
    setRowError((prev) => omitKey(prev, target.name))
    setSwitchBusy((prev) => ({ ...prev, [target.name]: true }))
    try {
      await callRpc('runner_session_set', { channel: sess.channel, chat_id: sess.chatID, name: target.name })
      setSessionTarget(target.name)
    } catch (e) {
      setRowError((prev) => ({ ...prev, [target.name]: errMessage(e) }))
    } finally {
      setSwitchBusy((prev) => omitKey(prev, target.name))
    }
  }, [])

  const switchToLocal = useCallback(async () => {
    let sess: SessionIdentity
    try {
      sess = requireSession()
    } catch (e) {
      setSessionError(errMessage(e))
      return
    }
    setSwitchLocalBusy(true)
    try {
      await callRpc('runner_session_set', { channel: sess.channel, chat_id: sess.chatID, name: '' })
      setSessionTarget('')
      setSessionError(null)
    } catch (e) {
      setSessionError(errMessage(e))
    } finally {
      setSwitchLocalBusy(false)
    }
  }, [])

  // ---- 删除机器（disconnect → deprovision → runner_delete → 移出 targets） ----

  const finishDelete = useCallback(async (target: MachineTarget) => {
    const name = target.name
    try {
      await callRpc('runner_delete', { name })
    } catch (e) {
      setDeleteState((prev) => ({ ...prev, [name]: { job: prev[name]?.job ?? null, error: errMessage(e) } }))
      return
    }
    const next = targetsRef.current.filter((x) => x.name !== name)
    try {
      const c = getCtx()
      if (!c?.config) {
        throw new Error(t('plugins.sshRunner.configUnavailable', '插件配置能力不可用（permissions 需含 config）'))
      }
      await c.config.set('targets', serializeTargets(next))
      targetsRef.current = next
      setTargets(next)
      setDeleteState((prev) => omitKey(prev, name))
      setRowStatus((prev) => omitKey(prev, name))
      setRowError((prev) => omitKey(prev, name))
      setConnectBusy((prev) => omitKey(prev, name))
    } catch (e) {
      setDeleteState((prev) => ({ ...prev, [name]: { job: prev[name]?.job ?? null, error: errMessage(e) } }))
    }
  }, [])

  const runDelete = useCallback(
    async (target: MachineTarget) => {
      const name = target.name
      setConfirmDelete(null)
      jobPollerRef.current?.cancel()
      connectPollersRef.current[name]?.cancel()
      delete connectPollersRef.current[name]
      setDeleteState((prev) => ({ ...prev, [name]: { job: null, error: null } }))
      // 1) 断开：管道是 runner 的生命线。断开失败不阻断删除——后续 runner_delete
      //    会丢弃注册并断开连接；若卸载作业失败，下方会显示可读错误供重试。
      try {
        await callRpc('xbot.ssh-runner.disconnect', { ssh: target.ssh, name })
      } catch {
        /* 见上：不阻断（可读结果由 deprovision/runner_delete 决定） */
      }
      try {
        // 2) 远端卸载（uninstall:false = 保留二进制，只清进程/服务态）。
        const res = await callRpc('xbot.ssh-runner.deprovision', { ssh: target.ssh, name, uninstall: false })
        if (!mountedRef.current) return
        jobPollerRef.current = pollJobStatus(
          res.job_id,
          (id) => callRpc('xbot.ssh-runner.job_status', { job_id: id }),
          (job) => {
            setDeleteState((prev) => ({
              ...prev,
              [name]: { job, error: job.state === 'failed' ? job.error || t('plugins.sshRunner.deleteFailed', '移除机器失败') : null },
            }))
            if (job.state === 'done') void finishDelete(target)
          },
          (message) => {
            setDeleteState((prev) => ({ ...prev, [name]: { job: null, error: message } }))
          },
        )
      } catch (e) {
        setDeleteState((prev) => ({ ...prev, [name]: { job: null, error: errMessage(e) } }))
      }
    },
    [finishDelete],
  )

  const removeRecordOnly = useCallback(async (target: MachineTarget) => {
    const name = target.name
    try {
      await callRpc('runner_delete', { name })
    } catch {
      /* 仅移除本地记录是远端不可达时的逃生口：runner 注册清理失败不阻断 */
    }
    const next = targetsRef.current.filter((x) => x.name !== name)
    try {
      const c = getCtx()
      if (!c?.config) {
        throw new Error(t('plugins.sshRunner.configUnavailable', '插件配置能力不可用（permissions 需含 config）'))
      }
      await c.config.set('targets', serializeTargets(next))
      targetsRef.current = next
      setTargets(next)
      setDeleteState((prev) => omitKey(prev, name))
    } catch (e) {
      setDeleteState((prev) => ({ ...prev, [name]: { job: prev[name]?.job ?? null, error: errMessage(e) } }))
    }
  }, [])

  // ---- 诊断（status / logs，按需拉取） ----

  const loadStatus = useCallback(async (target: MachineTarget) => {
    const name = target.name
    setDiag((prev) => ({ ...prev, [name]: { ...(prev[name] ?? EMPTY_DIAG), loading: true, error: null } }))
    try {
      const status = await callRpc('xbot.ssh-runner.status', { ssh: target.ssh, name })
      setDiag((prev) => ({ ...prev, [name]: { ...(prev[name] ?? EMPTY_DIAG), loading: false, status, error: null } }))
      setRowStatus((prev) => ({ ...prev, [name]: { loading: false, status, error: null } }))
    } catch (e) {
      setDiag((prev) => ({ ...prev, [name]: { ...(prev[name] ?? EMPTY_DIAG), loading: false, error: errMessage(e) } }))
    }
  }, [])

  const loadLogs = useCallback(async (target: MachineTarget) => {
    const name = target.name
    setDiag((prev) => ({ ...prev, [name]: { ...(prev[name] ?? EMPTY_DIAG), logsLoading: true, error: null } }))
    try {
      const res = await callRpc('xbot.ssh-runner.logs', { ssh: target.ssh, name, lines: 100 })
      setDiag((prev) => ({
        ...prev,
        [name]: { ...(prev[name] ?? EMPTY_DIAG), logsLoading: false, logs: res.lines ?? [], logsSource: res.source ?? null },
      }))
    } catch (e) {
      setDiag((prev) => ({ ...prev, [name]: { ...(prev[name] ?? EMPTY_DIAG), logsLoading: false, error: errMessage(e) } }))
    }
  }, [])

  const toggleDiag = useCallback(
    (target: MachineTarget) => {
      if (expanded === target.name) {
        setExpanded(null)
        return
      }
      setExpanded(target.name)
      void loadStatus(target)
    },
    [expanded, loadStatus],
  )

  // ---- 渲染 ----

  const c = getCtx()
  // ⚠️ 必须在全部 hook 之后才允许提前 return（React #310：条件 return 之后不得有 hook）。
  if (!c) {
    return (
      <div data-testid="ssh-runner-not-initialized" className="p-3 text-xs text-text-muted">
        {t('plugins.sshRunner.notInitialized', 'Remote Machines 插件尚未初始化（activate(ctx) 未调用）')}
      </div>
    )
  }

  const cfgForDisplay = config ?? mergeConfigValues(null)

  const sessionLine = !session.ready ? (
    <span>{t('plugins.sshRunner.sessionNotReady', '会话身份未就绪——切换功能暂不可用（等待会话加载）')}</span>
  ) : sessionError ? (
    <span className="text-red-500">{t('plugins.sshRunner.sessionError', '无法读取当前目标：{{msg}}', { msg: sessionError })}</span>
  ) : sessionTarget ? (
    <span>{t('plugins.sshRunner.currentTarget', '当前目标：{{name}}', { name: sessionTarget })}</span>
  ) : (
    <span>{t('plugins.sshRunner.currentLocal', '当前目标：本机')}</span>
  )

  const modeOptionLabel = (mode: ConnectionMode): string =>
    mode === 'tunnel'
      ? t('plugins.sshRunner.modeTunnelOption', 'tunnel（反向隧道，远端无需能访问 server）')
      : t('plugins.sshRunner.modeDirectOption', 'direct（runner 直连 server）')

  return (
    <div data-testid="ssh-runner-panel" className="flex h-full flex-col overflow-hidden text-xs">
      {/* 工具条 */}
      <div className="flex shrink-0 items-center gap-1 border-b border-border px-2 py-1.5">
        <button
          data-testid="ssh-switch-local"
          onClick={() => void switchToLocal()}
          disabled={!session.ready || switchLocalBusy}
          title={t('plugins.sshRunner.switchLocalHint', '把当前会话切回本机执行')}
          className="inline-flex items-center gap-1 rounded border border-border px-1.5 py-0.5 text-[10px] text-text-secondary hover:bg-bg-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-50"
        >
          <IconHome />
          {switchLocalBusy ? t('plugins.sshRunner.switching', '切换中…') : t('plugins.sshRunner.switchLocal', '切回本机')}
        </button>
        <div className="min-w-0 flex-1" />
        <button
          data-testid="ssh-refresh"
          onClick={() => {
            void reload()
            void loadSessionTarget()
          }}
          title={t('plugins.sshRunner.refresh', '刷新')}
          className="rounded p-1 text-text-muted hover:bg-bg-hover hover:text-text-primary"
        >
          <IconRefresh className={loading ? 'h-3.5 w-3.5 animate-spin' : 'h-3.5 w-3.5'} />
        </button>
        <button
          data-testid="ssh-add-open"
          onClick={openAdd}
          className="inline-flex items-center gap-1 rounded border border-border px-1.5 py-0.5 text-[10px] text-text-secondary hover:bg-bg-hover hover:text-text-primary"
        >
          <IconPlus />
          {t('plugins.sshRunner.add', '添加机器')}
        </button>
      </div>

      {/* 会话目标行 */}
      <div data-testid="ssh-session-line" className="shrink-0 border-b border-border px-2 py-1 text-[10px] text-text-muted">
        {sessionLine}
      </div>

      {/* 添加向导 */}
      {addOpen && (
        <div className="shrink-0 border-b border-border bg-bg-primary px-2 py-2">
          {flow.phase === 'form' && (
            <form
              onSubmit={(e) => {
                e.preventDefault()
                submitProbe()
              }}
            >
              <div className="mb-1 flex items-center gap-1.5 text-[11px] font-semibold text-text-primary">
                <IconServer />
                {t('plugins.sshRunner.addTitle', '添加远程机器')}
              </div>
              <label className="mb-1 block">
                <span className="mb-0.5 block text-[10px] text-text-muted">{t('plugins.sshRunner.fieldName', '名称')}</span>
                <input
                  data-testid="ssh-add-name"
                  value={flow.name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="gpu-box"
                  className="w-full rounded border border-border bg-bg-secondary px-1.5 py-1 text-[11px] text-text-primary outline-none placeholder:text-text-muted focus:border-accent"
                />
              </label>
              <label className="mb-1 block">
                <span className="mb-0.5 block text-[10px] text-text-muted">{t('plugins.sshRunner.fieldSSH', 'SSH 命令')}</span>
                <input
                  data-testid="ssh-add-ssh"
                  value={flow.ssh}
                  onChange={(e) => setSsh(e.target.value)}
                  placeholder="ssh user@host -p 22"
                  className="w-full rounded border border-border bg-bg-secondary px-1.5 py-1 font-mono text-[11px] text-text-primary outline-none placeholder:text-text-muted focus:border-accent"
                />
              </label>
              <label className="mb-1 block">
                <span className="mb-0.5 block text-[10px] text-text-muted">{t('plugins.sshRunner.connectionMode', '连接方式')}</span>
                <select
                  data-testid="ssh-add-mode"
                  value={flow.connectionMode}
                  onChange={(e) => setConnectionMode(e.target.value === 'direct' ? 'direct' : 'tunnel')}
                  className="w-full rounded border border-border bg-bg-secondary px-1.5 py-1 text-[11px] text-text-primary outline-none focus:border-accent"
                >
                  <option value="tunnel">{modeOptionLabel('tunnel')}</option>
                  <option value="direct">{modeOptionLabel('direct')}</option>
                </select>
              </label>
              <label className="mb-1 flex items-center gap-1.5 text-[10px] text-text-secondary">
                <input
                  type="checkbox"
                  data-testid="ssh-add-autoconnect"
                  checked={flow.autoConnect}
                  onChange={(e) => setAutoConnect(e.target.checked)}
                  className="h-3 w-3"
                />
                {t('plugins.sshRunner.autoConnect', '面板打开后自动连接该机器')}
              </label>
              <p className="mb-1 text-[10px] leading-snug text-text-muted">
                {t(
                  'plugins.sshRunner.connectionModeHint',
                  'tunnel：远端无需能访问 server（ssh -R 反向隧道）；direct：runner 直连 server。runner 跑在 SSH 会话前台，断开管道即结束。',
                )}
              </p>
              {flow.error !== null && (
                <div data-testid="ssh-add-error" className="mb-1 text-[10px] text-red-500">
                  {flow.error}
                </div>
              )}
              <div className="flex items-center gap-1.5 pt-0.5">
                <button
                  data-testid="ssh-add-probe"
                  type="submit"
                  className="rounded bg-accent px-2 py-0.5 text-[10px] font-medium text-accent-foreground hover:opacity-90"
                >
                  {t('plugins.sshRunner.probeBtn', '探测环境')}
                </button>
                <button type="button" onClick={closeAdd} className="rounded border border-border px-2 py-0.5 text-[10px] text-text-secondary hover:bg-bg-hover">
                  {t('plugins.sshRunner.cancel', '取消')}
                </button>
              </div>
            </form>
          )}

          {flow.phase === 'probing' && (
            <div className="flex items-center gap-1.5 text-[10px] text-text-muted">
              <Spinner />
              {t('plugins.sshRunner.probing', '正在创建 runner 并探测远程环境…')}
            </div>
          )}

          {flow.phase === 'confirm' && (
            <div data-testid="ssh-probe-report">
              <div className="mb-1 flex items-center justify-between text-[11px] font-semibold text-text-primary">
                <span className="inline-flex items-center gap-1.5">
                  <IconServer />
                  {t('plugins.sshRunner.probeTitle', '环境报告')}
                </span>
                <span className="font-mono text-[10px] font-normal text-text-muted">{flow.name}</span>
              </div>
              <div className="mb-1 space-y-0.5 rounded border border-border bg-bg-secondary p-1.5 text-[10px]">
                <div className="flex justify-between gap-2">
                  <span className="text-text-muted">{t('plugins.sshRunner.probeOS', '系统')}</span>
                  <span className="min-w-0 truncate font-mono text-text-secondary">
                    {flow.probe.os || '—'} / {flow.probe.arch || '—'}
                  </span>
                </div>
                <div className="flex justify-between gap-2">
                  <span className="text-text-muted">{t('plugins.sshRunner.probeUser', '用户')}</span>
                  <span className="min-w-0 truncate font-mono text-text-secondary">
                    {flow.probe.user || '—'}
                    {flow.probe.is_root ? ' (root)' : ''}
                  </span>
                </div>
                <div className="flex justify-between gap-2">
                  <span className="text-text-muted">{t('plugins.sshRunner.probeTools', '工具')}</span>
                  <span className="font-mono text-text-secondary">
                    {`systemd:${flow.probe.has_systemd ? t('plugins.sshRunner.yes', '是') : t('plugins.sshRunner.no', '否')}`}
                    {` curl:${flow.probe.has_curl ? t('plugins.sshRunner.yes', '是') : t('plugins.sshRunner.no', '否')}`}
                    {` wget:${flow.probe.has_wget ? t('plugins.sshRunner.yes', '是') : t('plugins.sshRunner.no', '否')}`}
                  </span>
                </div>
                <div className="flex justify-between gap-2">
                  <span className="text-text-muted">{t('plugins.sshRunner.probeInstalled', '已装版本')}</span>
                  <span className="min-w-0 truncate font-mono text-text-secondary">
                    {flow.probe.installed_version || t('plugins.sshRunner.probeNotInstalled', '未安装')}
                  </span>
                </div>
                <div className="flex justify-between gap-2">
                  <span className="text-text-muted">{t('plugins.sshRunner.probeInstallDir', '安装目录')}</span>
                  <span className="min-w-0 truncate font-mono text-text-secondary">{cfgForDisplay.installDir}</span>
                </div>
                <div className="flex justify-between gap-2">
                  <span className="text-text-muted">{t('plugins.sshRunner.connectionMode', '连接方式')}</span>
                  <span data-testid="ssh-probe-mode" className="font-mono text-text-secondary">
                    {flow.connectionMode}
                  </span>
                </div>
                <div className="flex justify-between gap-2">
                  <span className="text-text-muted">{t('plugins.sshRunner.autoConnect', '自动连接')}</span>
                  <span className="font-mono text-text-secondary">
                    {flow.autoConnect ? t('plugins.sshRunner.yes', '是') : t('plugins.sshRunner.no', '否')}
                  </span>
                </div>
              </div>
              {flow.error !== null && (
                <div data-testid="ssh-add-error" className="mb-1 text-[10px] text-red-500">
                  {flow.error}
                </div>
              )}
              <div className="flex items-center gap-1.5">
                <button
                  data-testid="ssh-add-confirm"
                  onClick={confirmProvision}
                  className="rounded bg-accent px-2 py-0.5 text-[10px] font-medium text-accent-foreground hover:opacity-90"
                >
                  {t('plugins.sshRunner.confirmInstall', '安装并连接')}
                </button>
                <button onClick={closeAdd} className="rounded border border-border px-2 py-0.5 text-[10px] text-text-secondary hover:bg-bg-hover">
                  {t('plugins.sshRunner.cancel', '取消')}
                </button>
              </div>
            </div>
          )}

          {flow.phase === 'provisioning' && (
            <div data-testid="ssh-job">
              <div className="mb-1 flex items-center gap-1.5 text-[11px] font-semibold text-text-primary">
                {flow.job?.state === 'done' ? (
                  <StepMark ok />
                ) : flow.error === null ? (
                  <Spinner />
                ) : (
                  <StepMark ok={false} />
                )}
                {flow.job?.state === 'done'
                  ? t('plugins.sshRunner.installDone', 'xbot-runner 已安装')
                  : flow.error === null
                    ? t('plugins.sshRunner.installing', '正在安装 xbot-runner（只安装二进制）…')
                    : t('plugins.sshRunner.installFailed', '安装失败')}
              </div>
              {flow.error !== null && (
                <div data-testid="ssh-add-error" className="mb-1 text-[10px] text-red-500">
                  {flow.error}
                </div>
              )}
              {flow.job !== null && flow.job.steps.length > 0 && (
                <div className="mb-1 space-y-0.5 rounded border border-border bg-bg-secondary p-1.5">
                  {flow.job.steps.map((step, i) => (
                    <div key={`${i}-${step.name}`} data-testid="ssh-job-step" className="flex items-start gap-1.5 text-[10px]">
                      <span className="mt-0.5">
                        <StepMark ok={step.ok} />
                      </span>
                      <span className="shrink-0 font-mono text-text-secondary">{step.name}</span>
                      {step.detail !== '' && <span className="min-w-0 flex-1 truncate text-text-muted">{step.detail}</span>}
                    </div>
                  ))}
                </div>
              )}
              <div className="flex items-center gap-1.5">
                {flow.error !== null && (
                  <>
                    <button
                      data-testid="ssh-add-retry"
                      onClick={retryProvision}
                      className="rounded bg-accent px-2 py-0.5 text-[10px] font-medium text-accent-foreground hover:opacity-90"
                    >
                      {t('plugins.sshRunner.retry', '重试')}
                    </button>
                    <button onClick={closeAdd} className="rounded border border-border px-2 py-0.5 text-[10px] text-text-secondary hover:bg-bg-hover">
                      {t('plugins.sshRunner.close', '关闭')}
                    </button>
                  </>
                )}
                {flow.error === null && (
                  <span className="text-[10px] text-text-muted">
                    {t('plugins.sshRunner.provisionRunningHint', '正在远端下载并安装二进制（不启动常驻服务）；完成后自动建立 SSH 连接。')}
                  </span>
                )}
              </div>
            </div>
          )}

          {flow.phase === 'connecting' && (
            <div data-testid="ssh-connect-wait">
              <div className="mb-1 flex items-center gap-1.5 text-[11px] font-semibold text-text-primary">
                {flow.error === null ? <Spinner /> : <StepMark ok={false} />}
                {flow.error === null
                  ? t('plugins.sshRunner.connectingTitle', '正在通过 SSH 建立连接…')
                  : t('plugins.sshRunner.connectNotEstablished', '连接未建立')}
              </div>
              {flow.status !== null && (
                <div className="mb-1 space-y-0.5 rounded border border-border bg-bg-secondary p-1.5 text-[10px]">
                  <div className="flex justify-between gap-2">
                    <span className="text-text-muted">{t('plugins.sshRunner.connectionState', '连接状态')}</span>
                    <span data-testid="ssh-connect-state" className={`font-mono ${CONN_TEXT[connectionStateOf(flow.status)]}`}>
                      {connectionStateLabel(connectionStateOf(flow.status))}
                    </span>
                  </div>
                  {flow.status.connection_mode === 'tunnel' && flow.status.remote_port > 0 && (
                    <div className="flex justify-between gap-2">
                      <span className="text-text-muted">{t('plugins.sshRunner.tunnelPort', '隧道端口')}</span>
                      <span className="font-mono text-text-secondary">{flow.status.remote_port}</span>
                    </div>
                  )}
                  {flow.status.restarts > 0 && (
                    <div className="flex justify-between gap-2">
                      <span className="text-text-muted">{t('plugins.sshRunner.restartsLabel', '重连次数')}</span>
                      <span className="font-mono text-text-secondary">{flow.status.restarts}</span>
                    </div>
                  )}
                  {flow.status.last_error !== '' && flow.status.last_error !== undefined && (
                    <div className="break-all text-amber-600 dark:text-amber-400">{flow.status.last_error}</div>
                  )}
                </div>
              )}
              {flow.note !== null && flow.error === null && (
                <div className="mb-1 text-[10px] text-amber-600 dark:text-amber-400">{flow.note}</div>
              )}
              {flow.error !== null && (
                <>
                  <div data-testid="ssh-add-error" className="mb-1 text-[10px] text-red-500">
                    {flow.error}
                  </div>
                  <div className="flex flex-wrap items-center gap-1.5">
                    <button
                      data-testid="ssh-connect-retry"
                      onClick={retryConnect}
                      className="rounded bg-accent px-2 py-0.5 text-[10px] font-medium text-accent-foreground hover:opacity-90"
                    >
                      {t('plugins.sshRunner.retryConnect', '重试连接')}
                    </button>
                    <button
                      data-testid="ssh-add-save-only"
                      onClick={saveOnly}
                      className="rounded border border-border px-2 py-0.5 text-[10px] text-text-secondary hover:bg-bg-hover"
                    >
                      {t('plugins.sshRunner.saveOnly', '仅保存机器')}
                    </button>
                    <button onClick={closeAdd} className="rounded border border-border px-2 py-0.5 text-[10px] text-text-secondary hover:bg-bg-hover">
                      {t('plugins.sshRunner.close', '关闭')}
                    </button>
                  </div>
                </>
              )}
              {flow.error === null && (
                <div className="flex flex-wrap items-center gap-1.5">
                  <span className="text-[10px] text-text-muted">
                    {t('plugins.sshRunner.connectWaitHint', 'runner 跑在 SSH 会话前台：管道断开会自动重连（先杀老 runner 再起新的）。')}
                  </span>
                  <button
                    data-testid="ssh-connect-cancel"
                    onClick={cancelConnectWait}
                    className="rounded border border-border px-1.5 py-px text-[10px] text-text-secondary hover:bg-bg-hover"
                  >
                    {t('plugins.sshRunner.cancelWait', '停止等待')}
                  </button>
                </div>
              )}
            </div>
          )}

          {flow.phase === 'completed' && (
            <div data-testid="ssh-add-completed">
              <div
                className={`mb-1 flex items-center gap-1.5 text-[11px] font-semibold ${
                  flow.connected ? 'text-emerald-600 dark:text-emerald-400' : 'text-text-primary'
                }`}
              >
                <StepMark ok={flow.connected} />
                {flow.connected
                  ? t('plugins.sshRunner.connectedDone', '已安装并连接')
                  : t('plugins.sshRunner.savedDone', '已安装并保存（尚未连接）')}
                ：<span className="font-mono">{flow.name}</span>
              </div>
              <button
                data-testid="ssh-add-close"
                onClick={closeAdd}
                className="rounded border border-border px-2 py-0.5 text-[10px] text-text-secondary hover:bg-bg-hover"
              >
                {t('plugins.sshRunner.close', '关闭')}
              </button>
            </div>
          )}
        </div>
      )}

      {/* 目标列表 */}
      <div className="min-h-0 flex-1 overflow-y-auto">
        {listError !== null && (
          <div data-testid="ssh-list-error" className="border-b border-border px-2 py-1.5 text-[10px] text-red-500">
            {listError}
          </div>
        )}
        {loading && config === null && (
          <div className="px-2 py-3 text-[10px] text-text-muted">{t('plugins.sshRunner.loading', '加载中…')}</div>
        )}
        {!loading && targets.length === 0 && (
          <div className="px-2 py-3 text-[10px] leading-relaxed text-text-muted">
            {t(
              'plugins.sshRunner.empty',
              '还没有纳管的远程机器。点击「添加机器」输入一条 SSH 命令（如 ssh user@host -p 22）：远端会自动安装 xbot-runner，然后通过 SSH 管道连接。',
            )}
          </div>
        )}
        {targets.map((target) => {
          const rs = rowStatus[target.name]
          const st = rs?.status ?? null
          const state = connectionStateOf(st)
          const connected = state === 'connected'
          const isCurrent = session.ready && sessionTarget === target.name
          const isOpen = expanded === target.name
          const del = deleteState[target.name]
          const delActive = del !== undefined && del.error === null
          const err = rowError[target.name]
          const busySwitch = switchBusy[target.name] === true
          const busyConnect = connectBusy[target.name] === true
          const dstate = diag[target.name]
          return (
            <div key={target.name} data-testid={`ssh-target-${target.name}`} className="border-b border-border px-2 py-1.5">
              <div className="flex items-center gap-1.5">
                <span data-testid={`ssh-conn-dot-${target.name}`} className={`h-1.5 w-1.5 shrink-0 rounded-full ${CONN_DOT[state]}`} />
                <span className="min-w-0 flex-1 truncate text-[11px] font-medium text-text-primary">{target.name}</span>
                {isCurrent && (
                  <span className="shrink-0 rounded-sm bg-accent/15 px-1 py-px text-[9px] font-medium text-accent">
                    {t('plugins.sshRunner.current', '当前')}
                  </span>
                )}
                <span data-testid={`ssh-conn-badge-${target.name}`} className={`shrink-0 text-[10px] ${CONN_TEXT[state]}`}>
                  {connectionStateLabel(state)}
                </span>
              </div>
              <div className="mt-0.5 flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-[10px] text-text-muted">
                <span className="min-w-0 max-w-full truncate font-mono" title={target.ssh}>
                  {maskSSH(target.ssh)}
                </span>
                <span className="shrink-0 rounded-sm bg-bg-tertiary px-1 py-px font-mono">
                  {target.connection_mode === 'tunnel'
                    ? t('plugins.sshRunner.modeTunnelShort', 'tunnel')
                    : t('plugins.sshRunner.modeDirectShort', 'direct')}
                </span>
                {st !== null && st.restarts > 0 && (
                  <span data-testid={`ssh-restarts-${target.name}`}>
                    {t('plugins.sshRunner.restarts', '重连 {{count}} 次', { count: st.restarts })}
                  </span>
                )}
                {st !== null && st.connection_mode === 'tunnel' && st.remote_port > 0 && (
                  <span data-testid={`ssh-tunnel-port-${target.name}`} className="font-mono">
                    {t('plugins.sshRunner.tunnelPort', '隧道端口')} :{st.remote_port}
                  </span>
                )}
                {st !== null && st.installed_version !== '' && (
                  <span data-testid={`ssh-version-${target.name}`} className="min-w-0 truncate font-mono" title={st.installed_version}>
                    {st.installed_version}
                  </span>
                )}
                {busyConnect && <Spinner />}
              </div>
              <div className="mt-1 flex flex-wrap items-center gap-1">
                {!connected && (
                  <button
                    data-testid={`ssh-connect-${target.name}`}
                    onClick={() => onConnectRow(target)}
                    disabled={busyConnect || delActive}
                    title={t('plugins.sshRunner.connectHint', 'runner_create 拿启动参数 → 发起一条 SSH 会话（runner 在会话前台运行）')}
                    className="inline-flex items-center gap-1 rounded border border-border px-1.5 py-px text-[10px] text-text-secondary hover:bg-bg-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-50"
                  >
                    <IconLink />
                    {t('plugins.sshRunner.connect', '连接')}
                  </button>
                )}
                {state !== 'disconnected' && (
                  <button
                    data-testid={`ssh-disconnect-${target.name}`}
                    onClick={() => onDisconnectRow(target)}
                    disabled={delActive}
                    title={t('plugins.sshRunner.disconnectHint', '结束 SSH 管道（远端 runner 随之退出）')}
                    className="inline-flex items-center gap-1 rounded border border-border px-1.5 py-px text-[10px] text-text-secondary hover:bg-bg-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-50"
                  >
                    <IconUnlink />
                    {t('plugins.sshRunner.disconnect', '断开')}
                  </button>
                )}
                <button
                  data-testid={`ssh-reconnect-${target.name}`}
                  onClick={() => onReconnectRow(target)}
                  disabled={busyConnect || delActive}
                  title={t('plugins.sshRunner.reconnectHint', '断开后重新连接（新连接会先杀老 runner）')}
                  className="inline-flex items-center gap-1 rounded border border-border px-1.5 py-px text-[10px] text-text-secondary hover:bg-bg-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-50"
                >
                  <IconRotate />
                  {t('plugins.sshRunner.reconnect', '重连')}
                </button>
                {!isCurrent && (
                  <button
                    data-testid={`ssh-switch-${target.name}`}
                    onClick={() => void switchTo(target)}
                    disabled={!session.ready || busySwitch || delActive}
                    className="rounded border border-border px-1.5 py-px text-[10px] text-text-secondary hover:bg-bg-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-50"
                  >
                    {busySwitch ? t('plugins.sshRunner.switching', '切换中…') : t('plugins.sshRunner.switch', '切换')}
                  </button>
                )}
                <button
                  data-testid={`ssh-diag-${target.name}`}
                  onClick={() => toggleDiag(target)}
                  className="rounded border border-border px-1.5 py-px text-[10px] text-text-secondary hover:bg-bg-hover hover:text-text-primary"
                >
                  {isOpen ? t('plugins.sshRunner.collapse', '收起') : t('plugins.sshRunner.diag', '诊断')}
                </button>
                {confirmDelete === target.name ? (
                  <>
                    <span className="text-[10px] text-red-500">{t('plugins.sshRunner.confirmDelete', '确认删除？')}</span>
                    <button
                      data-testid={`ssh-delete-confirm-${target.name}`}
                      onClick={() => void runDelete(target)}
                      className="rounded border border-red-500/40 px-1.5 py-px text-[10px] text-red-600 hover:bg-red-500/10 dark:text-red-400"
                    >
                      {t('plugins.sshRunner.delete', '删除')}
                    </button>
                    <button
                      onClick={() => setConfirmDelete(null)}
                      className="rounded border border-border px-1.5 py-px text-[10px] text-text-secondary hover:bg-bg-hover"
                    >
                      {t('plugins.sshRunner.cancel', '取消')}
                    </button>
                  </>
                ) : (
                  <button
                    data-testid={`ssh-delete-${target.name}`}
                    onClick={() => setConfirmDelete(target.name)}
                    disabled={delActive}
                    title={t('plugins.sshRunner.delete', '删除')}
                    className="rounded border border-border p-1 text-text-muted hover:bg-red-500/10 hover:text-red-600 disabled:cursor-not-allowed disabled:opacity-50 dark:hover:text-red-400"
                  >
                    <IconTrash />
                  </button>
                )}
              </div>
              {rs?.error !== null && rs?.error !== undefined && (
                <div data-testid={`ssh-status-error-${target.name}`} className="mt-0.5 text-[10px] text-amber-600 dark:text-amber-400">
                  {t('plugins.sshRunner.statusUnavailable', '无法读取连接状态：{{msg}}', { msg: rs.error })}
                </div>
              )}
              {err !== undefined && (
                <div data-testid={`ssh-row-error-${target.name}`} className="mt-0.5 text-[10px] text-red-500">
                  {err}
                </div>
              )}
              {delActive && (
                <div className="mt-0.5 flex items-center gap-1.5 text-[10px] text-text-muted">
                  <Spinner />
                  {t('plugins.sshRunner.deleting', '移除中…')}
                  {del?.job !== null && del?.job !== undefined && del.job.steps.length > 0 && (
                    <span className="min-w-0 truncate font-mono">{del.job.steps[del.job.steps.length - 1]?.name}</span>
                  )}
                </div>
              )}
              {del !== undefined && del.error !== null && (
                <div data-testid={`ssh-delete-error-${target.name}`} className="mt-0.5 flex flex-wrap items-center gap-1.5 text-[10px]">
                  <span className="text-red-500">{del.error}</span>
                  <button
                    onClick={() => void runDelete(target)}
                    className="rounded border border-border px-1.5 py-px text-text-secondary hover:bg-bg-hover"
                  >
                    {t('plugins.sshRunner.retry', '重试')}
                  </button>
                  <button
                    onClick={() => void removeRecordOnly(target)}
                    className="rounded border border-border px-1.5 py-px text-text-secondary hover:bg-bg-hover"
                  >
                    {t('plugins.sshRunner.removeRecordOnly', '仅移除记录（不卸载）')}
                  </button>
                </div>
              )}
              {isOpen && (
                <div data-testid={`ssh-diag-panel-${target.name}`} className="mt-1 rounded border border-border bg-bg-primary p-1.5">
                  {dstate?.loading === true && <div className="text-[10px] text-text-muted">{t('plugins.sshRunner.loading', '加载中…')}</div>}
                  {dstate?.error !== null && dstate?.error !== undefined && (
                    <div className="text-[10px] text-red-500">{dstate.error}</div>
                  )}
                  {dstate?.status !== null && dstate?.status !== undefined && (
                    <div className="space-y-0.5 text-[10px]">
                      <div className="flex justify-between gap-2">
                        <span className="text-text-muted">{t('plugins.sshRunner.connectionState', '连接状态')}</span>
                        <span className="font-mono text-text-secondary">{connectionStateLabel(connectionStateOf(dstate.status))}</span>
                      </div>
                      <div className="flex justify-between gap-2">
                        <span className="text-text-muted">{t('plugins.sshRunner.connectionMode', '连接方式')}</span>
                        <span className="font-mono text-text-secondary">{dstate.status.connection_mode || '—'}</span>
                      </div>
                      <div className="flex justify-between gap-2">
                        <span className="text-text-muted">{t('plugins.sshRunner.restartsLabel', '重连次数')}</span>
                        <span className="font-mono text-text-secondary">{dstate.status.restarts}</span>
                      </div>
                      {dstate.status.connection_mode === 'tunnel' && dstate.status.remote_port > 0 && (
                        <div className="flex justify-between gap-2">
                          <span className="text-text-muted">{t('plugins.sshRunner.tunnelPort', '隧道端口')}</span>
                          <span className="font-mono text-text-secondary">127.0.0.1:{dstate.status.remote_port}</span>
                        </div>
                      )}
                      {dstate.status.connected_at !== '' && (
                        <div className="flex justify-between gap-2">
                          <span className="text-text-muted">{t('plugins.sshRunner.connectedAt', '连接时间')}</span>
                          <span className="min-w-0 truncate font-mono text-text-secondary">{dstate.status.connected_at}</span>
                        </div>
                      )}
                      <div className="flex justify-between gap-2">
                        <span className="text-text-muted">{t('plugins.sshRunner.version', '版本')}</span>
                        <span className="min-w-0 truncate font-mono text-text-secondary">{dstate.status.installed_version || '—'}</span>
                      </div>
                      {dstate.status.last_error !== '' && (
                        <div className="break-all text-amber-600 dark:text-amber-400">
                          {t('plugins.sshRunner.lastError', '最近错误：')}
                          {dstate.status.last_error}
                        </div>
                      )}
                      {dstate.status.detail !== '' && (
                        <div className="whitespace-pre-wrap break-all text-text-secondary">{dstate.status.detail}</div>
                      )}
                    </div>
                  )}
                  <div className="mt-1 flex flex-wrap items-center gap-1.5">
                    <button
                      data-testid={`ssh-diag-status-${target.name}`}
                      onClick={() => void loadStatus(target)}
                      className="rounded border border-border px-1.5 py-px text-[10px] text-text-secondary hover:bg-bg-hover"
                    >
                      {t('plugins.sshRunner.refreshStatus', '刷新状态')}
                    </button>
                    <button
                      data-testid={`ssh-diag-logs-${target.name}`}
                      onClick={() => void loadLogs(target)}
                      disabled={dstate?.logsLoading === true}
                      className="rounded border border-border px-1.5 py-px text-[10px] text-text-secondary hover:bg-bg-hover disabled:cursor-not-allowed disabled:opacity-50"
                    >
                      {t('plugins.sshRunner.loadLogs', '加载日志')}
                    </button>
                    {dstate?.logsLoading === true && <Spinner />}
                    {dstate?.logsSource !== null && dstate?.logsSource !== undefined && (
                      <span data-testid={`ssh-diag-logs-source-${target.name}`} className="font-mono text-[9px] text-text-muted">
                        {dstate.logsSource === 'ssh-session'
                          ? t('plugins.sshRunner.logsSourceSession', '来源：SSH 会话输出')
                          : t('plugins.sshRunner.logsSourceRemote', '来源：远端日志文件')}
                      </span>
                    )}
                  </div>
                  {dstate?.logs !== null && dstate?.logs !== undefined && (
                    dstate.logs.length === 0 ? (
                      <div className="mt-1 text-[10px] text-text-muted">{t('plugins.sshRunner.logsEmpty', '（无日志）')}</div>
                    ) : (
                      <pre
                        data-testid={`ssh-diag-logs-content-${target.name}`}
                        className="mt-1 max-h-40 overflow-auto whitespace-pre-wrap break-all rounded bg-bg-tertiary p-1 font-mono text-[9px] leading-snug text-text-secondary"
                      >
                        {dstate.logs.join('\n')}
                      </pre>
                    )
                  )}
                </div>
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}
