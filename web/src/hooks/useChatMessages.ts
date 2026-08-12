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
import { MessageStore } from '@/components/agent/messageStore'
import { assertIterationContinuity } from '@/components/agent/progressStore'
import { getCachedMessages, getProgressGeneration, messagesCache, sessionCacheKey } from '@/lib/webCache'
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
  /** Called when a message is successfully sent (for optimistic busy trigger). */
  onSendSuccess?: () => void
  /** Called when cancel is successfully sent (for optimistic idle trigger). */
  onCancelSuccess?: () => void
  /** 外部共享 MessageStore（方案 A Step 3：与 useProgressStream 共享同一实例）。
   *  不传则内部自建。 */
  messageStore?: MessageStore
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
  /** Reload history for the current chatID. Resolves to the fresh rows (with
   *  dbID) or null when superseded/failed — rewind resolves a missing dbID
   *  from them. */
  reload: () => Promise<ChatMessage[] | null>
  /** Send a user message (+ optional uploaded file references). */
  sendMessage: (content: string, attachments?: Attachments) => void
  /** Cancel the running agent (sends a `cancel` WS message). */
  cancel: () => void
  /** True while cancel is in flight (shows spinner on cancel button). */
  cancelling: boolean
  /** Upload a file; returns the server upload metadata for sending with a message. */
  upload: (file: File) => Promise<UploadResponse>
  /** Append a finalized assistant message (called by useProgressStream). */
  appendAssistant: (content: string, iterations: WebIteration[], eventSeq?: number, turnID?: number, insertBeforeLastUser?: boolean) => void
  /** Inject a user message from a bg notification/cron (called by useProgressStream). */
  injectUserMessage: (content: string, turnID: number, isNotification: boolean) => void
  /** Remove the trailing assistant message by id (for cancellation cleanup). */
  removeMessage: (id: string) => void
  /** Clear committed messages immediately, used for TUI-style /new reset. */
  clearMessages: () => void
  /** Mark a destructive mutation — next reload discards live rows. */
  markDestructiveMutation: () => void
  /** Load older messages (scroll-up pagination). Returns false when no more. */
  loadMore: () => Promise<boolean>
  /** True if there are older messages available to load. */
  hasMore: boolean
  /** True while loadMore is fetching. */
  loadingMore: boolean
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
  // Replay-derived rows can share the same DB id (compress snapshots fall back
  // to the compress record's HistoryID for every snapshot message). The
  // virtualized MessageList keys rows by id — duplicate ids corrupt row-height
  // measurement and make an expanded <details> (e.g. [Compacted context])
  // overlap the rows below it. Make every row id unique with a -N suffix.
  const idCounts = new Map<string, number>()
  for (let i = 0; i < rows.length; i++) {
    const m = rows[i]

    // Iterations come pre-parsed from the WS RPC (protocol.HistoryIteration[]).
    const iterations: WebIteration[] = Array.isArray(m.iterations)
      ? (m.iterations.map(normalizeWebIteration).filter(Boolean) as WebIteration[])
      : []

    // Detect non-sequential iteration numbers (e.g. 1 → 148 gap) — indicates
    // lost iteration history, typically from a backend restart + cancel.
    if (iterations.length > 1) {
      assertIterationContinuity(iterations)
    }

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

    const baseId = m.id != null ? `db-${m.id}` : (m.seq != null ? `seq-${m.seq}` : `hist-${i}`)
    const seen = idCounts.get(baseId) ?? 0
    idCounts.set(baseId, seen + 1)
    const id = seen === 0 ? baseId : `${baseId}-${seen}`

    normalized.push({
      id,
      role: (m.role === 'user' ? 'user' : 'assistant') as ChatMessage['role'],
      content,
      iterations,
      timestamp: m.timestamp ?? '',
      isPartial: false,
      turnID: typeof m.turn_id === 'number' ? m.turn_id : 0,
      displayOnly: false,
      persisted: true,
      eventSeq: m.seq,
      dbID: m.id,
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
  onSendSuccess,
  onCancelSuccess,
  messageStore,
}: UseChatMessagesOptions): UseChatMessagesResult {
  const [messages, setMessages] = useState<ChatMessage[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [initialProgress, setInitialProgress] = useState<HistProgress | null>(null)
  const [resolvedChatID, setResolvedChatID] = useState<string | null>(null)
  const [processing, setProcessing] = useState(false)
  const [hasMore, setHasMore] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const oldestIdRef = useRef<number | null>(null)

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

  // ── MessageStore（方案 A）：committed 消息的唯一容器 ──
  // 每 turn 恰好 1 user + 1 assistant 槽位，live 由 useProgressStream 写入
  // （Step 3 接入）。唯一性由 Map 结构保证 —— 渲染层零去重。
  const storeRef = useRef<MessageStore | null>(null)
  if (storeRef.current === null) {
    storeRef.current = messageStore ?? new MessageStore()
  }
  const store = storeRef.current
  /** 从 store 同步 messages state + messagesRef（所有写操作后的统一出口）。 */
  const syncMessages = useCallback(() => {
    const rows = store.toRows()
    messagesRef.current = rows
    setMessages(rows)
  }, [store])
  // session 切换：清空 store（新会话从零开始，由 reload mergeHistory 重建）
  const prevStoreChatIDRef = useRef(chatID)
  if (prevStoreChatIDRef.current !== chatID) {
    prevStoreChatIDRef.current = chatID
    store.clear()
  }


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
    const requestIsSuperseded = () => gen !== reloadGenRef.current
    const requestHasMessageMutation = () => mutationGen !== messageMutationGenRef.current
    const requestHasDestructiveMutation = () => (
      destructiveMutationGen !== destructiveMutationGenRef.current
    )
    const reloadKey = activeMessageCacheKey
    const sameTarget = lastReloadKeyRef.current === reloadKey
    // Session switch: try messagesCache for instant render (LRU).
    // The cache stores the last-seen messages for recently visited sessions.
    // On a cache hit, render immediately while the network fetch refreshes.
    // Use activeMessageCacheKey (includes :role:instance:agentChatID suffix)
    // to avoid collision between parent session and SubAgent panels which
    // share the same channel+chatID but display different messages.
    if (!sameTarget) {
      const cached = activeMessageCacheKey ? getCachedMessages(activeMessageCacheKey) : null
      if (cached && cached.length > 0) {
        // 缓存恢复：store 重建（cached 有 turnID → mergeHistory 建 slot）
        store.clear()
        store.mergeHistory(cached, { replace: true })
        syncMessages()
        // Don't set loading — we have content to show immediately
        setLoading(false)
      } else {
        store.clear()
        messagesRef.current = []
        setMessages([])
        setHasMore(false)
        oldestIdRef.current = null
        setLoading(true)
      }
      setHasMore(false)
      oldestIdRef.current = null
    }
    setError(null)
    lastReloadKeyRef.current = reloadKey
    if (!sameTarget) setInitialProgress(null)
    try {
      // Live SubAgent mode: TUI renders from the in-memory agent session dump.
      if (agentChatID) {
        const dump = await w.rpc<AgentSessionDump>('get_agent_session_dump_by_full_key', {
          full_key: agentChatID,
        })
        if (requestIsSuperseded() || requestHasDestructiveMutation()) return null
        const dumpMessages = Array.isArray(dump?.messages) ? dump.messages : []
        const dumpIterations = Array.isArray(dump?.iterations) ? dump.iterations : []
        if (dumpMessages.length > 0 || dumpIterations.length > 0) {
          const parsed = parseSubAgentMessages(dumpMessages, dump?.iterations)
          // SubAgent dump 是完整权威列表 → 替换语义（clear + mergeHistory）
          store.clear()
          store.mergeHistory(parsed)
          syncMessages()
          setInitialProgress(null)
      setProcessing(false)
          return parsed
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
        if (requestIsSuperseded() || requestHasDestructiveMutation()) return null
        const dumpMessages = Array.isArray(dump?.messages) ? dump.messages : []
        const dumpIterations = Array.isArray(dump?.iterations) ? dump.iterations : []
        if (dumpMessages.length > 0 || dumpIterations.length > 0) {
          const parsed = parseSubAgentMessages(dumpMessages, dump?.iterations)
          // SubAgent dump 是完整权威列表 → 替换语义
          store.clear()
          store.mergeHistory(parsed)
          syncMessages()
          setInitialProgress(null)
          return parsed
        }
        const msgs = await w.rpc<SubAgentMsg[]>('get_session_messages', {
          channel,
          chat_id: parentChatID,
          role: subAgentRole,
          instance: subAgentInstance ?? '',
        })
        if (requestIsSuperseded() || requestHasDestructiveMutation()) return null
        const parsed = parseSubAgentMessages(Array.isArray(msgs) ? msgs : [])
        store.clear()
        store.mergeHistory(parsed)
        syncMessages()
        setInitialProgress(null)
        return parsed
      }
      // Normal mode: load via Web history snapshot (paginated: last 100 messages).
      const data = await fetchHistory(w, chatID ? { channel, chatID } : null, { limit: 100 })
      if (requestIsSuperseded() || requestHasDestructiveMutation()) return null
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
      // destructive（rewind/cancel）：DB 快照重建（丢弃本地提交）；否则
      // mergeHistory 保留 store 已有 slot（进行中 turn 的 live/提交/乐观 user）
      // 并回填 DB 字段 —— store 的槽位结构天然保证 persisted user / notification
      // 在竞态 reload 时不消失（等价于现有 reconcile 的保护规则）。
      if (requestHasDestructiveMutation()) {
        store.clear()
      }
      store.mergeHistory(parsed, { replace: true, watermark: data.last_seq ?? 0 })
      syncMessages()
      // Cache messages for instant render on next session switch (LRU).
      // Use activeMessageCacheKey to match the read path (avoids collision
      // between parent session and SubAgent panels). Store the progress
      // generation at write time — getCachedMessages drops stale entries
      // (progress/turn updates since the cache was written).
      if (activeMessageCacheKey) {
        messagesCache.set(activeMessageCacheKey, {
          messages: messagesRef.current,
          progressGen: progressCacheKey ? getProgressGeneration(progressCacheKey) : 0,
        })
        // LRU eviction: keep at most 5 sessions cached
        if (messagesCache.size > 5) {
          const oldestKey = messagesCache.keys().next().value
          if (oldestKey && oldestKey !== activeMessageCacheKey) messagesCache.delete(oldestKey)
        }
      }
      // Track pagination cursor.
      setHasMore(Boolean(data.has_more))
      oldestIdRef.current = data.oldest_id ?? null
      // Always restore active_progress — it contains the COMPLETE iterationHistory
      // from the server. Don't skip it when progressChanged (SSE delta arrived
      // during reload) — that's exactly when we need the full snapshot most,
      // because the delta only has 0-1 iterations while the server has all.
      setInitialProgress(data.active_progress ?? null)
      if (data.chat_id) setResolvedChatID(data.chat_id)
      return messagesRef.current // syncMessages 已更新为 store.toRows()（含 dbID）
    } catch (e) {
      if (requestIsSuperseded() || requestHasDestructiveMutation()) return null
      setError(e instanceof Error ? e.message : String(e))
      if (!sameTarget && !requestHasMessageMutation()) {
        messagesRef.current = []
        setMessages([])
      }
      setInitialProgress(null)
      return null
    } finally {
      if (gen === reloadGenRef.current) setLoading(false)
    }
  }, [channel, chatID, subAgentRole, subAgentInstance, parentChatID, agentChatID, activeMessageCacheKey])

  // Load older messages (scroll-up pagination).
  const loadMore = useCallback(async (): Promise<boolean> => {
    if (loadingMore || !hasMore || !oldestIdRef.current) return false
    const w = wsRef.current
    if (!w) return false
    setLoadingMore(true)
    try {
      const data = await fetchHistory(w, chatID ? { channel, chatID } : null, { limit: 100, beforeId: oldestIdRef.current })
      const rows = data.messages ?? []
      if (rows.length === 0) {
        setHasMore(false)
        return false
      }
      const parsed = parseHistoryMessages(rows)
      // Merge new messages with existing ones using dedupMessages, which
      // handles turnID:role-based MERGE (not DROP): when the batch boundary
      // splits a turn, batch 1 (newer) has the final assistant with Detail
      // iterations, batch 2 (older, from loadMore) has the tool_summary with
      // early iterations from flushPending. dedupMessages unions their
      // iterations by iteration number, preserving both sets of data.
      const prev = messagesRef.current
      const existingIds = new Set(prev.map((m) => m.id))
      // First pass: drop exact ID duplicates (same DB row returned by server)
      const noExactDups = parsed.filter((m) => !existingIds.has(m.id))
      // If the server returned only rows we already have (by ID), there is
      // genuinely no more data — stop pagination. This is the ONLY correct
      // stop condition: the server's has_more is authoritative for whether
      // MORE rows exist beyond this batch, but if all returned rows are
      // duplicates, we've reached the end.
      //
      // Do NOT use next.length === prev.length as a stop condition: when an
      // entire batch merges by turnID:role without adding new message slots
      // (e.g. a super-long turn spanning 3+ batches where the middle batch
      // has only same-turn assistant/tool messages), next.length stays the
      // same but the server still has more data (the turn's user message and
      // earlier turns). Stopping here would permanently break pagination.
      if (noExactDups.length === 0) {
        setHasMore(false)
        return false
      }
      // Second pass: merge by turnID:role — store.mergeHistory 内置迭代 union
      // （batch 边界拆 turn 时保留两侧迭代数据，等价原 dedupMessages 语义）
      store.mergeHistory(noExactDups)
      syncMessages()
      setHasMore(Boolean(data.has_more))
      oldestIdRef.current = data.oldest_id ?? null
      return true
    } catch {
      return false
    } finally {
      setLoadingMore(false)
    }
  }, [loadingMore, hasMore, channel, chatID])

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
      // replay_gap: SSE reconnect detected real data loss (TurnID changed,
      // turn ended during gap, or large iteration gap). Reload from DB to pick
      // up lost committed messages. When force_reload=true (large gap or
      // cross-turn), show a loading spinner during reload — the UI is too
      // stale to render incrementally.
      if (msg.type === 'replay_gap') {
        if (msg.metadata?.force_reload === 'true') {
          setLoading(true)
        }
        void reload()
        return
      }
      // resync_required: the backend's SSE ring buffer evicted events the
      // client missed (disconnect lasted longer than the buffer window, or
      // the buffer overflowed with high-frequency stream events). The events
      // (including progress_structured with iterationHistory deltas) are
      // PERMANENTLY LOST — no seq replay can recover them. Must reload from
      // DB to restore the authoritative iteration history; otherwise the
      // live message keeps an incomplete iteration list (漏 iter).
      if (msg.type === 'resync_required') {
        setLoading(true)
        void reload()
        return
      }
      if (!matchesChatID(msg, listenerChatID, channel)) return
      if (msg.type !== 'user_echo' && msg.type !== 'inject_user') return
      const content = msg.content ?? msg.original_content ?? ''
      if (!content) return
      const requestID = msg.id
      const id = `echo-${msg.ts ?? Date.now()}-${echoSeq++}`
      const ts = msg.ts ? new Date(msg.ts * 1000).toISOString() : new Date().toISOString()
      setMessages((prev) => {
        if (activeMessageCacheKeyRef.current !== listenerCacheKey) return prev
        messageMutationGenRef.current += 1
        const newMsg: ChatMessage = {
          id,
          role: 'user',
          content,
          iterations: [],
          timestamp: ts,
          isPartial: false,
          turnID: msg.turn_id ?? 0,
          persisted: true,
          eventSeq: msg.seq,
          requestID,
        }
        // 有 optimistic（同 requestID, persisted=false）→ 原地替换（保留 id，
        // TanStack Virtual key 稳定）；已 persisted → SSE replay 跳过。
        if (requestID) {
          const existing = store.findUserByRequestID(requestID)
          if (existing) {
            if (existing.persisted) return prev
            store.patchUserById(existing.id, { ...newMsg, id: existing.id })
            syncMessages()
            return prev // syncMessages 已 setMessages；返回 prev 避免二次更新
          }
        }
        store.setUser(msg.turn_id ?? 0, newMsg)
        syncMessages()
        return prev
      })
    })
    return off
  }, [ws, chatID, channel, activeMessageCacheKey, liveEventsEnabled])

  const sendMessage = useCallback(
    (content: string, attachments?: Attachments) => {
      const text = content.trim()
      if (!text && !attachments?.uploadKeys.length) return
      const requestID = newMessageRequestID()
      // Optimistic rendering: show the user message immediately.
      // No "sending" spinner — the REST response is typically <200ms, and
      // the spinner's height change (appear → disappear) causes the user
      // message bubble to resize, which triggers TanStack Virtual remeasurement
      // → scroll correction → visible jitter. The message appearing is enough
      // feedback; the busy state (from onSendSuccess) provides the rest.
      const resetCommand = text === '/new' && !attachments?.uploadKeys.length
      let optimisticID: string | null = null
      if (!resetCommand) {
        const id = `user-${Date.now()}-${echoSeq++}`
        optimisticID = id
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
          sending: true,
        }
        messageMutationGenRef.current += 1
        store.setUser(0, newMsg)
        syncMessages()
        // Pin to bottom immediately — the user just sent a message, they expect
        // to see it at the bottom. This survives all subsequent state updates
        // (user_echo replacement, busy placeholder, turn_started) because
        // stickToBottomRef is a ref, not state — it doesn't trigger re-render.
        // The MessageList's useEffect detects the new optimistic user row
        // (persisted=false) and calls resumeFollowing() + scheduleFollow().
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
      })
        .then((resp) => {
          // Call onSendSuccess BEFORE setMessages so the busy placeholder
          // appears in the same render cycle as the message update. Otherwise
          // (onSendSuccess after setMessages) there are two separate renders:
          // 1) message update (no height change since no spinner)
          // 2) busy placeholder appears (height increases)
          // Two renders with different scroll heights = visible jitter.
          // Calling onSendSuccess first lets both updates land in the same
          // React batch (React 18 automatic batching for promises).
          onSendSuccess?.()
          if (optimisticID && resp) {
            const sentID = optimisticID
            const respTurnID = resp.turn_id
            const respQueued = resp.queued === true
            const msgID = resp.message_id
            const serverTs = resp.timestamp
            const serverTimestamp = serverTs != null ? new Date(serverTs).toISOString() : undefined
            messageMutationGenRef.current += 1
            store.patchUserById(sentID, {
              persisted: true,
              sending: false,
              ...(msgID ? { dbID: msgID } : {}),
              ...(serverTimestamp ? { timestamp: serverTimestamp } : {}),
              ...(respTurnID && respTurnID > 0 ? { turnID: respTurnID } : {}),
              ...(respQueued ? { queued: true } : {}),
            })
            syncMessages()
          }
        })
        .catch((error: unknown) => {
          // Remove the optimistic message on send failure
          if (optimisticID) {
            const failedID = optimisticID
            messageMutationGenRef.current += 1
            store.removeById(failedID)
            syncMessages()
          }
          toast.error(error instanceof Error ? error.message : 'message send failed')
        })
    },
    [ws, channel],
  )

  const [cancelling, setCancelling] = useState(false)

  const cancel = useCallback(() => {
    setCancelling(true)
    void ws.send({ type: 'cancel', channel, chat_id: chatIDRef.current ?? undefined })
      .then(() => {
        setCancelling(false)
        onCancelSuccess?.()
      })
      .catch((error: unknown) => {
        setCancelling(false)
        toast.error(error instanceof Error ? error.message : 'cancel failed')
      })
  }, [ws, channel, onCancelSuccess])

  const upload = useCallback(async (file: File) => uploadFile(file), [])

  const appendAssistant = useCallback((content: string, iterations: WebIteration[], eventSeq?: number, turnID?: number, _insertBeforeLastUser?: boolean) => {
    if (!content && !iterations.length) return
    messageMutationGenRef.current += 1
    // store.commitAssistant 把 live → assistant 状态迁移（同一逻辑消息），
    // 按 turnID 路由到对应 slot —— 顺序由 turnIDs 排序保证，insertBeforeLastUser
    // 不再需要（结构上不可能插错位置）。
    store.commitAssistant(turnID ?? 0, content, iterations, eventSeq)
    syncMessages()
  }, [store, syncMessages])

  // injectUserMessage: display a bg-notification/cron user message.
  // Called by useProgressStream when a turn_started event with trigger
  // "notification" or "resume" arrives. The message is tagged with the
  // backend TurnID so the assistant response can be associated correctly.
  const injectUserMessage = useCallback((content: string, turnID: number, isNotification: boolean) => {
    // Dedup by turnID:role 由 store 的 slot 唯一性保证（每 turn 恰好 1 user）。
    // SSE reconnect 重放的重复 turn_started 不会产生重复行。
    messageMutationGenRef.current += 1
    const newMsg: ChatMessage = {
      id: `notif-${turnID}-${echoSeq++}`,
      role: 'user',
      content,
      iterations: [],
      timestamp: new Date().toISOString(),
      isPartial: false,
      turnID,
      isNotification,
      persisted: false,
      eventSeq: -1, // marker: dedup against history by turnID:role
    }
    store.setUser(turnID, newMsg)
    syncMessages()
  }, [store, syncMessages])

  const removeMessage = useCallback((id: string) => {
    messageMutationGenRef.current += 1
    destructiveMutationGenRef.current += 1
    store.removeById(id)
    syncMessages()
  }, [store, syncMessages])

  const clearMessages = useCallback(() => {
    messageMutationGenRef.current += 1
    destructiveMutationGenRef.current += 1
    store.clear()
    messagesRef.current = []
    setMessages([])
    setInitialProgress(null)
  }, [store])

  const markDestructiveMutation = useCallback(() => {
    messageMutationGenRef.current += 1
    destructiveMutationGenRef.current += 1
  }, [])

  // ── SSE replay gap → reload message list ──
  // When SSE disconnects and reconnects, the existing seq-gap detection in
  // sseConnection calls restoreActiveProgress. If that recovery detects a real
  // data loss (TurnID changed or turn ended during the gap), it dispatches a
  // `replay_gap` message. Only THEN do we reload — SSE event gaps are normal
  // (stateless coalescing, buffer drops), but TurnID/IterationID jumps indicate
  // committed messages (reply, notification) were lost and must be refetched.
  // Integrated into the user_echo listener to avoid registering a second
  // onMessage handler (which would interfere with test handler capture).

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
    cancelling,
    upload,
    appendAssistant,
    injectUserMessage,
    removeMessage,
    clearMessages,
    markDestructiveMutation,
    loadMore,
    hasMore,
    loadingMore,
  }
}

// historyProgressToLive has moved to @/components/agent/normalize so useChatMessages
// does not duplicate the normalization logic. Re-export for any existing callers.
export { historyProgressToLive } from '@/components/agent/normalize'
