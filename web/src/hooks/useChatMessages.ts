/**
 * useChatMessages — owns the committed chat message list for one Agent panel
 * (Spec 3/4 §3.8, §3.7).
 *
 * Responsibilities:
 *   - load history via /api/history and normalize rows into ChatMessage[]
 *     (parsing the pre-parsed `iterations` into WebIteration snapshots)
 *   - expose send / cancel / upload so the input area can drive the WS channel
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
import { useCallback, useEffect, useRef, useState } from 'react'

import {
  fetchHistory,
  uploadFile,
  type HistMsg,
  type HistProgress,
  type UploadResponse,
} from '@/components/agent/api'
import { normalizeWebIteration } from '@/components/agent/normalize'
import { dedupMessages } from '@/components/agent/progressStore'
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
  /** The WS connection (injected from DockviewContext for isolated roots). */
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
  appendAssistant: (content: string, iterations: WebIteration[]) => void
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
      id: `hist-${i}`,
      role: m.role,
      content,
      iterations,
      timestamp: m.timestamp ?? '',
      isPartial: false,
      turnID: 0,
      displayOnly: false,
      persisted: true,
    })
  }

  // History messages have unique DB IDs — no dedup needed.
  // dedupMessages is only used in the live append path (appendAssistant)
  // to catch duplicate onAssistantComplete calls from reconnect replay.
  return normalized
}

let echoSeq = 0

/** SubAgent message from get_session_messages RPC (agent.SessionMessage). */
interface SubAgentMsg {
  role: string
  content: string
}

interface AgentSessionDump {
  messages?: SubAgentMsg[]
  iterations?: unknown[]
}

const messageCache = new Map<string, ChatMessage[]>()
const loadedMessageKeys = new Set<string>()
const messageCacheSeq = new Map<string, number>()
let globalReloadSeq = 0

function commitMessageCache(key: string, rows: ChatMessage[], seq = ++globalReloadSeq): boolean {
  const latest = messageCacheSeq.get(key) ?? 0
  if (seq < latest) return false
  messageCacheSeq.set(key, seq)
  messageCache.set(key, rows)
  return true
}

function shouldKeepVisibleRowsOnRefresh(
  parsed: ChatMessage[],
  sameTarget: boolean,
  visibleRows: ChatMessage[],
): boolean {
  if (!sameTarget || parsed.length > 0 || visibleRows.length === 0) return false
  return true
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

  const chatIDRef = useRef(chatID)
  chatIDRef.current = chatID
  const lastReloadKeyRef = useRef<string | null>(null)

  // Generation counter to discard stale async fetches when the user rapidly
  // switches sessions (prevents session A's history from overwriting session
  // B's after a quick switch — Spec 5 §2.1).
  const reloadGenRef = useRef(0)
  const messagesRef = useRef(messages)
  messagesRef.current = messages

  const cacheCurrentMessages = useCallback((rows: ChatMessage[]) => {
    const key = lastReloadKeyRef.current
    if (key) commitMessageCache(key, rows)
  }, [])

  const reload = useCallback(async () => {
    const gen = ++reloadGenRef.current
    const globalSeq = ++globalReloadSeq
    const reloadKey = `${channel}:${chatID ?? ''}:${subAgentRole ?? ''}:${subAgentInstance ?? ''}:${agentChatID ?? ''}`
    const sameTarget = lastReloadKeyRef.current === reloadKey
    const cachedRows = messageCache.get(reloadKey)
    if (!sameTarget) {
      setMessages(cachedRows ?? [])
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
    try {
      // Live SubAgent mode: TUI renders from the in-memory agent session dump.
      if (agentChatID) {
        const dump = await ws.rpc<AgentSessionDump>('get_agent_session_dump_by_full_key', {
          full_key: agentChatID,
        })
        if (gen !== reloadGenRef.current) return
        const dumpMessages = Array.isArray(dump?.messages) ? dump.messages : []
        const dumpIterations = Array.isArray(dump?.iterations) ? dump.iterations : []
        if (dumpMessages.length > 0 || dumpIterations.length > 0) {
          const parsed = parseSubAgentMessages(dumpMessages, dump?.iterations)
          if (!commitMessageCache(reloadKey, parsed, globalSeq)) return
          loadedMessageKeys.add(reloadKey)
          setMessages(parsed)
          setInitialProgress(null)
          return
        }
      }
      // Live SubAgent mode: same runtime tuple as TUI.
      if (subAgentRole && parentChatID && !agentChatID) {
        const dump = await ws.rpc<AgentSessionDump>('get_agent_session_dump', {
          channel,
          chat_id: parentChatID,
          role: subAgentRole,
          instance: subAgentInstance ?? '',
        })
        if (gen !== reloadGenRef.current) return
        const dumpMessages = Array.isArray(dump?.messages) ? dump.messages : []
        const dumpIterations = Array.isArray(dump?.iterations) ? dump.iterations : []
        if (dumpMessages.length > 0 || dumpIterations.length > 0) {
          const parsed = parseSubAgentMessages(dumpMessages, dump?.iterations)
          if (!commitMessageCache(reloadKey, parsed, globalSeq)) return
          loadedMessageKeys.add(reloadKey)
          setMessages(parsed)
          setInitialProgress(null)
          return
        }
        const msgs = await ws.rpc<SubAgentMsg[]>('get_session_messages', {
          channel,
          chat_id: parentChatID,
          role: subAgentRole,
          instance: subAgentInstance ?? '',
        })
        if (gen !== reloadGenRef.current) return
        const parsed = parseSubAgentMessages(Array.isArray(msgs) ? msgs : [])
        if (shouldKeepVisibleRowsOnRefresh(parsed, sameTarget, messagesRef.current)) return
        if (!commitMessageCache(reloadKey, parsed, globalSeq)) return
        loadedMessageKeys.add(reloadKey)
        setMessages(parsed)
        setInitialProgress(null)
        return
      }
      // Normal mode: load via Web history snapshot (full history + progress).
      const data = await fetchHistory(ws, chatID ? { channel, chatID } : null)
      // Discard stale fetch — a newer reload() was triggered while we were
      // waiting for this response (rapid session switch or HistoryCompacted
      // during an in-flight reload).
      if (gen !== reloadGenRef.current) return
      // Store last_seq for WS reconnect incremental replay.
      if (data.last_seq) ws.setLastSeq(chatID ?? '', data.last_seq, channel)
      const rows = data.messages ?? []
      const parsed = parseHistoryMessages(rows)
      if (shouldKeepVisibleRowsOnRefresh(parsed, sameTarget, messagesRef.current)) return
      if (!commitMessageCache(reloadKey, parsed, globalSeq)) return
      loadedMessageKeys.add(reloadKey)
      setMessages(parsed)
      setInitialProgress(data.active_progress ?? null)
      if (data.chat_id) setResolvedChatID(data.chat_id)
    } catch (e) {
      if (gen !== reloadGenRef.current) return
      setError(e instanceof Error ? e.message : String(e))
      if (!sameTarget && !cachedRows) setMessages([])
      setInitialProgress(null)
    } finally {
      if (gen === reloadGenRef.current) setLoading(false)
    }
  }, [ws, channel, chatID, subAgentRole, subAgentInstance, parentChatID, agentChatID])

  // Load history when the chatID changes (or on first enable).
  useEffect(() => {
    if (!enabled) return
    void reload()
  }, [enabled, chatID, reload])

  // Echo back user messages the server re-serializes (e.g. with file info).
  // The server sends both `content` (with file markdown) and `original_content`
  // (raw text). We use `content` to preserve file rendering, and replace the
  // optimistic message we inserted in `sendMessage` rather than appending a
  // duplicate.
  //
  // Spec 5 §2.4 — match by chatID first, then find the optimistic message
  // using a 5-second freshness window to avoid replacing an older user message
  // when echoes arrive out of order.
  useEffect(() => {
    if (!liveEventsEnabled) return
    if (!chatID) return
    const off = ws.onMessage((msg: WSMessage) => {
      if (msg.chat_id && chatIDRef.current && msg.chat_id !== chatIDRef.current) return
      if (msg.type !== 'user_echo') return
      const content = msg.content ?? msg.original_content ?? ''
      if (!content) return
      const id = `echo-${msg.ts ?? Date.now()}-${echoSeq++}`
      const ts = msg.ts ? new Date(msg.ts * 1000).toISOString() : new Date().toISOString()
      const now = Date.now()
      setMessages((prev) => {
        // Replace the most recent optimistic user message (id starts with
        // 'user-') that was created within 5 seconds — prevents replacing an
        // older user message when echoes arrive out of order.
        const lastUserIdx = prev.findLastIndex((m) => {
          if (!m.id.startsWith('user-')) return false
          const match = m.id.match(/^user-(\d+)-/)
          if (!match) return false
          return now - parseInt(match[1], 10) < 5000
        })
        const newMsg: ChatMessage = {
          id,
          role: 'user',
          content,
          iterations: [],
          timestamp: ts,
          isPartial: false,
          turnID: 0,
          persisted: false,
        }
        if (lastUserIdx >= 0) {
          const copy = [...prev]
          copy[lastUserIdx] = newMsg
          cacheCurrentMessages(copy)
          return copy
        }
        const next = [...prev, newMsg]
        cacheCurrentMessages(next)
        return next
      })
    })
    return off
  }, [ws, chatID, cacheCurrentMessages, liveEventsEnabled])

  const sendMessage = useCallback(
    (content: string, attachments?: Attachments) => {
      const text = content.trim()
      if (!text && !attachments?.uploadKeys.length) return
      const resetCommand = text === '/new' && !attachments?.uploadKeys.length
      if (!resetCommand) {
        const id = `user-${Date.now()}-${echoSeq++}`
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
        }
        setMessages((prev) => {
          const next = [...prev, newMsg]
          cacheCurrentMessages(next)
          return next
        })
      }
      ws.send({
        type: 'message',
        channel,
        chat_id: chatIDRef.current ?? undefined,
        content: text,
        upload_keys: attachments?.uploadKeys,
        file_names: attachments?.fileNames,
        file_sizes: attachments?.fileSizes,
        file_mimes: attachments?.fileMimes,
      })
    },
    [ws, channel, cacheCurrentMessages],
  )

  const cancel = useCallback(() => {
    ws.send({ type: 'cancel', channel, chat_id: chatIDRef.current ?? undefined })
  }, [ws, channel])

  const upload = useCallback(async (file: File) => uploadFile(file), [])

  const appendAssistant = useCallback((content: string, iterations: WebIteration[]) => {
    if (!content && !iterations.length) return
    const id = `asst-${Date.now()}-${echoSeq++}`
    const newMsg: ChatMessage = {
      id,
      role: 'assistant',
      content,
      iterations,
      timestamp: new Date().toISOString(),
      isPartial: false,
      turnID: 0,
      persisted: false,
    }
    setMessages((prev) => {
      const next = dedupMessages([...prev, newMsg])
      cacheCurrentMessages(next)
      return next
    })
  }, [cacheCurrentMessages])

  const removeMessage = useCallback((id: string) => {
    setMessages((prev) => {
      const next = prev.filter((m) => m.id !== id)
      cacheCurrentMessages(next)
      return next
    })
  }, [cacheCurrentMessages])

  const clearMessages = useCallback(() => {
    setMessages([])
    const key = lastReloadKeyRef.current
    if (key) loadedMessageKeys.add(key)
    cacheCurrentMessages([])
    setInitialProgress(null)
  }, [cacheCurrentMessages])

  return {
    messages,
    loading,
    error,
    initialProgress,
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
