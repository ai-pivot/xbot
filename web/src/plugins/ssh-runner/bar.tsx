/**
 * xbot.ssh-runner —— 底部 bar（当前执行目标 + 快速切换）。
 *
 * 显示**当前会话绑定的 runner**（空 = 本机），点击弹出选择器（本机 + 全部 runner），
 * 选择后调核心 RPC `runner_session_set` —— 乐观更新、失败回滚并显示错误。
 *
 * ⚠️ 宿主有两条渲染路径，本模块同时服务两者（同一模块实例：esbuild `--splitting`
 * 让 index.js / bar.js 共享 `shared.js` 的 ctx 单例）：
 *  1) **移动端 InfoBar**：`PluginPanelContainer container="info_bar"`
 *     （web/src/plugins/InfoBar.tsx:23）→ 本模块的 **default export**；
 *  2) **桌面底栏**（与「检查更新」按钮同一行）：rail 只消费 panelRegistry 里
 *     zone `top`/`bottom` 的徽章（web/src/components/panel/rails.tsx:87 与 :136）。
 *     而 manifest 的 `info_bar` 视图会被并入**同插件主面板**的 badgeRender
 *     （web/src/plugin-runtime/panelRegistry.ts:146-165 的合并规则），side
 *     面板的 badgeRender 无人消费 ⇒ 桌面端必须由 `ctx.panels.register` 额外注册
 *     一个 bottom 徽章（见 index.tsx 的 registerRunnerBarBadge）。
 *
 * ⚠️ 徽章形态下本组件渲染在宿主 rail 的 `<button>` 内部（rails.tsx:268-284）：
 * 触发元素必须是 `span[role=button]`、选择器行必须是 `div[role=menuitem]`，
 * **绝不能出现 `<button>`**（嵌套 button = 无效 HTML + 点击事件双触发）。
 * 选择器是 `position: fixed` 浮层 —— 独立 bundle 不打包 react-dom（无 portal 可用），
 * fixed 定位可逃出 rail / InfoBar 的 `overflow-hidden` 而不被裁剪。
 */
import type { ReactNode } from 'react'

import {
  React,
  t,
  callRpc,
  resolveSession,
  errMessage,
  type RunnerInfo,
  type SessionIdentity,
} from './shared'

const { useCallback, useEffect, useRef, useState } = React

/** 会话身份轮询间隔（manifest 无 events 权限，只能轮询 window.__xbot_session__）。 */
const SESSION_POLL_MS = 3000
/** 选择器浮层宽度 px（定位 clamp 用；与 className 的 w-60 保持一致）。 */
const PICKER_WIDTH = 240

/**
 * 徽章状态（颜色只表达状态，不表达分类）：
 * - `local`：未绑定 ⇒ 工具在本机执行；
 * - `online` / `offline`：已绑定某 runner，该 runner 当前是否在线；
 * - `unavailable`：会话身份尚未就绪（无法读/写绑定）。
 */
export type BarStatus = 'local' | 'online' | 'offline' | 'unavailable'

// ---------- 内联 SVG 图标（禁止 emoji——缺字体会成方框） ----------

function IconHome() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" className="size-3 shrink-0" aria-hidden="true">
      <path d="M3 9.5 12 3l9 6.5V20a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1Z" />
      <path d="M9 21v-8h6v8" />
    </svg>
  )
}

function IconServer() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" className="size-3 shrink-0" aria-hidden="true">
      <rect x="3" y="3.5" width="18" height="7" rx="1.5" />
      <rect x="3" y="13.5" width="18" height="7" rx="1.5" />
      <path d="M7 7h.01M7 17h.01" />
    </svg>
  )
}

function IconChevron() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" className="size-2.5 shrink-0" aria-hidden="true">
      <path d="m6 9 6 6 6-6" />
    </svg>
  )
}

function IconCheck() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2.5} strokeLinecap="round" strokeLinejoin="round" className="size-3 shrink-0" aria-hidden="true">
      <path d="m5 12 5 5L20 7" />
    </svg>
  )
}

/** 状态色点（与连接徽章同色系；颜色只在表达状态）。 */
const STATUS_DOT: Record<BarStatus, string> = {
  local: 'bg-sky-500',
  online: 'bg-emerald-500',
  offline: 'bg-amber-500',
  unavailable: 'bg-slate-400 dark:bg-slate-500',
}

// ---------- 数据源（会话身份 + 当前绑定 + runner 列表） ----------

export interface RunnerBarData {
  session: SessionIdentity
  /** 当前绑定的 runner 名（'' = 本机）。 */
  current: string
  currentOnline: boolean
  runners: RunnerInfo[]
  error: string | null
  busy: boolean
  /** 重新拉取 runner 列表（选择器打开时刷新）。 */
  reloadList: () => Promise<void>
  /** 切换绑定；返回是否成功（失败已回滚 + error 已置）。 */
  select: (name: string) => Promise<boolean>
}

/**
 * bar 的数据源。身份未就绪时**不发任何会话相关 RPC**（伪造 chatID 会把切换写到
 * 错误的目标上——与面板同一条判据，见 shared.resolveSession 的契约）。
 */
export function useRunnerBar(): RunnerBarData {
  const [session, setSession] = useState<SessionIdentity>(resolveSession)
  const [current, setCurrent] = useState('')
  const [currentOnline, setCurrentOnline] = useState(false)
  const [runners, setRunners] = useState<RunnerInfo[]>([])
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const mountedRef = useRef(true)
  const sessionKeyRef = useRef('')
  /** 切换在途：轮询不得用旧值覆盖乐观值（否则 chip 会先回跳再被权威回读纠正）。 */
  const switchingRef = useRef(false)

  const reloadList = useCallback(async (): Promise<void> => {
    try {
      const list = await callRpc('runner_list', {})
      if (mountedRef.current) setRunners(list.runners ?? [])
    } catch (e) {
      if (mountedRef.current) setError(errMessage(e))
    }
  }, [])

  const reloadBinding = useCallback(async (sess: SessionIdentity): Promise<void> => {
    if (!sess.ready) return
    try {
      const cur = await callRpc('runner_session_get', { channel: sess.channel, chat_id: sess.chatID })
      if (!mountedRef.current) return
      setCurrent(cur.name || '')
      setCurrentOnline(cur.online === true)
      setError(null)
    } catch (e) {
      if (mountedRef.current) setError(errMessage(e))
    }
  }, [])

  /** 一轮同步：重读身份 → 身份变化时刷新列表 → 刷新绑定（面板/其它客户端切换后跟上）。 */
  const sync = useCallback(async (): Promise<void> => {
    const next = resolveSession()
    const key = next.ready ? `${next.channel}:${next.chatID}` : ''
    const changed = key !== sessionKeyRef.current
    if (changed) {
      sessionKeyRef.current = key
      setSession(next)
    }
    if (!next.ready) {
      setCurrent('')
      setCurrentOnline(false)
      setRunners([])
      return
    }
    if (changed) await reloadList()
    // 切换在途 ⇒ 跳过绑定刷新：轮询回包携带的是切换前的旧值。
    if (!switchingRef.current) await reloadBinding(next)
  }, [reloadList, reloadBinding])

  useEffect(() => {
    mountedRef.current = true
    void sync()
    const timer = setInterval(() => {
      void sync()
    }, SESSION_POLL_MS)
    return () => {
      mountedRef.current = false
      clearInterval(timer)
    }
  }, [sync])

  const select = useCallback(
    async (name: string): Promise<boolean> => {
      const sess = resolveSession()
      if (!sess.ready) {
        setError(t('sessionNotReady', '会话尚未就绪 —— 请先打开一个会话'))
        return false
      }
      const prevName = current
      const prevOnline = currentOnline
      switchingRef.current = true
      setBusy(true)
      setError(null)
      // 乐观更新：立刻反映选择（绑定 ≠ 连接，离线目标同样允许绑定）。
      setCurrent(name)
      setCurrentOnline(name === '' ? true : (runners.find((r) => r.name === name)?.online ?? false))
      try {
        await callRpc('runner_session_set', { channel: sess.channel, chat_id: sess.chatID, name })
        // 权威回读（服务端可能规范化；顺带刷新 online）。
        const cur = await callRpc('runner_session_get', { channel: sess.channel, chat_id: sess.chatID })
        if (!mountedRef.current) return true
        setCurrent(cur.name || '')
        setCurrentOnline(cur.online === true)
        return true
      } catch (e) {
        if (!mountedRef.current) return false
        // 失败回滚：绝不把乐观值留在界面上。
        setCurrent(prevName)
        setCurrentOnline(prevOnline)
        setError(errMessage(e))
        return false
      } finally {
        switchingRef.current = false
        if (mountedRef.current) setBusy(false)
      }
    },
    [current, currentOnline, runners],
  )

  return { session, current, currentOnline, runners, error, busy, reloadList, select }
}

// ---------- 视图 ----------

function statusOf(data: RunnerBarData): BarStatus {
  if (!data.session.ready) return 'unavailable'
  if (data.current === '') return 'local'
  return data.currentOnline ? 'online' : 'offline'
}

/** 选择器里的单个目标行（本机 + 每个 runner）。 */
function TargetRow({
  name,
  labelText,
  hintText,
  active,
  online,
  local,
  onSelect,
}: {
  name: string
  labelText: string
  hintText: string
  active: boolean
  online: boolean
  local: boolean
  onSelect: (name: string) => void
}) {
  return (
    <div
      role="menuitem"
      tabIndex={-1}
      data-testid={`ssh-runner-bar-option-${name === '' ? 'local' : name}`}
      data-active={active ? 'true' : 'false'}
      title={hintText}
      onClick={(e) => {
        // div（不是 button）：徽章内联在宿主 rail 的 <button> 里，嵌套 button 非法。
        e.stopPropagation()
        onSelect(name)
      }}
      className="flex min-w-0 cursor-pointer items-center gap-2 rounded px-2 py-1.5 transition-colors hover:bg-bg-tertiary"
    >
      <span className={local ? 'text-sky-500' : 'text-text-secondary'}>{local ? <IconHome /> : <IconServer />}</span>
      <span className="min-w-0 flex-1 truncate text-xs text-text-primary">{labelText}</span>
      {!local ? (
        <span
          className={`shrink-0 text-[10px] ${online ? 'text-emerald-500' : 'text-amber-500'}`}
          data-testid={`ssh-runner-bar-option-state-${name}`}
        >
          {online ? t('online', '在线') : t('offline', '离线')}
        </span>
      ) : null}
      {active ? (
        <span className="shrink-0 text-app-accent" data-testid="ssh-runner-bar-option-active">
          <IconCheck />
        </span>
      ) : null}
    </div>
  )
}

/** 选择器内容（本机 + 全部 runner；当前目标打勾 + 在线/离线标记）。 */
export function RunnerBarPicker({ data, onSelect }: { data: RunnerBarData; onSelect: (name: string) => void }): ReactNode {
  // 绑定值不在列表里（记录被删/未注册）也要可辨——补一行，绝不悄悄隐藏当前绑定。
  const extra = data.current !== '' && !data.runners.some((r) => r.name === data.current) ? [data.current] : []
  return (
    <div className="flex min-w-0 flex-col gap-0.5" data-testid="ssh-runner-bar-list">
      <div className="px-2 pb-1 text-[10px] uppercase tracking-wide text-text-muted">
        {t('barPickTitle', '切换执行目标')}
      </div>
      <TargetRow
        name=""
        labelText={t('currentLocal', '本机（服务器）')}
        hintText={t('switchLocalHint', '工具重新在本机执行')}
        active={data.current === ''}
        online
        local
        onSelect={onSelect}
      />
      {[...data.runners.map((r) => r.name), ...extra].map((name) => (
        <TargetRow
          key={name}
          name={name}
          labelText={name}
          hintText={name}
          active={data.current === name}
          online={data.runners.find((r) => r.name === name)?.online === true}
          local={false}
          onSelect={onSelect}
        />
      ))}
      {data.error ? (
        <div className="px-2 pt-1 text-[10px] text-red-500" data-testid="ssh-runner-bar-error">
          {data.error}
        </div>
      ) : null}
    </div>
  )
}

/**
 * bar 主体（视图 default export 与桌面徽章 badgeRender 共用同一组件）：
 * 紧凑 chip（当前目标 + 状态点）→ 点击弹出选择器 → 选择即调 `runner_session_set`。
 */
export function RunnerBarView(): ReactNode {
  const data = useRunnerBar()
  const [open, setOpen] = useState(false)
  const [pos, setPos] = useState<{ left: number; bottom: number } | null>(null)
  const rootRef = useRef<HTMLSpanElement | null>(null)
  const pickerRef = useRef<HTMLSpanElement | null>(null)

  const status = statusOf(data)
  const label = status === 'unavailable' ? '—' : data.current === '' ? t('barLocal', '本机') : data.current

  const openPicker = useCallback(() => {
    const rect = rootRef.current?.getBoundingClientRect()
    if (rect) {
      const viewport = window.innerWidth || PICKER_WIDTH + 16
      setPos({
        left: Math.max(8, Math.min(rect.left, viewport - PICKER_WIDTH - 8)),
        // 向上展开：底栏在窗口下沿，向下会被裁掉。
        bottom: Math.max(8, (window.innerHeight || 0) - rect.top + 6),
      })
    }
    setOpen(true)
    void data.reloadList()
  }, [data])

  const togglePicker = useCallback(() => {
    if (open) setOpen(false)
    else openPicker()
  }, [open, openPicker])

  // 浮层关闭：点击外部 / Esc（捕获阶段，先于宿主 rail 的点击语义）。
  useEffect(() => {
    if (!open) return
    const onDown = (e: Event): void => {
      const target = e.target as Node | null
      if (pickerRef.current?.contains(target) || rootRef.current?.contains(target)) return
      setOpen(false)
    }
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('pointerdown', onDown, true)
    document.addEventListener('keydown', onKey, true)
    return () => {
      document.removeEventListener('pointerdown', onDown, true)
      document.removeEventListener('keydown', onKey, true)
    }
  }, [open])

  const onSelect = useCallback(
    (name: string) => {
      void data.select(name).then((ok) => {
        // 成功即收起；失败保持打开——错误行就显示在选择器里。
        if (ok) setOpen(false)
      })
    },
    [data],
  )

  const hint =
    status === 'unavailable'
      ? t('sessionNotReady', '会话尚未就绪 —— 请先打开一个会话')
      : t('barHint', '当前执行目标：{{name}} —— 点击切换', { name: label })

  return (
    <span
      ref={rootRef}
      role="button"
      tabIndex={0}
      data-testid="ssh-runner-bar"
      data-status={status}
      data-busy={data.busy ? 'true' : undefined}
      aria-haspopup="menu"
      aria-expanded={open}
      title={hint}
      onClick={(e) => {
        // 徽章内联在宿主 rail 的 <button> 里：阻止冒泡，避免宿主再弹它自己的详情层。
        e.stopPropagation()
        if (!data.session.ready) return
        togglePicker()
      }}
      onKeyDown={(e) => {
        if (e.key !== 'Enter' && e.key !== ' ') return
        e.preventDefault()
        e.stopPropagation()
        if (!data.session.ready) return
        togglePicker()
      }}
      className="inline-flex min-w-0 cursor-pointer items-center gap-1 whitespace-nowrap rounded px-1 py-0.5 text-text-secondary transition-colors hover:bg-bg-tertiary focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-app-accent/50"
      style={data.busy ? { opacity: 0.6 } : undefined}
    >
      {data.current === '' && status !== 'unavailable' ? <IconHome /> : <IconServer />}
      <span className="min-w-0 truncate" data-testid="ssh-runner-bar-label">
        {label}
      </span>
      <span className={`size-1.5 shrink-0 rounded-full ${STATUS_DOT[status]}`} data-testid="ssh-runner-bar-dot" />
      <IconChevron />
      {open ? (
        <span
          ref={pickerRef}
          role="menu"
          data-testid="ssh-runner-bar-picker"
          onClick={(e) => e.stopPropagation()}
          style={{ position: 'fixed', left: pos?.left, bottom: pos?.bottom, zIndex: 60 }}
          className="block w-60 rounded-lg border border-border bg-bg-elevated p-1 text-left font-normal shadow-lg"
        >
          <RunnerBarPicker data={data} onSelect={onSelect} />
        </span>
      ) : null}
    </span>
  )
}

/** 视图 default export：宿主按 view.entry 加载本模块时取其 default。 */
export default RunnerBarView
