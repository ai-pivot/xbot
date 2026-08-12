/**
 * useProgressStream — subscribes a ProgressStore to the SSE event stream for one
 * chatID and exposes the live progress + streaming-preview message (Spec 3/4).
 *
 * Event mapping (see protocol/ws.go, channel/web/web.go):
 *   stream_content      → append to streamContent/reasoningStreamContent +
 *                         patch streamingTools (stream-only, no snapshot replace)
 *   progress_structured → applyStructuredEvent (carry-forward + iteration
 *                         snapshot + replace non-stream fields)
 *   text                → finalize: hand the full text to onAssistantComplete,
 *                         then reset the store for the next turn.
 *   session(HistoryCompacted) → onHistoryCompacted (reset + reload)
 *   session(idle)       → defensive finalize if stream content accumulated
 *                         without a trailing `text`.
 *
 * The hook returns:
 *   - `progressSnapshot`: throttled immutable ProgressSnapshot (useSyncExternalStore)
 *   - `liveMessage`: a transient assistant ChatMessage built from the snapshot,
 *     so the list can render it inline without waiting for finalization.
 *   - `isStreaming`: true while there is accumulated streaming content.
 *
 * `liveMessage` is derived from the same store snapshot (memoized), so it only
 * changes when the snapshot changes — i.e. at most once per frame.
 */
import { useEffect, useLayoutEffect, useMemo, useRef } from 'react'
import { useSyncExternalStore } from 'react'

import { ProgressStore, mergeIterations, normalizeWebSubAgents, normalizeWebTools } from '@/components/agent/progressStore'
import { MessageStore } from '@/components/agent/messageStore'
import {
  historyProgressToLive,
  normalizeWebIteration,
  parseWebIterations,
} from '@/components/agent/normalize'
import type { WSConnection } from '@/types/ws'
import type {
  ProgressSnapshot,
  WebIteration,
  ChatMessage,
  TodoItem,
  TokenUsageInfo,
} from '@/types/shared'
import { EMPTY_PROGRESS_SNAPSHOT } from '@/types/shared'
import type { HistProgress } from '@/components/agent/api'
import type { WSMessage, WebToolProgress } from '@/types/shared'
import {
  clearProgressSnapshot,
  progressSnapshotCache,
  sessionCacheKey,
} from '@/lib/webCache'

interface UseProgressStreamOptions {
  /** Chat ID this stream tracks (events for other chats are ignored). */
  chatID: string | null
  /** Channel this stream tracks. Progress events may qualify chat_id as channel:chatID. */
  channel?: string
  /** Called with the finalized assistant text when a `text` event arrives. */
  onAssistantComplete?: (finalText: string, iterations: WebIteration[], eventSeq?: number, turnID?: number, insertBeforeLastUser?: boolean) => void
  /** Called when the turn is cancelled (cancel ack). Should NOT re-render the
   *  message list — the user already sees the streamed content as-is.
   *  Only reset the live progress store. */
  onCancelComplete?: () => void
  /** Called when a bg notification / cron triggers a new turn — displays the injected user message. */
  onInjectUserMessage?: (content: string, turnID: number, isNotification: boolean) => void
  /** Called when turn_started arrives — the frontend should enter busy mode. */
  onTurnStarted?: (turnID: number, trigger: string) => void
  /** Called when the server signals HistoryCompacted (reset + reload). */
  onHistoryCompacted?: () => void
  /** Called when the server signals a slash-command session reset (/new). */
  onSessionReset?: () => void
  /**
   * Called when the live iterationHistory develops an internal id gap (an
   * iteration's delta was dropped on the wire — incremental data loss that no
   * later SSE snapshot can backfill). The caller should reload from DB.
   */
  onIterationGap?: () => void
  /**
   * Optional live-progress snapshot from history (active_progress). When the
   * tracked chat is busy (phase != done) this hydrates the store so a page
   * refresh resumes the progress panel instead of showing an empty stream.
   * Spec 4 §3.8.
   */
  initialProgress?: HistProgress | null
  /** The realtime connection (injected from DockviewContext for isolated roots). */
  ws: WSConnection
  /** Disable subscriptions for read-only panes such as SubAgent history tabs. */
  disabled?: boolean
  /** 共享 MessageStore（方案 A Step 3）：live 事件写入 MessageStore（渲染行由
   *  store.toRows() 输出含 live 行）；不传则仅用 progressStore（旧路径）。 */
  messageStore?: MessageStore
}

export interface UseProgressStreamResult {
  /** Throttled immutable progress snapshot. */
  progressSnapshot: ProgressSnapshot
  /** Transient streaming assistant message, or null when idle. */
  liveMessage: ChatMessage | null
  /** True while there is accumulated streaming content. */
  isStreaming: boolean
  /** Full internal ProgressStore state (un-throttled current + lastTurnID) —
   *  for the REC developer tool's 100%-frontend-reconstruction state dump. */
  dumpFullState: () => { current: ProgressSnapshot; lastTurnID: number }
  /** Reset the progress store (clear live message + iterations). */
  resetProgress: () => void
}

/**
 * 3-layer chatID check: some messages carry chat_id at the top level (text),
 * some in msg.session.chat_id (session events), and some in msg.progress.chat_id
 * with a "web:" prefix (stream_content, progress_structured). Strip the prefix
 * and compare.
 *
 * If the message carries NO chat_id in any layer, it passes through (legacy
 * behavior — early events may not carry chat_id).
 */
export function matchesChatID(msg: WSMessage, targetChatID: string, targetChannel = 'web'): boolean {
  // If no chat_id anywhere, don't filter (legacy behavior)
  if (!msg.chat_id && !msg.session?.chat_id && !msg.progress?.chat_id) {
    return true
  }
  // Layer 1: top-level chat_id
  if (msg.chat_id === targetChatID) return true
  if (msg.chat_id === `${targetChannel}:${targetChatID}`) return true
  // Layer 2: session.chat_id
  if (msg.session?.chat_id === targetChatID) return true
  if (msg.session?.chat_id === `${targetChannel}:${targetChatID}`) return true
  // Layer 3: progress.chat_id may be bare or channel-qualified.
  if (msg.progress?.chat_id) {
    const progressChatID = String(msg.progress.chat_id)
    if (progressChatID === targetChatID || progressChatID === `${targetChannel}:${targetChatID}`) return true
  }
  return false
}

export function useProgressStream({
  chatID,
  channel = 'web',
  onAssistantComplete,
  onCancelComplete,
  onHistoryCompacted,
  onInjectUserMessage,
  onTurnStarted,
  onSessionReset,
  onIterationGap,
  initialProgress,
  ws,
  messageStore,
  disabled = false,
}: UseProgressStreamOptions): UseProgressStreamResult {
  const storeRef = useRef<ProgressStore | null>(null)
  if (storeRef.current === null) {
    storeRef.current = new ProgressStore()
  }
  const store = storeRef.current

  // Keep the latest callbacks in refs so the effect's handlers don't re-subscribe
  // whenever the parent re-renders.
  const completeRef = useRef(onAssistantComplete)
  completeRef.current = onAssistantComplete
  const cancelCompleteRef = useRef(onCancelComplete)
  cancelCompleteRef.current = onCancelComplete
  const compactedRef = useRef(onHistoryCompacted)
  compactedRef.current = onHistoryCompacted
  const resetRef = useRef(onSessionReset)
  resetRef.current = onSessionReset
  const injectRef = useRef(onInjectUserMessage)
  injectRef.current = onInjectUserMessage
  const turnStartedRef = useRef(onTurnStarted)
  turnStartedRef.current = onTurnStarted
  const iterationGapRef = useRef(onIterationGap)
  iterationGapRef.current = onIterationGap
  // One-shot per store-lifetime: iterationHistory gap (incremental delta lost)
  // fires reload once; reset only when the history becomes contiguous again.
  const iterationGapFiredRef = useRef(false)
  // True right after a session switch (chatID changed → store.fullReset). The
  // store is BLANK at that moment and the server's active_progress is the ONLY
  // source of the session's full iterationHistory — the storeActive guard must
  // NOT block the first hydration replace, or the pre-switch iterations stay
  // lost forever (user report: "来回切换会话后迭代消失"). Cleared after the
  // first successful replace.
  const sessionSwitchedRef = useRef(false)

  // Guard against multiple onAssistantComplete calls per turn.
  // Reset to false when new streaming begins (stream_content arrives).
  const finalizedRef = useRef(false)
  // 最后一次 finalize 的 turnID：text/cancel 时记录。finalized guard 用它区分
  // 旧 turn（turn_id <= finalizedTurnID，迟到事件丢弃）与新 turn（turn_id 更大，
  // 放行 —— 用户场景：cancel 后新 turn 的 progress_structured 不能被
  // finalized 状态阻塞，否则 live 永不渲染卡"思考中"）。
  const finalizedTurnIDRef = useRef(0)
  // Set when commitLiveProgressAndReset commits old turn's content (turn_started
  // with hadVisibleProgress=true). Prevents initialProgress hydration from
  // re-introducing old turn's iterationHistory into the new turn's store.
  // Cleared when the first structured event of the new turn arrives (stream_content
  // or progress_structured with phase != done).
  const turnCommittedRef = useRef(false)
  // Set when PhaseDone is received. Prevents session(idle) from defensively
  // finalizing — PhaseDone means the turn ended, and the text event (normal
  // or cancel ack) is the authoritative finalizer. Without this, if
  // session(idle) arrives before text(cancelled), the defensive finalize
  // commits stale streamContent that the backend already persisted to DB →
  // duplicate message on history reload.
  const phaseDoneRef = useRef(false)
  const prevProgressCacheKeyRef = useRef<string | null>(null)

  // Track chatID inside the handlers via ref so we don't tear down the store on
  // every chat switch (we just reset it).
  const chatIDRef = useRef(chatID)
  chatIDRef.current = chatID

  // ── Cross-session global state pollution guard ──
  // store (ProgressStore) is a useRef created ONCE — it survives chatID changes
  // (AgentPanel does NOT remount useProgressStream on session switch; there is
  // no key on the hook). The OLD session's lastIter/iterationHistory/lastTurnID
  // and the finalized/phaseDone/turnCommitted refs stay behind and POISON the
  // NEW session's event handling: iterations mis-judged as regressed, late
  // events dropped by the finalized/phaseDone guards, iterationHistory mixed
  // with the old session — the new session's turn vanishes (user report:
  // "怀疑和不同 session 之间全局状态污染有关"; only a full page refresh,
  // which rebuilds everything, clears it). Reset everything on chat switch.
  // fullReset() clears lastTurnID + lastIter + all iteration state so the
  // hydration effect can restore the new session cleanly.
  const prevChatIDRef = useRef(chatID)
  useEffect(() => {
    if (prevChatIDRef.current !== chatID) {
      prevChatIDRef.current = chatID
      store.fullReset()
      finalizedRef.current = false
      phaseDoneRef.current = false
      turnCommittedRef.current = false
    }
  }, [chatID, store])

  // ── RENDER_LOSS diagnostic ──
  // Catch the "already-rendered content vanished during streaming" bug class
  // (turn-vanish reports) at the exact mutation: any streaming turn whose
  // iterationHistory or lastIter drops to zero means already-rendered
  // iterations disappeared from the live row. This MUST never happen — the log
  // pinpoints the corrupting event so the root cause can be fixed at the source
  // instead of patching symptoms. Non-streaming (turn ended) is excluded — the
  // committed reply replaces the live row legitimately.
  useEffect(() => {
    let prevIterCount = store.getSnapshot().iterationHistory.length
    let prevLastIter = store.getSnapshot().lastIter
    return store.subscribe(() => {
      const s = store.getSnapshot()
      if (s.streaming) {
        if (prevIterCount > 0 && s.iterationHistory.length === 0) {
          console.error('[RENDER_LOSS] iterationHistory cleared during streaming', {
            prev: prevIterCount,
            lastIter: s.lastIter,
            phase: s.phase,
            turnID: s.turnID,
            chatID,
          })
        }
        if (prevLastIter > 0 && s.lastIter === 0) {
          console.error('[RENDER_LOSS] lastIter reset during streaming', {
            prev: prevLastIter,
            phase: s.phase,
            turnID: s.turnID,
            chatID,
          })
        }
      }
      prevIterCount = s.iterationHistory.length
      prevLastIter = s.lastIter
    })
  }, [store, chatID])

  const progressCacheKey = chatID ? sessionCacheKey(channel, chatID) : null

  const progressSnapshot = useSyncExternalStore(
    store.subscribe,
    store.getSnapshot,
    store.getSnapshot,
  )

  // Switch immediately to this chat's in-memory snapshot while history refreshes.
  useLayoutEffect(() => {
    finalizedRef.current = false
    phaseDoneRef.current = false
    // Full reset on chatID change (including todos — different session).
    // On non-chatID triggers (disabled toggle), preserve todos via reset().
    if (progressCacheKey !== prevProgressCacheKeyRef.current) {
      const prevKey = prevProgressCacheKeyRef.current
      prevProgressCacheKeyRef.current = progressCacheKey
      // Session switch: the store is blanked and the server's active_progress
      // is the ONLY source of the session's full iterationHistory. Let the
      // first hydration replace run even if new SSE events make the store
      // look active — otherwise pre-switch iterations are lost forever
      // ("来回切换会话后迭代消失"). Only when switching (prevKey was set);
      // first mount must NOT bypass the storeActive guard.
      if (prevKey !== null) {
        sessionSwitchedRef.current = true
      }
      // Restore todos from progressSnapshotCache — switchSession writes
      // the /switch response todos here so they appear immediately,
      // before /api/history's active_progress arrives (which may return
      // null if the backend's snapshot was already cleaned up).
      if (progressCacheKey) {
        const cached = progressSnapshotCache.get(progressCacheKey)
        if (cached?.todos && cached.todos.length > 0) {
          // Atomic reset + replace: single notification instead of two
          // (fullReset → render → replace → render).
          store.resetAndReplace({ todos: cached.todos.map((t) => ({
            id: typeof t.id === 'number' ? t.id : 0,
            text: typeof t.text === 'string' ? t.text : '',
            done: Boolean(t.done),
          })) })
        } else {
          store.fullReset()
        }
      } else {
        store.fullReset()
      }
    } else {
      // CRITICAL: NEVER wipe a turn that is actively streaming. This branch
      // fires when `disabled` toggles (SSE subscription/connection state flips)
      // while the chatKey is unchanged. The OLD code called store.reset()
      // unconditionally — a mid-turn disabled flake (subscribe toggling during
      // reconnect, session-status jitter) BLANKED the entire live store →
      // liveMessage null → the whole live turn vanished from the DOM for the
      // duration (user report: [RENDER_LOSS_ROWS] rowsLen:0, liveMessageId:
      // null, busy:true). The live store is driven by SSE events and has its
      // own lifecycle — a subscription-state toggle must not wipe it. Only
      // reset when the store is genuinely idle (turn over: streaming=false and
      // no active/running phase), mirroring the hydration-effect guard.
      const snap = store.getSnapshot()
      const storeActive =
        snap.streaming ||
        snap.phase === 'thinking' ||
        snap.phase === 'tool_exec' ||
        snap.phase === 'running' ||
        snap.phase === 'frozen'
      if (!storeActive) store.reset()
    }
    if (disabled) {
      return
    }
  }, [store, progressCacheKey, disabled])

  // Hydrate from history when initialProgress changes (after reload completes).
  // Separated from the reset effect so that a chatID change does NOT hydrate
  // with the stale initialProgress from the previous session — only the new
  // session's data triggers hydration (Spec 5 §2.7).
  useEffect(() => {
    if (disabled) return
    const snap = store.getSnapshot()
    // CRITICAL: NEVER wipe a turn that is actively streaming. reload() may
    // complete mid-turn with active_progress=null (rewind cleared the server
    // snapshot, or the fetch raced the snapshot registration, or the reload was
    // triggered by an SSE seq gap). Resetting here makes the ENTIRE live turn
    // vanish from the DOM (user report: "agent turn 消失" — the turn's rows
    // disappear completely, not a blank area) until the next SSE event refills
    // the store. If SSE is still pushing events (streaming=true or an active
    // phase), the reload snapshot is STALE — let the live events drive. Only
    // reset when the store is genuinely idle (turn over: streaming=false and
    // no active/running phase).
    const storeActive =
      snap.streaming ||
      snap.phase === 'thinking' ||
      snap.phase === 'tool_exec' ||
      snap.phase === 'running' ||
      snap.phase === 'frozen'
    if (!initialProgress || !initialProgress.phase) {
      if (hasVisibleProgress(snap) && !storeActive) store.reset()
      return
    }
    if (initialProgress.phase === 'done') {
      // Turn ended. Clear progress but restore todos from server so they
      // survive session switch (todos persist across turns in the todoManager).
      if (progressCacheKey) clearProgressSnapshot(progressCacheKey)
      finalizedRef.current = false
      if (hasVisibleProgress(snap) && !storeActive) store.reset()
      // Unconditionally replace todos when the server explicitly returned an
      // array — INCLUDING an empty one. GetActiveProgress returns
      // `{phase:'done', todos}` after a turn (turn-end cleanupTodos clears the
      // list, so todos: [] means "cleared"). With the old `todos.length > 0`
      // guard, an empty list was treated as "no data → keep existing",
      // resurrecting stale todos on every session switch/refresh — the
      // frontend diverged from the server's authoritative state.
      if (Array.isArray(initialProgress.todos)) {
        // Use setStructuredTools, NOT replace: ProgressStore.replace strips
        // `todos` from its input (todos are managed separately to avoid
        // historyProgressToLive's todos:[] overwriting cached todos).
        // setStructuredTools applies todos via its dedicated todos path
        // (see the phase==='done' contract in progressStore.ts setStructuredTools).
        store.setStructuredTools({ todos: initialProgress.todos as TodoItem[] })
      }
      return
    }
    // Don't re-hydrate after finalization — the turn is over, and the
    // server's active_progress may be stale (not yet cleaned up). Only
    // hydrate if we haven't started receiving live events for this turn
    // (finalizedRef is false AND store is empty = fresh load/reconnect).
    if (finalizedRef.current) return
    // turnCommittedRef: turn_started committed old turn's live content.
    // reload()'s active_progress may still carry old turn's iterationHistory
    // (server hasn't cleaned up). Block hydration until the new turn's first
    // structured event arrives (which clears turnCommittedRef).
    if (turnCommittedRef.current) return
    // After turn_started set store.lastTurnID, a reload's active_progress may
    // still carry the PREVIOUS turn's data (server hasn't cleaned up yet).
    // Hydrating with it would re-introduce old iterationHistory → cross-turn
    // leak (new turn's iterations append to old). Skip if TurnID mismatches.
    if (store.lastTurnID > 0 && initialProgress.turn_id !== store.lastTurnID) {
      return
    }
    const live = historyProgressToLive(initialProgress)
    // initialProgress comes from the server's authoritative active_progress
    // (fetched via reload). Always replace — the cache-restored snapshot
    // (from the reset effect) may have a higher eventSeq (updated by live SSE
    // events), which would block the server's authoritative data and cause
    // incomplete iteration recovery on session switch.
    //
    // CRITICAL: NEVER overwrite a store that is actively streaming. reload()
    // completes mid-turn (resync_required / replay_gap / seq-gap reload while
    // the agent is still streaming), and the server snapshot can be STALE or
    // LACONIC vs. the live SSE-driven store: (a) from_iteration delta filtering
    // returns only NEW iterations, (b) an iteration-boundary snapshot has its
    // visible fields cleared by historyProgressToLive (content/completedTools
    // dropped to avoid duplication), (c) the server snapshot simply lags the
    // SSE events. Replacing a streaming store with it leaves the store with no
    // visible fields → liveMessage null → the ENTIRE live turn vanishes from
    // the DOM (rowsLen:0 — same bug class as the reset guards above; user
    // report: "回复显示到一半突然消失"). Let SSE events drive; only hydrate a
    // genuinely idle store. Mirrors the storeActive guard used for reset().
    //
    // EXCEPTION: when iterationHistory has an INTERNAL jump (delta lost —
    // unambiguous loss), allow the replace even while streaming: the reload was
    // triggered BY that gap (onIterationGap), and the server snapshot carries
    // the authoritative full iterationHistory — replacing repairs the broken
    // history. Without this exception the gap never healed and onIterationGap
    // re-fired every event → reload loop → "turn 消失维持一个完整的迭代".
    // storeActive guard 只保护 progressStore（避免覆盖正在流式的 live state）。
    // MessageStore 的 live 写入不受此 guard 影响 —— 切换会话时 progressStore
    // 可能有旧数据（storeActive=true），但 MessageStore 是空的（session 切换
    // clear 了），必须写入 hydration 的 live 否则 live 行不渲染（用户报告：
    // 切换会话后不显示"思考中"，刷新后正常）。
    const skipProgressStore = storeActive && !sessionSwitchedRef.current && !store.hasIterationGapNow()
    if (live.phase) {
      if (!skipProgressStore) {
        store.replace(live)
        sessionSwitchedRef.current = false
      }
      // Ensure turnID is tracked for same-turn dedup (MessageList uses
      // liveMessage.turnID to match committed history messages).
      if (live.turnID > 0) {
        store.lastTurnID = live.turnID
      }
      // MessageStore 同步（方案 A）：hydration 的 live（active_progress）必须
      // 写入共享 MessageStore，否则 chat.messages（store.toRows()）不含 live
      // 行 → MessageList 不渲染 assistant（切换会话后 turn 消失，用户报告）。
      // 不受 storeActive guard 影响（MessageStore 独立于 progressStore）。
      if (messageStore && live.turnID > 0) {
        messageStore.updateLive(live.turnID, {
          eventSeq: typeof live.eventSeq === 'number' ? live.eventSeq : 0,
          phase: live.phase,
          content: live.streamContent || live.content || '',
          reasoningStreamContent: live.reasoningStreamContent || '',
          iterations: live.iterationHistory ?? [],
          activeTools: live.activeTools ?? [],
          completedTools: live.completedTools ?? [],
          streamingTools: live.streamingTools ?? [],
          lastIter: typeof live.lastIter === 'number' ? live.lastIter : 0,
        })
      }
    }
  }, [store, initialProgress, disabled, progressCacheKey, messageStore])

  // Dispose on unmount.
  useEffect(() => {
    return () => {
      store.dispose()
      storeRef.current = null
    }
  }, [store])

  // Subscribe to SSE messages.
  // ws is held in a ref — its onMessage delegates to a stable MultiSSEManager
  // instance, so we don't need ws in the effect deps. Including ws would cause
  // the handler to be unregistered and re-registered on every connection state
  // change (connected/disconnected), creating a window where new SSE connections
  // created by useActiveSSESubscription don't have the handler yet — missing
  // progress_structured events (including TodoWrite updates).
  const wsRef = useRef(ws)
  wsRef.current = ws
  useEffect(() => {
    if (disabled) return
    const offMessage = wsRef.current.onMessage((msg: WSMessage) => {
      // 3-layer chatID filtering.
      if (chatIDRef.current && !matchesChatID(msg, chatIDRef.current, channel)) {
        return
      }
      if (chatIDRef.current && isTerminalProgressMessage(msg)) {
        clearProgressSnapshot(sessionCacheKey(channel, chatIDRef.current))
      }
      handleProgressMessage(msg, store, completeRef, compactedRef, resetRef, finalizedRef, finalizedTurnIDRef, phaseDoneRef, injectRef, turnStartedRef, cancelCompleteRef, turnCommittedRef, iterationGapRef, iterationGapFiredRef, messageStore)
    })
    return offMessage
  }, [store, disabled, channel, messageStore])

  // Derive a transient streaming message from the snapshot. Only the snapshot's
  // streamContent/streaming drives this, so it updates at frame rate (not per token).
  const liveMessage = useMemo<ChatMessage | null>(() => {
    const snap = progressSnapshot
    if (!hasVisibleProgress(snap)) return null
    if (snap.phase === 'done') return null
    // 'frozen' phase: turn is over (cancel/commit). Keep the live message
    // visible with its real turnID so buildMessageRows inserts it at the
    // correct position (above the newest user msg if the turn was cancelled).
    // The committed message (from appendAssistant in flushSync) will replace
    // it on the next render — but until then, the user sees the content
    // they were looking at (no disappearing content).
    //
    // SSE reconnect duplicate fix: when restoreActiveProgress repopulates
    // snap.iterationHistory during a reconnect, the frozen liveMessage would
    // duplicate the committed message's iterations. To prevent this,
    // buildMessageRows' turnExists check dedupes by turnID — if the
    // committed message (same turnID) is already in the messages array,
    // the frozen live row is NOT rendered (turnExists=true → insert at old
    // turn position, but the committed message at that position takes
    // precedence in the virtual list by key).
    // turnID: snapshot's authoritative turn, falling back to store.lastTurnID
    // ONLY for the frozen (cancelled) phase — a frozen live row must render
    // inside its own turn (above the next user msg). A STREAMING live with
    // snap.turnID=0 (turn_started lost to SSE coalescing) must NOT fall back:
    // store.lastTurnID is the PREVIOUS turn, and buildMessageRows' same-turn
    // merge would absorb the streaming live into the old committed assistant —
    // the turn "vanishes" mid-stream (user report). turnID=0 sorts the live to
    // the bottom (it IS the newest content).
    const tid = snap.turnID || (snap.phase === 'frozen' ? store.lastTurnID : 0) || 0
    return {
      id: `turn-${tid}-live`,
      role: 'assistant',
      content: snap.streamContent || snap.content || '',
      iterations: snap.iterationHistory,
      timestamp: new Date().toISOString(),
      isPartial: true,
      turnID: tid,
    }
  }, [progressSnapshot, chatID])

  return {
    progressSnapshot: progressSnapshot ?? EMPTY_PROGRESS_SNAPSHOT,
    liveMessage,
    isStreaming: hasVisibleProgress(progressSnapshot),
    /** Full internal ProgressStore state (un-throttled current + lastTurnID) —
     *  for the REC developer tool's 100%-frontend-reconstruction state dump.
     *  current.lastIter is the real iteration watermark (snapshot-level field
     *  advanced by structured events); lastTurnID is the only store-level field
     *  not derivable from the snapshot. */
    dumpFullState: () => store.dumpFullState(),
    resetProgress: () => {
      finalizedRef.current = true
      if (finalizedTurnIDRef) finalizedTurnIDRef.current = store.lastTurnID
      phaseDoneRef.current = false
      // NORMAL COMPLETION (text event): the committed message was already
      // added by appendAssistant inside the SAME flushSync — clear the live
      // store so the live row is NOT rendered again (final-iteration
      // duplicate: live + committed both showed the same reply when the
      // committed message's turnID didn't match the live turnID).
      // Cancel does NOT call this — text(cancelled) uses store.freeze() and
      // keeps the frozen live visible (user requirement: already-rendered
      // content never disappears). Cf81be66 made this a no-op to avoid
      // freeze()-induced flicker; that is obsolete since f4c43a45 — the
      // frozen-phase null check in liveMessage was removed, and reset()
      // inside flushSync renders committed + cleared-live atomically.
      store.reset()
    },
  }
}

export function hasVisibleProgress(snap: ProgressSnapshot): boolean {
  return Boolean(
    snap.streamContent ||
      snap.content ||
      snap.reasoningStreamContent ||
      snap.activeTools.length ||
      snap.completedTools.length ||
      snap.streamingTools.length ||
      snap.iterationHistory.length ||
      // Iteration-boundary instant: the previous iteration's active/completed
      // tools were JUST cleared (a new iteration started — the clearing event
      // is often a phase:undefined stream delta which carries NO
      // iteration_history), but the new iteration's iterationHistory delta has
      // not arrived yet. Every visible field is momentarily empty → without
      // this guard the live row VANISHES for a frame (user report: "agent
      // turn 消失然后又出现"). Already-rendered content must never disappear:
      // as long as the turn has made progress (lastIter > 0) the live row
      // stays. Turn end (text event) resets the store (lastIter=0), and the
      // pre-iteration "thinking" phase has lastIter=0 — both unaffected.
      snap.lastIter > 0 ||
      snap.lastReasoning ||
      snap.subAgents.length,
  )
}

/**
 * Commit any uncommitted live progress content to the committed message list,
 * then reset the store. Used when a new turn begins but the previous turn's
 * text event was lost (SSE coalescing/disconnect): the live content is the
 * ONLY display of the old turn's reply — wiping it without committing makes
 * the content vanish from the UI in one frame (flicker) AND loses it until a
 * history reload. Commit-then-reset hands the content over atomically: the
 * message stays visible at the same position, just re-parented from the live
 * stream to the committed list. No-op (plain reset) when nothing is visible.
 */
function commitLiveProgressAndReset(
  store: ProgressStore,
  complete: ((finalText: string, iterations: WebIteration[], eventSeq?: number, turnID?: number, insertBeforeLastUser?: boolean) => void) | undefined,
  newTurnID?: number,
): void {
  const snap = store.getSnapshot()
  if (hasVisibleProgress(snap)) {
    const text = snap.streamContent || snap.content || ''
    const liveReasoning = snap.reasoningStreamContent || snap.lastReasoning || ''
    let iters = snap.iterationHistory
    // Include the CURRENT iteration's IN-FLIGHT tools (activeTools only) —
    // but ONLY when there is no iteration history yet: a cancelled turn whose
    // tool was still RUNNING at cancel time lived in activeTools and was
    // visible in the live UI ("user msg1 iter1(cancelled) user msg2" lost
    // iter1 until a reload backfilled it from DB). When iterationHistory is
    // non-empty the finished tools are already recorded there — folding a
    // stale activeTools residue (e.g. a previous turn's tools that PhaseDone
    // did not clear) would leak them into this commit (cross-turn leak).
    // Include the CURRENT iteration's IN-FLIGHT tools (activeTools with
    // status running/generating) — but ONLY when there is no iteration
    // history yet: a cancelled turn whose tool was still RUNNING at cancel
    // time lived in activeTools and was visible in the live UI ("user msg1
    // iter1(cancelled) user msg2" lost iter1 until a reload backfilled it).
    // DONE/COMPLETED tools are NOT folded — they are already recorded in the
    // iteration history / progress_history, and a stale activeTools residue
    // (PhaseDone does not clear it until the text event) would leak the
    // PREVIOUS turn's tools into this commit (cross-turn leak, Bug 2).
    const liveTools: WebToolProgress[] = iters.length === 0
      ? snap.activeTools.filter(
          (t) => t && t.name && (t.status === 'running' || t.status === 'generating'),
        )
      : []
    if (liveTools.length > 0) {
      if (iters.length === 0) {
        iters = [{ iteration: 1, thinking: '', reasoning: liveReasoning, tools: liveTools, toolCount: liveTools.length }]
      } else {
        const last = iters[iters.length - 1]
        iters = [
          ...iters.slice(0, iters.length - 1),
          { ...last, tools: [...(last.tools ?? []), ...liveTools], toolCount: (last.toolCount ?? 0) + liveTools.length },
        ]
      }
    }
    if (liveReasoning) {
      if (iters.length === 0) {
        iters = [{ iteration: 1, thinking: '', reasoning: liveReasoning, tools: [], toolCount: 0 }]
      } else if (liveReasoning.length > (iters[iters.length - 1].reasoning || '').length) {
        iters = iters.map((it, i) =>
          i === iters.length - 1 ? { ...it, reasoning: liveReasoning } : it
        )
      }
    }
    if (text || iters.length > 0) {
      let commitText = text
      let commitIters = iters
      if (snap.phase === 'frozen' && text && iters.length === 0) {
        commitIters = [{ iteration: 1, thinking: text, reasoning: liveReasoning, tools: [], toolCount: 0 }]
        commitText = ''
      } else if (snap.phase === 'frozen' && text && iters.length > 0 && !iters[iters.length - 1].thinking) {
        commitIters = iters.map((it, i) =>
          i === iters.length - 1 ? { ...it, thinking: text } : it
        )
        commitText = ''
      }
      complete?.(commitText, commitIters, undefined, snap.turnID || newTurnID || store.lastTurnID, true)
    }
  }
  // After committing: reset the iteration state for a NORMAL new turn / session
  // switch — the already-rendered content is now in the committed message
  // (appendAssistant in flushSync; buildMessageRows' hasCommitted skips the
  // live row), so resetting only clears the live iteration counters. This is
  // REQUIRED: without it the previous turn's lastIter (e.g. 48) stays, and the
  // new turn's iteration (e.g. 29) is dropped by setStructuredTools'
  // iteration-regression guard — the new turn's live NEVER updates → the turn
  // vanishes (user report: ITER_ID_INVARIANT_VIOLATION prev:48 next:29
  // phase:'tool_exec' when switching sessions).
  // frozen (cancelled) phase does NOT reset — the already-rendered cancel
  // content stays visible (user requirement; cancel itself goes through
  // store.freeze() in text(cancelled), but a turn_id change on top of a frozen
  // live must preserve it).
  if (snap.phase !== 'frozen') {
    store.reset()
  }
}

/** Dispatch one WSMessage into the progress store. Shared with history hydration. */
/**
 * 把 progressStore 的最新 live 状态镜像到 MessageStore（方案 A Step 3）。
 * MessageStore 的 live 行供渲染（store.toRows() 输出），progressStore 的
 * ProgressSnapshot 供 liveProgress/context bar。两者并行，事件后同步。
 */
function writeLiveToMessageStore(
  ms: MessageStore | undefined,
  store: ProgressStore,
  p: { turn_id?: number; seq?: number },
): void {
  if (!ms) return
  let turnID = typeof p.turn_id === 'number' && p.turn_id > 0 ? p.turn_id : store.lastTurnID
  if (turnID <= 0) {
    // 早期流事件（stream_content 无 turn_id + 无 turn_started）：方案 A 前 live
    // 由 progressStore 派生（无 turn_id 也能渲染）；现在 live 在 MessageStore，
    // 需要归属。归到 turn 1（首个 turn 的流）；后续带 turn_id 的事件（turn_started/
    // progress_structured）会修正 slot 归属。
    turnID = 1
  }
  const cur = store.dumpFullState().current
  const prevLive = ms.getLive(turnID)
  const newContent = cur.streamContent || cur.content || ''
  const newReasoning = cur.reasoningStreamContent || ''
  ms.updateLive(turnID, {
    eventSeq: cur.eventSeq,
    phase: cur.phase,
    content: newContent || prevLive?.content || '',
    reasoningStreamContent: newReasoning || prevLive?.reasoningStreamContent || '',
    iterations: cur.iterationHistory,
    activeTools: cur.activeTools,
    completedTools: cur.completedTools,
    streamingTools: cur.streamingTools,
    lastIter: cur.lastIter,
  })
  // cancel（frozen phase）：MessageStore 冻结 live —— 已渲染内容永不消失
  // （toRows 里 assistant + frozen live 合并渲染）
  if (cur.phase === 'frozen') {
    ms.freeze(turnID)
  }
}

function handleProgressMessage(
  msg: WSMessage,
  store: ProgressStore,
  completeRef: React.MutableRefObject<UseProgressStreamOptions['onAssistantComplete']>,
  compactedRef: React.MutableRefObject<UseProgressStreamOptions['onHistoryCompacted']>,
  resetRef: React.MutableRefObject<UseProgressStreamOptions['onSessionReset']>,
  finalizedRef?: React.MutableRefObject<boolean>,
  finalizedTurnIDRef?: React.MutableRefObject<number>,
  phaseDoneRef?: React.MutableRefObject<boolean>,
  injectRef?: React.MutableRefObject<UseProgressStreamOptions['onInjectUserMessage']>,
  turnStartedRef?: React.MutableRefObject<UseProgressStreamOptions['onTurnStarted']>,
  cancelCompleteRef?: React.MutableRefObject<UseProgressStreamOptions['onCancelComplete']>,
  turnCommittedRef?: React.MutableRefObject<boolean>,
  iterationGapRef?: React.MutableRefObject<UseProgressStreamOptions['onIterationGap']>,
  iterationGapFiredRef?: React.MutableRefObject<boolean>,
  messageStore?: MessageStore,
): void {
  switch (msg.type) {
    case 'stream_content': {
      // If the turn is already finalized (cancel ack or text event arrived),
      // discard late stream_content — it reopens the store and re-displays
      // generating tools / streaming text that the user already saw cancelled.
      if (finalizedRef?.current) return
      // If PhaseDone already fired, the turn is ending — the text event or
      // cancel ack will arrive next with the final content. Late stream_content
      // is stale and would re-set streamingTools, causing iteration duplication
      // (the generating tool renders alongside the same iteration's "done" entry
      // in iterationHistory).
      if (phaseDoneRef?.current) return

      // New turn's first stream event — clear turnCommittedRef (set by
      // turn_started when it committed old turn's live content). This
      // unblocks initialProgress hydration for future reloads.
      if (turnCommittedRef) turnCommittedRef.current = false

      // stream_content carries content deltas in progress.stream_content /
      // progress.reasoning_stream_content (channel/web/web.go SendStreamContent).
      // Also carries streaming_tools (generating status, for tool name detection).
      const p = msg.progress
      if (!p) return

      // Set cumulative text (stream-only, does not replace the snapshot).
      // Delta pushes (bandwidth optimization: O(n) total per iteration)
      // APPEND to the accumulated text; checkpoint pushes (iteration-end
      // realignment / legacy full-push) REPLACE via setStreamContent.
      if (p.stream_delta) {
        store.appendStreamContent(String(p.stream_delta))
      } else if (p.stream_content) {
        store.setStreamContent(String(p.stream_content))
      }
      if (p.reasoning_stream_delta) {
        store.appendReasoningContent(String(p.reasoning_stream_delta))
      } else if (p.reasoning_stream_content) {
        store.setReasoningContent(String(p.reasoning_stream_content))
      }
      // GenUI streaming HTML (from display_html tool arguments)
      if (p.genui_content) store.setGenUIContent(p.genui_content)
      // Streaming tools (generating status) — patch only, no snapshot replace
      if (p.streaming_tools) {
        store.setStreamOnlyFields({
          streamingTools: normalizeWebTools(p.streaming_tools as unknown[]),
        })
      }
      // MessageStore live 同步（方案 A Step 3）
      writeLiveToMessageStore(messageStore, store, p)
      return
    }

    case 'progress_structured':
    case 'sync_progress': {
      const p = msg.progress
      if (!p) return
      // turn_started: a new agent turn is beginning. This replaces the old
      // inject_user side-channel — the notification user message is delivered
      // atomically with the TurnID through the progress stream.
      if (p.phase === 'turn_started') {
        // ── Stale turn_started guard ──
        // SSE replay can deliver a stale turn_started (turnID=9) after the
        // store has already advanced to turnID=10. Without this guard, the
        // stale event resets finalizedRef=false, phaseDoneRef=false, and
        // store.lastTurnID=9 — corrupting the current turn's state and
        // potentially causing duplicate onAssistantComplete calls.
        if (p.turn_id && p.turn_id > 0 && store.lastTurnID > 0 && p.turn_id < store.lastTurnID) {
          console.error('[TURN_ID_INVARIANT_VIOLATION] Stale turn_started dropped', {
            prev: store.lastTurnID,
            stale: p.turn_id,
            chatID: p.chat_id,
          })
          return
        }
        // ── Duplicate turn_started for the CURRENT turn guard ──
        // A turn_started with turn_id === store.lastTurnID (and NOT a resume)
        // is a duplicate/spurious re-emission (SSE reconnect replay of the same
        // turn, a notification turn_started arriving twice, or the backend
        // re-sending it). The turn is ALREADY active — running the commit path
        // again clears the live row (lastIter=0) AND sets finalizedRef=true,
        // which then DROPS every subsequent iteration (the live turn vanishes
        // permanently — user report: "最新 turn 不断消失又出现"). Ignore it.
        // AskUser resume (trigger='resume') legitimately reuses the same TurnID
        // (continuation of the same turn) and must NOT be ignored.
        if (p.turn_id && p.turn_id > 0 && p.turn_id === store.lastTurnID && p.turn_start?.trigger !== 'resume') {
          console.warn('[TURN_STARTED_DUP] Duplicate turn_started for active turn ignored', {
            turnID: p.turn_id,
            chatID: p.chat_id,
            trigger: p.turn_start?.trigger,
          })
          return
        }
        // ── Consistency check: TurnID must be strictly monotonic ──
        // AskUser answer (trigger=resume) reuses the same TurnID as the original
        // turn — turnID == lastTurnID is expected and NOT a violation.
        if (store.lastTurnID > 0 && p.turn_id && p.turn_id > 0 && p.turn_id !== store.lastTurnID) {
          if (p.turn_id <= store.lastTurnID) {
            console.error('[TURN_ID_INVARIANT_VIOLATION] TurnID must be strictly increasing', {
              prev: store.lastTurnID,
              next: p.turn_id,
              delta: p.turn_id - store.lastTurnID,
              chatID: p.chat_id,
              trigger: p.turn_start?.trigger,
            })
          } else if (p.turn_id !== store.lastTurnID + 1) {
            console.warn('[TURN_ID_GAP] TurnID jumped — intermediate turn(s) may have been lost', {
              prev: store.lastTurnID,
              next: p.turn_id,
              gap: p.turn_id - store.lastTurnID - 1,
              chatID: p.chat_id,
            })
          }
        }
        // NOTE: store.lastTurnID is NOT updated here — it must remain the OLD
        // value (previous turn's ID) so that commitLiveProgressAndReset can use
        // it as a fallback when snap.turnID is 0 (cancelled turn with no
        // structured events). Updating it before the commit causes
        // snap.turnID || store.lastTurnID to resolve to the NEW turn's ID,
        // giving the committed assistant the wrong turnID.
        // store.lastTurnID is updated AFTER the commit, below.
        const ts = p.turn_start
        // For "resume" trigger (InjectInboundResume — NOT AskUser answer),
        // preserve iterationHistory — the answer is a CONTINUATION of the
        // same turn, not a new turn. Only clear streaming state so the new
        // iteration starts clean, but previous iterations survive.
        // AskUser answer now uses trigger="user" (new turn, new turnID) —
        // it goes through the else branch (commitLiveProgressAndReset).
        if (ts?.trigger === 'resume') {
          store.resetStreamingState()
          if (finalizedRef) finalizedRef.current = false
          // AskUser 续跑（同 turnID）：旧 finalized 状态作废 —— 重置
          // finalizedTurnID，放行同 turn 的后续 text（否则 1 <= 1 被误杀）。
          if (finalizedTurnIDRef) finalizedTurnIDRef.current = 0
        } else {
          // Capture whether the store has visible progress BEFORE the commit.
          // If it does, commitLiveProgressAndReset will call onAssistantComplete
          // → resetProgress → finalizedRef = true.
          const hadVisibleProgress = hasVisibleProgress(store.getSnapshot())
          // Commit any uncommitted live content from the previous turn, then
          // reset. Unconditional commit (the helper no-ops on an empty store):
          // a store with visible content is by definition un-finalized — the
          // text event (the authoritative finalizer) resets it on arrival. If
          // the text event was lost (SSE coalescing/disconnect), the live
          // content is the ONLY display of the old turn's reply; committing it
          // before the reset keeps it visible at the same position (no flicker,
          // no data loss) instead of vanishing in one frame.
          commitLiveProgressAndReset(store, completeRef?.current, p.turn_id)
          store.lastIter = 0
          // 旧 turn（lastTurnID，turn_started 尚未更新）的 live 已 commit（finalize）。
          // 记录 finalizedTurnID：旧 turn 的迟到 text/progress_structured 据此丢弃
          // （防重复），新 turn（turn_id 更大）放行。
          if (finalizedTurnIDRef && store.lastTurnID > 0) {
            finalizedTurnIDRef.current = store.lastTurnID
          }
          // turn_started 是权威的新 turn 边界：无条件重置 finalizedRef=false，
          // 允许新 turn 的所有事件（progress_structured/stream_content）正常处理。
          // 旧 turn 的迟到 text 由 MessageStore 的 slot 唯一性保证幂等覆盖
          // （同 turnID 覆盖，不产生第二行），不需要 finalizedRef 防重复。
          // ⚠️ 不能在这里设 finalizedRef=true：cancel 后 store 有 frozen live
          // （hadVisibleProgress=true），旧逻辑设 true 会让新 turn 的所有
          // progress_structured 被 finalized guard 丢弃 → live 永不渲染 →
          // 前端卡"思考中"（用户报告：cancel 后发新消息，SSE 已出现但不渲染）。
          if (finalizedRef) finalizedRef.current = false
          // Block initialProgress hydration from re-introducing old turn's
          // iterationHistory. reload()'s active_progress may still carry
          // the old turn's data (server hasn't cleaned up yet). Without
          // this flag, session(busy) resets finalizedRef=false, then
          // initialProgress hydration calls store.replace() with old
          // iterationHistory → cross-turn iteration leak.
          if (hadVisibleProgress) {
            if (turnCommittedRef) turnCommittedRef.current = true
          }
        }
        if (ts && (ts.trigger === 'notification' || ts.trigger === 'resume') && ts.content && p.turn_id) {
          injectRef?.current?.(ts.content, p.turn_id, ts.trigger === 'notification')
        }
        // Signal the frontend to enter busy mode. This is critical for
        // notification/resume turns where session(busy) may be lost or delayed
        // (SSE coalescing) — without this, the input box stays in send mode.
        if (p.turn_id) {
          turnStartedRef?.current?.(p.turn_id, ts?.trigger ?? 'user')
          store.lastTurnID = p.turn_id
          // MessageStore：新 turn 开始（AskUser resume 保留 iterations）
          messageStore?.beginTurn(p.turn_id, { resume: ts?.trigger === 'resume' })
        }
        // Reset phaseDone guard for the new turn. finalizedRef was already
        // handled above (kept true if commit happened, reset to false otherwise).
        // For the resume case, finalizedRef is reset to false (the AskUser
        // answer is a continuation — text event is expected).
        if (phaseDoneRef) phaseDoneRef.current = false
        return
      }
      if (p.phase === 'done') {
        // PhaseDone: the turn is over. Mark it so session(idle) doesn't
        // defensively finalize — the text event (normal or cancel ack) is
        // the authoritative finalizer.
        //
        // Stale PhaseDone guard: when switching to a busy session, the store
        // is hydrated from initialProgress (e.g. seq=8 with activeTools).
        // SSE replay may then deliver a stale PhaseDone (seq=5) from the
        // PREVIOUS turn. Without this guard, phaseDoneRef is set to true and
        // agent-idle is dispatched — clearing the busy state and making the
        // running tool disappear. Skip stale PhaseDone entirely (only
        // preserve todos if present).
        const seq = typeof p.seq === 'number' ? p.seq : undefined
        if (seq !== undefined && seq > 0 && seq <= store.getSnapshot().eventSeq) {
          // Stale PhaseDone — preserve todos only, skip everything else.
          if (Array.isArray(p.todos) && p.todos.length > 0) {
            store.setStructuredTools({ eventSeq: seq, todos: p.todos.map((t) => ({
              id: typeof t.id === 'number' ? t.id : 0,
              text: typeof t.text === 'string' ? t.text : '',
              done: Boolean(t.done),
            })) })
          }
          return
        }
        if (phaseDoneRef) phaseDoneRef.current = true
        // Dispatch agent-idle so the sidebar clears the busy indicator.
        window.dispatchEvent(new CustomEvent('agent-idle', {
          detail: { chatID: p.chat_id ?? undefined, channel: undefined },
        }))
        // PhaseDone: the turn is over. Stop streaming animations AND clear the
        // busy fallback signal (progressSnapshot.streaming) — without this, the
        // AgentPanel busy fallback (streaming && phase !== 'done') keeps showing
        // "思考中…" until the text event arrives. DO NOT reset the store: tools
        // and iterations stay visible until the text event commits atomically.
        store.stopStreaming()
        // ── Final-reply loss guard (root cause of "某个迭代结束 agent turn 消失了") ──
        // The complete reply travels ONLY in the text event (authoritative
        // finalizer). On a stateless SSE stream that event can be dropped
        // (sendCh coalescing / reconnect gap). When it is lost, the store keeps
        // only the iteration records — streamContent was cleared at the last
        // iteration boundary and no new text arrived — so liveMessage stays
        // non-null (RENDER_LOSS_ROWS stays silent!) but renders EMPTY: the
        // user sees the turn's reply "vanish". Detect it here: visible progress
        // with NO accumulated reply text → the text event was likely lost →
        // reload from DB (authoritative complete reply). The committed reply
        // then merges with the live row via buildMessageRows' same-turn merge.
        const doneSnap = store.getSnapshot()
        if (hasVisibleProgress(doneSnap) && !doneSnap.streamContent && !doneSnap.content) {
          iterationGapRef?.current?.()
        }
        // Update todos if the PhaseDone event carries them. Do NOT clear
        // tools here — clearing causes a 4-5s gap where tools disappear
        // between PhaseDone and the text event. The text event (or cancel
        // ack) calls store.reset() which clears everything atomically.
        let doneTodos: TodoItem[] | undefined
        if (Array.isArray(p.todos)) {
          // Unconditionally apply todos, INCLUDING an empty array. The server
          // sends todos: [] when the list was cleared (todo_write([]) or
          // turn-end cleanupTodos) — the frontend must learn the list is now
          // empty, otherwise stale items survive until the next event/refresh.
          doneTodos = p.todos.map((t) => ({
            id: typeof t.id === 'number' ? t.id : 0,
            text: typeof t.text === 'string' ? t.text : '',
            done: Boolean(t.done),
          }))
        }
        if (doneTodos !== undefined) {
          store.setStructuredTools({ eventSeq: typeof p.seq === 'number' ? p.seq : undefined, todos: doneTodos })
        }
        return
      }
      // Do NOT reset finalizedRef here. A non-done structured event may be a
      // stale replay from a cancelled/ended turn (SSE reconnect recovery). 
      // Resetting would allow a subsequent session(idle) to re-finalize and
      // append stale content as a duplicate message. finalizedRef is reset
      // only on stream_content (genuine new LLM output) or session(busy) on a
      // clean store (genuine new turn).
      
      // Guard: if the turn is already finalized (text event or cancel ack
      // arrived), discard late progress_structured events. They would write
      // new state into the already-reset store, making liveMessage reappear
      // ("思考中…" spinner below the committed reply). This mirrors master's
      // turnDoneRef guard.
      // 只丢弃旧 turn（turn_id <= finalizedTurnID）的迟到事件；新 turn（turn_id
      // 更大）放行 —— 否则 cancel 后新 turn 的 progress_structured 被 finalized
      // 状态阻塞，live 永不渲染（用户报告：cancel 后发新消息卡"思考中"）。
      if (!p.history_compacted && p.phase !== 'done') {
        const evTurnID = typeof p.turn_id === 'number' && p.turn_id > 0 ? p.turn_id : 0
        const finalizedTurn = finalizedTurnIDRef?.current ?? 0
        if (finalizedTurn > 0 && evTurnID > 0 && evTurnID <= finalizedTurn) {
          return // 旧 turn 的迟到 progress_structured —— 丢弃
        }
        if (finalizedRef?.current && evTurnID === 0) {
          return // turn_id 缺失 + finalized（兼容旧逻辑：无 turn_id 事件）
        }
      }

      // New turn's first structured event — clear turnCommittedRef.
      if (turnCommittedRef) turnCommittedRef.current = false

      if (p.history_compacted) {
        store.reset()
        compactedRef.current?.()
        return
      }

      // Normalize tools from the structured event
      const active = normalizeWebTools(p.active_tools)
      const completed = normalizeWebTools(p.completed_tools)
      const iteration = typeof p.iteration === 'number' ? p.iteration : undefined
      const phase = typeof p.phase === 'string' ? p.phase : undefined
      const reasoning = typeof p.reasoning === 'string' ? p.reasoning : undefined
      const content = typeof p.content === 'string' ? p.content : undefined

      // Iteration history (live, from the structured event)
      let iterHistory: WebIteration[] | undefined
      if (Array.isArray(p.iteration_history)) {
        iterHistory = p.iteration_history
          .map(normalizeWebIteration)
          .filter(Boolean) as WebIteration[]
      }

      // TODO list (from TodoWrite tool)
      // p.todos is the raw array from the server. We must distinguish:
      //  - Array with items → map to TodoItem[]
      //  - Empty array [] → explicitly cleared by todo_write([]) → pass []
      //    so setStructuredTools updates draft.todos = []
      //  - undefined/null → not present in event → carry-forward (undefined)
      let todos: TodoItem[] | undefined
      if (Array.isArray(p.todos)) {
        todos = p.todos.map((t) => ({
          id: typeof t.id === 'number' ? t.id : 0,
          text: typeof t.text === 'string' ? t.text : '',
          done: Boolean(t.done),
        }))
      }
      const subAgents = Array.isArray(p.sub_agents)
        ? normalizeWebSubAgents(p.sub_agents as unknown[])
        : undefined

      // Token usage (from protocol.TokenUsage, carried forward when absent)
      let tokenUsage: TokenUsageInfo | undefined
      const rawTU = p.token_usage as Record<string, unknown> | undefined
      if (rawTU && typeof rawTU === 'object') {
        tokenUsage = {
          promptTokens: typeof rawTU.prompt_tokens === 'number' ? rawTU.prompt_tokens : 0,
          completionTokens: typeof rawTU.completion_tokens === 'number' ? rawTU.completion_tokens : 0,
          totalTokens: typeof rawTU.total_tokens === 'number' ? rawTU.total_tokens : 0,
        }
      }

      // Stream fields may also arrive inside structured events (the Web
      // channel forwards all ProgressEvents as type=progress_structured,
      // including stream callbacks' reasoning_stream_content / stream_content).
      // Handle them here so reasoning/content stay live — and BEFORE the seq
      // check (stream deltas are cumulative, not ordered by seq).
      //
      // DUPLICATE PREVENTION: when a structured event ALSO carries `content`
      // (structured snapshot) the same text would end up in BOTH
      // `content` (via setStructuredTools) AND `streamContent` (via
      // appendStreamContent) → TurnBody renders iterations.content + LiveIteration
      // renders streamContent → same text twice. When `content` is present,
      // RESET streamContent to it (replace, not append) so there's a single
      // source of truth. Same for reasoning.
      if (p.reasoning_stream_content) {
        if (p.reasoning !== undefined) {
          store.setReasoningContent(p.reasoning_stream_content)
        } else {
          store.appendReasoningContent(p.reasoning_stream_content)
        }
      }
      if (p.stream_content) {
        if (p.content !== undefined) {
          store.setStreamContent(String(p.stream_content))
        } else {
          store.appendStreamContent(String(p.stream_content))
        }
      }
      if (p.genui_content) store.setGenUIContent(p.genui_content)
      if (p.streaming_tools) {
        store.setStreamOnlyFields({
          streamingTools: normalizeWebTools(p.streaming_tools as unknown[]),
        })
      }

      // ── Consistency check: iteration must advance by exactly 1 within a turn ──
      // Iterations are 1-based: 0 = uninitialized, 1 = first iteration.
      // STRUCTURED events only (phase set). phase:undefined events are stream
      // deltas (stream_content/reasoning forwarded by the Web channel) carrying
      // the backend's CURRENT iteration — legitimately 1 while reasoning
      // streams before the loop starts, or LAGGING a prior iteration's deltas
      // (iter 2 stream text arriving after the snapshot advanced to 4).
      // Comparing them against lastIter false-alarms ITER_ID_INVARIANT_VIOLATION
      // (prev=4 next=2) on every such event and carries no iteration-order
      // semantics — skip the check entirely.
      if (phase !== undefined && iteration !== undefined && iteration >= 1) {
        if (store.lastIter >= 1 && iteration < store.lastIter) {
          console.error('[ITER_ID_INVARIANT_VIOLATION] iteration went backwards', {
            prev: store.lastIter,
            next: iteration,
            turnID: store.lastTurnID,
            chatID: p.chat_id,
            phase,
          })
        } else if (store.lastIter >= 1 && iteration !== store.lastIter + 1 && iteration > store.lastIter) {
          console.warn('[ITER_ID_GAP] iteration jumped — intermediate iteration(s) may have been lost', {
            prev: store.lastIter,
            next: iteration,
            gap: iteration - store.lastIter - 1,
            turnID: store.lastTurnID,
            chatID: p.chat_id,
          })
        }
        if (iteration > store.lastIter) {
          store.lastIter = iteration
        }
      }
      // Track TurnID from structured events (covers SSE reconnect recovery via
      // restoreActiveProgress, which dispatches a snapshot with TurnID but not
      // turn_started phase). Without this, lastTurnID stays stale after reconnect.
      if (p.turn_id && p.turn_id > 0 && p.turn_id !== store.lastTurnID) {
        // Fallback: turn_started was lost (SSE drop). Clear stale data so the
        // new turn's iterations don't append to the old turn's iterationHistory.
        // (turn_started normally handles this, but SSE can coalesce/drop it.)
        // Commit-then-reset: the old turn's text event may ALSO have been lost,
        // so the live content is the only display of the old reply — wiping it
        // directly makes it vanish in one frame (flicker). Hand it to the
        // committed message list first, then reset cleanly.
        if (store.lastTurnID > 0 && hasVisibleProgress(store.getSnapshot())) {
          commitLiveProgressAndReset(store, completeRef?.current)
        }
        store.lastTurnID = p.turn_id
        // NO optimistic user messages: user rows come from backend user_echo
        // with authoritative turn_id. turn_started only needs to notify the
        // panel (typing/active-turn state) — nothing to bind.
        if (turnStartedRef?.current) {
          turnStartedRef.current(p.turn_id, 'user')
        }
      }

      // Web channel forwards stream_content events as progress_structured
      // (type is normalized to progress_structured on the wire) — handle the
      // delta/checkpoint stream fields here too, mirroring the stream_content
      // branch. Without this, Web clients NEVER render streaming content
      // (only tool events; content/reasoning appear only as structured
      // snapshots after the tool completes — user report: "前端不再渲染
      // reason 和 content 的 stream 事件").
      if (p.stream_delta) {
        store.appendStreamContent(String(p.stream_delta))
      } else if (p.stream_content) {
        store.setStreamContent(String(p.stream_content))
      }
      if (p.reasoning_stream_delta) {
        store.appendReasoningContent(String(p.reasoning_stream_delta))
      } else if (p.reasoning_stream_content) {
        store.setReasoningContent(String(p.reasoning_stream_content))
      }

      // Apply structured event with carry-forward (stream-only fields preserved)
      store.setStructuredTools({
        eventSeq: typeof p.seq === 'number' ? p.seq : undefined,
        phase,
        iteration,
        content,
        activeTools: active.length ? active : undefined,
        completedTools: completed.length ? completed : undefined,
        reasoning,
        iterationHistory: iterHistory,
        todos,
        subAgents,
        tokenUsage,
        turnID: typeof p.turn_id === 'number' && p.turn_id > 0 ? p.turn_id : undefined,
      })
      // MessageStore live 同步（方案 A Step 3）
      writeLiveToMessageStore(messageStore, store, p)
      // ── Iteration-id gap → REAL incremental data loss → reload ──
      // iterationHistory is an incremental delta feed (0-1 entries per push):
      // an internal id jump (1→3 missing 2) means an iteration's delta was
      // dropped on the wire and NO later SSE snapshot can backfill it —
      // snapshots carry only NEW iterations. continuousIterations hides the
      // broken tail at RENDER time only; the DB is authoritative. One-shot:
      // fire reload once per broken history, re-arm when the history becomes
      // contiguous again (the reload's active_progress backfill repairs it).
      // hasIterationGapNow() reads the SYNCHRONOUS current (getSnapshot is
      // RAF-throttled and would lag one event).
      if (store.hasIterationGapNow()) {
        if (iterationGapFiredRef && !iterationGapFiredRef.current) {
          iterationGapFiredRef.current = true
          iterationGapRef?.current?.()
        }
      } else if (iterationGapFiredRef) {
        iterationGapFiredRef.current = false
      }
      return
    }

    case 'text': {
      if (msg.session_reset || msg.metadata?.session_reset === 'true') {
        if (finalizedRef) finalizedRef.current = true
        if (finalizedTurnIDRef) finalizedTurnIDRef.current = store.lastTurnID
        store.reset()
        resetRef.current?.()
        return
      }
      // Cancel ack: the turn was cancelled. Keep the live progress as-is —
      // whatever the user sees at cancel time stays. Do NOT reset the store
      // or commit a new message. The live message (with active tools, stream
      // content) remains visible. The persisted [interrupted] message (with
      // Detail + user_cancelled) will be fetched on the next history reload
      // (session switch or page refresh).
      if (msg.cancelled) {
        if (finalizedRef) finalizedRef.current = true
        if (finalizedTurnIDRef) finalizedTurnIDRef.current = msg.turn_id ?? store.lastTurnID
        if (phaseDoneRef) phaseDoneRef.current = true
        // Freeze: mark all in-progress tools as error, stop streaming/reasoning animations.
        // Do NOT reset the store — frozen content stays visible until the next turn.
        store.freeze()
        cancelCompleteRef?.current?.()
        // Do NOT dispatch agent-idle here — it would trigger the session(idle)
        // handler's defensive finalize, which calls completeRef + store.reset(),
        // wiping the frozen content. The backend's session(idle) SSE event
        // will arrive separately and be ignored (finalizedRef=true + phase=frozen).
        return
      }
      // Final assistant message: commit then clear the live stream.
      // Guard against duplicate onAssistantComplete within the same turn
      // (e.g. text + session(idle) arriving before RAF flushes).
      // 按 turnID 区分：旧 turn（turn_id <= finalizedTurnID）的迟到 text 丢弃
      // （防重复）；新 turn 的 text 放行（turn_started 已重置 finalizedRef）。
      const textTurnID = typeof msg.turn_id === 'number' && msg.turn_id > 0 ? msg.turn_id : 0
      const finalizedTurnID = finalizedTurnIDRef?.current ?? 0
      if (textTurnID > 0 && finalizedTurnID > 0 && textTurnID <= finalizedTurnID) return
      if (finalizedRef?.current && textTurnID === 0) return
      if (finalizedRef) finalizedRef.current = true
      if (finalizedTurnIDRef) finalizedTurnIDRef.current = msg.turn_id ?? store.lastTurnID
      const finalText = msg.content ?? ''
      const parsedIterations = parseWebIterations(msg.progress_history)
      const snap = store.getSnapshot()
      // Merge live + server iterations: the live snapshot (from SSE deltas)
      // may have GAPS if SSE dropped/coalesced some delta events. The server's
      // parsedIterations (from progress_history in the text event) has ALL
      // iterations. Merging them fills any gaps in the live snapshot while
      // preserving live-only data (e.g. streamed reasoning that structured
      // events didn't carry). Without this merge, continuousIterations would
      // truncate at the gap, hiding hundreds of iterations from the user.
      const iterations = mergeIterations(snap.iterationHistory, parsedIterations)
      // Merge live reasoningStreamContent into the last iteration's reasoning.
      // The streamed reasoning (from reasoning_stream_content events) is in
      // snap.reasoningStreamContent, but the iteration snapshot's reasoning
      // field may be empty (structured events don't always carry reasoning).
      // After store.reset(), reasoningStreamContent is gone — if we don't
      // merge it here, the committed message loses all reasoning.
      const liveReasoning = snap.reasoningStreamContent || snap.lastReasoning || ''
      let mergedIterations = iterations
      if (liveReasoning) {
        if (mergedIterations.length === 0) {
          // No iterations at all — create a synthetic one to carry the reasoning.
          mergedIterations = [{ iteration: 1, thinking: '', reasoning: liveReasoning, tools: [], toolCount: 0 }]
        } else {
          const lastIter = mergedIterations[mergedIterations.length - 1]
          // Always use live streamed reasoning if it's longer than what the
          // structured event provided (or if the iteration's reasoning is empty).
          if (liveReasoning.length > (lastIter.reasoning || '').length) {
            mergedIterations = mergedIterations.map((it, i) =>
              i === mergedIterations.length - 1 ? { ...it, reasoning: liveReasoning } : it
            )
          }
        }
      }
      completeRef.current?.(finalText, mergedIterations, msg.seq, msg.turn_id)
      // onAssistantComplete calls store.reset() synchronously inside flushSync.
      // Fallback: if onAssistantComplete did not reset (e.g., not set), reset here.
      // The reset is idempotent — if onAssistantComplete already cleared the
      // store, hasVisibleProgress returns false and no double-reset occurs.
      if (hasVisibleProgress(store.getSnapshot())) store.reset()
      // Dispatch agent-idle so useSessionStore clears the busy state.
      // PhaseDone normally handles this, but bang commands and slash commands
      // bypass Run() and never send PhaseDone.
      window.dispatchEvent(new CustomEvent('agent-idle', {
        detail: { chatID: msg.chat_id ?? undefined, channel: msg.channel ?? undefined },
      }))
      return
    }

    case 'genui': {
      // Final complete HTML from display_html tool (non-streaming, complete code)
      if (msg.content) store.setGenUIContent(msg.content)
      return
    }

    case 'session': {
      const action = msg.session?.action

      if (action === 'busy') {
        const snap = store.getSnapshot()
        // A new busy event means the turn is (re)starting. Reset the finalize
        // guards so stream_content events are NOT blocked. Without this, after
        // a PhaseDone (phaseDoneRef=true) or cancel (finalizedRef=true), all
        // subsequent stream_content events are silently dropped — the UI
        // freezes ("busy 时不更新"). With the new backend, turn_started handles
        // this; with the old backend (no turn_started), session(busy) is the
        // only reset point.
        if (finalizedRef) finalizedRef.current = false
        if (phaseDoneRef) phaseDoneRef.current = false
        // If we're already mid-stream, don't disrupt — a synthetic busy from
        // recovery must not wipe cumulative streamContent (causes typer restart).
        if (snap.streamContent || snap.reasoningStreamContent) {
          return
        }
        // If we have active tools (in-flight tool execution), don't disrupt —
        // a synthetic busy from SSE reconnect (restoreActiveProgress) must not
        // wipe activeTools via resetStreamingState(). This happens on page
        // refresh when the agent is mid-tool-execution: history hydrates
        // activeTools, then session(busy) arrives and would clear them.
        // BUT: clear stale streamingTools (generating tools from the previous
        // turn) to prevent iteration duplication — the generating tool would
        // render alongside the same iteration's "done" entry in iterationHistory.
        if (snap.activeTools.length > 0) {
          store.setStreamOnlyFields({ streamingTools: [] })
          return
        }
        // On a clean store (no visible progress), this is a genuine new turn.
        // Reset the finalize guard so a subsequent text event can complete.
        // This is safe because a clean store means no in-flight content to
        // protect — a recovery busy on a clean store is indistinguishable
        // from a genuine new turn, and both should allow finalization.
        if (!hasVisibleProgress(snap)) {
          if (finalizedRef) finalizedRef.current = false
          return
        }
        // Dirty store with no stream content and no active tools — clear
        // stale tool state (e.g. completed tools from a previous turn).
        store.resetStreamingState()
        return
      }

      // HistoryCompacted: reset store and trigger reload
      if (action === 'HistoryCompacted') {
        store.reset()
        compactedRef.current?.()
        return
      }

      // On idle, the turn is OVER. Clear all progress state.
      // If finalizedRef=true, onAssistantComplete already committed the content
      // via flushSync (appendAssistant + resetProgress). The store's
      // iterationHistory is now redundant — the committed message has its own
      // copy. A full reset() clears activeTools/completedTools/streamingTools
      // and iterationHistory, making liveMessage null (clean transition to
      // the committed row).
      // If finalizedRef=false AND phaseDoneRef=false (defensive finalize — no
      // text event arrived), commit the accumulated content first, then reset.
      // If phaseDoneRef=true, the turn ended via PhaseDone — the text event
      // (normal or cancel ack) is the authoritative finalizer. Skip defensive
      // finalize to avoid committing content the backend already persisted.
      if (action === 'idle') {
        if (finalizedRef?.current || phaseDoneRef?.current) {
          // If the store is frozen (cancel), DON'T reset — frozen content
          // must stay visible. Only reset for normal finalize (text event
          // committed the message, store content is now redundant).
          if (store.getSnapshot().phase === 'frozen') {
            if (phaseDoneRef) phaseDoneRef.current = false
            return
          }
          // If PhaseDone fired but no text event yet (cancel ack pending),
          // DON'T reset — the cancel ack needs store.streamContent to commit.
          // Only reset if finalizedRef=true (text event already committed).
          if (phaseDoneRef?.current && !finalizedRef?.current) {
            // PhaseDone without text — wait for text/cancel ack
            return
          }
          if (hasVisibleProgress(store.getSnapshot())) {
            store.reset()
          }
          if (phaseDoneRef) phaseDoneRef.current = false
          return
        }
        const snap = store.getSnapshot()
        if (hasVisibleProgress(snap)) {
          if (finalizedRef) finalizedRef.current = true
          const text = snap.streamContent
          const liveReasoning = snap.reasoningStreamContent || snap.lastReasoning || ''
          let iters = snap.iterationHistory
          if (liveReasoning) {
            if (iters.length === 0) {
              iters = [{ iteration: 1, thinking: '', reasoning: liveReasoning, tools: [], toolCount: 0 }]
            } else if (liveReasoning.length > (iters[iters.length - 1].reasoning || '').length) {
              iters = iters.map((it, i) =>
                i === iters.length - 1 ? { ...it, reasoning: liveReasoning } : it
              )
            }
          }
          completeRef.current?.(text, iters, msg.seq, msg.turn_id)
          store.reset()
        }
      }
      return
    }

    default:
      return
  }
}

function isTerminalProgressMessage(msg: WSMessage): boolean {
  if (msg.type === 'text') return true
  if (msg.progress?.phase === 'done') return true
  if (msg.type !== 'session') return false
  return ['busy', 'idle', 'deleted', 'HistoryCompacted'].includes(msg.session?.action ?? '')
}
