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
import { dedupMessages, mergeIterations, assertIterationContinuity } from '@/components/agent/progressStore'
import { getProgressGeneration, messagesCache, sessionCacheKey } from '@/lib/webCache'
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

/**
 * Incremental dedup: assumes the existing array (excluding newMsgIdx) is
 * already linearly consistent (previously deduped). Only the NEW message at
 * newMsgIdx can conflict — scan the array for a match by:
 * 1. turnID:role (same turn + role → merge iterations, prefer DB version)
 * 2. eventSeq (same SSE seq → replace)
 * 3. content:role (same content → content-based dedup for turnID=0 commits)
 *
 * If no conflict, returns the array unchanged. If conflict, merges in-place
 * and removes the duplicate. O(n) worst case but typically O(1) — the
 * conflicting message is almost always the last few rows (same turn).
 *
 * This replaces the previous O(n) dedupMessages(withMsg) call that re-scanned
 * the entire array on every appendAssistant invocation.
 */
function incrementalDedup<T extends { turnID: number; role: string; content?: string; id?: string; eventSeq?: number; dbID?: number; persisted?: boolean; iterations?: WebIteration[] }>(
  arr: T[],
  newMsgIdx: number,
): T[] {
  const msg = arr[newMsgIdx]
  // Check for conflicts only against the new message
  for (let i = 0; i < arr.length; i++) {
    if (i === newMsgIdx) continue
    const existing = arr[i]
    // 1. Same turnID:role (turnID > 0)
    if (msg.turnID > 0 && existing.turnID === msg.turnID && existing.role === msg.role) {
      const merged = mergeIterations(existing.iterations ?? [], msg.iterations ?? [])
      const result = [...arr]
      result[i] = {
        ...existing,
        iterations: merged.length > 0 ? merged : (existing.iterations ?? []),
        content: (existing.content ?? '') !== '' ? existing.content : (msg.content ?? ''),
      }
      result.splice(newMsgIdx, 1)
      return result
    }
    // 2. Same eventSeq (SSE replay)
    if (msg.eventSeq != null && existing.eventSeq === msg.eventSeq) {
      const result = [...arr]
      result[i] = msg // replace with newer version
      result.splice(newMsgIdx, 1)
      return result
    }
    // 3. Content-based dedup: same content + role='assistant'
    // (turnID=0 live commit vs turnID>0 DB message)
    const msgContent = msg.content ?? ''
    if (msgContent && msg.role === 'assistant' && existing.role === 'assistant' &&
        (existing.content ?? '') === msgContent) {
      // Prefer the DB version (turnID > 0) as base
      const base = existing.turnID > 0 ? existing : msg
      const other = existing.turnID > 0 ? msg : existing
      const merged = mergeIterations(base.iterations ?? [], other.iterations ?? [])
      const result = [...arr]
      result[i] = {
        ...base,
        iterations: merged.length > 0 ? merged : (base.iterations ?? []),
      }
      result.splice(newMsgIdx, 1)
      return result
    }
  }
  // No conflict — array is already consistent
  return arr
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
  // Build lookup sets from history for O(1) dedup checks.
  const historyTurnRoles = new Set<string>()
  const historyContentKeys = new Set<string>()
  let newestUserTurn = 0
  for (const m of history) {
    if (m.turnID > 0) {
      historyTurnRoles.add(`${m.turnID}:${m.role}`)
    }
    // Content+role fallback for messages without turnID (user_echo, etc.)
    if (m.content) {
      historyContentKeys.add(`${m.role}:${m.content}`)
    }
    if (m.role === 'user' && m.turnID > newestUserTurn) {
      newestUserTurn = m.turnID
    }
  }

  const liveRows = current.filter((message) => {
    // Persisted rows: normally covered by history. BUT user_echo rows carry
    // eventSeq=undefined (the backend echo has no seq), so the eventSeq guard
    // below would drop them even when the racing DB snapshot does NOT contain
    // the row yet — the user message vanishes until refresh. Keep persisted
    // USER rows that are NEWER than the snapshot's newest user turn (a racing
    // reload during the current turn); drop everything else per dedup rules.
    if (message.persisted !== false) {
      if (message.role !== 'user') return false
      if (message.turnID > 0 && historyTurnRoles.has(`${message.turnID}:${message.role}`)) return false
      if (message.content && historyContentKeys.has(`${message.role}:${message.content}`)) return false
      // Echoes carrying an eventSeq follow the watermark rule: below it they
      // are covered by history; at/above it they are post-snapshot data and
      // kept (SSE replay echo / reconnect recovery).
      if (message.eventSeq != null) {
        if (message.eventSeq < historyWatermark) return false
        return true
      }
      // No eventSeq (web_inbound.go echoes have none): a racing reload may
      // lack the row entirely (eager-save still in flight) — the user message
      // vanished until refresh. Keep it ONLY when its turn is newer than the
      // snapshot's newest user turn; a same-or-older turn is superseded by
      // the DB (e.g. same-session background reload replacing old content).
      if (message.turnID <= newestUserTurn) return false
      return true
    }
    // Unpersisted live rows (streaming assistant, cancel acks, frozen content).
    if (message.eventSeq == null) return false
    // Below watermark: always superseded by history.
    if (message.eventSeq < historyWatermark) return false
    // Same turnID:role already in history — drop the live row.
    // This is the PRIMARY dedup for cancel acks and final replies: the
    // locally-committed message (streaming content) and the DB message
    // ([interrupted] or normal reply) share the same turnID but have
    // different content/eventSeq, so content matching alone fails.
    if (message.turnID > 0 && historyTurnRoles.has(`${message.turnID}:${message.role}`)) return false
    // Content+role fallback for messages without turnID (user_echo).
    if (message.content && historyContentKeys.has(`${message.role}:${message.content}`)) return false
    return true
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
  onSendSuccess,
  onCancelSuccess,
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
      const cached = activeMessageCacheKey ? messagesCache.get(activeMessageCacheKey) : null
      if (cached && cached.length > 0) {
        messagesRef.current = cached
        setMessages(cached)
        // Don't set loading — we have content to show immediately
        setLoading(false)
      } else {
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
        if (requestIsSuperseded() || requestHasDestructiveMutation()) return
        const dumpMessages = Array.isArray(dump?.messages) ? dump.messages : []
        const dumpIterations = Array.isArray(dump?.iterations) ? dump.iterations : []
        if (dumpMessages.length > 0 || dumpIterations.length > 0) {
          const parsed = parseSubAgentMessages(dumpMessages, dump?.iterations)
          const mutated = requestHasMessageMutation()
          const next = mutated ? reconcileHistoryWithLiveRows(parsed, messagesRef.current, 0) : parsed
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
        messagesRef.current = next
        setMessages(next)
        setInitialProgress(null)
        return
      }
      // Normal mode: load via Web history snapshot (paginated: last 100 messages).
      const data = await fetchHistory(w, chatID ? { channel, chatID } : null, { limit: 100 })
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
      // ALWAYS reconcile (not only when `mutated`): markDestructiveMutation
      // (cancel) increments the gen BEFORE the next reload captures it, so
      // `mutated` is false for a cancel-triggered reload and the plain
      // `parsed` replacement would drop persisted user_echo rows when the
      // racing DB snapshot does not contain them yet — the user message
      // vanishes until refresh. reconcile keeps persisted USER rows that the
      // snapshot lacks (dedup by turnID:role / content:role) and drops
      // everything else per its rules, so it is safe unconditionally.
      const next = reconcileHistoryWithLiveRows(parsed, messagesRef.current, data.last_seq ?? 0)
      messagesRef.current = next
      setMessages(next)
      // Cache messages for instant render on next session switch (LRU).
      // Use activeMessageCacheKey to match the read path (avoids collision
      // between parent session and SubAgent panels).
      if (activeMessageCacheKey) {
        messagesCache.set(activeMessageCacheKey, next)
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
      // Second pass: merge by turnID:role — dedupMessages handles iteration union
      const next = dedupMessages([...noExactDups, ...prev])
      messagesRef.current = next
      setMessages(next)
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
        // Dedup by requestID for SSE replay — skip if a PERSISTED message
        // with the same requestID already exists. An optimistic (persisted=false)
        // message with the same requestID should be REPLACED, not skipped.
        if (requestID && prev.some((m) => m.requestID === requestID && m.persisted !== false)) return prev
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
        // If there's an optimistic message with the same requestID (from
        // sendMessage), replace it with the authoritative echo version
        // (persisted=true, turnID from server, dbID from DB). This updates
        // the existing row in-place instead of appending a duplicate.
        if (requestID) {
          const optimisticIdx = prev.findIndex(
            (m) => m.requestID === requestID && m.persisted === false,
          )
          if (optimisticIdx >= 0) {
            const copy = [...prev]
            copy[optimisticIdx] = { ...newMsg, id: prev[optimisticIdx].id }
            messagesRef.current = copy
            return copy
          }
        }
        const next = [...prev, newMsg]
        messagesRef.current = next
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
        }
        messageMutationGenRef.current += 1
        setMessages((prev) => {
          const next = [...prev, newMsg]
          messagesRef.current = next
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
            setMessages((prev) => {
              const next = prev.map((m) => m.id === sentID ? {
                ...m,
                persisted: true,
                ...(msgID ? { dbID: msgID } : {}),
                ...(serverTimestamp ? { timestamp: serverTimestamp } : {}),
                ...(respTurnID && respTurnID > 0 && !m.turnID ? { turnID: respTurnID } : {}),
                ...(respQueued ? { queued: true } : {}),
              } : m)
              messagesRef.current = next
              return next
            })
          }
        })
        .catch((error: unknown) => {
          // Remove the optimistic message on send failure
          if (optimisticID) {
            const failedID = optimisticID
            messageMutationGenRef.current += 1
            setMessages((prev) => {
              const next = prev.filter((m) => m.id !== failedID)
              messagesRef.current = next
              return next
            })
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

  const appendAssistant = useCallback((content: string, iterations: WebIteration[], eventSeq?: number, turnID?: number, insertBeforeLastUser?: boolean) => {
    if (!content && !iterations.length) return
    messageMutationGenRef.current += 1
    // Use the same id format as parseHistoryMessages (seq-${eventSeq}) so that
    // when reload returns and the server version replaces this optimistic row,
    // TanStack Virtual's getItemKey returns the same key — React reuses the
    // existing component instead of unmounting/remounting.
    const id = eventSeq != null ? `seq-${eventSeq}` : `asst-${Date.now()}-${echoSeq++}`
    const newMsg: ChatMessage = {
      id,
      role: 'assistant',
      content,
      iterations,
      timestamp: new Date().toISOString(),
      isPartial: false,
      turnID: turnID ?? 0,
      persisted: false,
      eventSeq,
    }
    setMessages((prev) => {
      // DEFAULT: append to the end. This preserves turn order — the assistant
      // reply belongs AFTER its user message.
      //
      // insertBeforeLastUser=true (turn_started(N+1) / turn_id-change fallback
      // commit path): the committed assistant belongs to the turn BEFORE the
      // newest user — it must land ABOVE that user, never below it. The rule
      // is deterministic and turn_id-independent: insert before the LAST user
      // message in the list (persisted or not). The newest user is exactly the
      // one that triggered the commit, so this always restores turn order.
      let insertIdx = prev.length
      if (insertBeforeLastUser) {
        // Two-step insertion:
        // 1. If turnID > 0: first scan for the assistant's OWN turn user
        //    (role=user && turnID matches) and insert AFTER it. This correctly
        //    positions the assistant even when the next turn's user hasn't been
        //    added to the messages array yet (race: turn_started arrives before
        //    sendMessage's setMessages is applied). Without this, the scan finds
        //    user1 (the ONLY user) and inserts BEFORE it: [assistant1, user1].
        // 2. Fallback: insert before the LAST user message (persisted or not).
        //    The newest user is the one that triggered the commit, so this
        //    restores turn order when the assistant's own turn user is unbound
        //    (turn_started was lost, turnID=0).
        let foundOwnTurnUser = false
        if (turnID) {
          for (let i = prev.length - 1; i >= 0; i--) {
            if (prev[i].role === 'user' && prev[i].turnID === turnID) {
              insertIdx = i + 1
              foundOwnTurnUser = true
              break
            }
          }
        }
        if (!foundOwnTurnUser) {
          for (let i = prev.length - 1; i >= 0; i--) {
            if (prev[i].role === 'user') {
              insertIdx = i
              break
            }
          }
        }
      }
      const withMsg = [...prev.slice(0, insertIdx), newMsg, ...prev.slice(insertIdx)]
      // Incremental dedup: the existing array (prev) is already linearly
      // consistent (previously deduped). Only the NEW message can conflict.
      // Check in O(1): scan for a match by turnID:role, eventSeq, or
      // content:role (content-based fallback for turnID=0 live commits).
      const next = incrementalDedup(withMsg, insertIdx)
      messagesRef.current = next
      return next
    })
  }, [])

  // injectUserMessage: display a bg-notification/cron user message.
  // Called by useProgressStream when a turn_started event with trigger
  // "notification" or "resume" arrives. The message is tagged with the
  // backend TurnID so the assistant response can be associated correctly.
  const injectUserMessage = useCallback((content: string, turnID: number, isNotification: boolean) => {
    setMessages((prev) => {
      // Dedup: if a notification with the same turnID already exists (from a
      // previous turn_started replay — SSE reconnect replays buffered
      // progress_structured events including turn_started), don't create a
      // duplicate. The ring buffer (web_hub.go isStatefulMsg) keeps
      // progress_structured events and replays them on reconnect, so
      // turn_started with trigger=notification can arrive twice.
      if (turnID > 0) {
        const existing = prev.find(m => m.turnID === turnID && m.role === 'user' && m.isNotification)
        if (existing) return prev
      }
      messageMutationGenRef.current += 1
      const id = `notif-${turnID}-${echoSeq++}`
      const newMsg: ChatMessage = {
        id,
        role: 'user',
        content,
        iterations: [],
        timestamp: new Date().toISOString(),
        isPartial: false,
        turnID,
        isNotification,
        persisted: false,
        eventSeq: -1, // marker: dedup against history by turnID:role in reconcile
      }
      const next = [...prev, newMsg]
      messagesRef.current = next
      return next
    })
  }, [])

  const removeMessage = useCallback((id: string) => {
    messageMutationGenRef.current += 1
    destructiveMutationGenRef.current += 1
    setMessages((prev) => {
      const next = prev.filter((m) => m.id !== id)
      messagesRef.current = next
      return next
    })
  }, [])

  const clearMessages = useCallback(() => {
    messageMutationGenRef.current += 1
    destructiveMutationGenRef.current += 1
    messagesRef.current = []
    setMessages([])
    setInitialProgress(null)
  }, [])

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
