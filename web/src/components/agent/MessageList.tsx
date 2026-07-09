/**
 * MessageList — virtualized chat message list (Spec 4 §3.4).
 *
 * Uses @tanstack/react-virtual with dynamic measurement so 100+ messages scroll
 * smoothly. The committed list comes from useChatMessages; a single live
 * streaming message (from useProgressStream) is appended as the last row when
 * present, so streamed text renders inline without touching committed state.
 *
 * Performance tactics (mirroring opencode session-ui, adapted to React):
 *   - stable item keys (message id) so the virtualizer reuses DOM across renders
 *   - measureElement for dynamic heights; estimateSize is a cheap fallback
 *   - React.memo'd MessageItem keeps mounted rows from re-rendering on scroll
 *   - the streaming row is the only one receiving liveProgress; others get null
 *   - auto-scroll to bottom while following; stops if the user scrolls up
 */
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'

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
  loading: boolean
  error: string | null
  onRewind?: (message: ChatMessage) => void
}

const ESTIMATE = 120
const BOTTOM_THRESHOLD = 80

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
  loading,
  error,
  onRewind,
}: MessageListProps) {
  const scrollRef = useRef<HTMLDivElement>(null)
  const contentRef = useRef<HTMLDivElement>(null)
  const stickToBottomRef = useRef(true)
  const userScrolledUpRef = useRef(false)
  const lastChatKeyRef = useRef<string | null | undefined>(chatKey)
  const lastRowCountRef = useRef(0)
  const lastFollowResetTokenRef = useRef(followResetToken)
  const userScrollIntentRef = useRef(false)
  const pendingScrollCancelRef = useRef<(() => void) | null>(null)
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
    liveProgress?.activeTools.length ?? 0,
    liveProgress?.completedTools.length ?? 0,
    liveProgress?.streamingTools.length ?? 0,
    liveProgress?.subAgents.length ?? 0,
    liveProgress?.iterationHistory.length ?? 0,
  ].join(':')

  // TanStack Virtual returns imperative functions; React Compiler deliberately
  // skips memoizing this hook. Safe to disable here (the virtualizer is meant
  // to be recreated per render anyway, keyed on rows.length).
  // eslint-disable-next-line react-hooks/incompatible-library
  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ESTIMATE,
    overscan: 8,
    getItemKey: (index) => rows[index]?.id ?? `row-${index}`,
  })

  // Track whether the viewport sits near the bottom to drive auto-scroll.
  const onScroll = useCallback(() => {
    const el = scrollRef.current
    if (!el) return
    const atBottom = isNearBottom(el)
    if (atBottom) {
      stickToBottomRef.current = true
      userScrolledUpRef.current = false
      userScrollIntentRef.current = false
      return
    }
    if (userScrollIntentRef.current) {
      stickToBottomRef.current = false
      userScrolledUpRef.current = true
    }
  }, [])

  const markUserScrollIntent = useCallback(() => {
    pendingScrollCancelRef.current?.()
    pendingScrollCancelRef.current = null
    userScrollIntentRef.current = true
    stickToBottomRef.current = false
    userScrolledUpRef.current = true
  }, [])

  const markFollowIntent = useCallback(() => {
    userScrollIntentRef.current = false
    stickToBottomRef.current = true
    userScrolledUpRef.current = false
  }, [])

  const scheduleFollowScroll = useCallback((el: HTMLDivElement) => {
    pendingScrollCancelRef.current?.()
    const cancel = scheduleScrollToBottom(el, () => {
      if (rows.length > 0) virtualizer.scrollToIndex(rows.length - 1, { align: 'end' })
    }, () => !userScrolledUpRef.current && stickToBottomRef.current)
    pendingScrollCancelRef.current = cancel
    return cancel
  }, [rows.length, virtualizer])

  const onWheel = useCallback((e: React.WheelEvent<HTMLDivElement>) => {
    const el = scrollRef.current
    if (!el) return
    if (e.deltaY < 0 || !isNearBottom(el)) {
      markUserScrollIntent()
    }
  }, [markUserScrollIntent])

  const onKeyDown = useCallback((e: React.KeyboardEvent<HTMLDivElement>) => {
    if (['ArrowUp', 'PageUp', 'Home', ' '].includes(e.key)) {
      markUserScrollIntent()
      return
    }
    if (['ArrowDown', 'PageDown', 'End'].includes(e.key)) {
      markFollowIntent()
    }
  }, [markFollowIntent, markUserScrollIntent])

  // Auto-scroll to bottom when the list grows and we're following.
  useEffect(() => {
    const el = scrollRef.current
    if (!el || userScrolledUpRef.current || !stickToBottomRef.current) return
    // Defer to next frame so newly measured rows have settled.
    return scheduleFollowScroll(el)
  }, [followSignal, rows.length, scheduleFollowScroll])

  // Keep following when already at bottom and late content measurement changes
  // row heights, e.g. images, KaTeX, or highlighted code blocks.
  useEffect(() => {
    const el = scrollRef.current
    const content = contentRef.current
    if (!el || !content || typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(() => {
      if (userScrolledUpRef.current || !stickToBottomRef.current) return
      if (!userScrolledUpRef.current && stickToBottomRef.current) {
        scheduleFollowScroll(el)
      }
    })
    observer.observe(content)
    return () => observer.disconnect()
  }, [rows.length, scheduleFollowScroll])

  // Chat switches should land at the latest message. Same-chat message growth is
  // handled by follow mode above so user scroll intent is preserved.
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
    userScrolledUpRef.current = false
    userScrollIntentRef.current = false
    return scheduleFollowScroll(el)
  }, [chatKey, followResetToken, rows.length, scheduleFollowScroll])

  useEffect(() => {
    return () => {
      pendingScrollCancelRef.current?.()
      pendingScrollCancelRef.current = null
    }
  }, [])

  return (
    <div className="relative min-h-0 flex-1 overflow-hidden">
      <div
        ref={scrollRef}
        onScroll={onScroll}
        onWheel={onWheel}
        onTouchStart={markUserScrollIntent}
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
                    onRewind={canRewindMessage(row, item.index, compactBoundaryIndex) ? onRewind : undefined}
                  />
                </div>
              )
            })}
          </div>
        )}
      </div>
    </div>
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

function scrollToBottom(el: HTMLDivElement): void {
  el.scrollTop = el.scrollHeight
}

function isNearBottom(el: HTMLDivElement): boolean {
  return el.scrollHeight - el.scrollTop - el.clientHeight < BOTTOM_THRESHOLD
}

function scheduleScrollToBottom(
  el: HTMLDivElement,
  scrollVirtualizer?: () => void,
  shouldRun?: () => boolean,
): () => void {
  let cancelled = false
  const timers: number[] = []
  const run = () => {
    if (cancelled) return
    if (shouldRun && !shouldRun()) return
    scrollVirtualizer?.()
    scrollToBottom(el)
  }
  run()
  let raf = requestAnimationFrame(() => {
    run()
    raf = requestAnimationFrame(() => {
      run()
      raf = requestAnimationFrame(run)
      timers.push(raf)
    })
    timers.push(raf)
  })
  timers.push(raf)
  for (const delay of [80, 180, 360]) {
    timers.push(window.setTimeout(run, delay))
  }
  return () => {
    cancelled = true
    for (const id of timers) {
      cancelAnimationFrame(id)
      clearTimeout(id)
    }
  }
}
