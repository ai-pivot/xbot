/**
 * 消息区交互入口（用户 2026-09-15 二次定稿）：**电脑右键 / 手机长按**，不再有任何常驻或 hover 悬浮条
 * （用户：「这个悬浮太丑了还挡着」）。复制粒度覆盖三层：
 *   - message  ：整条回复（回复 / 含思考 / 含工具调用 / 原始 Markdown）
 *   - iteration：**每个迭代都有**（这段思考 / 该迭代正文 / 该迭代含工具）—— 用户明确要求
 *   - tools    ：该迭代里的**每个工具**（该工具输出 / 该命令参数）
 * 判定收敛在 resolveCopyText / buildCopyVariant / iterationCopyText / toolCopyText 里，
 * 保证"只要这条消息/迭代/工具可渲染就一定复制得到内容"（iterations-only 的回复也能复制）。
 *
 * 另外两类**落点相关**的动作（2026-09-22）：
 *   - **打开链接 / 复制链接地址**：本组件对 contextmenu 做了 preventDefault（否则冒出来的是浏览器
 *     原生菜单），所以链接必须由这里给入口；协议白名单 http/https/mailto，`javascript:`/`data:`/
 *     `file:` 一律拒绝（消息内容来自模型与用户输入，不能给它新开窗口提权）。
 *   - **复制选区**：桌面拖选文字后右键 → 复制选区；触屏是 select-none（无选区）故不出现。
 */
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import type { ChatMessage, WebIteration, WebToolProgress } from '@/types/shared'

import { useIsTouch } from '@/hooks/useIsMobile'
import { useI18n } from '@/providers/i18n'

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

/** 可打开的链接协议白名单：http / https / mailto（相对链接按 base 解析）。 */
export function resolveOpenableHref(href: string, base?: string): string | null {
  if (!href) return null
  let url: URL
  try {
    url = new URL(href, base ?? (typeof window !== 'undefined' ? window.location.href : undefined))
  } catch {
    return null
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:' && url.protocol !== 'mailto:') return null
  return url.href
}

type LinkTarget = { href: string; text: string }

/** 右键/长按落点若命中 `<a href>` → 解析出可打开的目标（白名单外 / 非链接返回 null）。 */
export function resolveLinkTarget(target: EventTarget | null, base?: string): LinkTarget | null {
  const el = target as HTMLElement | null
  const anchor =
    el && typeof (el as HTMLElement).closest === 'function' ? (el.closest('a[href]') as HTMLAnchorElement | null) : null
  if (!anchor) return null
  const href = resolveOpenableHref(anchor.getAttribute('href') ?? '', base)
  if (!href) return null
  return { href, text: (anchor.textContent ?? '').trim() }
}

/**
 * 打开菜单那一瞬的选区文本。必须在 openAt 里读一次就好：菜单挂载后会 focus，
 * 焦点移动可能让选区折叠，之后再读就取不到了。
 */
export function readSelectionText(): string {
  try {
    return (window.getSelection()?.toString() ?? '').trim()
  } catch {
    return ''
  }
}

/** 新标签打开。noopener,noreferrer：链接来自消息内容，不能让它拿到 opener。 */
function openInNewTab(href: string) {
  window.open(href, '_blank', 'noopener,noreferrer')
}

type MenuItem = { label: string; run: () => void }

type OpenState = {
  kind: 'message' | 'iteration' | 'tools'
  x: number
  y: number
  iteration?: WebIteration
  tools?: WebToolProgress[]
  /** 落点命中链接时的目标（无则不给「打开链接 / 复制链接地址」）。 */
  link?: LinkTarget | null
  /** 打开瞬间的选区文本（触屏 select-none ⇒ 通常为空）。 */
  selection?: string
} | null

/** 长按判定容差（px）：触屏手指抖动不超过它就不算"划动"。 */
const LONG_PRESS_TOLERANCE = 10

function useLongPress(open: (x: number, y: number, target: EventTarget | null) => void) {
  const timer = useRef<number | null>(null)
  const fired = useRef(false)
  /** 按下起点：用位移是否超过容差来判断"抖动"还是"划动"。 */
  const origin = useRef<{ x: number; y: number } | null>(null)
  const clear = useCallback(() => {
    if (timer.current != null) window.clearTimeout(timer.current)
    timer.current = null
    origin.current = null
  }, [])
  const onPointerDown = useCallback(
    (e: React.PointerEvent) => {
      if (e.pointerType === 'mouse') return // 鼠标走右键
      e.stopPropagation() // 嵌套目标里只让最内层起长按计时
      fired.current = false
      const { clientX, clientY } = e
      const target = e.target
      clear() // clear() 会重置 origin，因此在其之后再记录起点
      origin.current = { x: clientX, y: clientY }
      timer.current = window.setTimeout(() => {
        fired.current = true
        open(clientX, clientY, target)
      }, 480)
    },
    [clear, open],
  )
  const onPointerMove = useCallback(
    (e: React.PointerEvent) => {
      // 触屏上手指必定有轻微抖动：旧实现"任何位移都取消计时"（无容差）导致手机上
      // 长按几乎不可能成功 —— 用户 2026-09-16 报告「手机上没有复制 user msg 的交互」，
      // 根因就在这里（长按被 1~2px 的抖动取消，菜单永远不弹）。
      // 改为**容差 10px**：小幅抖动不取消，真正在滚动/划动（>10px）才取消。
      const o = origin.current
      if (!o) {
        clear()
        return
      }
      if (Math.abs(e.clientX - o.x) > LONG_PRESS_TOLERANCE || Math.abs(e.clientY - o.y) > LONG_PRESS_TOLERANCE) {
        clear()
      }
    },
    [clear],
  )
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
 * CopyTarget —— 把"右键 / 长按 → 动作菜单"挂到任意内容上（message / iteration / tools）。
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
  const { t } = useI18n()
  const [open, setOpen] = useState<OpenState>(null)
  const openAt = useCallback(
    (x: number, y: number, target: EventTarget | null) => {
      // 落点上下文（链接 / 选区）在这里一次算清：菜单挂载后会 focus，选区可能折叠。
      const link = resolveLinkTarget(target)
      const selection = readSelectionText()
      if (kind === 'message') setOpen({ kind: 'message', x, y, link, selection })
      else if (kind === 'iteration' && iteration) setOpen({ kind: 'iteration', x, y, iteration, link, selection })
      else if (kind === 'tools' && tools) setOpen({ kind: 'tools', x, y, tools, link, selection })
    },
    [kind, iteration, tools],
  )
  const press = useLongPress(openAt)
  const isTouch = useIsTouch()

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

  const items: MenuItem[] = (() => {
    if (!open) return []
    const list: MenuItem[] = []
    // ① 落点相关：链接（打开 / 复制地址）优先于泛化的复制项。
    if (open.link) {
      const { href } = open.link
      list.push({
        label: t('agent.copyMenu.openLink'),
        run: () => {
          openInNewTab(href)
          setOpen(null)
        },
      })
      list.push({ label: t('agent.copyMenu.copyLinkAddress'), run: () => void copy(href) })
    }
    // ② 选区相关（桌面拖选后右键；触屏 select-none 无选区）。
    if (open.selection) {
      const selection = open.selection
      list.push({ label: t('agent.copyMenu.copySelection'), run: () => void copy(selection) })
    }
    // ③ 三层粒度：message / iteration / tools。
    if (open.kind === 'message' && message) {
      list.push(
        { label: t('agent.copyMenu.copyReply'), run: () => void copy(buildCopyVariant(message, 'reply')) },
        { label: t('agent.copyMenu.copyWithThinking'), run: () => void copy(buildCopyVariant(message, 'thinking')) },
        { label: t('agent.copyMenu.copyWithTools'), run: () => void copy(buildCopyVariant(message, 'tools')) },
        { label: t('agent.copyMenu.copyRawMarkdown'), run: () => void copy(buildCopyVariant(message, 'raw')) },
      )
    } else if (open.kind === 'iteration' && open.iteration) {
      const it = open.iteration
      const variants: Array<[string, string]> = [
        [t('agent.copyMenu.copyIterationThinking'), iterationCopyText(it, 'thinking')],
        [t('agent.copyMenu.copyIterationContent'), iterationCopyText(it, 'content')],
        [t('agent.copyMenu.copyIterationAll'), iterationCopyText(it, 'all')],
      ]
      // 空项按设计过滤（不给无内容的复制项）。
      for (const [label, text] of variants) {
        if (text) list.push({ label, run: () => void copy(text) })
      }
    } else if (open.kind === 'tools' && open.tools) {
      for (const tl of open.tools) {
        const label = t('agent.copyMenu.copyTool', { name: tl.label || tl.name })
        const text = toolCopyText(tl, 'output')
        if (text) list.push({ label, run: () => void copy(text) })
      }
      const all = open.tools
        .map((tl) => toolCopyText(tl, 'output'))
        .filter(Boolean)
        .join('\n\n')
      if (all) list.push({ label: t('agent.copyMenu.copyAllToolOutput'), run: () => void copy(all) })
    }
    return list
  })()

  return (
    <>
      <div
        data-copy-target={kind}
        // ⚠️ 包裹层自身必须 `min-w-0`：它常被插进 flex 行里（如 tools ⊂ iteration ⊂ message），
        // flex 子项默认 `min-width: auto` ⇒ 拒绝收缩到内容宽度以下 ⇒ 长参数把整行撑满，
        // 手机端 pill 退化成"一行一个"（2026-09-15 用户报告；根因就是我这一层漏了 min-w-0）。
        className={[
          'min-w-0',
          // 触屏：禁用原生文本选择与 iOS 长按 callout。否则长按消息会弹出浏览器的
          // 蓝色选中高亮 + 选择控件 —— 它在虚拟滚动 + transform 容器里位置不受我们
          // 控制（用户 2026-09-16：「手机长按老是变出那个蓝色选中判定，位置还根本
          // 不对」），并且会抢走长按手势（复制菜单永远弹不出来）。
          // 触屏的复制入口 = 长按菜单 / 可见复制按钮；桌面保留原生选择（拖选 / 右键复制）。
          isTouch ? 'select-none [-webkit-touch-callout:none]' : '',
          className ?? '',
        ]
          .filter(Boolean)
          .join(' ')}
        onContextMenu={(e) => {
          // 右键会冒泡：嵌套目标（tools ⊂ iteration ⊂ message）里只让**最内层**开菜单，
          // 否则会同时弹出 3 个菜单（用户右键工具时显然只要工具那一份）。
          e.preventDefault()
          e.stopPropagation()
          openAt(e.clientX, e.clientY, e.target)
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
            <CopyMenu x={open.x} y={open.y} items={items} onClose={() => setOpen(null)} />
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
  onClose,
}: {
  x: number
  y: number
  items: MenuItem[]
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
              it.run()
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
            it.run()
          }}
          className="block w-full px-3 py-2 text-left text-[12.5px] text-text-primary hover:bg-bg-tertiary"
        >
          {it.label}
        </button>
      ))}
    </div>
  )
}
