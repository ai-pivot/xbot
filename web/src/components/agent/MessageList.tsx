/**
 * MessageList — virtualized chat message list (Spec A §3+§4).
 *
 * Rewritten scroll logic with strict user-intent priority:
 *   - `stickToBottomRef` controls auto-follow; once false, no content increments
 *     trigger auto-scroll.
 *   - One cancellable RAF coalesces all application-level bottom scrolling.
 *   - Bottom "↓ new content" bubble appears while follow mode is paused.
 *   - Right-side floating navigation button group (top/prev-user/next-user/bottom).
 *
 * Uses @tanstack/react-virtual with dynamic measurement. The committed list
 * comes from useChatMessages; a single live streaming message is appended as
 * the last row when present.
 */
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import { AnimatePresence, motion } from 'framer-motion'
import { ChevronDown, ChevronUp, ChevronsDown, ChevronsUp } from 'lucide-react'

import { MessageItem } from './MessageItem'
import { useI18n } from '@/providers/i18n'
import type { ChatMessage, LiveProgress } from '@/types/agent'

interface MessageListProps {
  /** Stable chat/session identity; changing it forces initial scroll to bottom. */
  chatKey?: string | null
  /** Increment to force TUI-style follow mode after local user actions. */
  followResetToken?: number
  messages: ChatMessage[]
  /** Transient streaming assistant message appended as the last row, or null. */
  liveMessage: ChatMessage | null
  /** Live progress snapshot handed only to the streaming row. */
  liveProgress: LiveProgress | null
  collapseLevel: 'all' | 'minimal' | 'none'
  /** Whether to merge consecutive tools. Default true. */
  mergeTools?: boolean
  loading: boolean
  error: string | null
  /** Rewind callback — receives the edited content string. */
  onRewind?: (editedContent: string, originalMessage: ChatMessage) => void
  /** ID of the message currently being edited, or null. */
  editingMessageId?: string | null
  /** Callback to start editing a message. */
  onStartEdit?: (messageId: string) => void
  /** Callback to end editing (cancel or confirm). */
  onEndEdit?: () => void
  /** Optional footer rendered after the message list (e.g. AskUserPanel). */
  footer?: ReactNode
}

const ESTIMATE = 120
const EDGE_EPSILON = 2

export function latestCompactBoundaryIndex(rows: Pick<ChatMessage, 'role' | 'content'>[]): number {
  let idx = -1
  for (let i = 0; i < rows.length; i++) {
    const row = rows[i]
    if (isCompactMarker(row)) idx = i
  }
  return idx
}

export function isCompactMarker(row: Pick<ChatMessage, 'role' | 'content'>): boolean {
  return row.role === 'user' && row.content.trimStart().startsWith('[Compacted context]')
}

export function MessageList({
  chatKey,
  followResetToken = 0,
  messages,
  liveMessage,
  liveProgress,
  collapseLevel,
  mergeTools = true,
  loading,
  error,
  onRewind,
  editingMessageId,
  onStartEdit,
  onEndEdit,
  footer,
}: MessageListProps) {
  const scrollRef = useRef<HTMLDivElement>(null)
  const contentRef = useRef<HTMLDivElement>(null)
  const stickToBottomRef = useRef(true)
  const pendingFollowRafRef = useRef<number | null>(null)
  const lastChatKeyRef = useRef<string | null | undefined>(chatKey)
  const lastRowCountRef = useRef(0)
  const lastFollowResetTokenRef = useRef(followResetToken)
  const lastTouchYRef = useRef<number | null>(null)
  const pointerScrollingRef = useRef(false)

  // React state mirrors for re-rendering UI elements (bubble, nav buttons)
  const [hasNewContent, setHasNewContent] = useState(false)
  const [visibleRange, setVisibleRange] = useState({ start: 0, end: 0 })
  const [atTop, setAtTop] = useState(false)
  const [atBottom, setAtBottom] = useState(true)

  const { t } = useI18n()

  // Combined row list: committed messages + optional live streaming row.
  //
  // ALWAYS remove intermediate assistant messages after the last user message.
  // ConvertMessagesToHistory can split one turn into multiple assistant
  // messages (when a Content assistant appears between ToolCalls). Without
  // this, both assistants render the same tools — once from DB iterations
  // and once from the progress snapshot — causing duplicates.
  // Only the LAST assistant after the last user message is kept; all earlier
  // ones are absorbed (their tools are in the snapshot or in the last
  // assistant's iterations).
  const rows = useMemo<ChatMessage[]>(() => {
    // Remove intermediate assistant messages after the last user message.
    // Only apply when the last message is an assistant (active turn) —
    // if the last message is a user message, ALL previous assistants are
    // from completed turns and must be preserved.
    const last = messages[messages.length - 1]
    const deduped = [...messages]
    if (last && last.role === 'assistant') {
      for (let i = deduped.length - 2; i >= 0; i--) {
        if (deduped[i].role === 'user') break
        if (deduped[i].role === 'assistant') deduped.splice(i, 1)
      }
    }

    if (!liveMessage) return deduped
    const lastDeduped = deduped[deduped.length - 1]
    if (lastDeduped && lastDeduped.role === 'assistant' &&
        lastDeduped.eventSeq != null && liveMessage.eventSeq != null &&
        lastDeduped.eventSeq === liveMessage.eventSeq) {
      return deduped
    }
    // Active turn: last persisted assistant is the in-flight streaming slot.
    if (lastDeduped && lastDeduped.role === 'assistant' && liveMessage.isPartial) {
      return deduped
    }
    return [...deduped, liveMessage]
  }, [messages, liveMessage])
  // liveId points to the row that receives liveProgress. When the last
  // history assistant is the active turn, it IS the streaming slot.
  const liveId = liveMessage
    ? (messages.length > 0 &&
       messages[messages.length - 1].role === 'assistant' &&
       liveMessage.isPartial
        ? messages[messages.length - 1].id
        : liveMessage.id)
    : null
  const compactBoundaryIndex = useMemo(() => latestCompactBoundaryIndex(rows), [rows])
  const hasFooter = footer !== null && footer !== undefined

  // User message indices for navigation
  const userMessageIndices = useMemo(
    () => rows.map((r, i) => (r.role === 'user' ? i : -1)).filter((i) => i >= 0),
    [rows],
  )

  // TanStack Virtual
  // eslint-disable-next-line react-hooks/incompatible-library
  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ESTIMATE,
    overscan: 8,
    getItemKey: (index) => rows[index]?.id ?? `row-${index}`,
  })

  const cancelPendingFollow = useCallback(() => {
    if (pendingFollowRafRef.current === null) return
    cancelAnimationFrame(pendingFollowRafRef.current)
    pendingFollowRafRef.current = null
  }, [])

  const pauseFollowing = useCallback(() => {
    stickToBottomRef.current = false
    cancelPendingFollow()
  }, [cancelPendingFollow])

  const resumeFollowing = useCallback(() => {
    stickToBottomRef.current = true
    setHasNewContent(false)
  }, [])

  const scheduleFollow = useCallback(() => {
    if (!stickToBottomRef.current || pendingFollowRafRef.current !== null) return
    // Double RAF: first frame lets virtualizer update item sizes,
    // second frame scrolls to the now-correct scrollHeight.
    pendingFollowRafRef.current = requestAnimationFrame(() => {
      pendingFollowRafRef.current = requestAnimationFrame(() => {
        pendingFollowRafRef.current = null
        if (!stickToBottomRef.current) return
        const el = scrollRef.current
        if (el) el.scrollTop = el.scrollHeight
      })
    })
  }, [])

  // ── Scroll event handler ──────────────────────────────────────────────────
  // onScroll ONLY updates UI state (atTop, atBottom, visibleRange).
  // It does NOT control stickToBottomRef — that is exclusively driven by
  // user-input handlers (onWheel, onKeyDown, onPointer*, onTouch*).
  //
  // The previous implementation called pauseFollowing()/resumeFollowing()
  // inside onScroll, which created a race: a 1px wheel-up would pause
  // following, but onScroll immediately resumed it because the scroll was
  // still within EDGE_EPSILON (2px) of the bottom. This caused the viewport
  // to be yanked back to the bottom on the next content-growth frame.
  const onScroll = useCallback(() => {
    const el = scrollRef.current
    if (!el) return
    const atEnd = isAtBottom(el)
    const atStart = el.scrollTop <= EDGE_EPSILON
    setAtTop((prev) => (prev === atStart ? prev : atStart))
    setAtBottom((prev) => (prev === atEnd ? prev : atEnd))
    const items = virtualizer.getVirtualItems()
    if (items.length > 0) {
      const newStart = items[0].index
      const newEnd = items[items.length - 1].index
      setVisibleRange((prev) =>
        prev && prev.start === newStart && prev.end === newEnd
          ? prev
          : { start: newStart, end: newEnd },
      )
    }
  }, [virtualizer])

  // Check if we're at the bottom after a RAF (post-scroll) and resume following.
  const checkBottomAndResume = useCallback(() => {
    requestAnimationFrame(() => {
      const el = scrollRef.current
      if (el && isAtBottom(el)) resumeFollowing()
    })
  }, [resumeFollowing])

  // ── User scroll detection ─────────────────────────────────────────────────
  // Wheel: always pause first (both directions). If scrolling DOWN and we
  // end up at the bottom, resume following after the browser applies the scroll.
  const onWheel = useCallback((e: React.WheelEvent<HTMLDivElement>) => {
    pauseFollowing()
    if (e.deltaY > 0) checkBottomAndResume()
  }, [pauseFollowing, checkBottomAndResume])

  const onKeyDown = useCallback((e: React.KeyboardEvent<HTMLDivElement>) => {
    if (e.key === 'End') {
      resumeFollowing()
      scheduleFollow()
      return
    }
    if (['ArrowUp', 'PageUp', 'Home'].includes(e.key) || (e.key === ' ' && e.shiftKey)) {
      pauseFollowing()
    } else if (['ArrowDown', 'PageDown'].includes(e.key)) {
      pauseFollowing()
      checkBottomAndResume()
    }
  }, [pauseFollowing, resumeFollowing, scheduleFollow, checkBottomAndResume])

  // Treat the live snapshot as the activity revision: any progress update while
  // paused is new content, even when it does not change the rendered height.
  useEffect(() => {
    if (!stickToBottomRef.current) setHasNewContent(true)
  }, [rows.length, liveProgress, hasFooter])

  // ── ResizeObserver: follow bottom when sticky ─────────────────────────────
  useEffect(() => {
    const scrollElement = scrollRef.current
    const content = contentRef.current
    if (!scrollElement || !content || typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(() => {
      if (stickToBottomRef.current) scheduleFollow()
    })
    observer.observe(scrollElement)
    observer.observe(content)
    return () => {
      observer.disconnect()
      cancelPendingFollow()
    }
  }, [cancelPendingFollow, scheduleFollow])

  // ── Chat switch or new messages: follow bottom when sticky ────────────────
  useLayoutEffect(() => {
    const el = scrollRef.current
    const chatChanged = lastChatKeyRef.current !== chatKey
    const initialLoad = !chatChanged && lastRowCountRef.current === 0 && rows.length > 0
    const followReset = lastFollowResetTokenRef.current !== followResetToken
    const newMessagesAdded = !chatChanged && !initialLoad && !followReset && rows.length > lastRowCountRef.current
    lastChatKeyRef.current = chatKey
    lastRowCountRef.current = rows.length
    lastFollowResetTokenRef.current = followResetToken
    if (!el || rows.length === 0 || (!chatChanged && !initialLoad && !followReset && !newMessagesAdded)) return
    // If new messages were added (e.g. by background reload after assistant
    // completion), only follow if already sticky — don't yank the user down
    // if they scrolled up.
    if (newMessagesAdded && !stickToBottomRef.current) return
    resumeFollowing()
    scheduleFollow()
  }, [chatKey, followResetToken, rows.length, resumeFollowing, scheduleFollow])

  // ── Navigation helpers ────────────────────────────────────────────────────
  const scrollToTop = useCallback(() => {
    pauseFollowing()
    virtualizer.scrollToIndex(0, { align: 'start' })
  }, [pauseFollowing, virtualizer])

  const scrollToPrevUser = useCallback(() => {
    const visibleStart = visibleRange.start
    const prev = userMessageIndices.filter((i) => i < visibleStart).pop()
    if (prev !== undefined) {
      pauseFollowing()
      virtualizer.scrollToIndex(prev, { align: 'start' })
    }
  }, [pauseFollowing, userMessageIndices, visibleRange.start, virtualizer])

  const scrollToNextUser = useCallback(() => {
    const visibleStart = visibleRange.start
    const next = userMessageIndices.find((i) => i > visibleStart)
    if (next !== undefined) {
      pauseFollowing()
      virtualizer.scrollToIndex(next, { align: 'start' })
    }
  }, [pauseFollowing, userMessageIndices, visibleRange.start, virtualizer])

  const scrollToBottomClick = useCallback(() => {
    resumeFollowing()
    scheduleFollow()
  }, [resumeFollowing, scheduleFollow])

  // ── Nav button disabled states ────────────────────────────────────────────
  const visibleStart = visibleRange.start
  const hasPrevUser = userMessageIndices.some((i) => i < visibleStart)
  const hasNextUser = userMessageIndices.some((i) => i > visibleStart)

  return (
    <div className="relative min-h-0 flex-1 overflow-hidden">
      <div
        ref={scrollRef}
        onScroll={onScroll}
        onWheel={onWheel}
        onPointerDown={(e) => {
          if (e.pointerType === 'mouse') {
            pointerScrollingRef.current = true
            pauseFollowing()
          }
        }}
        onPointerMove={(e) => {
          if (pointerScrollingRef.current && e.pointerType === 'mouse') pauseFollowing()
        }}
        onPointerUp={() => {
          if (pointerScrollingRef.current) {
            pointerScrollingRef.current = false
            checkBottomAndResume()
          }
        }}
        onPointerCancel={() => {
          pointerScrollingRef.current = false
        }}
        onTouchMove={(e) => {
          // Only break sticky on upward touch scroll (finger moving down = content scrolling up = user reading up)
          const touch = e.touches[0]
          if (!touch) return
          if (lastTouchYRef.current !== null) {
            const delta = touch.clientY - lastTouchYRef.current
            if (delta > 0) pauseFollowing()
          }
          lastTouchYRef.current = touch.clientY
        }}
        onTouchStart={() => {
          lastTouchYRef.current = null
        }}
        onTouchEnd={() => {
          checkBottomAndResume()
        }}
        onKeyDown={onKeyDown}
        tabIndex={0}
        style={{ overflowAnchor: 'none' }}
        className="h-full overflow-y-auto overflow-x-hidden px-3 py-4"
      >
        {loading && rows.length === 0 && (
          <div className="flex h-full items-center justify-center text-sm text-text-muted">
            {t('agent.loading')}
          </div>
        )}
        {error && (
          <div className="mx-auto my-4 max-w-md rounded-md border border-status-error/40 bg-status-error/10 p-3 text-sm text-status-error">
            {error}
          </div>
        )}
        {rows.length === 0 && !loading && !error && (
          <div className="flex h-full items-center justify-center px-6 text-center text-sm text-text-muted">
            {t('agent.emptyConversation')}
          </div>
        )}

        <div ref={contentRef} data-message-list-content className="w-full">
          {rows.length > 0 && (
            <div
              style={{ height: `${virtualizer.getTotalSize()}px` }}
              className="relative w-full"
            >
              {virtualizer.getVirtualItems().map((item) => {
                const row = rows[item.index]
                if (!row) return null
                const canRewind = canRewindMessage(row, item.index, compactBoundaryIndex)
                const isEditing = editingMessageId === row.id
                const editDisabled = editingMessageId !== null && editingMessageId !== row.id
                return (
                  <div
                    key={item.key}
                    data-index={item.index}
                    ref={virtualizer.measureElement}
                    style={{
                      position: 'absolute',
                      top: 0,
                      left: 0,
                      width: '100%',
                      transform: `translateY(${item.start}px)`,
                    }}
                    className="py-1.5"
                  >
                    <MessageItem
                      message={row}
                      liveProgress={row.id === liveId ? liveProgress : null}
                      collapseLevel={collapseLevel}
                      mergeTools={mergeTools}
                      onRewind={canRewind && onRewind ? (editedContent: string) => onRewind(editedContent, row) : undefined}
                      isEditing={isEditing}
                      onStartEdit={canRewind && onStartEdit ? () => onStartEdit(row.id) : undefined}
                      onEndEdit={onEndEdit}
                      editDisabled={editDisabled}
                    />
                  </div>
                )
              })}
            </div>
          )}
          {footer}
        </div>
      </div>

      {/* ── Bottom new-content bubble ─────────────────────────────────────────── */}
      <AnimatePresence>
        {hasNewContent && (
          <motion.button
            initial={{ opacity: 0, y: 10 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: 10 }}
            transition={{ duration: 0.2 }}
            onClick={scrollToBottomClick}
            className="absolute bottom-4 left-1/2 -translate-x-1/2 z-10 rounded-full bg-accent px-3 py-1 text-xs text-accent-foreground shadow-md"
          >
            ↓ {t('agent.newContent')}
          </motion.button>
        )}
      </AnimatePresence>

      {/* ── Right-side floating navigation button group ─────────────────────── */}
      <div className="absolute right-2 top-1/2 -translate-y-1/2 z-10 flex flex-col gap-1">
        <NavButton
          onClick={scrollToTop}
          disabled={atTop || rows.length === 0}
          title={t('agent.navToTop')}
        >
          <ChevronsUp className="size-4" />
        </NavButton>
        <NavButton
          onClick={scrollToPrevUser}
          disabled={!hasPrevUser}
          title={t('agent.navPrevUser')}
        >
          <ChevronUp className="size-4" />
        </NavButton>
        <NavButton
          onClick={scrollToNextUser}
          disabled={!hasNextUser}
          title={t('agent.navNextUser')}
        >
          <ChevronDown className="size-4" />
        </NavButton>
        <NavButton
          onClick={scrollToBottomClick}
          disabled={atBottom || rows.length === 0}
          title={t('agent.navToBottom')}
        >
          <ChevronsDown className="size-4" />
        </NavButton>
      </div>
    </div>
  )
}

// ── Navigation button ────────────────────────────────────────────────────────
function NavButton({
  onClick,
  disabled,
  title,
  children,
}: {
  onClick: () => void
  disabled?: boolean
  title: string
  children: ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      title={title}
      className={`flex size-8 items-center justify-center rounded-md border border-border/50 bg-bg-secondary/80 backdrop-blur transition-all duration-150 ${
        disabled
          ? 'cursor-default opacity-20'
          : 'cursor-pointer opacity-40 hover:bg-accent/10 hover:text-accent hover:opacity-100'
      }`}
    >
      {children}
    </button>
  )
}

export function canRewindMessage(
  row: ChatMessage,
  index: number,
  compactBoundaryIndex: number,
): boolean {
  return row.role === 'user' &&
    !!row.timestamp &&
    row.persisted === true &&
    index > compactBoundaryIndex &&
    !isCompactMarker(row)
}

function isAtBottom(el: HTMLDivElement): boolean {
  return el.scrollHeight - el.scrollTop - el.clientHeight <= EDGE_EPSILON
}
