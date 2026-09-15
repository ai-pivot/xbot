/**
 * 复制入口（用户 2026-09-15 二次定稿）：**电脑右键 / 手机长按**，不再有任何常驻或 hover 悬浮条
 * （用户：「这个悬浮太丑了还挡着」）。复制粒度覆盖三层：
 *   - message  ：整条回复（回复 / 含思考 / 含工具调用 / 原始 Markdown）
 *   - iteration：**每个迭代都有**（这段思考 / 该迭代正文 / 该迭代含工具）—— 用户明确要求
 *   - tools    ：该迭代里的**每个工具**（该工具输出 / 该命令参数）
 * 判定收敛在 resolveCopyText / buildCopyVariant / iterationCopyText / toolCopyText 里，
 * 保证"只要这条消息/迭代/工具可渲染就一定复制得到内容"（iterations-only 的回复也能复制）。
 */
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import type { ChatMessage, WebIteration, WebToolProgress } from '@/types/shared'

export type CopyVariant = 'reply' | 'thinking' | 'tools' | 'raw'
export type IterationVariant = 'thinking' | 'content' | 'all'
export type ToolVariant = 'output' | 'command'

/** 消息级复制内容：顶层 content → 最后一迭代正文 → 该迭代思考。 */
export function resolveCopyText(message: ChatMessage): string {
  if (message.role === 'user') return message.content ?? ''
  if (message.content) return message.content
  const its = message.iterations ?? []
  for (let i = its.length - 1; i >= 0; i--) {
    const it = its[i]
    if (it?.content) return it.content
    if (it?.reasoning) return it.reasoning
  }
  return ''
}

export function buildCopyVariant(message: ChatMessage, variant: CopyVariant): string {
  const reply = resolveCopyText(message)
  if (variant === 'reply') return reply
  const its = message.iterations ?? []
  if (variant === 'thinking') {
    const think = its
      .filter((it) => it?.reasoning)
      .map((it) => `### 思考 ${it.iteration ?? ''}\n${it.reasoning}`)
      .join('\n\n')
    return think ? `${think}\n\n---\n\n${reply}` : reply
  }
  if (variant === 'tools') {
    const lines = its.flatMap((it) =>
      (it?.tools ?? []).map((tl) => `- [迭代 ${it.iteration ?? ''}] ${tl.label || tl.name}${toolBody(tl) ? `\n${toolBody(tl)}` : ''}`),
    )
    return lines.length ? `${lines.join('\n')}\n\n---\n\n${reply}` : reply
  }
  return '```md\n' + reply + '\n```'
}

function toolBody(tl: WebToolProgress): string {
  return (tl.detail || tl.summary || '').trim()
}

/** 迭代级复制：该迭代的思考 / 正文 / 全部（含工具）。 */
export function iterationCopyText(it: WebIteration, variant: IterationVariant): string {
  const head = `## 迭代 ${it.iteration ?? ''}`
  if (variant === 'thinking') return it.reasoning || ''
  if (variant === 'content') return it.content || ''
  const parts: string[] = [head]
  if (it.reasoning) parts.push(`### 思考\n${it.reasoning}`)
  if (it.content) parts.push(`### 正文\n${it.content}`)
  for (const tl of it.tools ?? []) {
    parts.push(`### 工具 ${tl.label || tl.name}\n${toolBody(tl)}`)
  }
  return parts.join('\n\n')
}

/** 工具级复制：该工具的原始输出 / 该命令参数。 */
export function toolCopyText(tl: WebToolProgress, variant: ToolVariant): string {
  if (variant === 'command') return (tl.args || '').trim()
  return toolBody(tl) || (tl.args || '').trim()
}

type OpenState =
  | { kind: 'message'; x: number; y: number }
  | { kind: 'iteration'; x: number; y: number; iteration: WebIteration }
  | { kind: 'tools'; x: number; y: number; tools: WebToolProgress[] }
  | null

function useLongPress(open: (x: number, y: number) => void) {
  const timer = useRef<number | null>(null)
  const fired = useRef(false)
  const clear = useCallback(() => {
    if (timer.current != null) window.clearTimeout(timer.current)
    timer.current = null
  }, [])
  const onPointerDown = useCallback(
    (e: React.PointerEvent) => {
      if (e.pointerType === 'mouse') return // 鼠标走右键
      e.stopPropagation() // 嵌套目标里只让最内层起长按计时
      fired.current = false
      const { clientX, clientY } = e
      clear()
      timer.current = window.setTimeout(() => {
        fired.current = true
        open(clientX, clientY)
      }, 480)
    },
    [clear, open],
  )
  const onPointerMove = useCallback(() => clear(), [clear])
  const onClickCapture = useCallback((e: React.MouseEvent) => {
    if (fired.current) {
      e.preventDefault()
      e.stopPropagation()
      fired.current = false
    }
  }, [])
  return { onPointerDown, onPointerMove, onPointerUp: clear, onPointerCancel: clear, onPointerLeave: clear, onClickCapture }
}

/**
 * CopyTarget —— 把"右键 / 长按 → 复制菜单"挂到任意内容上（message / iteration / tools）。
 * 不渲染任何可见 UI（只有触发后才出现菜单/面板），因此**不占位、不遮挡**。
 */
export function CopyTarget({
  kind,
  message,
  iteration,
  tools,
  children,
  className,
}: {
  kind: 'message' | 'iteration' | 'tools'
  message?: ChatMessage
  iteration?: WebIteration
  tools?: WebToolProgress[]
  children: ReactNode
  className?: string
}) {
  const [open, setOpen] = useState<OpenState>(null)
  const openAt = useCallback(
    (x: number, y: number) => {
      if (kind === 'message') setOpen({ kind: 'message', x, y })
      else if (kind === 'iteration' && iteration) setOpen({ kind: 'iteration', x, y, iteration })
      else if (kind === 'tools' && tools) setOpen({ kind: 'tools', x, y, tools })
    },
    [kind, iteration, tools],
  )
  const press = useLongPress(openAt)

  // ⚠️ 不使用任何全局 window 监听（仓库规则：per-session 代码禁止全局监听，防跨会话污染）。
  // 关闭方式改为纯 React：① 菜单底下铺一层**透明遮罩**，点它即关闭；
  // ② 菜单自身 onKeyDown 处理 Esc（菜单挂载时自动聚焦）。
  const copy = useCallback(async (text: string) => {
    if (!text) return
    try {
      await navigator.clipboard.writeText(text)
    } catch {
      /* 无剪贴板权限时静默 */
    }
    setOpen(null)
  }, [])

  const items: Array<{ label: string; text: string }> = (() => {
    if (!open) return []
    if (open.kind === 'message' && message) {
      return [
        { label: '复制回复', text: buildCopyVariant(message, 'reply') },
        { label: '复制含思考', text: buildCopyVariant(message, 'thinking') },
        { label: '复制含工具调用', text: buildCopyVariant(message, 'tools') },
        { label: '查看原始 Markdown', text: buildCopyVariant(message, 'raw') },
      ]
    }
    if (open.kind === 'iteration') {
      const it = open.iteration
      const list = [
        { label: '复制这段思考', text: iterationCopyText(it, 'thinking') },
        { label: '复制该迭代正文', text: iterationCopyText(it, 'content') },
        { label: '复制该迭代（含工具）', text: iterationCopyText(it, 'all') },
      ]
      return list.filter((i) => i.text)
    }
    if (open.kind === 'tools') {
      const list = open.tools.map((tl) => ({ label: `复制：${tl.label || tl.name}`, text: toolCopyText(tl, 'output') }))
      const all = open.tools.map((tl) => toolCopyText(tl, 'output')).filter(Boolean).join('\n\n')
      if (all) list.push({ label: '复制全部工具输出', text: all })
      return list.filter((i) => i.text)
    }
    return []
  })()

  return (
    <>
      <div
        data-copy-target={kind}
        className={className}
        onContextMenu={(e) => {
          // 右键会冒泡：嵌套目标（tools ⊂ iteration ⊂ message）里只让**最内层**开菜单，
          // 否则会同时弹出 3 个菜单（用户右键工具时显然只要工具那一份）。
          e.preventDefault()
          e.stopPropagation()
          openAt(e.clientX, e.clientY)
        }}
        {...press}
      >
        {children}
      </div>
      {open &&
        items.length > 0 &&
        typeof document !== 'undefined' &&
        // ⚠️ 必须 portal 到 body：虚拟行带 `transform: translateY(...)`，会把它内部的
        // `position: fixed` 变成**相对该行**定位；再叠加 `.virt-row{contain:layout}` /
        // `.iter-block{contain:layout paint}` 的裁剪 ⇒ 面板跑到对话中间且只露出一行
        //（2026-09-15 我自己截图发现的缺陷）。
        createPortal(
          <>
            {/* 透明遮罩：点任意处关闭（替代 window click 监听）。 */}
            <div
              data-testid="copy-backdrop"
              className="fixed inset-0 z-40"
              onMouseDown={() => setOpen(null)}
              onWheel={() => setOpen(null)}
              onContextMenu={(e) => {
                e.preventDefault()
                setOpen(null)
              }}
            />
            <CopyMenu x={open.x} y={open.y} items={items} onPick={(text) => void copy(text)} onClose={() => setOpen(null)} />
          </>,
          document.body,
        )}
    </>
  )
}

/** 菜单/面板：桌面按光标定位；触屏（窄屏）落到底部面板（≥44px 命中区）。 */
function CopyMenu({
  x,
  y,
  items,
  onPick,
  onClose,
}: {
  x: number
  y: number
  items: Array<{ label: string; text: string }>
  onPick: (text: string) => void
  onClose?: () => void
}) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    ref.current?.focus()
  }, [])
  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape') onClose?.()
  }
  const [sheet] = useState(() => {
    try {
      return window.matchMedia('(max-width: 640px), (hover: none)').matches
    } catch {
      return false
    }
  })
  if (sheet) {
    return (
      <div
        ref={ref}
        tabIndex={-1}
        onKeyDown={onKeyDown}
        data-testid="copy-sheet"
        className="fixed inset-x-0 bottom-0 z-50 rounded-t-xl border-t border-border bg-bg-secondary p-2 pb-3 shadow-2xl focus:outline-none"
      >
        {items.map((it) => (
          <button
            key={it.label}
            type="button"
            onClick={(e) => {
              e.stopPropagation()
              onPick(it.text)
            }}
            className="block min-h-11 w-full px-3 py-3 text-left text-sm text-text-primary active:bg-bg-tertiary"
          >
            {it.label}
          </button>
        ))}
      </div>
    )
  }
  const left = Math.min(x, Math.max(8, window.innerWidth - 220))
  const top = Math.min(y, Math.max(8, window.innerHeight - (items.length * 34 + 16)))
  return (
    <div
      ref={ref}
      tabIndex={-1}
      onKeyDown={onKeyDown}
      data-testid="copy-menu"
      style={{ left, top }}
      className="fixed z-50 w-52 overflow-hidden rounded-lg border border-border bg-bg-secondary py-1 shadow-xl focus:outline-none"
    >
      {items.map((it) => (
        <button
          key={it.label}
          type="button"
          onClick={(e) => {
            e.stopPropagation()
            onPick(it.text)
          }}
          className="block w-full px-3 py-2 text-left text-[12.5px] text-text-primary hover:bg-bg-tertiary"
        >
          {it.label}
        </button>
      ))}
    </div>
  )
}
