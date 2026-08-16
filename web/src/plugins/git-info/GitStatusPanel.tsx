/**
 * GitStatusPanel —— git-info 插件的 fancy web 视图。
 *
 * 数据来源：git-info.sh（script 插件）输出 "style|text" spans，经后端
 * web_widgets 推送到前端 PluginWidgetProvider。本组件从 infoBar zone 解析
 * 出分支/变更/领先/落后，fancy 渲染成带图标的 Git 状态徽章组。
 *
 * 这是"插件适配新系统"的示例：script 插件保持后端数据生产，前端经
 * PluginRuntime 的 view 贡献点（builtin 视图）渲染 fancy UI。
 */
import { GitBranch, GitCommitHorizontal, ArrowUp, ArrowDown, CircleDashed } from 'lucide-react'

import { usePluginWidgets } from '@/plugins/PluginWidgetProvider'

/** 解析 git-info.sh 输出的 "git:branch ΔN ↑A ↓B" 文本。 */
function parseGitSpan(text: string): {
  branch: string
  changes: number
  ahead: number
  behind: number
  clean: boolean
  notRepo: boolean
} | null {
  if (!text.startsWith('git:')) return null
  const body = text.slice('git:'.length).trim()
  if (body === '—' || body === '-' || body === '') {
    return { branch: '', changes: 0, ahead: 0, behind: 0, clean: true, notRepo: true }
  }
  // 提取分支名（到第一个空格或状态标记）
  const branchMatch = body.match(/^([^\sΔ↑↓✓]+)/)
  const branch = branchMatch?.[1] ?? ''
  const changes = /Δ(\d+)/.exec(body)?.[1] ? Number(/Δ(\d+)/.exec(body)![1]) : 0
  const ahead = /↑(\d+)/.exec(body)?.[1] ? Number(/↑(\d+)/.exec(body)![1]) : 0
  const behind = /↓(\d+)/.exec(body)?.[1] ? Number(/↓(\d+)/.exec(body)![1]) : 0
  const clean = body.includes('✓')
  return { branch, changes, ahead, behind, clean, notRepo: false }
}

export function GitStatusPanel() {
  const { zones } = usePluginWidgets()
  const spans = zones['infoBar'] ?? []
  let git: ReturnType<typeof parseGitSpan> = null
  for (const span of spans) {
    if (span.text?.startsWith('git:')) {
      git = parseGitSpan(span.text)
      break
    }
  }

  if (!git || git.notRepo) {
    return (
      <span className="inline-flex items-center gap-1 rounded-full border border-slate-200 bg-slate-50 px-2 py-0.5 text-[11px] text-slate-400">
        <GitBranch className="size-3" />
        not a repo
      </span>
    )
  }

  return (
    <span className="inline-flex items-center gap-2">
      {/* 分支徽章 */}
      <span className="inline-flex items-center gap-1 rounded-full border border-indigo-200 bg-indigo-50 px-2 py-0.5 text-[11px] font-medium text-indigo-600">
        <GitBranch className="size-3" />
        {git.branch}
      </span>
      {/* 状态指示：干净 / 有变更 */}
      {git.changes > 0 ? (
        <span className="inline-flex items-center gap-1 rounded-full border border-amber-200 bg-amber-50 px-2 py-0.5 text-[11px] text-amber-600">
          <CircleDashed className="size-3" />
          Δ{git.changes}
        </span>
      ) : (
        <span className="inline-flex items-center gap-1 rounded-full border border-green-200 bg-green-50 px-2 py-0.5 text-[11px] text-green-600">
          <GitCommitHorizontal className="size-3" />
          clean
        </span>
      )}
      {/* 领先/落后 */}
      {git.ahead > 0 && (
        <span className="inline-flex items-center gap-1 rounded-full border border-blue-200 bg-blue-50 px-2 py-0.5 text-[11px] text-blue-600">
          <ArrowUp className="size-3" />
          {git.ahead}
        </span>
      )}
      {git.behind > 0 && (
        <span className="inline-flex items-center gap-1 rounded-full border border-purple-200 bg-purple-50 px-2 py-0.5 text-[11px] text-purple-600">
          <ArrowDown className="size-3" />
          {git.behind}
        </span>
      )}
    </span>
  )
}

export default GitStatusPanel
