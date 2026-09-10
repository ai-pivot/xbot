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

// 本插件公开分享的内容类型。宿主不认识它 —— 只是把 artifact 原样交回本插件。
const SHARE_CONTENT_TYPE = 'xbot.genui/tsx'

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
  const r = result as { args?: string; detail?: string; status?: string; content?: string }
  // ⚠️ NEVER render UI for a FAILED tool call. A rejected/rejected-then-retried
  // display_html keeps its complete args.code but has NO persisted ui_code
  // (empty detail, error result) — rendering it produced a full-height expanded
  // panel for UI that never rendered, poisoning the virtual row height
  // (2026-08-28: three depth-2-rejected drafts each rendered ~700px of ghost
  // panel and broke fold/height calculations).
  if (r.status === 'error') return ''
  if (typeof r.content === 'string' && r.content.startsWith('Error:')) return ''
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
type ShareRenderer = {
  kind: 'shareRenderer'
  id: string
  contentType: string
  render: (artifact: { payload: string; title: string }) => unknown
}
type ShareAPI = {
  create: (input: { contentType: string; payload: string; title?: string }) => Promise<{ path: string }>
  registerRenderer: (decl: ShareRenderer) => () => void
}
type ActivateCtx = {
  contributes: { register: (c: MessageRenderer) => () => void }
  share?: ShareAPI
}

/**
 * 面板外壳 + 分享按钮。
 *
 * 分享是**插件自己**的 UI 能力：宿主只提供一个通用的一次性 API
 * （ctx.share.create），宿主完全不知道分享出去的是什么内容。未声明 share
 * 权限时原样渲染（能力即类型）。
 */
function ShareablePanel({ code, share, children }: { code: string; share?: ShareAPI; children?: unknown }) {
  // 插件模块从 window 取 React（可能未注入）—— activate 已 guard，这里取局部
  // 非空引用让类型收窄。
  const R = React as ReactLike
  const [state, setState] = R.useState<'idle' | 'busy' | 'done' | 'error'>('idle')
  const [url, setUrl] = R.useState('')

  const onShare = async (e: { stopPropagation: () => void }) => {
    e.stopPropagation()
    if (!share || state === 'busy') return
    setState('busy')
    try {
      // 只快照这份 TSX —— 面板是自包含的，链接不携带任何会话或数据访问权。
      const link = await share.create({
        contentType: SHARE_CONTENT_TYPE,
        payload: code,
        title: 'GenUI panel',
      })
      const absolute = new URL(link.path, window.location.origin).toString()
      setUrl(absolute)
      setState('done')
      try {
        await navigator.clipboard.writeText(absolute)
      } catch {
        // 剪贴板不可用（非安全上下文）——链接仍显示在按钮上，用户可手动复制。
      }
    } catch {
      setState('error')
    }
  }

  const label =
    state === 'busy' ? '生成链接…'
      : state === 'done' ? '已复制链接'
        : state === 'error' ? '分享失败，重试'
          : '分享'

  const button = share
    ? R.createElement(
        'button',
        {
          type: 'button',
          onClick: onShare,
          title: state === 'done' && url ? url : '生成公开链接（任何拿到链接的人都能查看）',
          'data-testid': 'genui-share',
          style: {
            position: 'absolute', top: '6px', right: '8px', zIndex: 10,
            padding: '2px 8px', fontSize: '11px', lineHeight: '18px',
            borderRadius: '6px', cursor: 'pointer',
            border: '1px solid rgba(148,163,184,.4)',
            background: state === 'done' ? 'rgba(34,197,94,.15)' : 'rgba(15,23,42,.06)',
            color: state === 'error' ? '#dc2626' : 'inherit',
          },
        },
        label,
      )
    : null

  return R.createElement('div', { style: { position: 'relative' } }, button, children as never)
}

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
      // ⚠️ The committed render MUST be height-bounded. The streaming path wraps
      // SandbredUI in GenUIPanel (max-h-[70vh] content), but the historical path
      // may render BARE (surface metadata is not persisted on every historical
      // tool snapshot → ToolRender skips its GenUIPanel wrapper). An unbounded
      // 2000px+ dashboard inside a virtual row estimated at 560px made rows
      // overlap (2026-08-28 历史渲染重叠). Renderer output contract: GenUI
      // content is ALWAYS height-bounded.
      const ui = React.createElement(SandboxedUI, { code, streaming: false, className: 'max-h-[70vh] overflow-auto' })
      return React.createElement(ShareablePanel, { code, share: ctx.share }, ui)
    },
  })

  const d1 = ctx.contributes.register(make('xbot.genui.renderer', 100, { uiMode: 'genui' }))
  const d2 = ctx.contributes.register(make('xbot.genui.legacy-display-html', 50, { tool: 'display_html' }))

  // 公开分享（通用能力）：注册渲染器 —— 宿主在 /s/:token 页按 contentType 派发
  // 回来。只有被主动分享过的 artifact 才会带 payload 到这里；插件不参与鉴权。
  const d3 = ctx.share?.registerRenderer({
    kind: 'shareRenderer',
    id: 'xbot.genui.share',
    contentType: SHARE_CONTENT_TYPE,
    render: (artifact) =>
      React.createElement(SandboxedUI, {
        code: artifact.payload,
        streaming: false,
        className: 'max-h-[80vh] overflow-auto',
      }),
  })
  return () => { d1(); d2(); d3?.() }
}