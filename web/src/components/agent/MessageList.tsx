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
import { ChevronDown, ChevronUp, ChevronsDown, ChevronsUp, Loader2 } from 'lucide-react'

import { MessageItem } from './MessageItem'
import { ShimmerThinking } from './ShimmerThinking'
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
  /** Whether the agent is busy (thinking/processing) — shows placeholder when
   *  no liveMessage yet (e.g. session just started, no iterations arrived). */
  busy?: boolean
  collapseLevel: 'all' | 'minimal' | 'none'
  /** Whether to merge consecutive tools. Default true. */
  mergeTools?: boolean
  loading: boolean
  /** True while loading older messages (scroll-up pagination). */
  loadingMore?: boolean
  /** True if there are older messages available to load. */
  hasMore?: boolean
  /** Called when the user scrolls to the top — load older messages. */
  onLoadMore?: () => Promise<boolean>
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

/**
 * Build the combined row list: committed messages + optional live streaming row.
 *
 * ALWAYS remove intermediate assistant messages after the last user message.
 * ConvertMessagesToHistory can split one turn into multiple assistant
 * messages (when a Content assistant appears between ToolCalls). Without
 * this, both assistants render the same tools — once from DB iterations
 * and once from the progress snapshot — causing duplicates. Only the LAST
 * assistant after the last user message is kept.
 *
 * Live insertion order (turn-aware):
 *  - Same turnID:role committed message exists → merge (no separate row).
 *  - Otherwise insert at the correct turn position. The live assistant must
 *    render AFTER its own turn's user message. If the optimistic user row
 *    (turnID=0, persisted=false) is still unbound — turn_started was lost
 *    (SSE drop/coalesce) so bindLastUserToTurn never ran — insert after it.
 *    Without this, the live assistant lands after the PREVIOUS turn's
 *    assistant and renders inside the wrong turn.
 */
export function buildMessageRows(
  messages: ChatMessage[],
  liveMessage: ChatMessage | null,
): ChatMessage[] {
  // Order = the message array's accumulation order (append-only — mirrors the
  // backend's DB row order). NO turnID re-sorting: user rows keep their
  // natural order, and a turn_id=0 user must NOT be grouped with other users.
  // The committed assistant's position is fixed by appendAssistant at commit
  // time (inserted before the newest user), so the array is already ordered.
  if (!liveMessage) return messages.length > 0 ? messages : []
  // The live message for a turn that already has a committed assistant is
  // merged into it (liveProgress flows via liveId) — never rendered twice.
  if (liveMessage.turnID > 0) {
    const hasCommitted = messages.some(
      (m) => m.turnID === liveMessage.turnID && m.role === liveMessage.role,
    )
    if (hasCommitted) {
      // The live message has a committed counterpart — liveProgress flows
      // to it via liveId (MessageList.tsx). AssistantMessage ignores
      // message.iterations when hasLiveProgress is true (it uses
      // progress.iterationHistory exclusively), so merging iterations here
      // would be wasted work that breaks MessageItem memo (new object ref
      // every frame). Just return messages as-is.
      return messages
    }
    // Distinguish the two live-row kinds by whether its turnID already exists
    // in the committed list:
    //  - EXISTS (e.g. a frozen row from a CANCELLED previous turn whose user is
    //    in the list): insert after that turn's last message — ABOVE the newest
    //    user. Without this, the new user flickered above the cancelled turn.
    //  - NOT EXISTS (the CURRENT turn's reply — its user was just sent and is
    //    still unbound / turnID not yet in the list): append at the END, below
    //    the new user. Falling through to the turnID scan skipped the unbound
    //    user (turnID=0) and inserted the reply ABOVE the user — the "reply
    //    rendered above my user msg" linear-consistency violation.
    //  - turnID=0 (frozen live message after cancel): NEVER match — turnID=0
    //    means "unbound", not a real turn. Appending at the end keeps the
    //    frozen live content below the newest user msg until the committed
    //    message replaces it.
    // CRITICAL: turnExists matches ANY role (user OR assistant) with the same
    // turnID > 0. The previous 'assistant only' restriction broke the frozen
    // live row case: cancel before any committed assistant → only user msg
    // has the turnID → turnExists=false → frozen live row appended at END
    // (below new user msg) instead of above it.
    // The turnID > 0 guard prevents matching optimistic user msgs (turnID=0).
    const turnExists = liveMessage.turnID > 0 && messages.some(
      (m) => m.turnID === liveMessage.turnID,
    )
    if (turnExists) {
      let insertIdx = messages.length
      for (let i = messages.length - 1; i >= 0; i--) {
        const m = messages[i]
        if (m.turnID > 0 && m.turnID <= liveMessage.turnID) {
          insertIdx = i + 1
          break
        }
      }
      return [...messages.slice(0, insertIdx), liveMessage, ...messages.slice(insertIdx)]
    }
  }
  // turnID=0 live, or the current turn's reply (turnID not in the committed
  // list yet) — append at the end (below the newest user).
  return [...messages, liveMessage]
}

export function MessageList({
  chatKey,
  followResetToken = 0,
  messages,
  liveMessage,
  liveProgress,
  busy = false,
  collapseLevel,
  mergeTools = true,
  loading,
  loadingMore = false,
  hasMore = false,
  onLoadMore,
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
  // Generation counter — each scheduleFollow call increments this. The
  // tryScroll loop checks it to know if it's the latest follow (cancel
  // old loops when a new scheduleFollow supersedes them).
  const followGenRef = useRef(0)
  // Marks scrolls caused by our own scheduleFollow (el.scrollTop = scrollHeight).
  // Set before the write and cleared via queueMicrotask after — so the flag is
  // only true during the synchronous scroll event our write dispatches, not
  // across unrelated later scroll events.
  const programmaticScrollRef = useRef(false)
  // Track scroll velocity for dynamic overscan: fast scrolling needs more
  // pre-rendered rows to avoid blank flashes; static needs fewer (less work).
  const lastScrollTopRef = useRef(0)
  const lastScrollTimeRef = useRef(0)
  const [dynamicOverscan, setDynamicOverscan] = useState(8)
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
  const rows = useMemo<ChatMessage[]>(() => buildMessageRows(messages, liveMessage), [messages, liveMessage])
  // Latest-rows ref: closures (IntersectionObserver, loadMore anchor restore)
  // must read the CURRENT rows, not a stale snapshot captured in effect deps —
  // after onLoadMore prepends older rows, the effect closure's `rows` is still
  // the pre-prepend array, so findIndex would miss the anchor.
  const rowsRef = useRef(rows)
  rowsRef.current = rows
  // loadMore scroll-anchor: id of the first VISIBLE row captured BEFORE older
  // rows prepend. After the prepend lands we scrollToIndex it back to 'start'
  // so the user's visible region stays put — new older rows appear above it,
  // which pushes the scrollbar toward the middle (not the top).
  const loadMoreAnchorIdRef = useRef<string | null>(null)
  // Invariant guard: the "thinking…" busy placeholder must never render below
  // a FINISHED assistant (copy button shown — turn complete). A finished turn
  // followed by "thinking…" would imply the completed turn is still running.
  // A committed assistant is isPartial=false with final content (approximation
  // of shouldRenderFinalContent at the row level).
  const lastIsFinishedAssistant =
    rows.length > 0 &&
    rows[rows.length - 1].role === 'assistant' &&
    rows[rows.length - 1].isPartial === false &&
    !!rows[rows.length - 1].content
  // liveId points to the row that receives liveProgress. Scan ALL rows
  // for a match by turnID:role (the committed message that liveProgress
  // should be passed to). If no match, liveMessage has its own row.
  const liveId = useMemo(() => {
    if (!liveMessage) return null
    if (liveMessage.turnID > 0) {
      // Check if rows contains a committed message with same turnID:role
      // (merge case — liveProgress goes to that message, not liveMessage).
      for (let i = rows.length - 1; i >= 0; i--) {
        const r = rows[i]
        if (r.turnID === liveMessage.turnID && r.role === liveMessage.role && r.id !== liveMessage.id) {
          return r.id
        }
      }
    }
    return liveMessage.id
  }, [rows, liveMessage])
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
    overscan: dynamicOverscan,
    getItemKey: (index) => rows[index]?.id ?? `row-${index}`,
  })

  // Workaround: virtual-core checks `this.shouldAdjustScrollPositionOnItemSizeChange`
  // (direct instance property) in resizeItem, but setOptions only stores it in
  // `this.options` — the option is never actually applied. Assign it directly.
  // Custom condition: only correct scrollTop when the resized item is ENTIRELY
  // above the viewport (item.end < scrollTop). The default condition
  // (item.start < scrollTop) also fires for items partially in the viewport —
  // when such an item changes size (code highlighting, image loading, markdown
  // settling), the correction moves the user's viewport even though they
  // didn't scroll. Using item.end ensures only items fully above the viewport
  // trigger correction, keeping visible items stable.
  useLayoutEffect(() => {
    const v = virtualizer as unknown as {
      shouldAdjustScrollPositionOnItemSizeChange?: (item: { start: number; end: number }, delta: number, instance: { scrollOffset: number | null }) => boolean
    }
    v.shouldAdjustScrollPositionOnItemSizeChange = (item, _delta, instance) => {
      return item.end < (instance.scrollOffset ?? 0)
    }
  }, [virtualizer])

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
    if (!stickToBottomRef.current) return
    // Coalesce: if a follow is already pending, don't cancel it — just mark
    // that a new follow was requested. The pending RAF will check the latest
    // scrollHeight. Cancelling starves the RAF when ResizeObserver fires rapidly.
    if (pendingFollowRafRef.current !== null) return
    setHasNewContent(false)
    const gen = ++followGenRef.current
    pendingFollowRafRef.current = requestAnimationFrame(() => {
      pendingFollowRafRef.current = null
      if (!stickToBottomRef.current || gen !== followGenRef.current) return
      const el = scrollRef.current
      if (el) {
        let attempts = 0
        const tryScroll = () => {
          // Increase from 15 to 30 attempts (~500ms at 60fps) — TanStack
          // Virtual's lazy measurement (measureElement via ResizeObserver) can
          // take >250ms for large lists with markdown/code highlighting.
          if (!stickToBottomRef.current || gen !== followGenRef.current || ++attempts > 30) return
          programmaticScrollRef.current = true
          const prev = el.scrollHeight
          el.scrollTop = el.scrollHeight
          queueMicrotask(() => { programmaticScrollRef.current = false })
          requestAnimationFrame(() => {
            if (stickToBottomRef.current && gen === followGenRef.current && el.scrollHeight > prev) tryScroll()
          })
        }
        tryScroll()
      }
    })
  }, [])

  // ── Scroll event handler ──────────────────────────────────────────────────
  // onScroll syncs stickToBottomRef with the true scroll position — this is
  // the ONLY event that knows whether the user is actually at the bottom,
  // including scroll paths that don't fire wheel/pointer/touch handlers
  // (e.g. scrollbar-drag on some browsers, programmatic/external scroll).
  //
  // A programmatic-scroll flag (programmaticScrollRef) distinguishes our own
  // scheduleFollow write from genuine user scroll. Without it, content growth
  // fires scheduleFollow → scrollTop=scrollHeight → onScroll fires while
  // scrollTop is momentarily at the old position (before the browser applies
  // the write) → a naive "not at bottom → pause" would kill following mid-stream.
  const onScroll = useCallback(() => {
    const el = scrollRef.current
    if (!el) return
    // Track scroll velocity for dynamic overscan
    const now = performance.now()
    const dt = now - lastScrollTimeRef.current
    if (dt > 0) {
      const delta = Math.abs(el.scrollTop - lastScrollTopRef.current)
      const velocity = delta / dt // px per ms
      // Fast scroll (>2px/ms): increase overscan to prevent blank flashes.
      // Slow/stop (<0.5px/ms): reduce overscan to save render work.
      const target = velocity > 2 ? 14 : velocity > 0.5 ? 8 : 5
      if (target !== dynamicOverscan) setDynamicOverscan(target)
    }
    lastScrollTopRef.current = el.scrollTop
    lastScrollTimeRef.current = now
    const atEnd = isAtBottom(el)
    const atStart = el.scrollTop <= EDGE_EPSILON
    setAtTop((prev) => (prev === atStart ? prev : atStart))
    setAtBottom((prev) => (prev === atEnd ? prev : atEnd))
    if (programmaticScrollRef.current) {
      return
    }
    // Do NOT set stickToBottomRef=false here. The virtualizer performs scroll
    // corrections during lazy measurement — it adjusts scrollTop to maintain
    // visual stability, which fires onScroll. If we set stick=false here, the
    // ResizeObserver callback (which re-scrolls to bottom) would be skipped,
    // leaving the viewport stuck mid-page. stick=false is set ONLY by user
    // input handlers (wheel/pointer/touch/keydown) — genuine user scroll.
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
  }, [virtualizer, cancelPendingFollow, dynamicOverscan])

  // Scroll-to-top sentinel ref — used by IntersectionObserver to detect
  // when the user scrolls to the top and trigger loadMore.
  const sentinelRef = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    const el = sentinelRef.current
    if (!el || !hasMore || !onLoadMore) return

    const observer = new IntersectionObserver(
      (entries) => {
        if (entries[0]?.isIntersecting && hasMore && !loadingMore) {
          // Capture the first VISIBLE row id BEFORE onLoadMore prepends older
          // rows — this is the scroll anchor we restore after the load lands,
          // so the viewport stays on the same content (not jumping to top).
          const items = virtualizer.getVirtualItems()
          const firstVisible = items[0]
          if (firstVisible) {
            const anchorRow = rowsRef.current[firstVisible.index]
            loadMoreAnchorIdRef.current = anchorRow?.id ?? null
          }
          void onLoadMore()
        }
      },
      { root: scrollRef.current, threshold: 0 },
    )
    observer.observe(el)
    return () => observer.disconnect()
  }, [hasMore, loadingMore, onLoadMore, virtualizer])

  // ── loadMore scroll-anchor: restore viewport after older rows prepend ────
  // Older rows prepend ABOVE the captured anchor, growing scrollHeight. Without
  // restoring, the viewport jumps to the new top. We scroll the anchor row back
  // to 'start' so the user's visible content is unchanged; the freshly loaded
  // older rows sit above it, so the scrollbar lands mid-list (not at the top).
  // Guarded by loadMoreAnchorIdRef so this is a no-op during normal streaming
  // (the ref is only set in the IntersectionObserver callback right before a load).
  useEffect(() => {
    const anchorId = loadMoreAnchorIdRef.current
    if (!anchorId) return
    const newIdx = rowsRef.current.findIndex((m) => m.id === anchorId)
    if (newIdx < 0) return
    // rAF: let the virtualizer mount + measure the freshly prepended rows so
    // scrollToIndex positions against real (not estimated) row heights.
    const raf = requestAnimationFrame(() => {
      loadMoreAnchorIdRef.current = null
      virtualizer.scrollToIndex(newIdx, { align: 'start' })
    })
    return () => cancelAnimationFrame(raf)
  }, [rows, virtualizer])

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
  // Only show "new content" when NOT following the bottom — if we're already
  // at the bottom, there's nothing the user needs to scroll to.
  // When following (stick=true) but not actually at the bottom (diff > 2px),
  // force-scroll to bottom — this is a safety net for cases where ResizeObserver
  // didn't fire (e.g. virtualizer corrected scrollHeight without resizing content).
  //
  // When NOT following (stick=false), we do NOT capture/restore scrollTop.
  // The virtualizer's own scroll correction (via its internal ResizeObserver)
  // keeps visible items stable when sizes change — it fires after useEffect
  // but before paint. A RAF restore would UNDO that correction, causing the
  // viewport to jump (jitter). The virtualizer's correction is authoritative.
  useEffect(() => {
    if (!stickToBottomRef.current) {
      setHasNewContent(true)
      return
    }
    // stick=true — ensure we're actually at the bottom
    const el = scrollRef.current
    if (el && el.scrollHeight - el.clientHeight - el.scrollTop > 2) {
      programmaticScrollRef.current = true
      el.scrollTop = el.scrollHeight
      queueMicrotask(() => { programmaticScrollRef.current = false })
    }
  }, [rows.length, liveProgress, hasFooter])

  // ── ResizeObserver: follow bottom when sticky ─────────────────────────────
  useEffect(() => {
    const scrollElement = scrollRef.current
    const content = contentRef.current
    if (!scrollElement || !content || typeof ResizeObserver === 'undefined') return
    // ResizeObserver fires during the browser's pre-paint phase (same as
    // useLayoutEffect), so synchronous scrolling here has no visual flicker.
    // This is critical for the virtualizer: it fires many ResizeObserver
    // callbacks during lazy measurement, and each one must immediately correct
    // scrollTop to the new scrollHeight. Using RAF (scheduleFollow) here causes
    // an active loop: the RAF cancels/reschedules faster than it can execute.
    //
    // OPTIMIZATION: Only observe scrollElement (not content). content height
    // changes are reflected in scrollElement.scrollHeight automatically.
    // Observing both caused duplicate callbacks (each item resize fired
    // twice), leading to redundant forced reflows (scrollTop = scrollHeight).
    const observer = new ResizeObserver(() => {
      if (!stickToBottomRef.current) return
      const el = scrollRef.current
      if (el) {
        programmaticScrollRef.current = true
        el.scrollTop = el.scrollHeight
        queueMicrotask(() => { programmaticScrollRef.current = false })
      }
    })
    observer.observe(scrollElement)
    return () => {
      observer.disconnect()
      cancelPendingFollow()
    }
  }, [cancelPendingFollow])

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
    if (newMessagesAdded) {
      // User sent a message (optimistic, not yet persisted) — always resume
      // following and scroll to bottom, even if the user had scrolled up.
      // Only for optimistic user messages (persisted=false), NOT for DB
      // messages loaded via reload (those don't represent user action).
      const lastRow = rows[rows.length - 1]
      if (lastRow?.role === 'user' && lastRow?.persisted === false) {
        resumeFollowing()
        scheduleFollow()
        return
      }
      // Assistant/streaming or DB messages: only follow if already sticky
      if (!stickToBottomRef.current) return
    }
    resumeFollowing()
    scheduleFollow()
  }, [chatKey, followResetToken, rows.length, resumeFollowing, scheduleFollow, virtualizer, loading])

  // ── Loading→false: scroll to bottom after history is fully loaded ──────────
  // (Removed polling — was not effective. Investigating root cause.)

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
        className="h-full overflow-y-auto overflow-x-hidden px-3 py-4 contain-content"
      >
        {loading && rows.length === 0 && (
          <div className="flex h-full flex-col items-center justify-center gap-3">
            <Loader2 className="size-5 animate-spin text-text-muted" />
            <span className="text-xs text-text-muted">{t('agent.loading')}</span>
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
          {/* Scroll-to-top sentinel: triggers loadMore via IntersectionObserver */}
          {hasMore && (
            <div ref={sentinelRef} className="flex justify-center py-2">
              {loadingMore ? (
                <Loader2 className="size-4 animate-spin text-text-muted" />
              ) : (
                <span className="text-xs text-text-muted">↑ 滚动加载更多</span>
              )}
            </div>
          )}
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
                    data-turn-id={row.turnID || undefined}
                    data-message-id={row.id}
                    data-role={row.role}
                    data-iter-count={row.iterations?.length ?? 0}
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
          {/* Busy placeholder: when agent is thinking but no streaming
              content has arrived yet (e.g. session just started, or
              switched to a busy tab with no iterations). Shown during
              loading when rows exist (the spinner handles the empty case),
              so the user always sees feedback on a busy session.
              INVARIANT: never show the placeholder below a FINISHED
              assistant message (one with a copy button — turn complete).
              A finished turn followed by "thinking…" would imply the
              completed turn is still running (linear-consistency
              violation). The placeholder only appears when the last row is
              a user message (new turn) or nothing at all. */}
          {busy && !liveMessage && !lastIsFinishedAssistant && !(loading && rows.length === 0) && (
            <div className="px-3 py-2">
              {liveProgress?.phase === 'compressing' ? (
                <div className="flex items-center gap-2 text-xs text-text-muted">
                  <Loader2 className="size-3.5 animate-spin" />
                  <span>{t('agent.compressing')}</span>
                </div>
              ) : (
                <ShimmerThinking />
              )}
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
