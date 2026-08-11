import { postAPI } from '@/lib/api'
import {
  bumpProgressGeneration,
  clearProgressSnapshot,
  getLastIteration,
  getLastSeq,
  hasLastSeq,
  progressSnapshotCache,
  resetLastIteration,
  resetLastSeq,
  sessionCacheKey,
  setLastIteration,
  setLastSeq,
} from '@/lib/webCache'
import type {
  ProgressEvent,
  SessionEvent,
  WSClientMessage,
  WSMessage,
} from '@/types/shared'
import type { WSConnection, SendMessageResponse } from '@/types/ws'

const STATUS_POLL_MS = 5_000
const REPLAY_GRACE_MS = 1_000
const SEND_RETRY_DELAYS_MS = [1_000, 2_000]

export const SSE_EVENT_TYPES = [
  'text',
  'progress_structured',
  'stream_content',
  'ask_user',
  'card',
  'user_echo',
  'inject_user',
  'plugin_widgets',
  'session',
  'runner_status',
  'sync_progress',
  'resync_required',
] as const

type Handler<T> = (payload: T) => void

/** One native EventSource for the active chat plus REST for client-to-server calls. */
export class SSEConnectionImpl implements WSConnection {
  private source: EventSource | null = null
  private _connected = false
  private _chatID: string | null = null
  private _channel = 'web'
  private disposed = false
  private reconnecting = false
  private eventsSinceOpen = 0
  private pollTimer: ReturnType<typeof setInterval> | null = null
  private pollRequestToken: object | null = null
  private replayTimer: ReturnType<typeof setTimeout> | null = null
  private sessionVersion = 0
  private progressVersion = 0
  private recoveryRequestVersion = 0
  // Prevents concurrent restoreActiveProgress calls and the self-perpetuating
  // loop: restoreActiveProgress dispatches a progress_structured event that may
  // carry a seq gap, which would trigger restoreActiveProgress again → infinite
  // RPC storm. Only one recovery is in-flight at a time; subsequent gap
  // detections are no-ops while it runs.
  private recoveryInProgress = false

  private messageHandlers = new Set<Handler<WSMessage>>()
  private sessionHandlers = new Set<Handler<SessionEvent>>()
  private progressHandlers = new Set<Handler<ProgressEvent>>()
  private connHandlers = new Set<Handler<boolean>>()

  get connected(): boolean {
    return this._connected
  }

  get chatID(): string | null {
    return this._chatID
  }

  get channel(): string | null {
    return this._chatID ? this._channel : null
  }

  setLastSeq(chatID: string, seq: number, channel = this._channel): void {
    const cacheKey = sessionCacheKey(channel, chatID)
    if (!chatID) return
    const hadCursor = hasLastSeq(cacheKey)
    const previousSeq = getLastSeq(cacheKey)
    setLastSeq(cacheKey, seq)
    // Only restart if the source is OPEN (readyState === 1). If it's still
    // CONNECTING (readyState === 0), the connecting EventSource already read
    // the cursor from cache at connect() time — restarting would close it
    // and create a duplicate connection. The cursor is already persisted;
    // when events arrive, handleEvent updates lastSentSeq naturally.
    if (
      (!hadCursor || seq > previousSeq) &&
      this._chatID === chatID &&
      this._channel === channel &&
      this.source &&
      this.source.readyState === 1
    ) this.restartSource()
  }

  async send(msg: WSClientMessage): Promise<SendMessageResponse | void> {
    switch (msg.type) {
      case 'message':
        return this.sendMessageWithRetry(msg)
      case 'cancel':
        await postAPI('/api/cancel', sessionBody(msg))
        return
      case 'ask_user_response':
        await postAPI('/api/ask_user/respond', {
          ...sessionBody(msg),
          answers: msg.answers,
          cancelled: msg.cancelled,
        })
        return
      default:
        throw new Error(`unsupported REST message type: ${msg.type}`)
    }
  }

  subscribe(chatID: string, channel = 'web'): void {
    if (this.disposed) return
    if (this._chatID === chatID && this._channel === channel && this.source) return
    this.disconnect()
    this._chatID = chatID
    this._channel = channel
    this.connect()
  }

  disconnect(): void {
    this.sessionVersion += 1
    this.clearPoll()
    this.clearReplayTimer()
    if (this.source) {
      this.source.close()
      this.source = null
    }
    this.reconnecting = false
    this.eventsSinceOpen = 0
    this._chatID = null
    this._channel = 'web'
    this.setConnected(false)
  }

  rpc<T = unknown>(method: string, params?: unknown): Promise<T> {
    return postAPI<T>('/api/rpc', { method, params: params ?? {} })
  }

  onMessage = (handler: Handler<WSMessage>) => this.subscribeHandler(this.messageHandlers, handler)
  onSession = (handler: Handler<SessionEvent>) => this.subscribeHandler(this.sessionHandlers, handler)
  onProgress = (handler: Handler<ProgressEvent>) => this.subscribeHandler(this.progressHandlers, handler)
  onConnectionChange = (handler: Handler<boolean>) => this.subscribeHandler(this.connHandlers, handler)

  dispose(): void {
    this.disposed = true
    this.disconnect()
    this.messageHandlers.clear()
    this.sessionHandlers.clear()
    this.progressHandlers.clear()
    this.connHandlers.clear()
  }

  private connect(): void {
    const chatID = this._chatID
    const channel = this._channel
    if (this.disposed || !chatID || typeof EventSource === 'undefined') return

    const params = new URLSearchParams({ chat_id: chatID, channel })
    const cacheKey = sessionCacheKey(channel, chatID)
    const lastSeq = getLastSeq(cacheKey)
    if (hasLastSeq(cacheKey)) {
      params.set('last_event_id', String(lastSeq))
    }

    let source: EventSource
    try {
      source = new EventSource(`/api/sse?${params.toString()}`)
    } catch {
      this.startPolling()
      return
    }
    this.source = source
    for (const eventType of SSE_EVENT_TYPES) {
      source.addEventListener(eventType, (event) => {
        if (this.source !== source) return
        this.handleEvent(eventType, event as MessageEvent<string>)
      })
    }
    source.onopen = () => {
      if (this.source !== source) return
      const resumed = this.reconnecting
      this.reconnecting = false
      this.eventsSinceOpen = 0
      this.clearPoll()
      this.setConnected(true)
      if (resumed) this.scheduleReplayFallback(source, channel, chatID)
    }
    source.onerror = () => {
      if (this.source !== source) return
      this.reconnecting = true
      this.setConnected(false)
      this.startPolling()
    }
  }

  private restartSource(): void {
    this.sessionVersion += 1
    this.clearPoll()
    this.clearReplayTimer()
    this.source?.close()
    this.source = null
    this.reconnecting = false
    this.eventsSinceOpen = 0
    // Don't set connected=false — we're immediately reconnecting via connect().
    // setConnected(false) causes ws identity to change, triggering a re-render
    // flash across all hooks that depend on ws.connected (useSessionContext,
    // useLLMSettings, etc.). The onerror handler will set connected=false if
    // the reconnection fails.
    this.connect()
  }

  private handleEvent(eventType: string, event: MessageEvent<string>): void {
    let msg: WSMessage
    try {
      msg = JSON.parse(event.data) as WSMessage
    } catch {
      return
    }
    msg.type = eventType
    const seq = msg.seq ?? parseSequence(event.lastEventId)
    const chatID = this._chatID
    const channel = this._channel
    const cacheKey = chatID ? sessionCacheKey(channel, chatID) : null
    let replayGap = false
    // Gap CROSSED an iteration boundary → an iteration's completion delta may
    // have been lost in the gap. This is the ONLY real-data-loss case for
    // iterations: iterationHistory is an incremental delta feed and no later
    // SSE snapshot carries lost iterations. Reload from DB immediately.
    let crossedIteration = false
    if (cacheKey && seq > 0) {
      let previousSeq = getLastSeq(cacheKey)
      if (seq < previousSeq) {
        resetLastSeq(cacheKey)
        resetLastIteration(cacheKey)
        previousSeq = 0
      } else if (seq === previousSeq) {
        return
      }
      // Track the last progress_structured iteration id for cross-iteration
      // gap detection (stateless events don't carry iterations).
      const isStateless = msg.type === 'stream_content'
        || msg.type === 'sync_progress'
        || msg.type === 'runner_status'
      if (seq > previousSeq + 1) {
        // Only trigger recovery for stateful events. Stateless events
        // (stream_content, sync_progress, runner_status) are coalesced by the
        // Hub — the server assigns a seq to each event but only delivers the
        // latest. A seq gap on a stateless event is normal coalescing
        // (intermediate values were merged), NOT a lost event. Triggering
        // restoreActiveProgress here caused an RPC storm: LLM streams at
        // ~20 tokens/sec, each coalesced stream_content triggered a
        // get_active_progress RPC.
        if (!isStateless) {
          replayGap = true
          if (msg.type === 'progress_structured') {
            const curIter = typeof msg.progress?.iteration === 'number' ? msg.progress.iteration : 0
            const prevIter = getLastIteration(cacheKey)
            if (prevIter > 0 && curIter > prevIter) {
              crossedIteration = true
            }
          }
        }
      }
      msg.seq = seq
      setLastSeq(cacheKey, seq)
      if (msg.type === 'progress_structured' && typeof msg.progress?.iteration === 'number' && msg.progress.iteration > 0) {
        setLastIteration(cacheKey, msg.progress.iteration)
      }
    }
    this.eventsSinceOpen += 1
    if (cacheKey && isProgressLifecycleEvent(msg)) {
      this.progressVersion += 1
      bumpProgressGeneration(cacheKey)
    }
    this.dispatch(msg)
    if (chatID && replayGap) {
      if (crossedIteration) {
        // Gap crossed an iteration boundary (e.g. iteration 3's events, then a
        // seq gap, then iteration 4's events). Iteration 3's COMPLETION delta
        // may be lost — no later snapshot backfills it, so the DB is
        // authoritative. Reload immediately (force_reload shows a spinner);
        // restoreActiveProgress is skipped — its recovery snapshot cannot
        // repair a lost delta and would race the reload.
        this.dispatch({ type: 'replay_gap', chat_id: `${channel}:${chatID}`, metadata: { force_reload: 'true' } })
      } else {
        // Gap on the SAME iteration: lost events were snapshots (reasoning /
        // tool updates) — the recovery snapshot below covers them. No reload.
        void this.restoreActiveProgress(channel, chatID)
      }
    }
  }

  private dispatch(msg: WSMessage): void {
    if (this._chatID) {
      const cacheKey = sessionCacheKey(this._channel, this._chatID)
      if (isTerminalProgressEvent(msg)) {
        clearProgressSnapshot(cacheKey)
      } else if (msg.type === 'progress_structured' && msg.progress) {
        progressSnapshotCache.set(cacheKey, msg.progress)
      }
    }
    if (msg.type === 'session' && msg.session) {
      this.sessionHandlers.forEach((handler) => handler(msg.session!))
    }
    if ((msg.type === 'progress_structured' || msg.type === 'stream_content' || msg.type === 'sync_progress') && msg.progress) {
      this.progressHandlers.forEach((handler) => handler(msg.progress!))
    }
    this.messageHandlers.forEach((handler) => handler(msg))
  }

  private async sendMessageWithRetry(msg: WSClientMessage): Promise<SendMessageResponse> {
    const requestID = msg.id || newMessageRequestID()
    const body = {
      id: requestID,
      content: msg.content ?? '',
      file_ids: msg.file_ids,
      file_names: msg.file_names,
      file_sizes: msg.file_sizes,
      upload_keys: msg.upload_keys,
      file_mimes: msg.file_mimes,
      ...sessionBody(msg),
    }
    for (let attempt = 0; attempt <= SEND_RETRY_DELAYS_MS.length; attempt += 1) {
      try {
        const result = await postAPI<SendMessageResponse>('/api/message', body)
        return result
      } catch (error) {
        if (attempt === SEND_RETRY_DELAYS_MS.length) throw error
        await delay(SEND_RETRY_DELAYS_MS[attempt])
      }
    }
    throw new Error('send: exhausted retries') // unreachable — loop always returns or throws
  }

  private scheduleReplayFallback(source: EventSource, channel: string, chatID: string): void {
    this.clearReplayTimer()
    this.replayTimer = setTimeout(() => {
      this.replayTimer = null
      if (this.source !== source || this._channel !== channel || this._chatID !== chatID || this.eventsSinceOpen > 0) return
      void this.restoreActiveProgress(channel, chatID)
    }, REPLAY_GRACE_MS)
  }

  private async restoreActiveProgress(channel: string, chatID: string): Promise<void> {
    if (this.recoveryInProgress) return
    this.recoveryInProgress = true
    const sessionVersion = this.sessionVersion
    const progressVersion = this.progressVersion
    const recoveryRequestVersion = ++this.recoveryRequestVersion
    const cacheKey = sessionCacheKey(channel, chatID)
    // Snapshot the cached progress BEFORE recovery to detect TurnID changes.
    const cachedProgress = cacheKey ? progressSnapshotCache.get(cacheKey) : undefined
    try {
      // Request iterations NEWER than our local watermark. SSE may have been
      // disconnected while the agent advanced many iterations — we need the
      // delta (iteration > localWatermark) to fill the gap, otherwise the
      // rendered iteration history is non-linear (missing middle iterations).
      //
      // Watermark = the last COMPLETED iteration we already have. Using
      // cachedProgress.iteration (current in-progress iteration) would skip
      // a just-completed iteration with the same number. Derive from
      // iteration_history's last entry; fall back to 0 (all iterations).
      //
      // CRITICAL: use the last entry's iteration number MINUS 1, not the
      // last entry itself. The last entry in iteration_history is the LAST
      // COMPLETED iteration — but the backend's from_iteration filter is
      // `iteration > from_iteration` (exclusive). If we pass the last
      // completed iteration as from_iteration, the backend returns only
      // iterations AFTER it — but the last completed iteration's delta may
      // have been lost during the SSE gap. Using last-1 ensures the last
      // completed iteration is re-fetched (deduped by appendIterations).
      // This fixes "SSE reconnect sometimes loses 1-2 iterations" — the
      // watermark was too high, skipping the last 1-2 completed iterations
      // whose deltas were lost during the disconnect.
      let fromIteration = 0
      if (cachedProgress) {
        const hist = cachedProgress.iteration_history
        if (Array.isArray(hist) && hist.length > 0) {
          const last = hist[hist.length - 1] as { iteration?: number } | undefined
          if (typeof last?.iteration === 'number' && last.iteration > 0) {
            // Use last - 1 to re-fetch the last completed iteration (in case
            // its delta was lost during SSE gap). appendIterations dedups by
            // iteration number, so re-fetching is safe.
            fromIteration = Math.max(0, last.iteration - 1)
          }
        }
      }
      const progress = await this.rpc<ProgressEvent | null>('get_active_progress', {
        channel,
        chat_id: chatID,
        from_iteration: fromIteration,
      })
      if (
        this._channel !== channel ||
        this._chatID !== chatID ||
        this.sessionVersion !== sessionVersion ||
        this.recoveryRequestVersion !== recoveryRequestVersion
      ) return

      // ── Turn ended on the server (or get_active_progress returned null) ──
      // The committed reply (text event) may have been lost during the SSE
      // gap; the DB is authoritative. ALWAYS reload from DB so the complete
      // turn (user + assistant) renders. This must run BEFORE the
      // progressVersion check below: any event arriving during the reconnect
      // window bumps progressVersion, and without this unconditional reload
      // the live row is cleared (phase=done) with no committed replacement —
      // the in-progress turn "vanishes" until a manual refresh (user report:
      // "重连之后 user msg 后进行中的 turn 消失了，刷新才能看到").
      if (!progress || progress.phase === 'done') {
        // Turn ended on the server (or get_active_progress returned null —
        // e.g. an active turn momentarily not registered). The committed
        // reply may have been lost during the SSE gap; the DB is authoritative
        // — reload from it.
        // CRITICAL: do NOT dispatch phase='done' / session(idle) here. They
        // clear the live store (liveMessage returns null on phase='done'), and
        // with a slow reload the already-rendered turn would VANISH until the
        // reload lands (user report: "这后面原本有十几个迭代，突然消失，过一
        // 会出现"). Keep the live row; the reload brings the committed message
        // and buildMessageRows' same-turn merge carries the live iterations
        // over. The sidebar busy state is cleared by the backend's own
        // session(idle) event once the turn really ends.
        this.dispatch({ type: 'replay_gap', chat_id: `${channel}:${chatID}` })
        return
      }
      // progressVersion changed during the fetch: newer events already arrived
      // (SSE replay delivers the live state), so the snapshot restore below
      // would be stale — skip it. The unconditional reload decision above is
      // unaffected (turn is still running here, so no reload needed).
      if (this.progressVersion !== progressVersion) return
      bumpProgressGeneration(cacheKey)
      this.progressVersion += 1

      // ── Detect real data loss: TurnID changed or COARSE iteration gap ──
      // SSE event gaps are normal (stateless coalescing, buffer drops) and the
      // recovery snapshot below covers most of them — progress_structured is a
      // SNAPSHOT, later events supersede earlier ones.
      //
      // This check is deliberately COARSE: cachedProgress.iteration_history is
      // only the LAST event's delta, NOT the cumulative history — it CANNOT
      // prove that iteration 3 is complete when the server is at 4 (a
      // difference of exactly 1 does NOT mean 3's delta arrived; it may have
      // been dropped while 4's events kept coming). The precise completeness
      // check lives in useProgressStream/progressStore (hasIterationGapNow:
      // the store's CUMULATIVE iterationHistory must cover up to lastIter-1)
      // and triggers a reload there. This check only catches large server
      // advances (>1) early as a fast path.
      const turnIDChanged = cachedProgress && progress &&
        typeof cachedProgress.turn_id === 'number' && typeof progress.turn_id === 'number' &&
        cachedProgress.turn_id !== progress.turn_id
      const sameTurn = cachedProgress && progress &&
        typeof cachedProgress.turn_id === 'number' && typeof progress.turn_id === 'number' &&
        cachedProgress.turn_id === progress.turn_id
      const cachedIter = cachedProgress?.iteration ?? 0
      const newIter = progress?.iteration ?? 0
      const iterationGap = sameTurn && cachedIter > 0 && newIter > 0 && (newIter - cachedIter) > 1

      if (turnIDChanged || iterationGap) {
        // force_reload=true: show a loading spinner during reload. For cross-turn
        // and iteration-id gaps, the UI is too stale to render incrementally — a
        // clean reload is better than a partially-inconsistent view.
        this.dispatch({ type: 'replay_gap', chat_id: `${channel}:${chatID}`, metadata: { force_reload: 'true' } })
      }
      // Recovery snapshot — carry its seq so setStructuredTools can apply the
      // stale watermark check (an old snapshot must not roll back a newer
      // live state that SSE already delivered during the reconnect window).
      this.dispatch({
        type: 'progress_structured',
        chat_id: chatID,
        progress,
      })
      // Dispatch busy so the sidebar shows running state after SSE reconnect.
      // Without this, a busy event lost during the SSE gap leaves the sidebar
      // stuck on idle until the next refresh.
      this.dispatch({
        type: 'session',
        session: { channel, chat_id: chatID, action: 'busy' },
      })
    } catch {
      // The next native SSE reconnect or status poll gets another recovery chance.
    } finally {
      this.recoveryInProgress = false
    }
  }

  private startPolling(): void {
    if (this.pollTimer || !this._chatID) return
    this.pollTimer = setInterval(() => {
      void this.pollSessionStatus()
    }, STATUS_POLL_MS)
  }

  private async pollSessionStatus(): Promise<void> {
    if (this.pollRequestToken || !this._chatID) return
    const token = {}
    const chatID = this._chatID
    const channel = this._channel
    const source = this.source
    this.pollRequestToken = token
    try {
      await postAPI('/api/session/status', { channel, chat_id: chatID })
      if (
        this._chatID !== chatID ||
        this._channel !== channel ||
        this._connected ||
        this.source !== source
      ) return
      if (!source || source.readyState === 2) {
        source?.close()
        this.source = null
        this.connect()
      }
    } catch {
      // Continue polling until the native EventSource reconnects.
    } finally {
      if (this.pollRequestToken === token) this.pollRequestToken = null
    }
  }

  private clearPoll(): void {
    if (this.pollTimer) {
      clearInterval(this.pollTimer)
      this.pollTimer = null
    }
    this.pollRequestToken = null
  }

  private clearReplayTimer(): void {
    if (!this.replayTimer) return
    clearTimeout(this.replayTimer)
    this.replayTimer = null
  }

  private setConnected(value: boolean): void {
    if (this._connected === value) return
    this._connected = value
    this.connHandlers.forEach((handler) => handler(value))
  }

  private subscribeHandler<T>(handlers: Set<Handler<T>>, handler: Handler<T>): () => void {
    handlers.add(handler)
    return () => handlers.delete(handler)
  }

  // Stubs for WSConnection interface methods implemented by MultiSSEManager wrapper.
  // SSEConnectionImpl itself manages a single connection; multi-subscription
  // logic lives in MultiSSEManager.
  addSubscription(_chatID: string, _channel: string): string {
    throw new Error('Use MultiSSEManager.addSubscription for multi-connection support')
  }
  removeSubscription(_id: string): void {
    throw new Error('Use MultiSSEManager.removeSubscription for multi-connection support')
  }
}

/**
 * MultiSSEManager — manages multiple SSE connections for concurrent Agent panels.
 *
 * The Web UI opens multiple Agent panels simultaneously (main Agent + SubAgent
 * tabs). Each panel needs its own SSE stream to receive live progress events.
 * The old design used a single EventSource that was "handed off" to the active
 * panel — switching tabs disconnected the non-active panel's stream, freezing
 * its progress display.
 *
 * MultiSSEManager creates one SSEConnectionImpl per (chatID, channel) pair.
 * All SSE connections share the same message/session/progress/connection
 * handlers, so consumers that call `ws.onMessage()` receive events from all
 * connections. Event routing is done via the existing `matchesChatID` 3-layer
 * filter in useProgressStream.
 *
 * The "primary" connection (legacy `subscribe`/`disconnect`/`chatID`/`channel`)
 * is kept for backward compatibility — used by useSessionStore for ask_user
 * routing and by TerminalPanel for its own SSE lifecycle.
 */
export class MultiSSEManager implements WSConnection {
  private primary: SSEConnectionImpl
  private extra = new Map<string, SSEConnectionImpl>()
  private disposed = false

  // Track registered handlers so new connections can be subscribed to them.
  private messageHandlers = new Set<Handler<WSMessage>>()
  private sessionHandlers = new Set<Handler<SessionEvent>>()
  private progressHandlers = new Set<Handler<ProgressEvent>>()
  private connHandlers = new Set<Handler<boolean>>()

  constructor() {
    this.primary = new SSEConnectionImpl()
  }

  get connected(): boolean {
    return this.primary.connected
  }

  get chatID(): string | null {
    return this.primary.chatID
  }

  get channel(): string | null {
    return this.primary.channel
  }

  /** Legacy single-subscribe — delegates to the primary connection. */
  subscribe(chatID: string, channel = 'web'): void {
    this.primary.subscribe(chatID, channel)
  }

  /** Legacy single-disconnect — delegates to the primary connection. */
  disconnect(): void {
    this.primary.disconnect()
  }

  /**
   * Add a persistent SSE subscription for a chatID+channel.
   * If the primary connection already targets this (chatID, channel), no extra
   * connection is created — the primary is reused.
   * Returns a subscription ID for later removal.
   */
  addSubscription(chatID: string, channel: string): string {
    if (this.disposed) return ''

    // If the primary connection is idle (no chatID), use it as the primary sub.
    if (!this.primary.chatID && !this.primary.channel) {
      this.primary.subscribe(chatID, channel)
      return 'primary'
    }

    // If the primary already targets this pair, return it.
    if (this.primary.chatID === chatID && this.primary.channel === channel) {
      return 'primary'
    }

    // Check if an extra connection already exists for this pair.
    const key = `${channel}:${chatID}`
    if (this.extra.has(key)) {
      return key
    }

    // Create a new SSE connection for this pair.
    const conn = new SSEConnectionImpl()
    // Subscribe the new connection to all existing handlers before connecting.
    for (const h of this.messageHandlers) conn.onMessage(h)
    for (const h of this.sessionHandlers) conn.onSession(h)
    for (const h of this.progressHandlers) conn.onProgress(h)
    for (const h of this.connHandlers) conn.onConnectionChange(h)
    conn.subscribe(chatID, channel)
    this.extra.set(key, conn)
    return key
  }

  /** Remove a persistent SSE subscription by its ID. */
  removeSubscription(id: string): void {
    if (id === 'primary') {
      // Disconnect the primary connection back to idle state so it can be
      // reused by the next addSubscription call. Without this, the primary
      // SSE connection stays open after the panel closes, leaking resources.
      this.primary.disconnect()
      return
    }
    const conn = this.extra.get(id)
    if (conn) {
      conn.dispose()
      this.extra.delete(id)
    }
  }

  async send(msg: WSClientMessage): Promise<SendMessageResponse | void> {
    return this.primary.send(msg)
  }

  rpc<T = unknown>(method: string, params?: unknown): Promise<T> {
    return this.primary.rpc(method, params)
  }

  setLastSeq(chatID: string, seq: number, channel?: string): void {
    this.primary.setLastSeq(chatID, seq, channel)
  }

  onMessage = (handler: Handler<WSMessage>): (() => void) => {
    this.messageHandlers.add(handler)
    const unsubPrimary = this.primary.onMessage(handler)
    const unsubs: (() => void)[] = [unsubPrimary]
    for (const conn of this.extra.values()) {
      unsubs.push(conn.onMessage(handler))
    }
    return () => {
      this.messageHandlers.delete(handler)
      unsubs.forEach((u) => u())
    }
  }

  onSession = (handler: Handler<SessionEvent>): (() => void) => {
    this.sessionHandlers.add(handler)
    const unsubPrimary = this.primary.onSession(handler)
    const unsubs: (() => void)[] = [unsubPrimary]
    for (const conn of this.extra.values()) {
      unsubs.push(conn.onSession(handler))
    }
    return () => {
      this.sessionHandlers.delete(handler)
      unsubs.forEach((u) => u())
    }
  }

  onProgress = (handler: Handler<ProgressEvent>): (() => void) => {
    this.progressHandlers.add(handler)
    const unsubPrimary = this.primary.onProgress(handler)
    const unsubs: (() => void)[] = [unsubPrimary]
    for (const conn of this.extra.values()) {
      unsubs.push(conn.onProgress(handler))
    }
    return () => {
      this.progressHandlers.delete(handler)
      unsubs.forEach((u) => u())
    }
  }

  onConnectionChange = (handler: Handler<boolean>): (() => void) => {
    this.connHandlers.add(handler)
    // Only route the primary connection's state to consumers.
    // Extra connections (per-panel SSE) should not trigger global UI
    // disconnect/reconnect overlays — only the primary matters.
    const unsubPrimary = this.primary.onConnectionChange(handler)
    return () => {
      this.connHandlers.delete(handler)
      unsubPrimary()
    }
  }

  dispose(): void {
    if (this.disposed) return
    this.disposed = true
    this.primary.dispose()
    for (const conn of this.extra.values()) {
      conn.dispose()
    }
    this.extra.clear()
    this.messageHandlers.clear()
    this.sessionHandlers.clear()
    this.progressHandlers.clear()
    this.connHandlers.clear()
  }
}

function sessionBody(msg: WSClientMessage): { channel?: string; chat_id?: string } {
  return { channel: msg.channel, chat_id: msg.chat_id }
}

function parseSequence(raw: string): number {
  const value = Number.parseInt(raw, 10)
  return Number.isFinite(value) ? value : 0
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

function newMessageRequestID(): string {
  const id = globalThis.crypto?.randomUUID?.()
  return id ? id.replaceAll('-', '') : `web-${Date.now()}-${Math.random().toString(36).slice(2)}`
}

function isProgressLifecycleEvent(msg: WSMessage): boolean {
  if (
    msg.type === 'progress_structured' ||
    msg.type === 'sync_progress' ||
    msg.type === 'text'
  ) return true
  if (msg.type !== 'session') return false
  return ['busy', 'idle', 'deleted', 'HistoryCompacted'].includes(msg.session?.action ?? '')
}

function isTerminalProgressEvent(msg: WSMessage): boolean {
  if (msg.type === 'text') return true
  if (msg.progress?.phase === 'done') return true
  if (msg.type !== 'session') return false
  return ['busy', 'idle', 'deleted', 'HistoryCompacted'].includes(msg.session?.action ?? '')
}
