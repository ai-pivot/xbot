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

import {
  fetchHistory,
  uploadFile,
  type HistMsg,
  type HistProgress,
  type UploadResponse,
} from '@/components/agent/api'
import { normalizeWebIteration } from '@/components/agent/normalize'
import { dedupMessages } from '@/components/agent/progressStore'
import { getProgressGeneration, sessionCacheKey } from '@/lib/webCache'
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
  /** SubAgent role — when set, loads SubAgent messages via get_session_messages RPC. */
  subAgentRole?: string
  /** SubAgent instance ID (required when subAgentRole is set). */
  subAgentInstance?: string
  /** Parent chatID for SubAgent message loading. */
  parentChatID?: string
  /** Full persisted agent tenant chatID for historical SubAgent tabs. */
  agentChatID?: string
}

export interface UseChatMessagesResult {
  messages: ChatMessage[]
  loading: boolean
  error: string | null
  /** Active progress snapshot from history (for resuming a busy session). */
  initialProgress: HistProgress | null
  /** Whether the backend reports this session as actively processing. */
  processing: boolean
  /** The chat_id reported by the most recent history load (server's active chat). */
  resolvedChatID: string | null
  /** Reload history for the current chatID. */
  reload: () => Promise<void>
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
 * 1. Skip display_only messages (cron results, [interrupted] markers).
 * 2. Parse `detail` JSON into WebIteration[] for each message.
 * 3. Tool_calls fallback: if NO message in the entire history has a non-empty
 *    detail, synthesize iteration history from tool_calls — preserves tool
 *    visibility for cancelled/unsaved runs (master ChatPage.tsx:607-623).
 * 4. Compression tool summary stripping: clear content of assistant messages
 *    that are >500 chars, start with `- **ToolName**:`, and have no
 *    tool_calls/detail — these are LLM-context compression artifacts (master
 *    ChatPage.tsx:638-646).
 * 5. Broader empty filter: skip assistant messages with no content AND no
 *    iterations (master ChatPage.tsx:654).
 * 6. Merge consecutive tool_calls-only fallback messages into one message
 *    with sequential iteration numbers (master ChatPage.tsx:656-663).
 */
function parseHistoryMessages(rows: HistMsg[]): ChatMessage[] {
  // Normalize each row from the WS RPC format (protocol.HistoryMessage).
  // Iterations are already pre-parsed by the backend (no detail JSON to parse).
  const normalized: ChatMessage[] = []
  for (let i = 0; i < rows.length; i++) {
    const m = rows[i]

    // Iterations come pre-parsed from the WS RPC (protocol.HistoryIteration[]).
    const iterations: WebIteration[] = Array.isArray(m.iterations)
      ? (m.iterations.map(normalizeWebIteration).filter(Boolean) as WebIteration[])
      : []

    const content = m.content ?? ''

    // Broader empty filter: skip assistant messages with no content AND no
    // iterations (catches all empty shells).
    if (
      m.role === 'assistant' &&
      (!content || content.trim() === '') &&
      iterations.length === 0
    ) {
      continue
    }

    normalized.push({
      id: m.seq != null ? `seq-${m.seq}` : `hist-${i}`,
      role: m.role,
      content,
      iterations,
      timestamp: m.timestamp ?? '',
      isPartial: false,
      turnID: 0,
      displayOnly: false,
      persisted: true,
      eventSeq: m.seq,
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

/** SubAgent message from get_session_messages RPC (agent.SessionMessage). */
interface SubAgentMsg {
  role: string
  content: string
}

interface AgentSessionDump {
  messages?: SubAgentMsg[]
  iterations?: unknown[]
}

const loadedMessageKeys = new Set<string>()
let globalReloadSeq = 0

// commitMessageCache is a no-op — message caching is disabled to avoid
// stale data bugs (blank panels, disappearing tools) on tab/session switch.
// All message state lives in React; reload always fetches fresh from server.
function commitMessageCache(_key: string, _rows: ChatMessage[], _seq = ++globalReloadSeq): boolean {
  return true
}

function messageCacheKey(
  channel: string,
  chatID: string | null,
  subAgentRole?: string,
  subAgentInstance?: string,
  agentChatID?: string,
): string {
  const key = sessionCacheKey(channel, chatID ?? 'current')
  if (!subAgentRole && !agentChatID) return key
  return `${key}:${subAgentRole ?? ''}:${subAgentInstance ?? ''}:${agentChatID ?? ''}`
}


// reconcileHistoryWithLiveRows merges server history with live (unpersisted)
// rows. A live row is kept only if its eventSeq is ABOVE the history
// watermark (last_seq) — meaning it was delivered via SSE after the history
// snapshot was taken, so it's not yet in history. Rows at or below the
// watermark are already covered by history. Rows without an eventSeq
// (optimistic user messages from sendMessage) are always dropped — the
// server persists them before/during the turn.
function reconcileHistoryWithLiveRows(
  history: ChatMessage[],
  current: ChatMessage[],
  historyWatermark: number,
): ChatMessage[] {
  const liveRows = current.filter((message) => {
    if (message.persisted !== false) return false
    // Optimistic messages (no eventSeq) are always superseded by history.
    if (message.eventSeq == null) return false
    // Keep only rows delivered via SSE after the history snapshot.
    return message.eventSeq > historyWatermark
  })
  return [...history, ...liveRows]
}

/** Parse SubAgent messages (simple role/content) into ChatMessage[]. */
function parseSubAgentMessages(rows: SubAgentMsg[], rawIterations?: unknown[]): ChatMessage[] {
  const iterations = Array.isArray(rawIterations)
    ? (rawIterations.map(normalizeWebIteration).filter(Boolean) as WebIteration[])
    : []
  const messages: ChatMessage[] = rows
    .filter((m) => m.content && m.content.trim())
    .map((m, i) => ({
      id: `sub-${i}`,
      role: (m.role === 'user' ? 'user' : 'assistant') as ChatMessage['role'],
      content: m.content,
      iterations: [],
      timestamp: '',
      isPartial: false,
      turnID: 0,
      displayOnly: false,
      persisted: true,
    }))
  if (iterations.length === 0) return messages
  const lastAssistant = messages.findLastIndex((m) => m.role === 'assistant')
  if (lastAssistant >= 0) {
    const next = [...messages]
    next[lastAssistant] = { ...next[lastAssistant], iterations }
    return next
  }
  return [
    ...messages,
    {
      id: 'sub-iterations',
      role: 'assistant',
      content: '',
      iterations,
      timestamp: '',
      isPartial: false,
      turnID: 0,
      displayOnly: false,
      persisted: true,
    },
  ]
}

export function useChatMessages({
  chatID,
  channel = 'web',
  enabled = true,
  ws,
  liveEventsEnabled = true,
  subAgentRole,
  subAgentInstance,
  parentChatID,
  agentChatID,
}: UseChatMessagesOptions): UseChatMessagesResult {
  const [messages, setMessages] = useState<ChatMessage[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [initialProgress, setInitialProgress] = useState<HistProgress | null>(null)
  const [resolvedChatID, setResolvedChatID] = useState<string | null>(null)
  const [processing, setProcessing] = useState(false)

  const chatIDRef = useRef(chatID)
  chatIDRef.current = chatID
  const activeMessageCacheKey = messageCacheKey(
    channel,
    chatID,
    subAgentRole,
    subAgentInstance,
    agentChatID,
  )
  const activeMessageCacheKeyRef = useRef(activeMessageCacheKey)
  activeMessageCacheKeyRef.current = activeMessageCacheKey
  const lastReloadKeyRef = useRef<string | null>(null)

  // Generation counter to discard stale async fetches when the user rapidly
  // switches sessions (prevents session A's history from overwriting session
  // B's after a quick switch — Spec 5 §2.1).
  const reloadGenRef = useRef(0)
  const messageMutationGenRef = useRef(0)
  const destructiveMutationGenRef = useRef(0)
  const messagesRef = useRef(messages)
  messagesRef.current = messages

  const cacheCurrentMessages = useCallback((rows: ChatMessage[]) => {
    commitMessageCache(activeMessageCacheKeyRef.current, rows)
  }, [])

  // Hold ws in a ref — its methods delegate to a stable MultiSSEManager instance,
  // so we don't need ws in the reload deps. Including ws would cause an infinite
  // loop: connected changes → ws identity changes → reload changes → useEffect
  // fires → ws.setLastSeq → restartSource → connected changes → ...
  const wsRef = useRef(ws)
  wsRef.current = ws

  const reload = useCallback(async () => {
    const w = wsRef.current
    const gen = ++reloadGenRef.current
    const mutationGen = messageMutationGenRef.current
    const destructiveMutationGen = destructiveMutationGenRef.current
    const progressCacheKey = chatID ? sessionCacheKey(channel, chatID) : null
    const progressGen = progressCacheKey ? getProgressGeneration(progressCacheKey) : null
    const globalSeq = ++globalReloadSeq
    const requestIsSuperseded = () => gen !== reloadGenRef.current
    const requestHasMessageMutation = () => mutationGen !== messageMutationGenRef.current
    const requestHasDestructiveMutation = () => (
      destructiveMutationGen !== destructiveMutationGenRef.current
    )
    const reloadKey = activeMessageCacheKey
    const sameTarget = lastReloadKeyRef.current === reloadKey
    // No cache read — always start fresh on session switch.
    if (!sameTarget) {
      messagesRef.current = []
      setMessages([])
    }
    const targetHasLoaded = loadedMessageKeys.has(reloadKey)
    const hasVisibleRows = sameTarget && messagesRef.current.length > 0
    setLoading(!targetHasLoaded && !hasVisibleRows)
    setError(null)
    lastReloadKeyRef.current = reloadKey
    if (!sameTarget) setInitialProgress(null)
    try {
      // Live SubAgent mode: TUI renders from the in-memory agent session dump.
      if (agentChatID) {
        const dump = await w.rpc<AgentSessionDump>('get_agent_session_dump_by_full_key', {
          full_key: agentChatID,
        })
        if (requestIsSuperseded() || requestHasDestructiveMutation()) return
        const dumpMessages = Array.isArray(dump?.messages) ? dump.messages : []
        const dumpIterations = Array.isArray(dump?.iterations) ? dump.iterations : []
        if (dumpMessages.length > 0 || dumpIterations.length > 0) {
          const parsed = parseSubAgentMessages(dumpMessages, dump?.iterations)
          const mutated = requestHasMessageMutation()
          const next = mutated ? reconcileHistoryWithLiveRows(parsed, messagesRef.current, 0) : parsed
          if (!commitMessageCache(reloadKey, next, mutated ? ++globalReloadSeq : globalSeq)) return
          loadedMessageKeys.add(reloadKey)
          messagesRef.current = next
          setMessages(next)
          setInitialProgress(null)
      setProcessing(false)
          return
        }
      }
      // Live SubAgent mode: same runtime tuple as TUI.
      if (subAgentRole && parentChatID && !agentChatID) {
        const dump = await w.rpc<AgentSessionDump>('get_agent_session_dump', {
          channel,
          chat_id: parentChatID,
          role: subAgentRole,
          instance: subAgentInstance ?? '',
        })
        if (requestIsSuperseded() || requestHasDestructiveMutation()) return
        const dumpMessages = Array.isArray(dump?.messages) ? dump.messages : []
        const dumpIterations = Array.isArray(dump?.iterations) ? dump.iterations : []
        if (dumpMessages.length > 0 || dumpIterations.length > 0) {
          const parsed = parseSubAgentMessages(dumpMessages, dump?.iterations)
          const mutated = requestHasMessageMutation()
          const next = mutated ? reconcileHistoryWithLiveRows(parsed, messagesRef.current, 0) : parsed
          if (!commitMessageCache(reloadKey, next, mutated ? ++globalReloadSeq : globalSeq)) return
          loadedMessageKeys.add(reloadKey)
          messagesRef.current = next
          setMessages(next)
          setInitialProgress(null)
          return
        }
        const msgs = await w.rpc<SubAgentMsg[]>('get_session_messages', {
          channel,
          chat_id: parentChatID,
          role: subAgentRole,
          instance: subAgentInstance ?? '',
        })
        if (requestIsSuperseded() || requestHasDestructiveMutation()) return
        const parsed = parseSubAgentMessages(Array.isArray(msgs) ? msgs : [])
        const mutated = requestHasMessageMutation()
        const next = mutated ? reconcileHistoryWithLiveRows(parsed, messagesRef.current, 0) : parsed
        if (!commitMessageCache(reloadKey, next, mutated ? ++globalReloadSeq : globalSeq)) return
        loadedMessageKeys.add(reloadKey)
        messagesRef.current = next
        setMessages(next)
        setInitialProgress(null)
        return
      }
      // Normal mode: load via Web history snapshot (full history + progress).
      const data = await fetchHistory(w, chatID ? { channel, chatID } : null)
      if (requestIsSuperseded() || requestHasDestructiveMutation()) return
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
      const next = mutated ? reconcileHistoryWithLiveRows(parsed, messagesRef.current, data.last_seq ?? 0) : parsed
      if (!commitMessageCache(reloadKey, next, mutated ? ++globalReloadSeq : globalSeq)) return
      loadedMessageKeys.add(reloadKey)
      messagesRef.current = next
      setMessages(next)
      setInitialProgress(progressChanged || mutated ? null : (data.active_progress ?? null))
      setProcessing(Boolean(data.processing))
      if (data.chat_id) setResolvedChatID(data.chat_id)
    } catch (e) {
      if (requestIsSuperseded() || requestHasDestructiveMutation()) return
      setError(e instanceof Error ? e.message : String(e))
      if (!sameTarget && !requestHasMessageMutation()) {
        messagesRef.current = []
        setMessages([])
      }
      setInitialProgress(null)
    } finally {
      if (gen === reloadGenRef.current) setLoading(false)
    }
  }, [channel, chatID, subAgentRole, subAgentInstance, parentChatID, agentChatID, activeMessageCacheKey])

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
    const off = ws.onMessage((msg: WSMessage) => {
      if (activeMessageCacheKeyRef.current !== listenerCacheKey) return
      if (!matchesChatID(msg, listenerChatID, channel)) return
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
        const lastUserIdx = msg.type === 'user_echo' ? prev.findLastIndex((m) => {
          if (requestID) return m.requestID === requestID
          if (!m.id.startsWith('user-') || m.content !== msg.original_content) return false
          const match = m.id.match(/^user-(\d+)-/)
          return Boolean(match && now - parseInt(match[1], 10) < 5000)
        }) : -1
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
  }, [ws, chatID, channel, activeMessageCacheKey, liveEventsEnabled])

  const sendMessage = useCallback(
    (content: string, attachments?: Attachments) => {
      const text = content.trim()
      if (!text && !attachments?.uploadKeys.length) return
      const requestID = newMessageRequestID()
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
      void ws.send({
        type: 'message',
        id: requestID,
        channel,
        chat_id: chatIDRef.current ?? undefined,
        content: text,
        upload_keys: attachments?.uploadKeys,
        file_names: attachments?.fileNames,
        file_sizes: attachments?.fileSizes,
        file_mimes: attachments?.fileMimes,
      }).catch((error: unknown) => {
        if (optimisticID) {
          const failedID = optimisticID
          messageMutationGenRef.current += 1
          setMessages((prev) => {
            const next = prev.filter((message) => message.id !== failedID)
            messagesRef.current = next
            return next
          })
        }
        toast.error(error instanceof Error ? error.message : 'message send failed')
      })
    },
    [ws, channel, cacheCurrentMessages],
  )

  const cancel = useCallback(() => {
    void ws.send({ type: 'cancel', channel, chat_id: chatIDRef.current ?? undefined })
      .catch((error: unknown) => {
        toast.error(error instanceof Error ? error.message : 'cancel failed')
      })
  }, [ws, channel])

  const upload = useCallback(async (file: File) => uploadFile(file), [])

  const appendAssistant = useCallback((content: string, iterations: WebIteration[], eventSeq?: number) => {
    if (!content && !iterations.length) return
    messageMutationGenRef.current += 1
    // Use the same id format as parseHistoryMessages (seq-${eventSeq}) so that
    // when reload returns and the server version replaces this optimistic row,
    // TanStack Virtual's getItemKey returns the same key — React reuses the
    // existing component instead of unmounting/remounting (which causes the
    // "last iteration disappears for a frame" flicker).
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
  }, [cacheCurrentMessages])

  const removeMessage = useCallback((id: string) => {
    messageMutationGenRef.current += 1
    destructiveMutationGenRef.current += 1
    setMessages((prev) => {
      const next = prev.filter((m) => m.id !== id)
      messagesRef.current = next
      cacheCurrentMessages(next)
      return next
    })
  }, [cacheCurrentMessages])

  const clearMessages = useCallback(() => {
    messageMutationGenRef.current += 1
    destructiveMutationGenRef.current += 1
    messagesRef.current = []
    setMessages([])
    const key = lastReloadKeyRef.current
    if (key) loadedMessageKeys.add(key)
    cacheCurrentMessages([])
    setInitialProgress(null)
  }, [cacheCurrentMessages])

  // Render from local state directly — no cross-session cache logic.
  // The panel always shows its own messages; reload fetches fresh history.
  return {
    messages,
    loading,
    error,
    initialProgress,
    processing,
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
