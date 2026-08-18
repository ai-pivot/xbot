import { createElement } from 'react'

import type { MessageRendererContribution } from '@/plugin-api'
import type { WebToolProgress } from '@/types/shared'

import { GenUIBlock } from './GenUIBlock'

/**
 * isGenUITool — metadata-driven GenUI detection.
 *
 * A tool renders via the GenUI runtime when — and ONLY when — it declares
 * ui.mode === 'genui' (populated from the tool's UIDecl on the backend).
 * The legacy `display_html` tool-name fallback has been REMOVED: pre-metadata
 * history rows are served by the `builtinLegacyDisplayHtmlRenderer`
 * messageRenderer below, not by a host-side name special-case.
 *
 * See docs/agent/genui-plugin-design.md §9.
 */
export function isGenUITool(tool: WebToolProgress | { name?: string; uiMode?: string } | null | undefined): boolean {
  if (!tool) return false
  return tool.uiMode === 'genui'
}

/** Extract the GenUI source code from a tool (args.code preferred, Detail fallback). */
export function genUICode(tool: WebToolProgress | { detail?: string; args?: string } | null | undefined): string {
  if (!tool) return ''
  if (tool.detail) return stripGenUIPrefix(tool.detail)
  return ''
}

/**
 * Strip the Summary prefix that legacy (pre-plugin-fix) display_html tools
 * stored in Detail: "🎨 UI rendered (5929 chars)\nexport default function App() {...}".
 * The prefix makes sucrase compilation fail → blank iframe. New backend writes
 * pure TSX to Detail; this normalizes BOTH old history rows and any future
 * prefix leakage so the committed render always compiles.
 */
export function stripGenUIPrefix(code: string): string {
  if (!code) return ''
  // Match a leading line like "🎨 UI rendered (5929 chars)" (any summary text
  // before the first code marker). We only strip when the remainder looks like
  // a TSX module (contains "export default" / "function App" / "=>" / JSX).
  const lines = code.split('\n')
  if (lines.length > 1) {
    const first = lines[0].trim()
    // Legacy Summary prefix patterns: "🎨 UI rendered (N chars)" or any
    // non-code header line followed by a TSX module.
    if (!/^(import|export|const|function|class|return|\/\/|\/\*|#|\{|\()/.test(first) && /export default|function App|<[A-Z]/.test(code)) {
      return lines.slice(1).join('\n').trimStart()
    }
  }
  return code
}

/**
 * builtinGenuiRenderer —— 内置 GenUI messageRenderer 声明。
 *
 * 这是「GenUI 迁移到 messageRenderers 声明」的落点：宿主不再硬编码
 * `tool.name === 'display_html'` 分支，而是把一个 messageRenderer 贡献点
 * 注册到 PluginRuntime（registerBuiltinRenderer），由 renderTool 调度器按
 * matches 匹配派发。
 *
 * matches 用 { uiMode: 'genui' }（metadata-driven）而非工具名——任何声明
 * ui.mode === 'genui' 的工具（display_html 或未来的新工具）都走此渲染。
 */
export const builtinGenuiRenderer: MessageRendererContribution = {
  kind: 'messageRenderer',
  id: 'xbot.genui',
  priority: 100,
  matches: { uiMode: 'genui' },
  render: (msg) => {
    const tool = (msg as { tool?: { result?: unknown } }).tool?.result as
      | WebToolProgress
      | undefined
    const code = genUICode(tool)
    if (!code) return null
    return createElement(GenUIBlock, { code })
  },
}

/**
 * builtinLegacyDisplayHtmlRenderer —— pre-metadata 历史消息兜底。
 *
 * 旧的（元数据存在前持久化的）display_html 工具没有 uiMode，只有 name。
 * 兜底通过「渲染器声明」（matches { tool: 'display_html' }）而非宿主
 * name 硬编码，符合「删除 display_html 硬编码」——display_html 字符串只
 * 出现在 messageRenderer 的 matches 声明里（插件系统的可插拔映射），
 * 不出现在宿主渲染的 switch/if 分支里。
 */
export const builtinLegacyDisplayHtmlRenderer: MessageRendererContribution = {
  kind: 'messageRenderer',
  id: 'xbot.genui.legacy-display-html',
  priority: 50,
  matches: { tool: 'display_html' },
  render: (msg) => {
    const tool = (msg as { tool?: { result?: unknown } }).tool?.result as
      | WebToolProgress
      | undefined
    const code = genUICode(tool)
    if (!code) return null
    return createElement(GenUIBlock, { code })
  },
}
