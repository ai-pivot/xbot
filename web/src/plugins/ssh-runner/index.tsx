/**
 * xbot.ssh-runner —— 远程机器管理面板（右侧栏视图）。
 *
 * 一条 SSH 命令纳管远程机器：探测环境（probe）→ runner_create 拿远端启动参数
 * → 远端安装并启动 xbot-runner（provision，异步 job 轮询）→ 写入插件配置
 * targets → 会话可切换到该机器执行（runner_session_set）。
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
  pollJobStatus,
  errMessage,
  maskSSH,
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
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2.5} strokeLinecap="round" className="h-2.5 w-2.5 shrink-0 text-text-muted">
      <path d="M6 12h12" />
    </svg>
  )
}

// ---------- 面板状态类型 ----------

type AddFlow =
  | { phase: 'form'; name: string; ssh: string; error: string | null }
  | { phase: 'probing'; name: string; ssh: string }
  | { phase: 'confirm'; name: string; ssh: string; command: string; probe: ProbeResult; error: string | null }
  | { phase: 'provisioning'; name: string; ssh: string; command: string; jobId: string; job: JobStatus | null; error: string | null }
  | { phase: 'completed'; name: string; ssh: string }

interface DiagState {
  loading: boolean
  status: RemoteStatus | null
  logs: string[] | null
  logsLoading: boolean
  error: string | null
}

interface DeleteState {
  job: JobStatus | null
  error: string | null
}

const EMPTY_DIAG: DiagState = { loading: false, status: null, logs: null, logsLoading: false, error: null }

function initialForm(): AddFlow {
  return { phase: 'form', name: '', ssh: '', error: null }
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
  const [runners, setRunners] = useState<RunnerInfo[]>([])
  const [loading, setLoading] = useState(true)
  const [listError, setListError] = useState<string | null>(null)
  const [session, setSession] = useState<SessionIdentity>(resolveSession)
  const [sessionTarget, setSessionTarget] = useState('')
  const [sessionError, setSessionError] = useState<string | null>(null)
  const [switchLocalBusy, setSwitchLocalBusy] = useState(false)
  const [switchBusy, setSwitchBusy] = useState<Record<string, boolean>>({})
  const [addOpen, setAddOpen] = useState(false)
  const [flow, setFlow] = useState<AddFlow>(initialForm)
  const [expanded, setExpanded] = useState<string | null>(null)
  const [diag, setDiag] = useState<Record<string, DiagState>>({})
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null)
  const [deleteState, setDeleteState] = useState<Record<string, DeleteState>>({})
  const [rowError, setRowError] = useState<Record<string, string>>({})

  const targetsRef = useRef<MachineTarget[]>([])
  const configRef = useRef<RunnerConfigValues>(mergeConfigValues(null))
  const jobPollerRef = useRef<JobPoller | null>(null)
  const sessionKeyRef = useRef('')
  const mountedRef = useRef(true)

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
      setRunners(list.runners ?? [])
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

  // 卸载：必须停止作业轮询（provision/deprovision 的 job_status 轮询随组件停止）。
  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
      jobPollerRef.current?.cancel()
      jobPollerRef.current = null
    }
  }, [])

  // ---- 新增机器流程 ----

  const finishProvision = useCallback(async (name: string, ssh: string) => {
    const cfg = configRef.current
    const target: MachineTarget = {
      name,
      ssh,
      install_dir: cfg.installDir,
      service_mode: cfg.serviceMode,
      added_at: new Date().toISOString(),
    }
    const next = [...targetsRef.current.filter((x) => x.name !== name), target]
    try {
      const c = getCtx()
      if (!c?.config) {
        throw new Error(t('plugins.sshRunner.configUnavailable', '插件配置能力不可用（permissions 需含 config）'))
      }
      await c.config.set('targets', JSON.stringify(next))
      targetsRef.current = next
      setTargets(next)
      setFlow((prev) => (prev.phase === 'provisioning' ? { phase: 'completed', name, ssh } : prev))
      void reload()
    } catch (e) {
      setFlow((prev) => (prev.phase === 'provisioning' ? { ...prev, error: errMessage(e) } : prev))
    }
  }, [reload])

  const startProvision = useCallback((name: string, ssh: string, command: string) => {
    const cfg = configRef.current
    jobPollerRef.current?.cancel()
    setFlow({ phase: 'provisioning', name, ssh, command, jobId: '', job: null, error: null })
    void (async () => {
      try {
        const res = await callRpc('xbot.ssh-runner.provision', {
          ssh,
          name,
          connect_cmd: command,
          download_base: cfg.downloadBase,
          install_dir: cfg.installDir,
          service_mode: cfg.serviceMode,
        })
        if (!mountedRef.current) return
        setFlow((prev) => (prev.phase === 'provisioning' ? { ...prev, jobId: res.job_id } : prev))
        jobPollerRef.current = pollJobStatus(
          res.job_id,
          (id) => callRpc('xbot.ssh-runner.job_status', { job_id: id }),
          (st) => {
            setFlow((prev) =>
              prev.phase === 'provisioning'
                ? { ...prev, job: st, error: st.state === 'failed' ? st.error || t('plugins.sshRunner.installFailed', '安装失败') : prev.error }
                : prev,
            )
            if (st.state === 'done') void finishProvision(name, ssh)
          },
          (msg) => {
            setFlow((prev) => (prev.phase === 'provisioning' ? { ...prev, error: msg } : prev))
          },
        )
      } catch (e) {
        setFlow((prev) => (prev.phase === 'provisioning' ? { ...prev, error: errMessage(e) } : prev))
      }
    })()
  }, [finishProvision])

  const setName = useCallback((name: string) => {
    setFlow((prev) => (prev.phase === 'form' ? { ...prev, name } : prev))
  }, [])

  const setSsh = useCallback((ssh: string) => {
    setFlow((prev) => (prev.phase === 'form' ? { ...prev, ssh } : prev))
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
    setFlow({ phase: 'probing', name, ssh })
    void (async () => {
      try {
        const created = await callRpc('runner_create', { name })
        const probe = await callRpc('xbot.ssh-runner.probe', { ssh })
        setFlow({ phase: 'confirm', name, ssh, command: created.command, probe, error: null })
      } catch (e) {
        setFlow({ phase: 'form', name, ssh, error: errMessage(e) })
      }
    })()
  }, [flow])

  const confirmProvision = useCallback(() => {
    if (flow.phase !== 'confirm') return
    startProvision(flow.name, flow.ssh, flow.command)
  }, [flow, startProvision])

  const retryProvision = useCallback(() => {
    if (flow.phase !== 'provisioning') return
    startProvision(flow.name, flow.ssh, flow.command)
  }, [flow, startProvision])

  const closeAdd = useCallback(() => {
    setAddOpen(false)
    setFlow(initialForm())
  }, [])

  // ---- 会话切换 ----

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

  // ---- 删除机器 ----

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
      await c.config.set('targets', JSON.stringify(next))
      targetsRef.current = next
      setTargets(next)
      setDeleteState((prev) => omitKey(prev, name))
    } catch (e) {
      setDeleteState((prev) => ({ ...prev, [name]: { job: prev[name]?.job ?? null, error: errMessage(e) } }))
    }
  }, [])

  const runDelete = useCallback(async (target: MachineTarget) => {
    const name = target.name
    setConfirmDelete(null)
    jobPollerRef.current?.cancel()
    setDeleteState((prev) => ({ ...prev, [name]: { job: null, error: null } }))
    try {
      const res = await callRpc('xbot.ssh-runner.deprovision', { ssh: target.ssh, name, uninstall: false })
      if (!mountedRef.current) return
      jobPollerRef.current = pollJobStatus(
        res.job_id,
        (id) => callRpc('xbot.ssh-runner.job_status', { job_id: id }),
        (st) => {
          setDeleteState((prev) => ({ ...prev, [name]: { job: st, error: st.state === 'failed' ? st.error || t('plugins.sshRunner.deleteFailed', '卸载失败') : null } }))
          if (st.state === 'done') void finishDelete(target)
        },
        (msg) => {
          setDeleteState((prev) => ({ ...prev, [name]: { job: null, error: msg } }))
        },
      )
    } catch (e) {
      setDeleteState((prev) => ({ ...prev, [name]: { job: null, error: errMessage(e) } }))
    }
  }, [finishDelete])

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
      await c.config.set('targets', JSON.stringify(next))
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
    } catch (e) {
      setDiag((prev) => ({ ...prev, [name]: { ...(prev[name] ?? EMPTY_DIAG), loading: false, error: errMessage(e) } }))
    }
  }, [])

  const loadLogs = useCallback(async (target: MachineTarget) => {
    const name = target.name
    setDiag((prev) => ({ ...prev, [name]: { ...(prev[name] ?? EMPTY_DIAG), logsLoading: true, error: null } }))
    try {
      const res = await callRpc('xbot.ssh-runner.logs', { ssh: target.ssh, name, lines: 100 })
      setDiag((prev) => ({ ...prev, [name]: { ...(prev[name] ?? EMPTY_DIAG), logsLoading: false, logs: res.lines ?? [] } }))
    } catch (e) {
      setDiag((prev) => ({ ...prev, [name]: { ...(prev[name] ?? EMPTY_DIAG), logsLoading: false, error: errMessage(e) } }))
    }
  }, [])

  const toggleDiag = useCallback((target: MachineTarget) => {
    if (expanded === target.name) {
      setExpanded(null)
      return
    }
    setExpanded(target.name)
    void loadStatus(target)
  }, [expanded, loadStatus])

  // ---- 渲染 ----

  const runnerIndex = useMemo(() => new Map(runners.map((r) => [r.name, r])), [runners])

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
          onClick={() => {
            setAddOpen(true)
            setFlow(initialForm())
          }}
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
                  <span className="text-text-muted">{t('plugins.sshRunner.probeServiceMode', '服务方式')}</span>
                  <span className="font-mono text-text-secondary">{cfgForDisplay.serviceMode}</span>
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
                  {t('plugins.sshRunner.confirmInstall', '确认安装')}
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
                  ? t('plugins.sshRunner.installDone', '安装完成')
                  : flow.error === null
                    ? t('plugins.sshRunner.installing', '正在安装 xbot-runner…')
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
                    {t('plugins.sshRunner.provisionRunningHint', '远端作业进行中——完成后自动写入目标；请保持面板打开')}
                  </span>
                )}
              </div>
            </div>
          )}

          {flow.phase === 'completed' && (
            <div data-testid="ssh-add-completed">
              <div className="mb-1 flex items-center gap-1.5 text-[11px] font-semibold text-emerald-600 dark:text-emerald-400">
                <StepMark ok />
                {t('plugins.sshRunner.installDone', '安装完成')}：<span className="font-mono">{flow.name}</span>
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
            {t('plugins.sshRunner.empty', '还没有纳管的远程机器。点击「添加机器」输入一条 SSH 命令（如 ssh user@host -p 22）开始。')}
          </div>
        )}
        {targets.map((target) => {
          const info = runnerIndex.get(target.name)
          const online = info?.online
          const isCurrent = session.ready && sessionTarget === target.name
          const isOpen = expanded === target.name
          const del = deleteState[target.name]
          const delActive = del !== undefined && del.error === null
          const err = rowError[target.name]
          const busySwitch = switchBusy[target.name] === true
          const dstate = diag[target.name]
          return (
            <div key={target.name} data-testid={`ssh-target-${target.name}`} className="border-b border-border px-2 py-1.5">
              <div className="flex items-center gap-1.5">
                <span
                  className={`h-1.5 w-1.5 shrink-0 rounded-full ${
                    online === true ? 'bg-emerald-500' : online === false ? 'bg-slate-400 dark:bg-slate-500' : 'bg-amber-500'
                  }`}
                />
                <span className="min-w-0 flex-1 truncate text-[11px] font-medium text-text-primary">{target.name}</span>
                {isCurrent && (
                  <span className="shrink-0 rounded-sm bg-accent/15 px-1 py-px text-[9px] font-medium text-accent">
                    {t('plugins.sshRunner.current', '当前')}
                  </span>
                )}
                <span
                  className={`shrink-0 text-[10px] ${
                    online === true
                      ? 'text-emerald-600 dark:text-emerald-400'
                      : online === false
                        ? 'text-text-muted'
                        : 'text-amber-600 dark:text-amber-400'
                  }`}
                >
                  {online === true
                    ? t('plugins.sshRunner.online', '在线')
                    : online === false
                      ? t('plugins.sshRunner.offline', '离线')
                      : t('plugins.sshRunner.unknown', '未注册')}
                </span>
              </div>
              <div className="mt-0.5 flex items-center gap-1">
                <span className="min-w-0 flex-1 truncate font-mono text-[10px] text-text-muted" title={target.ssh}>
                  {maskSSH(target.ssh)}
                </span>
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
              {err !== undefined && (
                <div data-testid={`ssh-row-error-${target.name}`} className="mt-0.5 text-[10px] text-red-500">
                  {err}
                </div>
              )}
              {delActive && (
                <div className="mt-0.5 flex items-center gap-1.5 text-[10px] text-text-muted">
                  <Spinner />
                  {t('plugins.sshRunner.deleting', '卸载中…')}
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
                        <span className="text-text-muted">{t('plugins.sshRunner.serviceState', '服务')}</span>
                        <span className="font-mono text-text-secondary">{dstate.status.service_state || '—'}</span>
                      </div>
                      <div className="flex justify-between gap-2">
                        <span className="text-text-muted">{t('plugins.sshRunner.version', '版本')}</span>
                        <span className="font-mono text-text-secondary">{dstate.status.installed_version || '—'}</span>
                      </div>
                      {dstate.status.detail !== '' && (
                        <div className="whitespace-pre-wrap break-all text-text-secondary">{dstate.status.detail}</div>
                      )}
                    </div>
                  )}
                  <div className="mt-1 flex items-center gap-1.5">
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
                  </div>
                  {dstate?.logs !== null && dstate?.logs !== undefined && (
                    dstate.logs.length === 0 ? (
                      <div className="mt-1 text-[10px] text-text-muted">{t('plugins.sshRunner.logsEmpty', '（无日志）')}</div>
                    ) : (
                      <pre data-testid={`ssh-diag-logs-content-${target.name}`} className="mt-1 max-h-40 overflow-auto whitespace-pre-wrap break-all rounded bg-bg-tertiary p-1 font-mono text-[9px] leading-snug text-text-secondary">
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
