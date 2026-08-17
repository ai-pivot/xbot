/**
 * xbot.git-fancy —— fancy Git 面板（独立 stdio 插件）。
 *
 * 数据源：后端独立 Go 进程（protocol.Run），前端通过
 * ctx.rpc.call('xbot.git-fancy.status'|'log'|'diff'|'branches', {...}) 拉取。
 * 后端在 session CWD 下执行只读 git 命令；diff 返回行级结构（lines[]），
 * 前端做 VSC 风格行级 +/- 着色。
 *
 * 本模块由 PluginRuntime 通过 `/plugins/xbot.git-fancy/web/index.js` 动态
 * import。不 import 任何宿主内部模块——React 从 window 获取，rpc 在
 * activate(ctx) 时拿到后存模块级变量。
 *
 * 构建：esbuild --bundle --format=esm --jsx=transform（React external）。
 */

const w = window as unknown as { React: typeof import('react') }

interface GitChange {
  path: string
  status: string
  added: number
  deleted: number
}
interface GitStatus {
  repo: boolean
  branch: string
  clean: boolean
  changes: GitChange[]
  ahead: number
  behind: number
  error?: string
}
interface GitCommit {
  hash: string
  author: string
  when: string
  subject: string
}
interface DiffLine {
  kind: 'hunk' | 'add' | 'del' | 'ctx' | 'meta'
  old_line: number
  new_line: number
  text: string
}
interface DiffResult {
  path: string
  content: string
  lines: DiffLine[]
}

/** 后端 RPC 调用器——由 activate(ctx) 注入。 */
type RpcCall = (method: string, params: Record<string, unknown>) => Promise<unknown>
let rpc: RpcCall | null = null

const React = w.React
const { useState, useEffect, useCallback } = React

/** 激活时注入 ctx（PluginRuntime 调用 mod.activate(ctx)）。 */
export function activate(ctx: unknown): void {
  const c = ctx as { rpc?: { call: (m: string, p: Record<string, unknown>) => Promise<unknown> } }
  const r = c?.rpc
  if (r) {
    rpc = (m, p) => r.call(m, p)
  }
}

// ---------- 渲染工具 ----------
function statusBadge(status: string): { label: string; cls: string } {
  // status：U未跟踪 / M修改 / A新增 / D删除 / R重命名 / C复制 / 冲突其他
  switch (status) {
    case 'U':
      return { label: 'U', cls: 'text-slate-400 bg-slate-100 dark:bg-slate-800' }
    case 'M':
      return { label: 'M', cls: 'text-amber-600 bg-amber-100 dark:bg-amber-900/40' }
    case 'A':
      return { label: 'A', cls: 'text-green-600 bg-green-100 dark:bg-green-900/40' }
    case 'D':
      return { label: 'D', cls: 'text-red-600 bg-red-100 dark:bg-red-900/40' }
    case 'R':
      return { label: 'R', cls: 'text-blue-600 bg-blue-100 dark:bg-blue-900/40' }
    case 'C':
      return { label: 'C', cls: 'text-cyan-600 bg-cyan-100 dark:bg-cyan-900/40' }
    default:
      return { label: status || '?', cls: 'text-text-secondary bg-bg-hover' }
  }
}

function Stat({ label, value, cls }: { label: string; value: string; cls: string }) {
  return React.createElement(
    'span', { className: 'inline-flex items-center gap-1 whitespace-nowrap text-xs' },
    React.createElement('span', { className: 'text-[10px] uppercase tracking-wide text-text-muted' }, label),
    React.createElement('span', { className: `font-mono font-semibold ${cls}` }, value),
  )
}

// VSC 风格 diff 行渲染：+ 绿底 / - 红底 / 上下文 / hunk 头
function renderDiffLine(l: DiffLine, idx: number) {
  let cls = ''
  let gutter = ''
  switch (l.kind) {
    case 'add':
      cls = 'bg-green-500/10 text-green-600 dark:text-green-400'
      gutter = '+'
      break
    case 'del':
      cls = 'bg-red-500/10 text-red-600 dark:text-red-400'
      gutter = '-'
      break
    case 'hunk':
      cls = 'bg-bg-hover text-blue-500 font-semibold'
      gutter = '@@'
      break
    case 'meta':
      cls = 'text-text-muted italic'
      gutter = ' '
      break
    default:
      gutter = ' '
      break
  }
  const lineNo =
    l.kind === 'add' ? l.new_line : l.kind === 'del' ? l.old_line : l.kind === 'ctx' ? (l.old_line ? String(l.old_line) : String(l.new_line)) : ''
  return React.createElement(
    'div',
    {
      key: idx,
      className: `flex items-stretch font-mono text-[10px] leading-relaxed ${cls}`,
    },
    React.createElement('span', { className: 'w-4 shrink-0 select-none text-center opacity-70' }, gutter),
    React.createElement('span', { className: 'w-8 shrink-0 select-none text-right pr-1 opacity-50' }, lineNo),
    React.createElement('span', { className: 'min-w-0 flex-1 whitespace-pre-wrap break-all' }, l.text),
  )
}

// ---------- 主面板 ----------
function GitFancyPanel() {
  const [status, setStatus] = useState<GitStatus | null>(null)
  const [commits, setCommits] = useState<GitCommit[]>([])
  const [diffPath, setDiffPath] = useState<string | null>(null)
  const [diffLines, setDiffLines] = useState<DiffLine[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [activeChat, setActiveChat] = useState<{ channel: string; chatID: string } | null>(null)

  // 从当前会话解析 channel/chatID —— 宿主注入 window.__xbot_session__（若有）
  const resolveChat = useCallback((): { channel: string; chatID: string } => {
    const s = (window as unknown as { __xbot_session__?: { channel?: string; chatID?: string } }).__xbot_session__
    if (s?.chatID) {
      return { channel: s.channel ?? 'web', chatID: s.chatID }
    }
    return { channel: 'web', chatID: 'default' }
  }, [])

  const refresh = useCallback(async () => {
    if (!rpc) return
    const chat = resolveChat()
    setActiveChat(chat)
    try {
      const [st, lg] = await Promise.all([
        rpc('xbot.git-fancy.status', chat) as Promise<GitStatus>,
        rpc('xbot.git-fancy.log', { ...chat, limit: 8 }) as Promise<{ commits: GitCommit[] }>,
      ])
      setStatus(st)
      setCommits(lg.commits ?? [])
      setError(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [resolveChat])

  // 初始加载 + 3s 自动轮询
  useEffect(() => {
    void refresh()
    const timer = setInterval(() => void refresh(), 3000)
    return () => clearInterval(timer)
  }, [refresh])

  const openDiff = useCallback(async (path: string) => {
    if (!rpc || !activeChat) return
    setDiffPath(path)
    setDiffLines([])
    try {
      const res = (await rpc('xbot.git-fancy.diff', { ...activeChat, path })) as DiffResult
      setDiffLines(res.lines ?? [])
    } catch {
      setDiffLines([{ kind: 'meta', old_line: 0, new_line: 0, text: '(diff 加载失败)' }])
    }
  }, [activeChat])

  if (!rpc) {
    return React.createElement('div', { className: 'p-2 text-xs text-text-muted' }, 'Git 插件未初始化')
  }

  if (loading && !status) {
    return React.createElement('div', { className: 'p-2 text-xs text-text-muted' }, '加载 git 状态…')
  }

  if (error && !status) {
    return React.createElement('div', { className: 'p-2 text-xs text-red-500' }, error)
  }

  if (status && !status.repo) {
    return React.createElement('div', { className: 'p-2 text-xs text-text-muted' }, '当前目录不是 git 仓库')
  }

  const totalAdded = status?.changes.reduce((a, c) => a + c.added, 0) ?? 0
  const totalDeleted = status?.changes.reduce((a, c) => a + c.deleted, 0) ?? 0

  const header = React.createElement(
    'div', { className: 'flex items-center justify-between border-b border-border px-2 py-1.5' },
    React.createElement(
      'span', { className: 'flex items-center gap-1.5 text-xs font-semibold text-text-primary' },
      React.createElement('span', { className: 'text-text-muted' }, '⎇'),
      status?.branch || '(detached)',
    ),
    React.createElement(
      'div', { className: 'flex items-center gap-2' },
      status && status.ahead > 0 && React.createElement(Stat, { label: '↑', value: String(status.ahead), cls: 'text-green-600' }),
      status && status.behind > 0 && React.createElement(Stat, { label: '↓', value: String(status.behind), cls: 'text-red-600' }),
      React.createElement(
        'button',
        { onClick: () => void refresh(), className: 'rounded px-1.5 py-0.5 text-[10px] text-text-muted hover:bg-bg-hover', title: '刷新' },
        '↻',
      ),
    ),
  )

  // 变更文件列表 —— 整行可点击打开 diff（触屏无 hover，不能用 group-hover
  // 隐藏操作按钮；± 统计常显）。
  const changeRows = (status?.changes ?? []).map((c) => {
    const badge = statusBadge(c.status)
    return React.createElement(
      'div',
      {
        key: c.path,
        onClick: () => void openDiff(c.path),
        className: 'flex cursor-pointer items-center gap-1.5 px-2 py-0.5 hover:bg-bg-hover active:bg-bg-hover',
        title: '查看 diff',
      },
      React.createElement('span', { className: `inline-flex h-4 w-4 shrink-0 items-center justify-center rounded text-[10px] font-bold ${badge.cls}` }, badge.label),
      React.createElement('span', { className: 'min-w-0 flex-1 truncate font-mono text-[11px] text-text-secondary' }, c.path),
      React.createElement(
        'span', { className: 'flex shrink-0 items-center gap-1 font-mono text-[10px]' },
        c.added > 0 && React.createElement('span', { className: 'text-green-600' }, `+${c.added}`),
        c.deleted > 0 && React.createElement('span', { className: 'text-red-600' }, `-${c.deleted}`),
        React.createElement('span', { className: 'text-text-muted' }, '›'),
      ),
    )
  })

  // 提交历史
  const commitRows = (commits ?? []).map((c) =>
    React.createElement(
      'div', { key: c.hash, className: 'flex items-center gap-1.5 px-2 py-0.5' },
      React.createElement('span', { className: 'font-mono text-[10px] text-text-muted' }, c.hash),
      React.createElement('span', { className: 'min-w-0 flex-1 truncate text-[11px] text-text-secondary' }, c.subject),
      React.createElement('span', { className: 'shrink-0 text-[10px] text-text-muted' }, c.when),
    ),
  )

  const diffBlock =
    diffPath !== null
      ? React.createElement(
          'div', { className: 'border-t border-border' },
          React.createElement(
            'div', { className: 'flex items-center justify-between px-2 py-1.5' },
            React.createElement('span', { className: 'font-mono text-[10px] text-text-muted' }, diffPath),
            React.createElement('button', { onClick: () => setDiffPath(null), className: 'text-[10px] text-text-muted hover:text-text-primary' }, '✕'),
          ),
          React.createElement(
            'div', { className: 'max-h-48 overflow-auto py-0.5' },
            diffLines.length > 0
              ? diffLines.map((l, i) => renderDiffLine(l, i))
              : React.createElement('div', { className: 'px-2 py-1 font-mono text-[10px] text-text-muted' }, '无 diff 或无改动'),
          ),
        )
      : null

  return React.createElement(
    'div', { className: 'flex h-full flex-col overflow-y-auto text-xs' },
    header,
    React.createElement(
      'div', { className: 'border-b border-border px-2 py-1 text-[10px] uppercase tracking-wide text-text-muted' },
      `变更 ${status?.changes.length ?? 0} · +${totalAdded} -${totalDeleted}`,
    ),
    React.createElement('div', { className: 'py-0.5' }, changeRows),
    React.createElement(
      'div', { className: 'border-t border-border px-2 py-1 text-[10px] uppercase tracking-wide text-text-muted' },
      '最近提交',
    ),
    React.createElement('div', { className: 'py-0.5' }, commitRows),
    diffBlock,
  )
}

export default GitFancyPanel
