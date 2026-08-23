/**
 * xbot.git-fancy —— commit 详情视图（主编辑区动态 tab，container='main'）。
 *
 * 由侧边栏提交历史通过 ctx.ui.openViewTab 打开：
 *   params: { hash: string }
 *
 * 渲染 commit 元信息（作者/日期/完整 message）+ 变更文件列表（状态/±行数），
 * 点击文件再次 openViewTab 打开该 commit 内此文件的全宽 diff
 * （openDiffTab(path, hash) —— key 含 commit，与工作区 diff 各占一个 tab）。
 *
 * 本模块由 PluginRuntime 通过 `/plugins/xbot.git-fancy/web/commit.js` 动态
 * import；props（viewParams）由宿主 PluginView 透传。React 从 window 获取。
 */
import { React, gitRpc, getRpc, openDiffTab, statusBadge, type CommitDetail } from './shared'

const { useState, useEffect } = React

export interface GitCommitViewProps {
  /** commit 哈希（长短均可，git 前缀匹配）。 */
  hash?: string
}

export function GitCommitView({ hash }: GitCommitViewProps) {
  const [detail, setDetail] = useState<CommitDetail | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let alive = true
    setLoading(true)
    setDetail(null)
    setError(null)
    if (!hash || !getRpc()) {
      setLoading(false)
      setError(!hash ? '缺少 hash 参数' : 'Git 插件未初始化')
      return
    }
    gitRpc<CommitDetail>('commit', { hash })
      .then((res) => {
        if (!alive) return
        if (res.error) {
          setError(res.error)
        } else {
          setDetail(res)
        }
      })
      .catch((e: unknown) => {
        if (!alive) return
        setError(e instanceof Error ? e.message : String(e))
      })
      .finally(() => {
        if (alive) setLoading(false)
      })
    return () => {
      alive = false
    }
  }, [hash])

  if (loading) {
    return <div className="flex h-full items-center justify-center text-xs text-text-muted">加载 commit 详情…</div>
  }

  if (error) {
    return <div className="p-4 font-mono text-xs text-red-500">{error}</div>
  }

  if (!detail) return null

  const totalAdded = detail.files.reduce((a, f) => a + f.added, 0)
  const totalDeleted = detail.files.reduce((a, f) => a + f.deleted, 0)

  // commit 元信息头（VSCode SCM 详情布局）。
  const metaHeader = (
    <div className="border-b border-border px-3 py-2">
      <div className="flex flex-wrap items-center gap-2">
        <span className="rounded bg-indigo-500/10 px-1.5 py-0.5 font-mono text-[11px] text-indigo-500">
          {detail.short}
        </span>
        <span className="text-xs text-text-primary">{detail.files.length} 个文件变更</span>
        <span className="ml-auto flex items-center gap-2 font-mono text-[11px]">
          <span className="text-green-600">+{totalAdded}</span>
          <span className="text-red-600">-{totalDeleted}</span>
        </span>
      </div>
      <div className="mt-1.5 font-mono text-xs leading-relaxed whitespace-pre-wrap text-text-secondary">
        {detail.message}
      </div>
      <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-[10px] text-text-muted">
        <span>{detail.author}{detail.email ? ` <${detail.email}>` : ''}</span>
        <span>{detail.date}</span>
        <span className="font-mono">{detail.hash}</span>
      </div>
    </div>
  )

  // 文件列表 —— 点击在主编辑区打开该 commit 内此文件的 diff tab。
  const fileRows = detail.files.map((f) => {
    const badge = statusBadge(f.status)
    return (
      <div
        key={f.path}
        onClick={() => openDiffTab(f.path, detail.hash)}
        className="flex cursor-pointer items-center gap-2 px-3 py-1 hover:bg-bg-hover active:bg-bg-hover"
        title="在编辑区查看此 commit 内该文件的 diff"
      >
        <span
          className={`inline-flex h-4 w-4 shrink-0 items-center justify-center rounded text-[10px] font-bold ${badge.cls}`}
        >
          {badge.label}
        </span>
        <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-text-secondary">{f.path}</span>
        <span className="flex shrink-0 items-center gap-1.5 font-mono text-[10px]">
          {f.added > 0 && <span className="text-green-600">+{f.added}</span>}
          {f.deleted > 0 && <span className="text-red-600">-{f.deleted}</span>}
          <span className="text-text-muted">›</span>
        </span>
      </div>
    )
  })

  return (
    <div className="flex h-full flex-col">
      {metaHeader}
      <div className="min-h-0 flex-1 overflow-auto py-1">{fileRows}</div>
    </div>
  )
}

export default GitCommitView
