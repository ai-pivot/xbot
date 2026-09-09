/**
 * MessageUserNav — 消息列表右上角用户消息导航（第三版，替代 ChatMinimap 竖条）。
 *
 * 右上角悬浮按钮：hover 或点击展开用户消息列表面板，点击条目跳转
 * 对应 user turn（virtualizer.scrollToIndex）。当前可视 turn 高亮，
 * 面板打开时自动滚到 active 条目。
 *
 * 交互（VS Code outline 式）：
 *   - hover 展开（移开 250ms 延迟收起，面板内移动不收起）
 *   - click 切换持久展开（触屏唯一入口）——跳转后收起
 *   - 条目：编号 01/02… + user 文本（line-clamp-2）+ assistant 摘要（line-clamp-1）
 */
import { memo, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ListTree } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { ChatMessage } from '@/types/agent'

const PREVIEW_HIDE_DELAY = 250
/** 可视首行 + 容差内视为 active（虚拟列表 overscan 不精确，容差防抖动）。 */
const ACTIVE_ANCHOR_ROWS = 2

interface TurnEntry {
  /** rows 里的行号（virtualizer scrollToIndex 目标）。 */
  rowIndex: number
  /** 1-based turn 编号（显示 01/02…）。 */
  seq: number
}

interface TurnPreview extends TurnEntry {
  userText: string
  assistantText: string
}

/** turn 的预览文本：user 全文 + 同 turn 首个 assistant 回复。 */
function extractTurnPreview(
  rows: ChatMessage[],
  turn: TurnEntry,
  nextUserRow: number,
): TurnPreview {
  let assistantText = ''
  for (let i = turn.rowIndex + 1; i < nextUserRow && i < rows.length; i++) {
    if (rows[i].role === 'assistant' && rows[i].content) {
      assistantText = rows[i].content
      break
    }
  }
  return { ...turn, userText: rows[turn.rowIndex]?.content ?? '', assistantText }
}

interface Props {
  rows: ChatMessage[]
  /** user turn 行号索引（升序，来自 MessageList 的 userMessageIndices）。 */
  userRowIndexes: number[]
  /** virtualizer 可视首行（active 跟随锚点）。 */
  visibleStart: number
  /** 跳转回调（pauseFollowing + scrollToIndex(rowIndex, align start)）。 */
  onNavigate: (rowIndex: number) => void
}

export const MessageUserNav = memo(function MessageUserNav({
  rows,
  userRowIndexes,
  visibleStart,
  onNavigate,
}: Props) {
  const hideTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const panelRef = useRef<HTMLDivElement>(null)
  const itemRefs = useRef(new Map<number, HTMLButtonElement>())
  const rootRef = useRef<HTMLDivElement>(null)

  const [open, setOpen] = useState(false)
  const [hovered, setHovered] = useState(false)
  const panelOpen = open || hovered

  // turns 轻量索引（预览文本延迟提取——rows 流式高频变化时保持 O(N_user)）
  const turns = useMemo<TurnEntry[]>(
    () => userRowIndexes.map((rowIndex, k) => ({ rowIndex, seq: k + 1 })),
    [userRowIndexes],
  )

  // active：可视区顶部（+容差）最近的 turn（1-based，0 = 无）
  const activeSeq = useMemo(() => {
    let seq = 0
    for (const idx of userRowIndexes) {
      if (idx <= visibleStart + ACTIVE_ANCHOR_ROWS) seq++
      else break
    }
    return seq
  }, [userRowIndexes, visibleStart])

  // 预览文本：面板打开时一次性提取（关闭期间 rows 变化不重算）
  const previewTurns = useMemo(() => {
    if (!panelOpen) return []
    return turns.map((t, k) => {
      const nextUserRow = k + 1 < userRowIndexes.length ? userRowIndexes[k + 1] : rows.length
      return extractTurnPreview(rows, t, nextUserRow)
    })
  }, [panelOpen, turns, userRowIndexes, rows])

  const cancelHide = useCallback(() => {
    if (hideTimerRef.current) {
      clearTimeout(hideTimerRef.current)
      hideTimerRef.current = null
    }
  }, [])
  const scheduleHide = useCallback(() => {
    cancelHide()
    hideTimerRef.current = setTimeout(() => {
      hideTimerRef.current = null
      setHovered(false)
    }, PREVIEW_HIDE_DELAY)
  }, [cancelHide])
  useEffect(() => () => cancelHide(), [cancelHide])

  // outside click 收起（触屏无 hover-out：open 面板点击外部必须收起）
  useEffect(() => {
    if (!open) return
    const onPointerDown = (e: PointerEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) {
        setOpen(false)
        setHovered(false)
      }
    }
    document.addEventListener('pointerdown', onPointerDown)
    return () => document.removeEventListener('pointerdown', onPointerDown)
  }, [open])

  // 面板打开时自动滚动到 active 条目（跟随当前阅读位置）
  useEffect(() => {
    if (!panelOpen || activeSeq <= 0) return
    const panel = panelRef.current
    const item = itemRefs.current.get(activeSeq)
    if (!panel || !item) return
    const target = item.offsetTop - (panel.clientHeight - item.offsetHeight) / 2
    panel.scrollTop = Math.max(0, target)
  }, [panelOpen, activeSeq])

  if (turns.length < 2) return null

  return (
    <div
      ref={rootRef}
      className="absolute right-2 top-2 z-10"
      onMouseEnter={() => {
        cancelHide()
        setHovered(true)
      }}
      onMouseLeave={scheduleHide}
    >
      <button
        type="button"
        data-testid="message-user-nav"
        aria-label="用户消息导航"
        title="用户消息导航"
        onClick={() => setOpen((v) => !v)}
        className={cn(
          'flex size-8 items-center justify-center rounded-md border border-border/50 bg-bg-secondary/80 backdrop-blur transition-all',
          panelOpen
            ? 'bg-accent/10 text-accent opacity-100'
            : 'opacity-40 hover:bg-accent/10 hover:text-accent hover:opacity-100',
        )}
      >
        <ListTree className="size-4" />
      </button>

      {panelOpen && previewTurns.length > 0 && (
        <div
          ref={panelRef}
          data-testid="message-user-nav-panel"
          className="absolute right-0 top-10 z-20 max-h-[60vh] w-[min(20rem,calc(100vw-1.5rem))] overflow-y-auto rounded-none border border-border bg-bg-elevated p-1.5 shadow-lg"
          onMouseEnter={cancelHide}
        >
          {previewTurns.map((turn) => (
            <button
              key={turn.seq}
              type="button"
              data-user-nav-item={turn.seq}
              data-active={activeSeq === turn.seq ? '' : undefined}
              ref={(el) => {
                if (el) itemRefs.current.set(turn.seq, el)
                else itemRefs.current.delete(turn.seq)
              }}
              onClick={() => {
                onNavigate(turn.rowIndex)
                setOpen(false)
                setHovered(false)
              }}
              className={cn(
                'block w-full rounded-none px-2 py-1.5 text-left',
                activeSeq === turn.seq ? 'bg-accent/10' : 'hover:bg-bg-tertiary',
              )}
            >
              <div className="flex items-baseline gap-1.5">
                <span className="font-mono text-[10px] text-text-muted">
                  {String(turn.seq).padStart(2, '0')}
                </span>
                <span className="line-clamp-2 min-w-0 flex-1 break-all text-xs text-text-primary">
                  {turn.userText || '(empty)'}
                </span>
              </div>
              {turn.assistantText && (
                <p className="line-clamp-1 pl-6 text-[10px] text-text-muted">{turn.assistantText}</p>
              )}
            </button>
          ))}
        </div>
      )}
    </div>
  )
})
