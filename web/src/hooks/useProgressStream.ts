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

import { ProgressStore, normalizeWebSubAgents, normalizeWebTools } from '@/components/agent/progressStore'
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
import type { WSMessage } from '@/types/shared'
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
  onAssistantComplete?: (finalText: string, iterations: WebIteration[], eventSeq?: number, turnID?: number) => void
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
}

export interface UseProgressStreamResult {
  /** Throttled immutable progress snapshot. */
  progressSnapshot: ProgressSnapshot
  /** Transient streaming assistant message, or null when idle. */
  liveMessage: ChatMessage | null
  /** True while there is accumulated streaming content. */
  isStreaming: boolean
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
  initialProgress,
  ws,
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

  // Guard against multiple onAssistantComplete calls per turn.
  // Reset to false when new streaming begins (stream_content arrives).
  const finalizedRef = useRef(false)
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
      prevProgressCacheKeyRef.current = progressCacheKey
      store.fullReset()
      // Restore todos from progressSnapshotCache — switchSession writes
      // the /switch response todos here so they appear immediately,
      // before /api/history's active_progress arrives (which may return
      // null if the backend's snapshot was already cleaned up).
      if (progressCacheKey) {
        const cached = progressSnapshotCache.get(progressCacheKey)
        if (cached?.todos && cached.todos.length > 0) {
          store.replace({ todos: cached.todos.map((t) => ({
            id: typeof t.id === 'number' ? t.id : 0,
            text: typeof t.text === 'string' ? t.text : '',
            done: Boolean(t.done),
          })) })
        }
      }
    } else {
      store.reset()
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
    if (!initialProgress || !initialProgress.phase) {
      if (hasVisibleProgress(store.getSnapshot())) store.reset()
      return
    }
    if (initialProgress.phase === 'done') {
      // Turn ended. Clear progress but restore todos from server so they
      // survive session switch (todos persist across turns in the todoManager).
      if (progressCacheKey) clearProgressSnapshot(progressCacheKey)
      finalizedRef.current = false
      if (hasVisibleProgress(store.getSnapshot())) store.reset()
      const todos = (initialProgress.todos ?? []) as TodoItem[]
      // Only replace todos if the server returned non-empty todos.
      // If server returned empty [], preserve existing todos (from /switch
      // response cache) — the server may not have todos in active_progress
      // when phase='done' (snapshot already cleaned up).
      if (todos.length > 0) {
        store.replace({ todos })
      }
      return
    }
    // Don't re-hydrate after finalization — the turn is over, and the
    // server's active_progress may be stale (not yet cleaned up). Only
    // hydrate if we haven't started receiving live events for this turn
    // (finalizedRef is false AND store is empty = fresh load/reconnect).
    if (finalizedRef.current) return
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
    if (live.phase) {
      store.replace(live)
      // Ensure turnID is tracked for same-turn dedup (MessageList uses
      // liveMessage.turnID to match committed history messages).
      if (live.turnID > 0) {
        store.lastTurnID = live.turnID
      }
    }
  }, [store, initialProgress, disabled, progressCacheKey])

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
      handleProgressMessage(msg, store, completeRef, compactedRef, resetRef, finalizedRef, phaseDoneRef, injectRef, turnStartedRef, cancelCompleteRef)
    })
    return offMessage
  }, [store, disabled, channel])

  // Derive a transient streaming message from the snapshot. Only the snapshot's
  // streamContent/streaming drives this, so it updates at frame rate (not per token).
  const liveMessage = useMemo<ChatMessage | null>(() => {
    const snap = progressSnapshot
    if (!hasVisibleProgress(snap)) return null
    if (snap.phase === 'done') return null
    return {
      id: `live-${chatID ?? 'unknown'}`,
      role: 'assistant',
      content: snap.streamContent || snap.content || '',
      iterations: snap.iterationHistory,
      timestamp: new Date().toISOString(),
      isPartial: true,
      turnID: snap.turnID || store.lastTurnID,
    }
  }, [progressSnapshot, chatID])

  return {
    progressSnapshot: progressSnapshot ?? EMPTY_PROGRESS_SNAPSHOT,
    liveMessage,
    isStreaming: hasVisibleProgress(progressSnapshot),
    resetProgress: () => {
      finalizedRef.current = true
      phaseDoneRef.current = false
      store.reset()
    },
  }
}

function hasVisibleProgress(snap: ProgressSnapshot): boolean {
  return Boolean(
    snap.streamContent ||
      snap.content ||
      snap.reasoningStreamContent ||
      snap.activeTools.length ||
      snap.completedTools.length ||
      snap.streamingTools.length ||
      snap.iterationHistory.length ||
      snap.lastReasoning ||
      snap.subAgents.length,
  )
}

/** Dispatch one WSMessage into the progress store. Shared with history hydration. */
function handleProgressMessage(
  msg: WSMessage,
  store: ProgressStore,
  completeRef: React.MutableRefObject<UseProgressStreamOptions['onAssistantComplete']>,
  compactedRef: React.MutableRefObject<UseProgressStreamOptions['onHistoryCompacted']>,
  resetRef: React.MutableRefObject<UseProgressStreamOptions['onSessionReset']>,
  finalizedRef?: React.MutableRefObject<boolean>,
  phaseDoneRef?: React.MutableRefObject<boolean>,
  injectRef?: React.MutableRefObject<UseProgressStreamOptions['onInjectUserMessage']>,
  turnStartedRef?: React.MutableRefObject<UseProgressStreamOptions['onTurnStarted']>,
  cancelCompleteRef?: React.MutableRefObject<UseProgressStreamOptions['onCancelComplete']>,
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

      // stream_content carries content deltas in progress.stream_content /
      // progress.reasoning_stream_content (channel/web/web.go SendStreamContent).
      // Also carries streaming_tools (generating status, for tool name detection).
      const p = msg.progress
      if (!p) return

      // Set cumulative text (stream-only, does not replace the snapshot)
      if (p.stream_content) store.appendStreamContent(String(p.stream_content))
      if (p.reasoning_stream_content) {
        store.appendReasoningContent(p.reasoning_stream_content)
      }
      // GenUI streaming HTML (from display_html tool arguments)
      if (p.genui_content) store.setGenUIContent(p.genui_content)
      // Streaming tools (generating status) — patch only, no snapshot replace
      if (p.streaming_tools) {
        store.setStreamOnlyFields({
          streamingTools: normalizeWebTools(p.streaming_tools as unknown[]),
        })
      }
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
        store.lastTurnID = p.turn_id ?? 0
        const ts = p.turn_start
        // For "resume" trigger (AskUser answer), preserve iterationHistory —
        // the answer is a CONTINUATION of the same turn, not a new turn.
        // Only clear streaming state (streamContent, activeTools, etc.) so
        // the new iteration starts clean, but previous iterations survive.
        // For "user"/"notification" triggers, full reset (new turn).
        if (ts?.trigger === 'resume') {
          store.resetStreamingState()
        } else {
          store.reset()
          store.lastIter = 0
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
        }
        // Reset finalize guards for the new turn.
        if (finalizedRef) finalizedRef.current = false
        if (phaseDoneRef) phaseDoneRef.current = false
        return
      }
      if (p.phase === 'done') {
        // PhaseDone: the turn is over. Mark it so session(idle) doesn't
        // defensively finalize — the text event (normal or cancel ack) is
        // the authoritative finalizer.
        if (phaseDoneRef) phaseDoneRef.current = true
        // Dispatch agent-idle so the sidebar clears the busy indicator.
        window.dispatchEvent(new CustomEvent('agent-idle', {
          detail: { chatID: p.chat_id ?? undefined, channel: undefined },
        }))
        // Update todos if the PhaseDone event carries them. Do NOT clear
        // tools here — clearing causes a 4-5s gap where tools disappear
        // between PhaseDone and the text event. The text event (or cancel
        // ack) calls store.reset() which clears everything atomically.
        let doneTodos: TodoItem[] | undefined
        if (Array.isArray(p.todos) && p.todos.length > 0) {
          doneTodos = p.todos.map((t) => ({
            id: typeof t.id === 'number' ? t.id : 0,
            text: typeof t.text === 'string' ? t.text : '',
            done: Boolean(t.done),
          }))
        }
        if (doneTodos) {
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

      // ── Consistency check: iteration must advance by exactly 1 within a turn ──
      // Iterations are 1-based: 0 = uninitialized, 1 = first iteration.
      if (iteration !== undefined && iteration >= 1) {
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
        if (store.lastTurnID > 0 && hasVisibleProgress(store.getSnapshot())) {
          store.reset()
        }
        store.lastTurnID = p.turn_id
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
      return
    }

    case 'text': {
      if (msg.session_reset || msg.metadata?.session_reset === 'true') {
        if (finalizedRef) finalizedRef.current = true
        store.reset()
        resetRef.current?.()
        return
      }
      // Cancel ack: the turn was cancelled. The live store already has the
      // rendered content + iterations (built incrementally via SSE). We do
      // NOT reset the store or fetch server data — the user already sees the
      // content, we just commit it as a regular message + append
      // user_cancelled so the iteration is preserved as-is.
      //
      // PhaseDone may have fired before this (clearing activeTools but the
      // text/cancel ack carries progress_history with the full iteration
      // history including user_cancelled). We use the server's
      // progress_history as the source — it's authoritative and includes
      // user_cancelled. If the live store still has data (PhaseDone didn't
      // fire), we merge: server iterations + any live-only iterations.
      if (msg.cancelled) {
        if (finalizedRef) finalizedRef.current = true
        if (phaseDoneRef) phaseDoneRef.current = false
        // Cancel: commit the live content as a regular message (same as
        // normal text event), then reset the store. This avoids the
        // PhaseDone → liveMessage=null gap (iterations vanish between
        // PhaseDone and cancel ack). The committed message carries the
        // iterations from progress_history, so they survive permanently.
        //
        // We DON'T trigger reload — the message is committed locally,
        // not fetched from server. The next reload (session switch) will
        // fetch the same data from /api/history (cancelMsg has Detail).
        const snap = store.getSnapshot()
        const liveIters = snap.iterationHistory
        const parsedIterations = parseWebIterations(msg.progress_history)
        const serverIterNums = new Set(parsedIterations.map((i) => i.iteration))
        const liveOnly = liveIters.filter((i) => !serverIterNums.has(i.iteration))
        const iters = [...parsedIterations, ...liveOnly]
        // Use streamContent as final text (the partially-streamed response)
        const text = snap.streamContent || snap.content || ''
        // Only commit if there's something to show (content or iterations)
        if (text || iters.length > 0) {
          completeRef.current?.(text, iters, msg.seq, msg.turn_id)
        }
        // onAssistantComplete calls store.reset() inside flushSync.
        if (hasVisibleProgress(store.getSnapshot())) store.reset()
        cancelCompleteRef?.current?.()
        // Dispatch agent-idle so useSessionStore clears the busy state even
        // if the session(idle) SSE event was dropped (sendCh full / network).
        window.dispatchEvent(new CustomEvent('agent-idle', {
          detail: { chatID: msg.chat_id ?? undefined, channel: msg.channel ?? undefined },
        }))
        return
      }
      // Final assistant message: commit then clear the live stream.
      // Guard against duplicate onAssistantComplete within the same turn
      // (e.g. text + session(idle) arriving before RAF flushes).
      // Cross-reconnect replay is handled by dedupMessages in appendAssistant.
      if (finalizedRef?.current) return
      if (finalizedRef) finalizedRef.current = true
      const finalText = msg.content ?? ''
      const parsedIterations = parseWebIterations(msg.progress_history)
      const snap = store.getSnapshot()
      // Prefer the live snapshot's iterationHistory — it was built incrementally
      // via SSE and already contains all completed iterations. Using the
      // server's parsedIterations instead would replace the data source, causing
      // all iterations to re-render (tool labels/status may differ in format).
      // Only fall back to parsedIterations when the snapshot has no iterations
      // (e.g. reconnect where no SSE events were received).
      const iterations = snap.iterationHistory.length > 0 ? snap.iterationHistory : parsedIterations
      completeRef.current?.(finalText, iterations, msg.seq, msg.turn_id)
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
          const iters = snap.iterationHistory
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
