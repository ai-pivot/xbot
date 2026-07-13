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
import { useEffect, useMemo, useRef } from 'react'
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
  ProgressEvent,
  WebIteration,
  ChatMessage,
  TodoItem,
} from '@/types/shared'
import { EMPTY_PROGRESS_SNAPSHOT } from '@/types/shared'
import type { HistProgress } from '@/components/agent/api'
import type { WSMessage } from '@/types/shared'
import { clearProgressSnapshot, progressSnapshotCache } from '@/lib/webCache'

interface UseProgressStreamOptions {
  /** Chat ID this stream tracks (events for other chats are ignored). */
  chatID: string | null
  /** Channel this stream tracks. Progress events may qualify chat_id as channel:chatID. */
  channel?: string
  /** Called with the finalized assistant text when a `text` event arrives. */
  onAssistantComplete?: (finalText: string, iterations: WebIteration[]) => void
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
    const sep = progressChatID.indexOf(':')
    if (sep > 0 && progressChatID.slice(sep + 1) === targetChatID) return true
  }
  return false
}

export function useProgressStream({
  chatID,
  channel = 'web',
  onAssistantComplete,
  onHistoryCompacted,
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

  // Guard against multiple onAssistantComplete calls per turn.
  // Reset to false when new streaming begins (stream_content arrives).
  const finalizedRef = useRef(false)

  // Track chatID inside the handlers via ref so we don't tear down the store on
  // every chat switch (we just reset it).
  const chatIDRef = useRef(chatID)
  chatIDRef.current = chatID

  const progressSnapshot = useSyncExternalStore(
    store.subscribe,
    store.getSnapshot,
    store.getSnapshot,
  )

  // Switch immediately to this chat's in-memory snapshot while history refreshes.
  useEffect(() => {
    finalizedRef.current = false
    store.reset()
    if (disabled) {
      return
    }
    const cached = chatID ? progressSnapshotCache.get(chatID) : undefined
    if (cached?.phase && cached.phase !== 'done') {
      store.replace(historyProgressToLive(cached))
    }
  }, [store, chatID, disabled])

  // Hydrate from history when initialProgress changes (after reload completes).
  // Separated from the reset effect so that a chatID change does NOT hydrate
  // with the stale initialProgress from the previous session — only the new
  // session's data triggers hydration (Spec 5 §2.7).
  useEffect(() => {
    if (disabled) return
    if (!initialProgress || !initialProgress.phase || initialProgress.phase === 'done') {
      if (initialProgress?.phase === 'done') {
        if (chatID) clearProgressSnapshot(chatID)
        finalizedRef.current = false
        if (hasVisibleProgress(store.getSnapshot())) store.reset()
      }
      return
    }
    const live = historyProgressToLive(initialProgress)
    // Only hydrate if we got something meaningful (non-empty snapshot)
    if (live.phase) {
      if (chatID) progressSnapshotCache.set(chatID, initialProgress as ProgressEvent)
      store.replace(live)
    }
  }, [store, initialProgress, disabled, chatID])

  // Dispose on unmount.
  useEffect(() => {
    return () => {
      store.dispose()
      storeRef.current = null
    }
  }, [store])

  // Subscribe to SSE messages.
  useEffect(() => {
    if (disabled) return
    const offMessage = ws.onMessage((msg: WSMessage) => {
      // 3-layer chatID filtering.
      if (chatIDRef.current && !matchesChatID(msg, chatIDRef.current, channel)) {
        return
      }
      if (chatIDRef.current && isTerminalProgressMessage(msg)) {
        clearProgressSnapshot(chatIDRef.current)
      }
      handleProgressMessage(msg, store, completeRef, compactedRef, resetRef, finalizedRef)
    })
    return offMessage
  }, [ws, store, disabled, channel])

  // Derive a transient streaming message from the snapshot. Only the snapshot's
  // streamContent/streaming drives this, so it updates at frame rate (not per token).
  const liveMessage = useMemo<ChatMessage | null>(() => {
    const snap = progressSnapshot
    if (!hasVisibleProgress(snap)) return null
    return {
      id: `live-${chatID ?? 'unknown'}`,
      role: 'assistant',
      content: snap.streamContent || '',
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
  }
}

function hasVisibleProgress(snap: ProgressSnapshot): boolean {
  return Boolean(
    snap.streaming ||
      snap.streamContent ||
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
): void {
  switch (msg.type) {
    case 'stream_content': {
      // New streaming content arriving → reset the finalize guard for the new turn.
      if (finalizedRef) finalizedRef.current = false

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
      if (p.phase === 'done') {
        if (finalizedRef) finalizedRef.current = false
        store.reset()
        return
      }
      if (finalizedRef && p.phase !== 'done') finalizedRef.current = false
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

      // Apply structured event with carry-forward (stream-only fields preserved)
      store.setStructuredTools({
        phase,
        iteration,
        activeTools: active.length ? active : undefined,
        completedTools: completed.length ? completed : undefined,
        reasoning,
        iterationHistory: iterHistory,
        todos,
        subAgents,
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
      // Final assistant message: commit then clear the live stream.
      // Guard against duplicate onAssistantComplete within the same turn
      // (e.g. text + session(idle) arriving before RAF flushes).
      // Cross-reconnect replay is handled by dedupMessages in appendAssistant.
      if (finalizedRef?.current) return
      if (finalizedRef) finalizedRef.current = true
      const finalText = msg.content ?? ''
      const parsedIterations = parseWebIterations(msg.progress_history)
      const snap = store.getSnapshot()
      const iterations = parsedIterations.length > 0 ? parsedIterations : snap.iterationHistory
      completeRef.current?.(finalText, iterations)
      store.reset()
      return
    }

    case 'session': {
      const action = msg.session?.action

      if (action === 'busy') {
        if (finalizedRef) finalizedRef.current = false
        store.reset()
        return
      }

      // HistoryCompacted: reset store and trigger reload
      if (action === 'HistoryCompacted') {
        store.reset()
        compactedRef.current?.()
        return
      }

      // On idle, if we had accumulated stream content without a closing text,
      // finalize defensively. Skip if already finalized (text event arrived first).
      if (action === 'idle') {
        if (finalizedRef?.current) {
          store.reset()
          return
        }
        const snap = store.getSnapshot()
        if (hasVisibleProgress(snap)) {
          if (finalizedRef) finalizedRef.current = true
          const text = snap.streamContent
          const iters = snap.iterationHistory
          store.reset()
          completeRef.current?.(text, iters)
        } else {
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
