/**
 * xbot-genui frontend plugin — 独立 ESM 模块，与第三方插件完全一致的加载路径：
 * 由 PluginRuntime 通过 `/plugins/xbot.genui/web/index.js` 动态 import，经
 * web_plugin_list → activate() 注册 messageRenderer，支持 web 面板热重载。
 *
 * 与 iteration-stats / git-fancy 相同：本模块不 import 任何宿主内部模块。
 * React 与通用 UI 原语（SandboxedUI）通过 window 全局获取（宿主持暴露）。
 *
 * 0-injection：只给 LLM React + hooks，无组件库。
 */

type ReactLike = typeof import('react')
interface WindowHost {
  React?: ReactLike
  __xbot_ui__?: { SandboxedUI?: (props: { code?: string; streaming?: boolean; className?: string }) => import('react').ReactElement }
}
const w = window as unknown as WindowHost
const React = w.React
const SandboxedUI = w.__xbot_ui__?.SandboxedUI

// ─── Code extraction ───────────────────────────────────────────
function stripGenUIPrefix(code: string): string {
  if (!code) return ''
  const lines = code.split('\n')
  if (lines.length > 1) {
    const first = lines[0].trim()
    if (!/^(import|export|const|function|class|return|\/\/|\/\*|#|\{|\()/.test(first) && /export default|function App|<[A-Z]/.test(code)) {
      return lines.slice(1).join('\n').trimStart()
    }
  }
  return code
}
function parseToolArgs(args?: string): Record<string, unknown> | null {
  if (!args) return null
  try { return JSON.parse(args) as Record<string, unknown> } catch { return null }
}
function genUICode(result: unknown): string {
  if (!result || typeof result !== 'object') return ''
  const r = result as { args?: string; detail?: string }
  const args = parseToolArgs(r.args)
  if (typeof args?.code === 'string' && args.code.trim()) return stripGenUIPrefix(args.code)
  if (r.detail) return stripGenUIPrefix(r.detail)
  return ''
}

// ─── activate ──────────────────────────────────────────────────
type RenderCtx = { chatID: string }
type MessageRenderer = {
  kind: 'messageRenderer'
  id: string
  priority: number
  matches: { uiMode?: string; tool?: string }
  render: (msg: { tool?: { result?: unknown } }, ctx: RenderCtx) => unknown
}
type ActivateCtx = { contributes: { register: (c: MessageRenderer) => () => void } }

export function activate(ctx: ActivateCtx): () => void {
  if (!React || !SandboxedUI) return () => {}

  const make = (id: string, priority: number, matches: MessageRenderer['matches']): MessageRenderer => ({
    kind: 'messageRenderer',
    id,
    priority,
    matches,
    render: (msg) => {
      const code = genUICode(msg.tool?.result)
      if (!code) return null
      return React.createElement(SandboxedUI, { code, streaming: false })
    },
  })

  const d1 = ctx.contributes.register(make('xbot.genui.renderer', 100, { uiMode: 'genui' }))
  const d2 = ctx.contributes.register(make('xbot.genui.legacy-display-html', 50, { tool: 'display_html' }))
  return () => { d1(); d2() }
}