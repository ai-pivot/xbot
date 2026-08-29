/**
 * xbot.git-fancy —— fancy Git 面板（侧边栏视图，VSCode Source Control 语义）。
 *
 * 数据源：后端独立 Go 进程（protocol.Run），前端通过
 * gitRpc('status'|'log'|...) 拉取。后端在 session CWD 下执行只读 git 命令。
 *
 * 本面板只做入口列表（变更文件 + 提交历史分页）；点击文件/commit 通过
 * ctx.ui.openViewTab 在主编辑区打开全宽 diff / commit 详情 tab
 * （VSCode editor view 语义，见宿主 plugin-runtime/editorTabs.ts）。
 *
 * 本模块由 PluginRuntime 通过 `/plugins/xbot.git-fancy/web/index.js` 动态
 * import（既是插件主模块——activate(ctx) 注入 rpc/ui，也是侧边栏视图——
 * default export GitFancyPanel）。不 import 任何宿主内部模块——React 从
 * window 获取。
 *
 * 构建：esbuild --bundle --splitting --format=esm --jsx=transform（React external）。
 */
import {
  React,
  setSharedApi,
  getRpc,
  gitRpc,
  openDiffTab,
  onSessionChange,
  resolveChat,
  statusBadge,
  type GitStatus,
  type GitCommit,
  type GitLogResult,
  type CommitDetail,
} from './shared'

const { useState, useEffect, useCallback, useRef } = React

/** 拖拽分隔条：两个垂直区块之间的比例拖拽（git-fancy 变更区 / commit 区）。
 *  - 拖拽期间 body cursor=ns-resize + userSelect=none（防文字选中/光标闪烁）
 *  - 比例用百分比存 localStorage（跨会话记忆）
 *  - 上区最小 80px（变更列表至少 3 行可见）；下区最小 80px（commit 列表至少 3 行） */
function useSplitRatio(storageKey: string, initialTopPct = 40): { topPct: number; onDragStart: (e: React.PointerEvent) => void } {
  const [topPct, setTopPct] = useState(() => {
    const saved = typeof localStorage !== 'undefined' ? localStorage.getItem(storageKey) : null
    const n = saved ? parseFloat(saved) : NaN
    return Number.isFinite(n) && n >= 10 && n <= 90 ? n : initialTopPct
  })

  const onDragStart = useCallback((e: React.PointerEvent) => {
    e.preventDefault()
    e.stopPropagation()
    const el = e.currentTarget as HTMLElement
    const container = el.parentElement
    if (!container) return
    const rect = container.getBoundingClientRect()
    const startY = e.clientY
    const startPct = topPct
    const prevCursor = document.body.style.cursor
    const prevUserSelect = document.body.style.userSelect
    document.body.style.cursor = 'ns-resize'
    document.body.style.userSelect = 'none'
    try { el.setPointerCapture(e.pointerId) } catch { /* jsdom */ }

    const onMove = (ev: PointerEvent) => {
      const deltaPct = ((ev.clientY - startY) / rect.height) * 100
      const next = Math.min(90, Math.max(10, startPct + deltaPct))
      setTopPct(next)
    }
    const onUp = () => {
      document.body.style.cursor = prevCursor
      document.body.style.userSelect = prevUserSelect
      setTopPct((cur) => {
        if (typeof localStorage !== 'undefined') localStorage.setItem(storageKey, String(cur))
        return cur
      })
      el.removeEventListener('pointermove', onMove)
      el.removeEventListener('pointerup', onUp)
      el.removeEventListener('pointercancel', onUp)
    }
    el.addEventListener('pointermove', onMove)
    el.addEventListener('pointerup', onUp)
    el.addEventListener('pointercancel', onUp)
  }, [topPct])

  return { topPct, onDragStart }
}

/** 激活时注入 ctx（PluginRuntime 调用 mod.activate(ctx)）——rpc/ui/events 存入共享单例。 */
export function activate(ctx: unknown): void {
  const c = ctx as {
    rpc?: { call: (m: string, p: Record<string, unknown>) => Promise<unknown> }
    ui?: { openViewTab: (o: never) => void; openFileTab: (path: string) => void }
    events?: { on: (name: string, handler: (payload: unknown) => void) => () => void }
  }
  setSharedApi(
    c?.rpc ? (m, p) => c.rpc!.call(m, p) : undefined,
    c?.ui ? (c.ui as unknown as import('./shared').PluginUIApi) : undefined,
    c?.events ? (c.events as unknown as import('./shared').PluginEventsApi) : undefined,
  )
}

const PAGE_SIZE = 10

// ---------- 主面板 ----------
export function GitFancyPanel() {
  const [status, setStatus] = useState<GitStatus | null>(null)
  const [commits, setCommits] = useState<GitCommit[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // 面板可见性：后台 tab / 页面隐藏时暂停轮询（后台节流）。
  const visibleRef = useRef(true)
  // 展开的 commit（inline accordion）。必须在条件提前 return 之前——
  // hook 在 return 之后会在 loading→loaded 切换时改变 hooks 数量（React #310）。
  // 持久化到 localStorage：刷新后恢复展开态。
  const [expandedHash, setExpandedHash] = useState<string | null>(() => {
    if (typeof localStorage === 'undefined') return null
    return localStorage.getItem('git-fancy:expanded-hash')
  })
  const toggleExpand = useCallback((hash: string) => {
    setExpandedHash((prev) => {
      const next = prev === hash ? null : hash
      if (typeof localStorage !== 'undefined') {
        if (next) localStorage.setItem('git-fancy:expanded-hash', next)
        else localStorage.removeItem('git-fancy:expanded-hash')
      }
      return next
    })
  }, [])

  const refresh = useCallback(async () => {
    if (!getRpc()) return
    try {
      const [st, lg] = await Promise.all([
        gitRpc<GitStatus>('status'),
        gitRpc<GitLogResult>('log', { limit: PAGE_SIZE }),
      ])
      setStatus(st)
      setCommits(lg.commits ?? [])
      setTotal(lg.total ?? 0)
      setError(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [])

  // 状态-only 轮询：只刷 status，不重置 commits（"加载更多"的分页累积状态
  // 不能被轮询冲掉）。
  const refreshStatusOnly = useCallback(async () => {
    try {
      const st = await gitRpc<GitStatus>('status')
      setStatus(st)
    } catch {
      /* 轮询失败静默，下次再试 */
    }
  }, [])

  // 初始加载 + 3s 自动轮询（仅前台可见时）。
  // 轮询同时检测 session key 变化（belt-and-suspenders：session.switched 事件
  // 在 split layout tab focus 切换时可能不触发——直接轮询 resolveChat() 的
  // key，变了就全量 refresh 含 commits）。
  const lastSessionKeyRef = useRef('')
  useEffect(() => {
    const checkSessionKey = () => {
      const chat = resolveChat()
      const key = `${chat.channel}:${chat.chatID}`
      if (lastSessionKeyRef.current && lastSessionKeyRef.current !== key) {
        // Session 变了——全量刷新（status + commits）
        setCommits([])
        setTotal(0)
        void refresh()
      }
      lastSessionKeyRef.current = key
    }
    checkSessionKey()
    void refresh()
    const timer = setInterval(() => {
      if (visibleRef.current && document.visibilityState !== 'hidden') {
        checkSessionKey()
        void refreshStatusOnly()
      }
    }, 3000)
    const onVis = () => {
      visibleRef.current = document.visibilityState !== 'hidden'
    }
    document.addEventListener('visibilitychange', onVis)
    return () => {
      clearInterval(timer)
      document.removeEventListener('visibilitychange', onVis)
    }
  }, [refresh, refreshStatusOnly])

  // 会话切换时自动刷新（对标 VSCode Source Control 切 tab 即刷新）。
  // 通过通用 session.switched 事件（宿主 PluginRuntimeBootstrap 发射），
  // 任何插件都可订阅——非 git-fancy 专属机制。
  useEffect(() => {
    const unsubscribe = onSessionChange(() => {
      // 重置分页 + 重新加载新会话的 git 数据。
      // 不清 expandedHash——刷新时 session.switched 会触发（prevSessionKey 初始
      // null），清了会把 useState 刚从 localStorage 恢复的值冲掉。新会话 commit
      // 列表加载后 hash 不匹配自然不展开（无害）。
      setCommits([])
      setTotal(0)
      setLoading(true)
      void refresh()
    })
    return unsubscribe
  }, [refresh])

  const loadMore = useCallback(async () => {
    if (loadingMore || commits.length >= total) return
    setLoadingMore(true)
    try {
      const lg = await gitRpc<GitLogResult>('log', { limit: PAGE_SIZE, skip: commits.length })
      setCommits((prev) => {
        const seen = new Set(prev.map((c) => c.hash))
        return [...prev, ...((lg.commits ?? []).filter((c) => !seen.has(c.hash)))]
      })
    } catch {
      /* 加载失败保持现状 */
    } finally {
      setLoadingMore(false)
    }
  }, [commits.length, total, loadingMore])

  // ⚠️ useSplitRatio 必须在条件 return 之前调用（React hooks 顺序不可变）。
  const { topPct, onDragStart } = useSplitRatio('git-fancy:split-ratio', 40)

  if (loading && !status) {
    return <div className="p-2 text-xs text-text-muted">加载 git 状态…</div>
  }

  if (error && !status) {
    return <div className="p-2 text-xs text-red-500">{error}</div>
  }

  if (status && !status.repo) {
    return <div className="p-2 text-xs text-text-muted">当前目录不是 git 仓库</div>
  }

  const totalAdded = status?.changes.reduce((a, c) => a + c.added, 0) ?? 0
  const totalDeleted = status?.changes.reduce((a, c) => a + c.deleted, 0) ?? 0

  const header = (
    <div className="flex items-center justify-between border-b border-border px-2 py-1.5">
      <span className="flex items-center gap-1.5 text-xs font-semibold text-text-primary">
        <span className="text-text-muted">⎇</span>
        {status?.branch || '(detached)'}
      </span>
      <div className="flex items-center gap-2">
        {status && status.ahead > 0 && (
          <span className="inline-flex items-center gap-1 text-xs">
            <span className="text-[10px] uppercase text-text-muted">↑</span>
            <span className="font-mono font-semibold text-green-600">{status.ahead}</span>
          </span>
        )}
        {status && status.behind > 0 && (
          <span className="inline-flex items-center gap-1 text-xs">
            <span className="text-[10px] uppercase text-text-muted">↓</span>
            <span className="font-mono font-semibold text-red-600">{status.behind}</span>
          </span>
        )}
        <button
          onClick={() => void refresh()}
          className="rounded px-1.5 py-0.5 text-[10px] text-text-muted hover:bg-bg-hover"
          title="刷新"
        >
          ↻
        </button>
      </div>
    </div>
  )

  // 变更文件列表 —— 整行可点击，在主编辑区打开全宽 diff tab。
  const changeRows = (status?.changes ?? []).map((c) => {
    const badge = statusBadge(c.status)
    return (
      <div
        key={c.path}
        onClick={() => openDiffTab(c.path)}
        className="flex cursor-pointer items-center gap-1.5 px-2 py-0.5 hover:bg-bg-hover active:bg-bg-hover"
        title="在编辑区查看 diff"
      >
        <span
          className={`inline-flex h-4 w-4 shrink-0 items-center justify-center rounded text-[10px] font-bold ${badge.cls}`}
        >
          {badge.label}
        </span>
        <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-text-secondary">{c.path}</span>
        <span className="flex shrink-0 items-center gap-1 font-mono text-[10px]">
          {c.added > 0 && <span className="text-green-600">+{c.added}</span>}
          {c.deleted > 0 && <span className="text-red-600">-{c.deleted}</span>}
          <span className="text-text-muted">›</span>
        </span>
      </div>
    )
  })

  // 提交历史 —— 点击 inline 展开 commit 详情（accordion：message + 变更
  // 文件列表），文件点击打开该 commit 内的 diff tab；再点收起。
  const commitRows = commits.map((c) => {
    const expanded = expandedHash === c.hash
    return (
      <div key={c.hash}>
        <div
          onClick={() => toggleExpand(c.hash)}
          className="flex cursor-pointer items-center gap-1.5 px-2 py-0.5 hover:bg-bg-hover active:bg-bg-hover"
          title="展开 commit 详情"
        >
          <span className={`font-mono text-[10px] text-indigo-500 transition-transform ${expanded ? 'rotate-90' : ''}`}>▸</span>
          <span className="font-mono text-[10px] text-indigo-500">{c.hash.slice(0, 7)}</span>
          <span className="min-w-0 flex-1 truncate text-[11px] text-text-secondary">{c.subject}</span>
          <span className="shrink-0 text-[10px] text-text-muted">{c.when}</span>
        </div>
        {expanded && <CommitExpand hash={c.hash} />}
      </div>
    )
  })

  const hasMore = commits.length < total

  return (
    <div className="flex h-full flex-col overflow-hidden text-xs">
      {header}
      <div className="shrink-0 border-b border-border bg-bg-primary px-2 py-1 text-[10px] uppercase tracking-wide text-text-muted">
        {`变更 ${status?.changes.length ?? 0} · +${totalAdded} -${totalDeleted}`}
      </div>
      {/* 上区（变更文件）—— flex-basis 按拖拽比例 */}
      <div className="min-h-0 overflow-y-auto py-0.5" style={{ flexBasis: `${topPct}%`, flexGrow: 0, flexShrink: 1 }}>
        {changeRows}
      </div>
      {/* 拖拽分隔条 */}
      <div
        onPointerDown={onDragStart}
        className="group flex h-1.5 shrink-0 cursor-ns-resize touch-none items-center justify-center"
        title="拖拽调整上下区域比例"
      >
        <div className="h-[2px] w-8 rounded-full bg-border transition-all group-hover:w-12 group-hover:bg-accent/50 group-active:bg-accent" />
      </div>
      <div className="shrink-0 border-t border-border bg-bg-primary px-2 py-1 text-[10px] uppercase tracking-wide text-text-muted">
        {`提交 ${commits.length}/${total}`}
      </div>
      {/* 下区（commit 历史）—— flex-1 占剩余空间 */}
      <div className="min-h-0 flex-1 overflow-y-auto py-0.5">{commitRows}</div>
      {hasMore && (
        <button
          onClick={() => void loadMore()}
          disabled={loadingMore}
          className="sticky bottom-0 border-t border-border bg-bg-primary px-2 py-1.5 text-center text-[10px] text-text-muted hover:bg-bg-hover hover:text-text-primary disabled:opacity-50"
        >
          {loadingMore ? '加载中…' : `加载更多（${total - commits.length} 条）`}
        </button>
      )}
    </div>
  )
}

/**
 * commit inline 展开详情（accordion 内容）：message + author/date + 变更
 * 文件列表；文件点击打开该 commit 内此文件的 diff（手机端全屏 / 桌面 tab）。
 */
function CommitExpand({ hash }: { hash: string }) {
  const [detail, setDetail] = useState<CommitDetail | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let alive = true
    setDetail(null)
    setError(null)
    gitRpc<CommitDetail>('commit', { hash })
      .then((d) => {
        if (!alive) return
        if (d.error) setError(d.error)
        else setDetail(d)
      })
      .catch((e: unknown) => {
        if (alive) setError(e instanceof Error ? e.message : String(e))
      })
    return () => {
      alive = false
    }
  }, [hash])

  if (error) {
    return <div className="px-2 py-1 font-mono text-[10px] text-red-500">{error}</div>
  }
  if (!detail) {
    return <div className="px-2 py-1 text-[10px] text-text-muted">加载 commit 详情…</div>
  }

  return (
    <div className="max-h-72 overflow-y-auto border-b border-border bg-bg-secondary/50">
      <div className="px-2.5 pt-1.5 pb-1">
        <div className="whitespace-pre-wrap font-mono text-[10px] leading-relaxed text-text-secondary">
          {detail.message}
        </div>
        <div className="mt-1 flex flex-wrap items-center gap-x-2 text-[9px] text-text-muted">
          <span>{detail.author}</span>
          <span>{detail.date}</span>
          <span className="font-mono">{detail.short}</span>
        </div>
      </div>
      {detail.files.map((f) => {
        const badge = statusBadge(f.status)
        return (
          <div
            key={f.path}
            onClick={() => openDiffTab(f.path, detail.hash)}
            className="flex cursor-pointer items-center gap-1.5 py-0.5 pl-4 pr-2 hover:bg-bg-hover active:bg-bg-hover"
            title="查看此 commit 内该文件的 diff"
          >
            <span
              className={`inline-flex h-4 w-4 shrink-0 items-center justify-center rounded text-[10px] font-bold ${badge.cls}`}
            >
              {badge.label}
            </span>
            <span className="min-w-0 flex-1 truncate font-mono text-[10px] text-text-secondary">{f.path}</span>
            <span className="flex shrink-0 items-center gap-1 font-mono text-[9px]">
              {f.added > 0 && <span className="text-green-600">+{f.added}</span>}
              {f.deleted > 0 && <span className="text-red-600">-{f.deleted}</span>}
            </span>
          </div>
        )
      })}
    </div>
  )
}

export default GitFancyPanel
