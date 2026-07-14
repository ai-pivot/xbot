/**
 * MessageList — virtualized chat message list (Spec A §3+§4).
 *
 * Rewritten scroll logic with strict user-intent priority:
 *   - `stickyRef` controls auto-follow; once false, no content increments
 *     trigger auto-scroll.
 *   - `unreadCountRef` tracks new messages while scrolled up.
 *   - Bottom "↓ N new" bubble appears when unread > 0.
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
  onRewind?: (message: ChatMessage) => void
  /** Optional footer rendered after the message list (e.g. AskUserPanel). */
  footer?: ReactNode
}

const ESTIMATE = 120
const BOTTOM_THRESHOLD = 48

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
  footer,
}: MessageListProps) {
  const scrollRef = useRef<HTMLDivElement>(null)
  const contentRef = useRef<HTMLDivElement>(null)
  const stickToBottomRef = useRef(true)
  const unreadCountRef = useRef(0)
  const lastChatKeyRef = useRef<string | null | undefined>(chatKey)
  const lastRowCountRef = useRef(0)
  const lastFollowResetTokenRef = useRef(followResetToken)

  // React state mirrors for re-rendering UI elements (bubble, nav buttons)
  const [unreadCount, setUnreadCount] = useState(0)
  const [visibleRange, setVisibleRange] = useState({ start: 0, end: 0 })
  const [atTop, setAtTop] = useState(false)
  const [atBottom, setAtBottom] = useState(true)

  const { t } = useI18n()

  // Combined row list: committed messages + optional live streaming row.
  // Dedup: if liveMessage content matches the last committed assistant message,
  // skip adding liveMessage (prevents one-frame overlap during finalize).
  const rows = useMemo<ChatMessage[]>(() => {
    if (!liveMessage) return messages
    const last = messages[messages.length - 1]
    if (last && last.role === 'assistant' && last.content && liveMessage.content &&
        last.content === liveMessage.content) {
      return messages
    }
    return [...messages, liveMessage]
  }, [messages, liveMessage])
  const liveId = liveMessage?.id ?? null
  const compactBoundaryIndex = useMemo(() => latestCompactBoundaryIndex(rows), [rows])
  const followSignal = [
    rows.length,
    liveProgress?.phase ?? '',
    liveProgress?.iteration ?? 0,
    liveProgress?.streamContent ?? '',
    liveProgress?.reasoningStreamContent ?? '',
    liveProgress?.iterationHistory.length ?? 0,
  ].join(':')

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

  const doScrollToBottom = useCallback(() => {
    if (rows.length > 0) {
      virtualizer.scrollToIndex(rows.length - 1, { align: 'end' })
    }
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [rows.length, virtualizer])

  // ── Scroll event handler ──────────────────────────────────────────────────
  const onScroll = useCallback(() => {
    const el = scrollRef.current
    if (!el) return
    const nearBottom = isNearBottom(el)
    const nearTop = el.scrollTop <= BOTTOM_THRESHOLD
    setAtTop(nearTop)
    setAtBottom(nearBottom)
    if (nearBottom) {
      stickToBottomRef.current = true
      if (unreadCountRef.current > 0) {
        unreadCountRef.current = 0
        setUnreadCount(0)
      }
    }
    // Update visible range for nav button state
    const items = virtualizer.getVirtualItems()
    if (items.length > 0) {
      setVisibleRange({ start: items[0].index, end: items[items.length - 1].index })
    }
  }, [virtualizer])

  // ── User scroll detection ─────────────────────────────────────────────────
  const onWheel = useCallback((e: React.WheelEvent<HTMLDivElement>) => {
    const el = scrollRef.current
    if (!el) return
    // deltaY > 0 (scroll down) AND near bottom → keep sticky
    // deltaY < 0 (scroll up) OR not near bottom → break sticky
    if (e.deltaY > 0 && isNearBottom(el)) {
      stickToBottomRef.current = true
    } else {
      stickToBottomRef.current = false
    }
  }, [])

  const onKeyDown = useCallback((e: React.KeyboardEvent<HTMLDivElement>) => {
    if (['ArrowUp', 'PageUp', 'Home', ' '].includes(e.key)) {
      stickToBottomRef.current = false
      return
    }
    if (['ArrowDown', 'PageDown', 'End'].includes(e.key)) {
      stickToBottomRef.current = true
    }
  }, [])

  // ── Auto-scroll when new content arrives ──────────────────────────────────
  useEffect(() => {
    if (stickToBottomRef.current) {
      // Use requestAnimationFrame for smooth scroll after DOM measurement
      const raf = requestAnimationFrame(() => doScrollToBottom())
      return () => cancelAnimationFrame(raf)
    }
    // Not sticky → increment unread count if rows grew
    const el = scrollRef.current
    if (el && rows.length > lastRowCountRef.current) {
      const delta = rows.length - lastRowCountRef.current
      unreadCountRef.current += delta
      setUnreadCount(unreadCountRef.current)
    }
  }, [followSignal, rows.length, doScrollToBottom])

  // ── ResizeObserver: follow bottom when sticky ─────────────────────────────
  useEffect(() => {
    const el = scrollRef.current
    const content = contentRef.current
    if (!el || !content || typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(() => {
      if (stickToBottomRef.current) {
        const raf = requestAnimationFrame(() => doScrollToBottom())
        // Cleanup is handled by the next observation cycle
        return () => cancelAnimationFrame(raf)
      }
      // sticky = false → do NOT scroll, do NOT reset sticky
    })
    observer.observe(content)
    return () => observer.disconnect()
  }, [rows.length, doScrollToBottom])

  // ── Chat switch: force scroll to bottom ────────────────────────────────────
  useLayoutEffect(() => {
    const el = scrollRef.current
    const chatChanged = lastChatKeyRef.current !== chatKey
    const initialLoad = !chatChanged && lastRowCountRef.current === 0 && rows.length > 0
    const followReset = lastFollowResetTokenRef.current !== followResetToken
    lastChatKeyRef.current = chatKey
    lastRowCountRef.current = rows.length
    lastFollowResetTokenRef.current = followResetToken
    if (!el || rows.length === 0 || (!chatChanged && !initialLoad && !followReset)) return
    stickToBottomRef.current = true
    unreadCountRef.current = 0
    setUnreadCount(0)
    const raf = requestAnimationFrame(() => doScrollToBottom())
    return () => cancelAnimationFrame(raf)
  }, [chatKey, followResetToken, rows.length, doScrollToBottom])

  // ── Navigation helpers ────────────────────────────────────────────────────
  const scrollToTop = useCallback(() => {
    virtualizer.scrollToIndex(0, { align: 'start' })
    const el = scrollRef.current
    if (el) el.scrollTop = 0
  }, [virtualizer])

  const scrollToPrevUser = useCallback(() => {
    const visibleStart = visibleRange.start
    const prev = userMessageIndices.filter((i) => i < visibleStart).pop()
    if (prev !== undefined) {
      virtualizer.scrollToIndex(prev, { align: 'start' })
    }
  }, [userMessageIndices, visibleRange.start, virtualizer])

  const scrollToNextUser = useCallback(() => {
    const visibleStart = visibleRange.start
    const next = userMessageIndices.find((i) => i > visibleStart)
    if (next !== undefined) {
      virtualizer.scrollToIndex(next, { align: 'start' })
    }
  }, [userMessageIndices, visibleRange.start, virtualizer])

  const scrollToBottomClick = useCallback(() => {
    stickToBottomRef.current = true
    unreadCountRef.current = 0
    setUnreadCount(0)
    doScrollToBottom()
  }, [doScrollToBottom])

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
        onTouchMove={(e) => {
          // Only break sticky on upward touch scroll (deltaY < 0)
          if (e.touches[0] && e.touches[0].clientY > 0) {
            stickToBottomRef.current = false
          }
        }}
        onKeyDown={onKeyDown}
        tabIndex={0}
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

        {rows.length > 0 && (
          <div
            ref={contentRef}
            style={{ height: `${virtualizer.getTotalSize()}px` }}
            className="relative w-full"
          >
            {virtualizer.getVirtualItems().map((item) => {
              const row = rows[item.index]
              if (!row) return null
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
                    onRewind={canRewindMessage(row, item.index, compactBoundaryIndex) ? onRewind : undefined}
                  />
                </div>
              )
            })}
          </div>
        )}
        {footer}
      </div>

      {/* ── Bottom "N new" unread bubble ─────────────────────────────────────── */}
      <AnimatePresence>
        {unreadCount > 0 && (
          <motion.button
            initial={{ opacity: 0, y: 10 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: 10 }}
            transition={{ duration: 0.2 }}
            onClick={scrollToBottomClick}
            className="absolute bottom-4 left-1/2 -translate-x-1/2 z-10 rounded-full bg-accent px-3 py-1 text-xs text-accent-foreground shadow-md"
          >
            ↓ {unreadCount} {t('agent.newContent')}
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

function isNearBottom(el: HTMLDivElement): boolean {
  return el.scrollHeight - el.scrollTop - el.clientHeight < BOTTOM_THRESHOLD
}
