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
  sessionCacheKey,
} from '@/lib/webCache'

interface UseProgressStreamOptions {
  /** Chat ID this stream tracks (events for other chats are ignored). */
  chatID: string | null
  /** Channel this stream tracks. Progress events may qualify chat_id as channel:chatID. */
  channel?: string
  /** Called with the finalized assistant text when a `text` event arrives. */
  onAssistantComplete?: (finalText: string, iterations: WebIteration[], eventSeq?: number, turnID?: number) => void
  /** Called when a bg notification / cron triggers a new turn — displays the injected user message. */
  onInjectUserMessage?: (content: string, turnID: number, isNotification: boolean) => void
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
  onHistoryCompacted,
  onInjectUserMessage,
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
  const compactedRef = useRef(onHistoryCompacted)
  compactedRef.current = onHistoryCompacted
  const resetRef = useRef(onSessionReset)
  resetRef.current = onSessionReset
  const injectRef = useRef(onInjectUserMessage)
  injectRef.current = onInjectUserMessage

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
    } else {
      store.reset()
    }
    if (disabled) {
      return
    }
    // No cache restore — history's active_progress is the single source.
    // The server returns the complete progress snapshot including iterationHistory.
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
      // Always replace — empty array clears stale todos, non-empty restores.
      store.replace({ todos })
      return
    }
    // Don't re-hydrate after finalization — the turn is over, and the
    // server's active_progress may be stale (not yet cleaned up). Only
    // hydrate if we haven't started receiving live events for this turn
    // (finalizedRef is false AND store is empty = fresh load/reconnect).
    if (finalizedRef.current) return
    const live = historyProgressToLive(initialProgress)
    // initialProgress comes from the server's authoritative active_progress
    // (fetched via reload). Always replace — the cache-restored snapshot
    // (from the reset effect) may have a higher eventSeq (updated by live SSE
    // events), which would block the server's authoritative data and cause
    // incomplete iteration recovery on session switch.
    if (live.phase) {
      store.replace(live)
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
      handleProgressMessage(msg, store, completeRef, compactedRef, resetRef, finalizedRef, phaseDoneRef, injectRef)
    })
    return offMessage
  }, [store, disabled, channel])

  // Derive a transient streaming message from the snapshot. Only the snapshot's
  // streamContent/streaming drives this, so it updates at frame rate (not per token).
  const liveMessage = useMemo<ChatMessage | null>(() => {
    const snap = progressSnapshot
    if (!hasVisibleProgress(snap)) return null
    // Phase="done" means the turn is over. Don't create a liveMessage —
    // the committed history row should render without a stale live overlay.
    if (snap.phase === 'done') return null
    return {
      id: `live-${chatID ?? 'unknown'}`,
      role: 'assistant',
      content: snap.streamContent || snap.content || '',
      iterations: snap.iterationHistory,
      timestamp: new Date().toISOString(),
      isPartial: true,
      turnID: 0,
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
    snap.streaming ||
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
): void {
  switch (msg.type) {
    case 'stream_content': {
      // Reset finalizedRef here — stream_content is the first sign that the
      // LLM is actively generating for this turn. This is safer than resetting
      // on session(busy), which can arrive from SSE reconnect recovery
      // (restoreActiveProgress) and would allow stale text events to be
      // re-processed, causing duplicate messages.
      if (finalizedRef) finalizedRef.current = false
      if (phaseDoneRef) phaseDoneRef.current = false

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
        if (store.lastTurnID > 0 && p.turn_id && p.turn_id > 0) {
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
        store.lastIter = -1 // reset iteration tracking for the new turn
        const ts = p.turn_start
        if (ts && (ts.trigger === 'notification' || ts.trigger === 'resume') && ts.content && p.turn_id) {
          injectRef?.current?.(ts.content, p.turn_id, ts.trigger === 'notification')
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
        // PhaseDone: the turn is over. Clear in-flight tool state
        // (activeTools/completedTools/streamingTools) so no tool appears
        // "running" after the turn ends. Preserve iterationHistory and
        // streamContent — the text event or cancel ack will read them.
        window.dispatchEvent(new CustomEvent('agent-idle', {
          detail: { chatID: p.chat_id ?? undefined, channel: undefined },
        }))
        // Update todos if the PhaseDone event carries them.
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
        } else {
          // No todos to update — still clear in-flight tools.
          store.resetStreamingState()
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

      // TODO list (from TodoWrite tool, carry-forward when absent)
      let todos: TodoItem[] | undefined
      if (Array.isArray(p.todos) && p.todos.length > 0) {
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
      if (iteration !== undefined && iteration >= 0) {
        if (store.lastIter >= 0 && iteration < store.lastIter) {
          console.error('[ITER_ID_INVARIANT_VIOLATION] iteration went backwards', {
            prev: store.lastIter,
            next: iteration,
            turnID: store.lastTurnID,
            chatID: p.chat_id,
            phase,
          })
        } else if (store.lastIter >= 0 && iteration !== store.lastIter + 1 && iteration > store.lastIter) {
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
        const parsedIterations = parseWebIterations(msg.progress_history)
        const snap = store.getSnapshot()
        const liveIters = snap.iterationHistory
        // Merge: server iterations (authoritative, has user_cancelled) +
        // any live-only iterations not in server data.
        const serverIterNums = new Set(parsedIterations.map((i) => i.iteration))
        const liveOnly = liveIters.filter((i) => !serverIterNums.has(i.iteration))
        const iters = [...parsedIterations, ...liveOnly]
        const text = snap.streamContent || snap.content || ''
        if (text || iters.length > 0) {
          completeRef.current?.(text, iters, msg.seq, msg.turn_id)
          if (hasVisibleProgress(store.getSnapshot())) store.reset()
        } else if (hasVisibleProgress(snap)) {
          store.reset()
        }
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
        // If we're already mid-stream, don't disrupt — a synthetic busy from
        // recovery must not wipe cumulative streamContent (causes typer restart).
        if (snap.streamContent || snap.reasoningStreamContent) {
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
        // Dirty store with no stream content — clear stale tool state.
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
