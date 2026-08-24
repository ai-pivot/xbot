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
  statusBadge,
  type GitStatus,
  type GitCommit,
  type GitLogResult,
  type CommitDetail,
} from './shared'

const { useState, useEffect, useCallback, useRef } = React

/** 激活时注入 ctx（PluginRuntime 调用 mod.activate(ctx)）——rpc/ui 存入共享单例。 */
export function activate(ctx: unknown): void {
  const c = ctx as {
    rpc?: { call: (m: string, p: Record<string, unknown>) => Promise<unknown> }
    ui?: { openViewTab: (o: never) => void; openFileTab: (path: string) => void }
  }
  setSharedApi(
    c?.rpc ? (m, p) => c.rpc!.call(m, p) : undefined,
    c?.ui ? (c.ui as unknown as import('./shared').PluginUIApi) : undefined,
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
  const [expandedHash, setExpandedHash] = useState<string | null>(null)

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
  useEffect(() => {
    void refresh()
    const timer = setInterval(() => {
      if (visibleRef.current && document.visibilityState !== 'hidden') {
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
          onClick={() => setExpandedHash(expanded ? null : c.hash)}
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
    <div className="flex h-full flex-col overflow-y-auto text-xs">
      {header}
      <div className="sticky top-0 z-10 border-b border-border bg-bg-primary px-2 py-1 text-[10px] uppercase tracking-wide text-text-muted">
        {`变更 ${status?.changes.length ?? 0} · +${totalAdded} -${totalDeleted}`}
      </div>
      {/* 每个区域独立 max-height + 滚动：单个区域过长不会把其他区域挤出视口 */}
      <div className="max-h-56 overflow-y-auto py-0.5">{changeRows}</div>
      <div className="sticky top-0 z-10 border-t border-border bg-bg-primary px-2 py-1 text-[10px] uppercase tracking-wide text-text-muted">
        {`提交 ${commits.length}/${total}`}
      </div>
      <div className="max-h-96 overflow-y-auto py-0.5">{commitRows}</div>
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
