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
import { bindTurnIDs, orderMessageRows } from './messageOrder'
import { useI18n } from '@/providers/i18n'
import type { ChatMessage, LiveProgress } from '@/types/agent'

interface MessageListProps {
  /** Stable chat/session identity; changing it forces initial scroll to bottom. */
  chatKey?: string | null
  /** Increment to force TUI-style follow mode after local user actions. */
  followResetToken?: number
  messages: ChatMessage[]
  /** Live progress snapshot handed only to the streaming row (方案 A：live 行
   *  已在 messages 里，liveId 匹配 isPartial 行）。 */
  liveProgress: LiveProgress | null
  /** Whether the agent is busy (thinking/processing) — shows placeholder when
   *  no live row yet (e.g. session just started, no iterations arrived). */
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
// genui 行（顶层面板）：⚠️ 禁止 estimate —— TanStack 反复 measure（独立 createRoot
// 内容在行滚出/滚回重挂载时高度不稳定）→ estimate(400)↔measure(实际) 反复震荡 → 跳变。
// 用 module-level map 缓存高度：measure 一次后永久固定，之后 estimateSize 直接返回
// 缓存值，绝不重新 estimate。key = getItemKey 返回的稳定键（`turn-{turnID}-{role}`）。
const GENUI_ESTIMATE = 400
const genuiHeights = new Map<string, number>()
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
  // Committed rows are order-stable between frames — bind+sort them ONCE per
  // `messages` change (history reload, appendAssistant, injectUserMessage).
  // 方案 A：messages 含 live 行（store.toRows() 输出，useChatMessages 订阅
  // store 每帧 syncMessages）。注意：不使用 useSyncExternalStore —— 它对
  // 高频流式 store 写（live 每帧变化 → getSnapshot 每帧新引用）会触发额外
  // re-render 循环（E2E assistantRows=0 / stream-jitter 超时）。props.messages
  // 由 useChatMessages 的订阅驱动，每帧更新即可。
  const rows = useMemo<ChatMessage[]>(
    () => orderMessageRows(bindTurnIDs(messages)),
    [messages],
  )
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
  // loadMore prepend 前的 scrollTop 与 totalSize 快照：prepend 后 scrollTop
  // 增量补偿（ΔscrollTop == ΔtotalSize），让视口内容在 paint 前就保持不变——
  // 新旧行只出现在视口上方，加载只是「数据多了」，不闪到顶部再跳回底部。
  const loadMorePrevScrollTopRef = useRef<number>(0)
  const loadMorePrevTotalSizeRef = useRef<number>(0)
  // Invariant guard: the "thinking…" busy placeholder must never render below
  // a FINISHED assistant (copy button shown — turn complete). A finished turn
  // followed by "thinking…" would imply the completed turn is still running.
  // A committed assistant is isPartial=false with final content (approximation
  // of shouldRenderFinalContent at the row level).
  // busy placeholder 不再使用 lastIsFinishedAssistant —— committed assistant
  // 后面也可能有新 iter 在跑（busy=true），需要显示 placeholder。
  // const lastIsFinishedAssistant = ...  // 已删除
  // liveId 指向接收 liveProgress 的行（方案 A：live 行已在 messages 里，
  // isPartial 行）。没有 live 行时返回 null（liveProgress 不传给任何行）。
  // ⚠️ 必须匹配【最后一个】isPartial 行（最新 live），不能用 find（第一个）：
  // V5 让 cancel 后的 frozen 合并行也 isPartial=true，cancel 后发新消息时
  // rows 同时存在旧 frozen 行 + 新 live 行两个 isPartial。find 返回旧的
  // frozen 行 → 新 turn 的 streaming liveProgress（liveId 匹配的那行拿到的
  // 是 progress）传给旧行 → 旧行（user msg 上方）的 LiveIteration 渲染
  // "思考中…"（用户报告：cancel 后思考中显示在最新 user msg 上方）。最新的
  // isPartial 才是真正在接收 live 进度的行。
  const liveId = useMemo(() => {
    for (let i = rows.length - 1; i >= 0; i--) {
      if (rows[i].isPartial) return rows[i].id
    }
    return null
  }, [rows])
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
    estimateSize: (index) => {
      const row = rows[index]
      if (!row) return ESTIMATE
      // genui 行：⚠️ 禁用 estimate。从 genuiHeights map 取缓存高度（measure 一次后永久
      // 固定）。无缓存时用占位 GENUI_ESTIMATE，measure 回调会写入 map 并固化——
      // 之后绝不再 estimate/measure，行高恒定 → 滚动无跳变。
      if (rowHasGenUI(row)) {
        const key = stableRowKey(row)
        const cached = key && genuiHeights.has(key) ? genuiHeights.get(key)! : null
        // [GENUI_JUMP_DIAG] 记录 estimate 返回值（区分 estimate 是否在反复变）
        if (cached == null) {
          console.log(`[GENUI_JUMP_DIAG] estimate MISS key=${key} → use GENUI_ESTIMATE=${GENUI_ESTIMATE}`)
        } else {
          console.log(`[GENUI_JUMP_DIAG] estimate HIT key=${key} → cached=${cached}`)
        }
        return cached ?? GENUI_ESTIMATE
      }
      return ESTIMATE
    },
    overscan: dynamicOverscan,
    getItemKey: (index) => {
      const r = rows[index]
      if (!r) return `row-${index}`
      // 稳定 turn 键：assistant 行 live→committed 使用同一个 turnID+role（live 行
      // id="turn-N-live"、committed 行 id=assistant.id）—— 若用 row.id，提交瞬间
      // item.key 改变 → TanStack 整行 <div key> 卸载重建（"agent turn 结束后整个
      // turn DOM 重建"根因）。keying 用 turnID+role 让行在 live→committed 间保持
      // 挂载，内容由 React reconcile（不 remount）。legacy（turnID=0）与 pending
      // 用户行（MAX_SAFE_INTEGER，绑定真实 turn 前）回退 row.id。
      if (r.turnID > 0 && r.turnID < Number.MAX_SAFE_INTEGER) {
        return `turn-${r.turnID}-${r.role}`
      }
      return r.id ?? `row-${index}`
    },
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

  // [GENUI_JUMP_DIAG] totalSize / scrollTop 突变监听 —— 定位跳变到底来自哪
  // （totalSize 突变=行高/行数变化；scrollTop 突变=滚动被校正/跟随）。
  useEffect(() => {
    const el = scrollRef.current
    if (!el) return
    let prevTotal = virtualizer.getTotalSize()
    let prevScroll = el.scrollTop
    let lastLog = 0
    const id = setInterval(() => {
      const total = virtualizer.getTotalSize()
      const scroll = el.scrollTop
      const now = Date.now()
      const dTotal = Math.abs(total - prevTotal)
      const dScroll = Math.abs(scroll - prevScroll)
      // 只在突变明显且距上次日志 > 300ms 时打印，避免刷屏
      if ((dTotal > 50 || dScroll > 50) && now - lastLog > 300) {
        lastLog = now
        console.log(`[GENUI_JUMP_DIAG] total ${prevTotal}→${total} (Δ${Math.round(dTotal)}) | scrollTop ${prevScroll}→${scroll} (Δ${Math.round(dScroll)}) | genuiHeights=[${Array.from(genuiHeights.entries()).map(([k, v]) => `${k}:${Math.round(v)}`).join(', ')}]`)
      }
      prevTotal = total
      prevScroll = scroll
    }, 50)
    return () => clearInterval(id)
  }, [virtualizer])

  // ── GenUI 行高度固化（measure 一次后永久固定，禁止预测/重测）──────────────
  // TanStack 默认 measureElement 在 genui 行滚出视口（虚拟化卸载）再滚回时会重新
  // 测量独立 createRoot 的实际高度（内容不稳定）→ estimate(400)↔measure 反复震荡
  // → 滚动跳变。此回调：genui 行首次测量后写入 genuiHeights map；estimateSize 对
  // genui 行优先返回缓存值（已缓存则不再重测）→ 高度恒定，滚动无跳变。
  const measureRef = useCallback(
    (node: HTMLElement | null) => {
      if (!node) return
      const index = Number(node.dataset?.index ?? -1)
      const row = rows[index]
      if (row && rowHasGenUI(row)) {
        const key = stableRowKey(row)
        if (key && genuiHeights.has(key)) {
          // ⚠️ 已缓存 → 高度已由 map 固化（estimateSize 返回该值），**跳过
          // virtualizer.measureElement** —— 否则 TanStack 在 genui 行滚出→滚回
          // （虚拟化卸载→重挂载）时反复 measure 该行，触发 resizeItem →
          // shouldAdjustScrollPosition 校正 scrollTop → totalSize/scrollTop 同步突变
          // → 滚动跳变（diag 实证：RE-DETECT 每次滚动经过都触发 + total Δ578）。
          // 跳过 measure 后 TanStack 恒用 estimateSize 的 map 值（恒定），零跳变。
          return
        }
        if (key) {
          // 首测：读实际高度写 map（固化），然后交给 TanStack 首次测量。
          const rect = node.getBoundingClientRect()
          if (rect.height > 0) genuiHeights.set(key, rect.height)
          console.log(`[GENUI_JUMP_DIAG] measure FIRST set key=${key} → height=${rect.height}`)
        }
      }
      // 非 genui 行 / genui 首测：交给 TanStack 默认 measureElement（ResizeObserver 观测）。
      virtualizer.measureElement(node)
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [rows, virtualizer.measureElement],
  )

  // ── RENDER-LOSS / VIRTUALIZER-DROP monitor ────────────────────────────────
  // User report: "agent turn 消失" — the live tail row vanishes from the DOM
  // until the next iteration's first SSE event. rows is the FULL array
  // (committed + live); a live tail row disappearing WITHOUT a committed
  // replacement while the turn is still busy is REAL data loss — not
  // virtualization (getVirtualItems only returns the visible window, so its
  // length shrinking on scroll is NORMAL and must NOT be treated as a signal).
  // This effect also watches getVirtualItems() tail-index regression while
  // sticking to the bottom (the user's originally requested monitor).
  const prevRowsTailRef = useRef<{
    id: string | null
    turnID: number
    isPartial: boolean
    chatKey: string | null | undefined
    len: number
  } | null>(null)
  useEffect(() => {
    const tail = rows.length > 0 ? rows[rows.length - 1] : null
    const prev = prevRowsTailRef.current
    if (prev && prev.chatKey !== chatKey) {
      // Session switch: chatKey updates on the React render BEFORE the rows
      // are reloaded (useChatMessages still holds the OLD session's rows in
      // the same frame). Comparing the old session's live tail against the
      // new session's (still empty) rows would false-fire RENDER_LOSS_ROWS
      // (observed: chatKey=web:chat_A91F476D963A, prevTail=turn-337-live,
      // rowsLen=0 right after switching). Reset the baseline so the new
      // session's first rows start clean.
      prevRowsTailRef.current = null
      return
    }
    prevRowsTailRef.current = {
      id: tail?.id ?? null,
      turnID: tail?.turnID ?? 0,
      isPartial: tail?.isPartial ?? false,
      chatKey,
      len: rows.length,
    }
    if (!prev || prev.chatKey !== chatKey) return // session switch → rows replaced legitimately
    // 1) ROWS-LEVEL: live tail row vanished without committed replacement.
    const liveVanished = prev.isPartial && prev.id !== null && !rows.some((r) => r.id === prev.id)
    if (liveVanished && busy) {
      // Legal replacement paths: (a) normal text-event finalize — a committed
      // assistant with the same turnID appears; (b) commitLiveProgressAndReset
      // (turn_started/commit) — a committed assistant appears. If rows contain
      // NO committed assistant at all, the live content was wiped with nothing
      // taking its place → the "turn 消失只剩 user msg" report.
      const sameTurnCommitted = prev.turnID > 0 &&
        rows.some((r) => r.role === 'assistant' && !r.isPartial && r.turnID === prev.turnID)
      const anyCommittedAssistant = rows.some((r) => r.role === 'assistant' && !r.isPartial)
      if (!sameTurnCommitted && !anyCommittedAssistant) {
        console.error('[RENDER_LOSS_ROWS] live turn vanished without committed replacement', {
          prevTailId: prev.id,
          prevTurnID: prev.turnID,
          prevLen: prev.len,
          rowsLen: rows.length,
          busy,
          chatKey,
          lastRow: tail ? { id: tail.id, role: tail.role, turnID: tail.turnID, isPartial: tail.isPartial } : null,
          liveId,
          rowIds: rows.map((r) => r.id).slice(-5),
        })
        console.error(new Error('[RENDER_LOSS_ROWS] stack'))
      }
    }
    // 2) VIRTUALIZER-LEVEL: only alarm when the LAST row is the LIVE row
    // (isPartial=true) yet getVirtualItems() does not cover it while sticking
    // to the bottom. Historical committed rows sitting outside the viewport is
    // NORMAL virtualization (refresh lands at the top) — NOT a bug. The live
    // tail row being unrendered is the "agent turn 消失" symptom.
    const lastRow = rows.length > 0 ? rows[rows.length - 1] : null
    if (lastRow?.isPartial && stickToBottomRef.current) {
      const items = virtualizer.getVirtualItems()
      if (items.length > 0) {
        const lastItemIdx = items[items.length - 1].index
        if (lastItemIdx < rows.length - 1) {
          console.error('[VIRTUALIZER_TAIL_DROP] getVirtualItems() does not cover the LIVE last row while sticking to bottom', {
            lastItemIdx,
            rowsLen: rows.length,
            itemsLen: items.length,
            lastRowId: lastRow.id,
            lastRowRole: lastRow.role,
            busy,
          })
          console.error(new Error('[VIRTUALIZER_TAIL_DROP] stack'))
        }
      }
    }
  }, [rows, busy, chatKey, liveId, virtualizer])

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
          // Guard: skip if anchor restore is in progress (double-rAF pending).
          // During anchor restore the viewport briefly shows the top (sentinel
          // visible) → without this guard, IntersectionObserver fires again
          // → recursive loadMore (user report: "加载完后视角变到新内容最上方，
          // 导致再次触发加载").
          if (loadMoreAnchorIdRef.current !== null) return
          // Capture a scroll-anchor snapshot BEFORE onLoadMore prepends older
          // rows: current scrollTop + totalSize. After the prepend lands we
          // restore via ΔscrollTop == ΔtotalSize (not absolute anchor-row
          // lookup). That keeps the viewport pixel-stable on the same content —
          // older rows appear ABOVE the viewport, so the user only sees "more
          // data", never a jump-to-top then jump-back.
          const scroller = scrollRef.current
          if (scroller) {
            loadMorePrevScrollTopRef.current = scroller.scrollTop
            loadMorePrevTotalSizeRef.current = virtualizer.getTotalSize()
            loadMoreAnchorIdRef.current = '__load-more__'
          } else {
            loadMoreAnchorIdRef.current = null
          }
          void onLoadMore()
        }
      },
      { root: scrollRef.current, threshold: 0 },
    )
    observer.observe(el)
    return () => observer.disconnect()
  }, [hasMore, loadingMore, onLoadMore, virtualizer])

  // ── loadMore scroll-anchor: keep viewport pixel-stable via ΔscrollTop ────
  // Older rows prepend ABOVE the captured anchor, growing totalSize. Instead of
  // looking up an anchor row and scrollToIndex(它) (which required double-rAF +
  // Retry for ResizeObserver to measure real heights — during which the
  // viewport flashed to the top then jumped back), we do standard scroll
  // anchoring: ΔscrollTop == ΔtotalSize. Use useLayoutEffect (runs synchronously
  // BEFORE paint) so the compensation lands in the same frame as the prepend —
  // the user sees the identical pixels, only "more data" above, never a jump.
  //
  // The prepend rows are estimated-height at this point (ResizeObserver hasn't
  // measured them yet), so the ΔtotalSize here is estimated. The residual error
  // is corrected later by TanStack's shouldAdjustScrollPositionOnItemSizeChange
  // (configured below to only adjust items FULLY above the viewport), which
  // fires as a scroll around and keeps visible content stable — no flash.
  useLayoutEffect(() => {
    const anchorId = loadMoreAnchorIdRef.current
    if (anchorId !== '__load-more__') return
    const el = scrollRef.current
    loadMoreAnchorIdRef.current = null
    if (!el) return
    const newTotal = virtualizer.getTotalSize()
    const delta = newTotal - loadMorePrevTotalSizeRef.current
    if (delta <= 0) return
    programmaticScrollRef.current = true
    // Restore: old scrollTop + the prepended height. Content that was visible
    // before loadMore stays at the same viewport position; the new older rows
    // sit above (scrollbar moves toward the middle), exactly "data got more".
    el.scrollTop = loadMorePrevScrollTopRef.current + delta
    queueMicrotask(() => { programmaticScrollRef.current = false })
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
    // CRITICAL: observe BOTH the content and the scroll element — they cover
    // DIFFERENT resize cases:
    //
    //  - CONTENT growth (live row height changes, iteration history append,
    //    code highlighting) changes ONLY scrollHeight. ResizeObserver reports
    //    contentRect (clientHeight), and the scroll element's clientHeight
    //    stays fixed at the viewport height — so a scroll-element observer
    //    NEVER fires for content growth. contentRef wraps the virtualizer's
    //    sizing div (height = totalSize), so ITS contentRect tracks content
    //    height and fires on every growth.
    //
    //  - VIEWPORT shrink (the composer auto-grows up to 200px and squeezes
    //    this flex-1 list) changes clientHeight with content unchanged — the
    //    content observer NEVER fires, the sticky scrollTop stays at its old
    //    value, and the last row ends up hidden behind the taller composer
    //    (user-reported bug). The scroll element's own contentRect DOES
    //    change here, so observing it re-anchors to the bottom while sticky.
    const observer = new ResizeObserver(() => {
      if (!stickToBottomRef.current) return
      const el = scrollRef.current
      if (el) {
        programmaticScrollRef.current = true
        el.scrollTop = el.scrollHeight
        queueMicrotask(() => { programmaticScrollRef.current = false })
      }
    })
    observer.observe(content)
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
        className="h-full overflow-y-auto overflow-x-hidden px-4 py-3 contain-content md:px-3 md:py-4"
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
                    ref={measureRef}
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
          {busy && !(loading && rows.length === 0) &&
            (liveId === null || rows.length === 0 || rows[rows.length - 1].role === 'user') && (
            // busy placeholder 显示条件（方案 A）：
            // 1. 没有 live 行（liveId=null）—— 新 iter 还没到达，或切换会话后
            //    live 还没渲染。即使最后一个 row 是 committed assistant（turn
            //    还在跑），也应该显示"思考中"——否则用户看到卡死。
            // 2. 最后一个 row 是 user —— 新 turn 刚发，live 还没到。
            // 3. rows 为空 —— 首次加载 + busy。
            // 不再检查 lastIsFinishedAssistant：committed assistant 后面也可能
            // 有新 iter 在跑（busy=true），需要显示 placeholder。
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

// 稳定行 key（与 getItemKey 一致）：turnID>0 用 `turn-${turnID}-${role}`，否则 row.id。
// 用于 genui 高度缓存 map 的 key —— 行在 live→committed 间保持同一 key → 高度只 measure 一次。
export function stableRowKey(row: ChatMessage): string {
  if (row.turnID > 0 && row.turnID < Number.MAX_SAFE_INTEGER) {
    return `turn-${row.turnID}-${row.role}`
  }
  return row.id ?? ''
}

// 判断一行是否含 GenUI 面板（committed：迭代里有 uiMode 工具；live：流式 genuiContent）。
// estimateSize 据此返回更大的基数，缩小 estimate 与实际高度差距 → 滚动跳变小。
export function rowHasGenUI(row: ChatMessage): boolean {
  for (const iter of row.iterations ?? []) {
    for (const tool of iter.tools ?? []) {
      if (tool.uiMode) return true
    }
  }
  if ((row as ChatMessage & { genuiContent?: string }).genuiContent) return true
  return false
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
