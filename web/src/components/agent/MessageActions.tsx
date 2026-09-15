/**
 * MessageActions — 每条消息统一的操作入口（用户 2026-09-15 定稿方案 R1b）。
 *
 * 设计要点（由两轮真实截图 + 多模态评图选出）：
 *   1. 位置：气泡**右下角** hover 浮出（absolute ⇒ **零占高、零跳变**；读完正文就在手边）。
 *      对比被淘汰的方案：右上角（长消息离阅读终点 400+px）、气泡下方一条（每条永久多 30px 空白）、
 *      左侧装订线（占列宽、归属感弱）、行尾内联（在正文里像多余符号）。
 *   2. 覆盖：**每条消息**都有（user / assistant / 通知 / 工具行 / 流式中）—— 不再有
 *      `!isStreaming && !!content` 这类条件挂载（那正是"按钮突然冒出/消失"的根因）。
 *   3. 判定收敛：`resolveCopyText()` 是唯一权威 —— assistant 顶层 content 为空时**回退到
 *      最后一迭代的正文/思考**（v55 架构下回复常只存在于 iterations，旧实现因此"按钮没了"）。
 *   4. 触屏：无 hover ⇒ ⋯ 常显并弹出**带文字标签的底部面板**（≥44px 命中区）。
 */
import { useCallback, useState, type ReactNode } from 'react'
import { Check, Copy, MoreHorizontal } from 'lucide-react'
import type { ChatMessage } from '@/types/shared'

export type CopyVariant = 'reply' | 'thinking' | 'tools' | 'raw'

/** 复制内容判定（唯一权威）：顶层 content → 最后一迭代正文 → 该迭代思考。 */
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

/** 变体拼装：reply（默认）/ thinking（含思考）/ tools（含工具调用）/ raw（fenced markdown）。 */
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
      (it?.tools ?? []).map((tl) => {
        const head = `- [迭代 ${it.iteration ?? ''}] ${tl.label || tl.name}`
        return tl.detail ? `${head}\n${tl.detail}` : head
      }),
    )
    return lines.length ? `${lines.join('\n')}\n\n---\n\n${reply}` : reply
  }
  return '```md\n' + reply + '\n```'
}

function detectTouch(): boolean {
  try {
    return window.matchMedia('(hover: none), (pointer: coarse)').matches
  } catch {
    return false
  }
}

const ITEMS: ReadonlyArray<{ key: CopyVariant; label: string }> = [
  { key: 'reply', label: '复制回复' },
  { key: 'thinking', label: '复制含思考' },
  { key: 'tools', label: '复制含工具调用' },
  { key: 'raw', label: '查看原始 Markdown' },
]

export function MessageActions({
  message,
  extra,
}: {
  message: ChatMessage
  /** 额外操作（如 user 行的"编辑并重发"）—— 与复制并列在同一个 action row 里。 */
  extra?: ReactNode
}) {
  const [copied, setCopied] = useState(false)
  const [menuOpen, setMenuOpen] = useState(false)
  const [sheetOpen, setSheetOpen] = useState(false)
  const [touch] = useState(detectTouch)
  const text = resolveCopyText(message)

  const doCopy = useCallback(
    async (variant: CopyVariant) => {
      const payload = buildCopyVariant(message, variant)
      if (!payload) return
      try {
        await navigator.clipboard.writeText(payload)
      } catch {
        /* 无剪贴板权限（非 https / 权限被拒）时静默，不阻塞交互 */
      }
      setCopied(true)
      setMenuOpen(false)
      setSheetOpen(false)
      window.setTimeout(() => setCopied(false), 1500)
    },
    [message],
  )

  // 桌面：hover/focus 才显示（不占位）；触屏：常显（无 hover）。
  const visibility = touch
    ? 'opacity-100'
    : 'opacity-0 group-hover:opacity-100 group-focus-within:opacity-100'

  return (
    <div
      data-testid="msg-actions"
      className={`absolute bottom-2 right-2 z-10 flex items-center gap-0.5 rounded-lg border border-border bg-bg-secondary/90 px-1 py-0.5 shadow-sm backdrop-blur transition-opacity duration-150 ${visibility}`}
    >
      <button
        type="button"
        onClick={() => void doCopy('reply')}
        disabled={!text}
        title={text ? '复制' : '暂无可复制内容'}
        aria-label="copy message"
        data-testid="msg-copy"
        className="flex size-6 items-center justify-center rounded-md text-text-muted hover:bg-bg-tertiary hover:text-text-primary disabled:opacity-40"
      >
        {copied ? <Check className="size-3.5 text-status-success" /> : <Copy className="size-3.5" />}
      </button>
      <button
        type="button"
        onClick={() => (touch ? setSheetOpen(true) : setMenuOpen((v) => !v))}
        aria-label="more actions"
        data-testid="msg-more"
        className="flex size-6 items-center justify-center rounded-md text-text-muted hover:bg-bg-tertiary hover:text-text-primary"
      >
        <MoreHorizontal className="size-3.5" />
      </button>
      {extra}
      {menuOpen && !touch && (
        <div
          data-testid="msg-menu"
          className="absolute bottom-8 right-0 z-20 w-44 overflow-hidden rounded-lg border border-border bg-bg-secondary shadow-lg"
        >
          {ITEMS.map((it) => (
            <button
              key={it.key}
              type="button"
              onClick={() => void doCopy(it.key)}
              className="block w-full px-3 py-2 text-left text-[12.5px] text-text-primary hover:bg-bg-tertiary"
            >
              {it.label}
            </button>
          ))}
        </div>
      )}
      {sheetOpen && touch && (
        <div
          data-testid="msg-sheet"
          className="fixed inset-x-0 bottom-0 z-50 rounded-t-xl border-t border-border bg-bg-secondary p-2 pb-3 shadow-2xl"
        >
          {ITEMS.map((it) => (
            <button
              key={it.key}
              type="button"
              onClick={() => void doCopy(it.key)}
              className="block min-h-11 w-full px-3 py-3 text-left text-sm text-text-primary active:bg-bg-tertiary"
            >
              {it.label}
            </button>
          ))}
          <button
            type="button"
            onClick={() => setSheetOpen(false)}
            className="mt-1 block min-h-11 w-full px-3 py-3 text-left text-sm text-text-muted"
          >
            取消
          </button>
        </div>
      )}
    </div>
  )
}
