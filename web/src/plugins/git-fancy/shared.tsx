/**
 * xbot.git-fancy 共享模块 —— 类型 + RPC/UI 桥 + 通用渲染工具。
 *
 * 三个入口（entry.tsx 侧边栏面板 / diff.tsx / commit.tsx 主编辑区动态视图）
 * 经 esbuild --splitting 共享本模块的单例：activate(ctx) 在主入口被调用时
 * 注入 rpc/ui，三个视图模块读取同一实例（ESM 模块缓存按 URL，共享 chunk
 * 无 query → 同一实例）。
 *
 * 不 import 任何宿主内部模块——React 从 window 获取，rpc/ui 在 activate(ctx)
 * 时注入并存模块级变量。
 */

const w = window as unknown as { React: typeof import('react') }

export const React = w.React

// ---------- 类型（与后端 main.go 的 JSON 输出一一对应） ----------

export interface GitChange {
  path: string
  status: string
  added: number
  deleted: number
}

export interface GitStatus {
  repo: boolean
  branch: string
  clean: boolean
  changes: GitChange[]
  ahead: number
  behind: number
  error?: string
}

export interface GitCommit {
  hash: string
  author: string
  when: string
  subject: string
}

export interface GitLogResult {
  commits: GitCommit[]
  total: number
}

export interface DiffLine {
  kind: 'hunk' | 'add' | 'del' | 'ctx' | 'meta'
  old_line: number
  new_line: number
  text: string
}

export interface DiffResult {
  path: string
  content: string
  lines: DiffLine[]
  commit?: string
  untracked?: boolean
}

export interface CommitFile {
  path: string
  status: string
  added: number
  deleted: number
}

export interface CommitDetail {
  hash: string
  short: string
  author: string
  email: string
  date: string
  message: string
  files: CommitFile[]
  error?: string
}

// ---------- RPC / UI / Events 桥（activate 注入） ----------

/** 后端 RPC 调用器——由主入口 activate(ctx) 注入。 */
type RpcCall = (method: string, params: Record<string, unknown>) => Promise<unknown>

/** UI 能力（openViewTab/openDiffTab 等）——由主入口 activate(ctx) 注入。 */
export interface PluginUIApi {
  openViewTab(options: {
    viewId: string
    title: string
    icon?: string
    key?: string
    params?: Record<string, unknown>
  }): void
  openFileTab(path: string): void
  /** 宿主原生 diff 编辑器 tab（VSCode DiffEditor，插件零渲染）。 */
  openDiffTab(options: {
    title: string
    original: string
    modified: string
    path?: string
    key?: string
    scope?: string
  }): void
}

/** 事件 API 子集（仅需 on/once/dispose）。 */
export interface PluginEventsApi {
  on(name: string, handler: (payload: unknown) => void): () => void
}

let rpc: RpcCall | null = null
let ui: PluginUIApi | null = null
let events: PluginEventsApi | null = null

/** 主入口 activate(ctx) 调用——注入 rpc/ui/events 到共享单例。 */
export function setSharedApi(rpcCall?: RpcCall, uiApi?: PluginUIApi, eventsApi?: PluginEventsApi): void {
  if (rpcCall) rpc = rpcCall
  if (uiApi) ui = uiApi
  if (eventsApi) events = eventsApi
}

export function getRpc(): RpcCall | null {
  return rpc
}

export function getUi(): PluginUIApi | null {
  return ui
}

// ---------- 会话变化订阅（通用事件机制） ----------

/**
 * 订阅会话切换事件（对标 VSCode onDidChangeActiveEditor）。
 * 宿主在 activeSession 变化时发射 session.switched 事件，
 * 插件通过 ctx.events.on('session.switched', handler) 订阅。
 * 返回退订函数。
 */
export function onSessionChange(handler: () => void): () => void {
  if (!events) return () => {}
  return events.on('session.switched', handler)
}

// ---------- 会话解析 ----------

/** 从当前会话解析 channel/chatID —— 宿主注入 window.__xbot_session__（若有）。 */
export function resolveChat(): { channel: string; chatID: string } {
  const s = (window as unknown as { __xbot_session__?: { channel?: string; chatID?: string } }).__xbot_session__
  if (s?.chatID) {
    return { channel: s.channel ?? 'web', chatID: s.chatID }
  }
  return { channel: 'web', chatID: 'default' }
}

/** 调用 git-fancy 后端 RPC（带 session 标识，后端注入 cwd）。 */
export async function gitRpc<T>(method: string, extra: Record<string, unknown> = {}): Promise<T> {
  if (!rpc) throw new Error('Git 插件未初始化（rpc 未注入）')
  const res = await rpc(`xbot.git-fancy.${method}`, { ...resolveChat(), ...extra })
  return res as T
}

// ---------- 视图打开（editor tab） ----------

export const COMMIT_VIEW_ID = 'xbot.git-fancy.commit'

let warnedUiMissing = false

/**
 * 打开文件 diff（宿主原生 DiffEditor tab，插件零渲染）：拉取两侧内容
 * （original = 父版本/HEAD/空，modified = commit 版本/工作区）后经
 * ctx.ui.openDiffTab 交给宿主——语言推断、Monaco 渲染、tab 去重全由宿主负责。
 */
export async function openDiffTab(path: string, commit?: string): Promise<void> {
  if (!ui) {
    // 权限缺失诊断：plugin.json permissions 必须含 "ui" 才有 ctx.ui。
    if (!warnedUiMissing) {
      warnedUiMissing = true
      console.warn('[git-fancy] openDiffTab 不可用：ctx.ui 未注入（检查 plugin.json permissions 是否含 "ui"）')
    }
    return
  }
  const res = await gitRpc<{ original?: string; modified?: string }>('diff', {
    path,
    commit: commit ?? '',
  })
  const label = path.split('/').pop() ?? path
  ui.openDiffTab({
    title: commit ? `${label} @${commit.slice(0, 7)}` : label,
    original: res.original ?? '',
    modified: res.modified ?? '',
    path,
    key: commit ? `git-diff:${commit}:${path}` : `git-diff:worktree:${path}`,
    scope: commit ? `commit ${commit.slice(0, 7)}` : '工作区',
  })
}

/** 在主编辑区打开 commit 详情 tab。 */
export function openCommitTab(hash: string, subject: string): void {
  if (!ui) {
    if (!warnedUiMissing) {
      warnedUiMissing = true
      console.warn('[git-fancy] openCommitTab 不可用：ctx.ui 未注入（检查 plugin.json permissions 是否含 "ui"）')
    }
    return
  }
  ui.openViewTab({
    viewId: COMMIT_VIEW_ID,
    title: `${hash.slice(0, 7)} ${subject}`.trim(),
    icon: 'git-commit-horizontal',
    key: `git-commit:${hash}`,
    params: { hash },
  })
}

// ---------- 通用渲染工具 ----------

/** 状态徽章样式（U/M/A/D/R/C）。 */
export function statusBadge(status: string): { label: string; cls: string } {
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

/** VSC 风格 diff 行渲染（全宽 tab 版：双列行号 + +/- 着色）。 */
export function DiffLineRow({ line }: { line: DiffLine }) {
  let cls = ''
  let gutter = ''
  switch (line.kind) {
    case 'add':
      cls = 'bg-green-500/10 text-green-700 dark:text-green-400'
      gutter = '+'
      break
    case 'del':
      cls = 'bg-red-500/10 text-red-700 dark:text-red-400'
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
  return (
    <div className={`flex items-stretch font-mono text-[11px] leading-[1.6] ${cls}`}>
      <span className="w-5 shrink-0 select-none text-center opacity-70">{gutter}</span>
      <span className="w-12 shrink-0 select-none text-right pr-2 opacity-50">{line.old_line || ''}</span>
      <span className="w-12 shrink-0 select-none text-right pr-2 opacity-50">{line.new_line || ''}</span>
      <span className="min-w-0 flex-1 whitespace-pre-wrap break-all pr-2">{line.text}</span>
    </div>
  )
}
