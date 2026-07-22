/**
 * useChatMessages — owns the committed chat message list for one Agent panel
 * (Spec 3/4 §3.8, §3.7).
 *
 * Responsibilities:
 *   - load history via /api/history and normalize rows into ChatMessage[]
 *     (parsing the pre-parsed `iterations` into WebIteration snapshots)
 *   - expose send / cancel / upload through the REST connection adapter
 *   - append a committed assistant message when useProgressStream finalizes a
 *     run (onAssistantComplete), and echo user messages on send
 *   - dedup messages by (turnID, role) when turnID > 0 — prevents duplicate
 *     messages from PhaseDone + handleAgentMessage racing
 *
 * The hook does NOT own live streaming — that lives in useProgressStream. The
 * split keeps the high-frequency token stream out of the committed-list state
 * so the virtualized list only re-renders on real list changes (load / send /
 * finalize), never per token.
 */
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { toast } from 'sonner'

import { continueInteractiveSession, fetchHistory, uploadFile, type HistMsg, type HistProgress, type UploadResponse } from '@/components/agent/api'
import { normalizeWebIteration } from '@/components/agent/normalize'
import { dedupMessages } from '@/components/agent/progressStore'
import { getProgressGeneration, getWebCacheEpoch, messagesCache, sessionCacheKey } from '@/lib/webCache'
import { matchesChatID } from '@/hooks/useProgressStream'
import type { WSConnection } from '@/types/ws'
import type { ChatMessage, WebIteration } from '@/types/shared'
import type { WSMessage } from '@/types/shared'

interface UseChatMessagesOptions {
  /** Chat ID this list tracks. */
  chatID: string | null
  /** Channel this list tracks. */
  channel?: string
  /** If true, history is (re)loaded whenever chatID changes. */
  enabled?: boolean
  /** The REST + SSE connection (injected from DockviewContext for isolated roots). */
  ws: WSConnection
  /** Whether this panel should consume live WS events. History RPC loading remains enabled separately. */
  liveEventsEnabled?: boolean
  /** Restore caller-owned draft state when an interactive continuation fails. */
  onSendError?: (content: string, error: unknown) => void
}

export interface UseChatMessagesResult {
  messages: ChatMessage[]
  loading: boolean
  error: string | null
  /** Authoritative active-run flag returned with the history snapshot. */
  processing: boolean
  /** Active progress snapshot from history (for resuming a busy session). */
  initialProgress: HistProgress | null
  /** The chat_id reported by the most recent history load (server's active chat). */
  resolvedChatID: string | null
  /** Reload history for the current chatID. */
  reload: () => Promise<boolean>
  /** Send a user message (+ optional uploaded file references). */
  sendMessage: (content: string, attachments?: Attachments) => void
  /** Cancel the running agent (sends a `cancel` WS message). */
  cancel: () => void
  /** Upload a file; returns the server upload metadata for sending with a message. */
  upload: (file: File) => Promise<UploadResponse>
  /** Append a finalized assistant message (called by useProgressStream). */
  appendAssistant: (content: string, iterations: WebIteration[], eventSeq?: number) => void
  /** Remove the trailing assistant message by id (for cancellation cleanup). */
  removeMessage: (id: string) => void
  /** Clear committed messages immediately, used for TUI-style /new reset. */
  clearMessages: () => void
}

/** File references resolved from an upload, ready to attach to a message. */
export interface Attachments {
  uploadKeys: string[]
  fileNames: string[]
  fileSizes: number[]
  fileMimes: string[]
}

/**
 * Parse raw history rows into ChatMessage[], porting master's defensive logic:
 *
 * Preserve the server's append-only order and relationship metadata. Message
 * and compression rows are filtered for display by MessageList; display_only
 * remains metadata so those user rows cannot be rewound.
 */
function parseHistoryMessages(rows: HistMsg[]): ChatMessage[] {
  // Normalize each row from the WS RPC format (protocol.HistoryMessage).
  // Iterations are already pre-parsed by the backend (no detail JSON to parse).
  const normalized: ChatMessage[] = []
  for (let i = 0; i < rows.length; i++) {
    const m = rows[i]

    // Iterations come pre-parsed from the WS RPC (protocol.HistoryIteration[]).
    const iterations: WebIteration[] = Array.isArray(m.iterations) ? (m.iterations.map(normalizeWebIteration).filter(Boolean) as WebIteration[]) : []

    const content = m.content ?? ''

    normalized.push({
      id: m.history_id
        ? `hist-${m.history_id}`
        : m.seq != null
          ? `seq-${m.seq}`
          : `hist-${i}`,
      historyID: m.history_id,
      role: m.role === 'control' ? 'system' : m.role,
      content,
      reasoningContent: m.reasoning_content,
      toolCallID: m.tool_call_id,
      toolName: m.tool_name,
      toolArguments: m.tool_arguments,
      toolCalls: m.tool_calls,
      iterations,
      timestamp: m.timestamp ?? '',
      isPartial: false,
      turnID: 0,
      displayOnly: m.display_only ?? false,
      persisted: true,
      eventSeq: m.seq,
      recordType: m.record_type,
      compactedBy: m.compacted_by,
      compression: m.compression
        ? {
            startHistoryID: m.compression.start_history_id,
            endHistoryID: m.compression.end_history_id,
            sourceHistoryIDs: m.compression.source_history_ids,
          }
        : undefined,
    })
  }

  // History messages have unique DB IDs — no dedup needed.
  // dedupMessages is only used in the live append path (appendAssistant)
  // to catch duplicate onAssistantComplete calls from reconnect replay.
  return normalized
}

let echoSeq = 0

function newMessageRequestID(): string {
  const id = globalThis.crypto?.randomUUID?.()
  return id ? id.replaceAll('-', '') : `web-${Date.now()}-${echoSeq++}`
}

const loadedMessageKeys = new Set<string>()
const messageCacheSeq = new Map<string, number>()
let globalReloadSeq = 0
let messageModuleCacheEpoch = getWebCacheEpoch()

function syncMessageModuleCacheEpoch(): number {
  const current = getWebCacheEpoch()
  if (current !== messageModuleCacheEpoch) {
    loadedMessageKeys.clear()
    messageCacheSeq.clear()
    messageModuleCacheEpoch = current
  }
  return current
}

function commitMessageCache(key: string, rows: ChatMessage[], seq = ++globalReloadSeq): boolean {
  const latest = messageCacheSeq.get(key) ?? 0
  if (seq < latest) return false
  messageCacheSeq.set(key, seq)
  messagesCache.set(key, rows)
  return true
}

function messageCacheKey(channel: string, chatID: string | null): string {
  return sessionCacheKey(channel, chatID ?? 'current')
}

function reconcileHistoryWithLiveRows(history: ChatMessage[], current: ChatMessage[]): ChatMessage[] {
  const matchedHistoryRows = new Set<number>()
  const liveRows = current.filter((message) => {
    if (message.persisted !== false) return false
    const match = history.findIndex((persisted, index) => !matchedHistoryRows.has(index) && sameMessageOccurrence(persisted, message))
    if (match < 0) return true
    matchedHistoryRows.add(match)
    return false
  })
  return [...history, ...liveRows]
}

function sameMessageOccurrence(persisted: ChatMessage, live: ChatMessage): boolean {
  if (persisted.role !== live.role || persisted.content !== live.content) return false
  const persistedAt = Date.parse(persisted.timestamp)
  const liveAt = Date.parse(live.timestamp)
  return Number.isFinite(persistedAt) && Number.isFinite(liveAt) && Math.abs(persistedAt - liveAt) <= 5_000
}

export function useChatMessages({
  chatID,
  channel = 'web',
  enabled = true,
  ws,
  liveEventsEnabled = true,
  onSendError,
}: UseChatMessagesOptions): UseChatMessagesResult {
  const [messages, setMessages] = useState<ChatMessage[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [processing, setProcessing] = useState(false)
  const [initialProgress, setInitialProgress] = useState<HistProgress | null>(null)
  const [resolvedChatID, setResolvedChatID] = useState<string | null>(null)

  const chatIDRef = useRef(chatID)
  chatIDRef.current = chatID
  const activeMessageCacheKey = messageCacheKey(channel, chatID)
  const activeMessageCacheKeyRef = useRef(activeMessageCacheKey)
  activeMessageCacheKeyRef.current = activeMessageCacheKey
  const lastReloadKeyRef = useRef<string | null>(null)
  const mountedRef = useRef(true)

  // Generation counter to discard stale async fetches when the user rapidly
  // switches sessions (prevents session A's history from overwriting session
  // B's after a quick switch — Spec 5 §2.1).
  const reloadGenRef = useRef(0)
  const messageMutationGenRef = useRef(0)
  const destructiveMutationGenRef = useRef(0)
  const messagesRef = useRef(messages)
  messagesRef.current = messages

  const cacheCurrentMessages = useCallback((rows: ChatMessage[]) => {
    syncMessageModuleCacheEpoch()
    commitMessageCache(activeMessageCacheKeyRef.current, rows)
  }, [])

  // Hold ws in a ref — its methods delegate to a stable MultiSSEManager instance,
  // so we don't need ws in the reload deps. Including ws would cause an infinite
  // loop: connected changes → ws identity changes → reload changes → useEffect
  // fires → ws.setLastSeq → restartSource → connected changes → ...
  const wsRef = useRef(ws)
  wsRef.current = ws

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
      reloadGenRef.current += 1
    }
  }, [])

  const reload = useCallback(async () => {
    if (!mountedRef.current) return false
    const w = wsRef.current
    const cacheEpoch = syncMessageModuleCacheEpoch()
    const gen = ++reloadGenRef.current
    const mutationGen = messageMutationGenRef.current
    const destructiveMutationGen = destructiveMutationGenRef.current
    const progressCacheKey = chatID ? sessionCacheKey(channel, chatID) : null
    const progressGen = progressCacheKey ? getProgressGeneration(progressCacheKey) : null
    const globalSeq = ++globalReloadSeq
    const requestIsSuperseded = () => !mountedRef.current || gen !== reloadGenRef.current || cacheEpoch !== getWebCacheEpoch()
    const requestHasMessageMutation = () => mutationGen !== messageMutationGenRef.current
    const requestHasDestructiveMutation = () => destructiveMutationGen !== destructiveMutationGenRef.current
    const reloadKey = activeMessageCacheKey
    const sameTarget = lastReloadKeyRef.current === reloadKey
    const cachedRows = messagesCache.get(reloadKey)
    if (!sameTarget) {
      const next = cachedRows ?? []
      messagesRef.current = next
      setMessages(next)
    }
    const targetHasLoaded = loadedMessageKeys.has(reloadKey)
    const hasVisibleRows = sameTarget && messagesRef.current.length > 0
    setLoading(!targetHasLoaded && !cachedRows && !hasVisibleRows)
    setError(null)
    // Keep the current rows until the replacement history arrives. This avoids
    // a visible empty "loading" flash during background refreshes and session
    // switches; stale async results are still discarded by reloadGenRef.
    lastReloadKeyRef.current = reloadKey
    if (!sameTarget) setInitialProgress(null)
    if (!sameTarget) setProcessing(false)
    try {
      // All sessions, including Agent children, use the canonical history API.
      const data = await fetchHistory(w, chatID ? { channel, chatID } : null)
      if (requestIsSuperseded() || requestHasDestructiveMutation()) return false
      const mutated = requestHasMessageMutation()
      // Store last_seq for SSE deduplication and reconnect replay.
      const cursorChatID = data.chat_id ?? chatID
      const cursorChannel = data.channel ?? channel
      const cursorCacheKey = cursorChatID ? sessionCacheKey(cursorChannel, cursorChatID) : null
      const progressChanged = Boolean(
        cursorCacheKey &&
        progressCacheKey &&
        progressGen !== null &&
        cursorCacheKey === progressCacheKey &&
        getProgressGeneration(cursorCacheKey) !== progressGen,
      )
      if (typeof data.last_seq === 'number' && cursorChatID && !progressChanged && !mutated) {
        w.setLastSeq(cursorChatID, data.last_seq, cursorChannel)
      }
      const rows = data.messages ?? []
      const parsed = parseHistoryMessages(rows)
      const next = mutated ? reconcileHistoryWithLiveRows(parsed, messagesRef.current) : parsed
      if (!commitMessageCache(reloadKey, next, mutated ? ++globalReloadSeq : globalSeq)) return false
      loadedMessageKeys.add(reloadKey)
      messagesRef.current = next
      setMessages(next)
      setInitialProgress(progressChanged || mutated ? null : (data.active_progress ?? null))
      setProcessing(progressChanged ? false : data.processing === true)
      if (data.chat_id) setResolvedChatID(data.chat_id)
      return true
    } catch (e) {
      if (requestIsSuperseded() || requestHasDestructiveMutation()) return false
      setError(e instanceof Error ? e.message : String(e))
      if (!sameTarget && !cachedRows && !requestHasMessageMutation()) {
        messagesRef.current = []
        setMessages([])
      }
      setInitialProgress(null)
      setProcessing(false)
      return false
    } finally {
      if (mountedRef.current && cacheEpoch === getWebCacheEpoch() && gen === reloadGenRef.current) setLoading(false)
    }
  }, [channel, chatID, activeMessageCacheKey])

  // Load history when the chatID changes (or on first enable).
  useLayoutEffect(() => {
    if (!enabled) return
    void reload()
  }, [enabled, chatID, reload])

  // Echo back user messages the server re-serializes (e.g. with file info).
  // The server sends both `content` (with file markdown) and `original_content`
  // (raw text). We use `content` to preserve file rendering, and replace the
  // optimistic message we inserted in `sendMessage` rather than appending a
  // duplicate.
  //
  // Spec 5 §2.4 — match by chatID and stable request ID. Legacy echoes without
  // an ID fall back to exact original content within a 5-second window.
  useEffect(() => {
    if (!liveEventsEnabled) return
    if (!chatID) return
    const listenerChatID = chatID
    const listenerCacheKey = activeMessageCacheKey
    const listenerCacheEpoch = syncMessageModuleCacheEpoch()
    const off = ws.onMessage((msg: WSMessage) => {
      if (!mountedRef.current || listenerCacheEpoch !== getWebCacheEpoch()) return
      if (activeMessageCacheKeyRef.current !== listenerCacheKey) return
      if (!matchesChatID(msg, listenerChatID, channel)) return
      if (msg.type === 'history_gap' || msg.type === 'resync_required') {
        void reload()
        return
      }
      if (msg.type !== 'user_echo' && msg.type !== 'inject_user') return
      const content = msg.content ?? msg.original_content ?? ''
      if (!content) return
      const requestID = msg.id
      const id = `echo-${msg.ts ?? Date.now()}-${echoSeq++}`
      const ts = msg.ts ? new Date(msg.ts * 1000).toISOString() : new Date().toISOString()
      const now = Date.now()
      setMessages((prev) => {
        if (activeMessageCacheKeyRef.current !== listenerCacheKey) return prev
        messageMutationGenRef.current += 1
        // A replayed echo finds the already-replaced row by requestID, so it
        // updates in place instead of appending a duplicate.
        const lastUserIdx =
          msg.type === 'user_echo'
            ? prev.findLastIndex((m) => {
                if (requestID) return m.requestID === requestID
                if (!m.id.startsWith('user-') || m.content !== msg.original_content) return false
                const match = m.id.match(/^user-(\d+)-/)
                return Boolean(match && now - parseInt(match[1], 10) < 5000)
              })
            : -1
        const newMsg: ChatMessage = {
          id,
          role: 'user',
          content,
          iterations: [],
          timestamp: ts,
          isPartial: false,
          turnID: 0,
          persisted: false,
          eventSeq: msg.seq,
          requestID,
        }
        if (lastUserIdx >= 0) {
          const copy = [...prev]
          copy[lastUserIdx] = newMsg
          messagesRef.current = copy
          commitMessageCache(listenerCacheKey, copy)
          return copy
        }
        const next = [...prev, newMsg]
        messagesRef.current = next
        commitMessageCache(listenerCacheKey, next)
        return next
      })
    })
    return off
  }, [ws, chatID, channel, activeMessageCacheKey, liveEventsEnabled, reload])

  const sendMessage = useCallback(
    (content: string, attachments?: Attachments) => {
      const text = content.trim()
      if (!text && !attachments?.uploadKeys.length) return
      if (channel === 'agent' && attachments?.uploadKeys.length) {
        toast.error('Interactive Agent continuations do not support attachments')
        return
      }
      const requestID = newMessageRequestID()
      const sendCacheEpoch = syncMessageModuleCacheEpoch()
      const resetCommand = text === '/new' && !attachments?.uploadKeys.length
      let optimisticID: string | null = null
      if (!resetCommand) {
        const id = `user-${Date.now()}-${echoSeq++}`
        optimisticID = id
        // Optimistically show normal user messages. /new waits for
        // session_reset so the old history does not flash with a visible
        // slash-command row.
        const newMsg: ChatMessage = {
          id,
          role: 'user',
          content: text,
          iterations: [],
          timestamp: new Date().toISOString(),
          isPartial: false,
          turnID: 0,
          persisted: false,
          requestID,
        }
        messageMutationGenRef.current += 1
        setMessages((prev) => {
          const next = [...prev, newMsg]
          messagesRef.current = next
          cacheCurrentMessages(next)
          return next
        })
      }
      const sendCacheKey = activeMessageCacheKeyRef.current
      const targetChatID = chatIDRef.current
      const sendPromise =
        channel === 'agent'
          ? targetChatID
            ? continueInteractiveSession(ws, targetChatID, text)
            : Promise.reject(new Error('interactive Agent session is unavailable'))
          : ws.send({
              type: 'message',
              id: requestID,
              channel,
              chat_id: targetChatID ?? undefined,
              content: text,
              upload_keys: attachments?.uploadKeys,
              file_names: attachments?.fileNames,
              file_sizes: attachments?.fileSizes,
              file_mimes: attachments?.fileMimes,
            })
      void sendPromise.catch((error: unknown) => {
        if (!mountedRef.current || sendCacheEpoch !== getWebCacheEpoch()) return
        if (optimisticID) {
          const failedID = optimisticID
          const cached = messagesCache.get(sendCacheKey) ?? []
          commitMessageCache(
            sendCacheKey,
            cached.filter((message) => message.id !== failedID),
          )
          if (activeMessageCacheKeyRef.current === sendCacheKey) {
            messageMutationGenRef.current += 1
            setMessages((prev) => {
              const next = prev.filter((message) => message.id !== failedID)
              messagesRef.current = next
              commitMessageCache(sendCacheKey, next)
              return next
            })
          }
        }
        onSendError?.(text, error)
        toast.error(error instanceof Error ? error.message : 'message send failed')
      })
    },
    [ws, channel, cacheCurrentMessages, onSendError],
  )

  const cancel = useCallback(() => {
    void ws
      .send({
        type: 'cancel',
        channel,
        chat_id: chatIDRef.current ?? undefined,
      })
      .catch((error: unknown) => {
        toast.error(error instanceof Error ? error.message : 'cancel failed')
      })
  }, [ws, channel])

  const upload = useCallback(async (file: File) => uploadFile(file), [])

  const appendAssistant = useCallback(
    (content: string, iterations: WebIteration[], eventSeq?: number) => {
      if (!mountedRef.current || (!content && !iterations.length)) return
      messageMutationGenRef.current += 1
      // Match parseHistoryMessages so history replacement reuses the same row.
      const id = eventSeq != null ? `seq-${eventSeq}` : `asst-${Date.now()}-${echoSeq++}`
      const newMsg: ChatMessage = {
        id,
        role: 'assistant',
        content,
        iterations,
        timestamp: new Date().toISOString(),
        isPartial: false,
        turnID: 0,
        persisted: false,
        eventSeq,
      }
      setMessages((prev) => {
        const next = dedupMessages([...prev, newMsg])
        messagesRef.current = next
        cacheCurrentMessages(next)
        return next
      })
    },
    [cacheCurrentMessages],
  )

  const removeMessage = useCallback(
    (id: string) => {
      messageMutationGenRef.current += 1
      destructiveMutationGenRef.current += 1
      setMessages((prev) => {
        const next = prev.filter((m) => m.id !== id)
        messagesRef.current = next
        cacheCurrentMessages(next)
        return next
      })
    },
    [cacheCurrentMessages],
  )

  const clearMessages = useCallback(() => {
    messageMutationGenRef.current += 1
    destructiveMutationGenRef.current += 1
    messagesRef.current = []
    setMessages([])
    const key = lastReloadKeyRef.current
    if (key) loadedMessageKeys.add(key)
    cacheCurrentMessages([])
    setInitialProgress(null)
    setProcessing(false)
  }, [cacheCurrentMessages])

  // Effects hydrate the backing state, but render from the target cache key so
  // a session transition can never expose the previous session for one frame.
  const visibleMessages = lastReloadKeyRef.current === activeMessageCacheKey ? messages : (messagesCache.get(activeMessageCacheKey) ?? [])
  const visibleInitialProgress = lastReloadKeyRef.current === activeMessageCacheKey ? initialProgress : null
  const visibleProcessing = lastReloadKeyRef.current === activeMessageCacheKey ? processing : false

  return {
    messages: visibleMessages,
    loading,
    error,
    processing: visibleProcessing,
    initialProgress: visibleInitialProgress,
    resolvedChatID,
    reload,
    sendMessage,
    cancel,
    upload,
    appendAssistant,
    removeMessage,
    clearMessages,
  }
}

// historyProgressToLive has moved to @/components/agent/normalize so useChatMessages
// does not duplicate the normalization logic. Re-export for any existing callers.
export { historyProgressToLive } from '@/components/agent/normalize'
