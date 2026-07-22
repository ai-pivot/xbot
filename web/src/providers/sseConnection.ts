import { postAPI } from '@/lib/api'
import {
  bumpProgressGeneration,
  clearProgressSnapshot,
  getLastSeq,
  hasLastSeq,
  progressSnapshotCache,
  resetLastSeq,
  sessionCacheKey,
  setLastSeq,
} from '@/lib/webCache'
import type {
  ProgressEvent,
  SessionEvent,
  WSClientMessage,
  WSMessage,
} from '@/types/shared'
import type { WSConnection } from '@/types/ws'

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
    if (
      (!hadCursor || seq > previousSeq) &&
      this._chatID === chatID &&
      this._channel === channel &&
      this.source
    ) this.restartSource()
  }

  async send(msg: WSClientMessage): Promise<void> {
    switch (msg.type) {
      case 'message':
        await this.sendMessageWithRetry(msg)
        return
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
        this.handleEvent(eventType, event as MessageEvent<string>, channel, chatID)
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
    this.setConnected(false)
    this.connect()
  }

  private handleEvent(eventType: string, event: MessageEvent<string>, sourceChannel: string, sourceChatID: string): void {
    let msg: WSMessage
    try {
      msg = JSON.parse(event.data) as WSMessage
    } catch {
      return
    }
    msg.type = eventType
    annotateSourceIdentity(msg, sourceChannel, sourceChatID)
    const unsequencedResync =
      msg.type === 'resync_required' && !(typeof msg.seq === 'number' && msg.seq > 0)
    const seq = unsequencedResync ? 0 : (msg.seq ?? parseSequence(event.lastEventId))
    const chatID = this._chatID
    const channel = this._channel
    const cacheKey = chatID ? sessionCacheKey(channel, chatID) : null
    let replayGap = false
    let gapFromSeq = 0
    if (cacheKey && unsequencedResync) {
      resetLastSeq(cacheKey)
      setLastSeq(cacheKey, parseSequence(msg.metadata?.baseline_seq ?? ''))
    }
    if (cacheKey && seq > 0) {
      let previousSeq = getLastSeq(cacheKey)
      if (seq < previousSeq) {
        resetLastSeq(cacheKey)
        previousSeq = 0
      } else if (seq === previousSeq) {
        return
      }
      if (seq > previousSeq + 1 && msg.type !== 'resync_required') {
        replayGap = true
        gapFromSeq = previousSeq
      }
      msg.seq = seq
      setLastSeq(cacheKey, seq)
    }
    this.eventsSinceOpen += 1
    if (cacheKey && isProgressLifecycleEvent(msg)) {
      this.progressVersion += 1
      bumpProgressGeneration(cacheKey)
    }
    this.dispatch(msg)
    if (chatID && replayGap) {
      this.dispatch({
        type: 'history_gap',
        channel,
        chat_id: chatID,
        metadata: {
          from_seq: String(gapFromSeq),
          to_seq: String(seq),
        },
      })
      void this.restoreActiveProgress(channel, chatID)
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

  private async sendMessageWithRetry(msg: WSClientMessage): Promise<void> {
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
        await postAPI('/api/message', body)
        return
      } catch (error) {
        if (attempt === SEND_RETRY_DELAYS_MS.length) throw error
        await delay(SEND_RETRY_DELAYS_MS[attempt])
      }
    }
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
    const sessionVersion = this.sessionVersion
    const progressVersion = this.progressVersion
    const recoveryRequestVersion = ++this.recoveryRequestVersion
    try {
      const progress = await this.rpc<ProgressEvent | null>('get_active_progress', {
        channel,
        chat_id: chatID,
      })
      if (
        this._channel !== channel ||
        this._chatID !== chatID ||
        this.sessionVersion !== sessionVersion ||
        this.progressVersion !== progressVersion ||
        this.recoveryRequestVersion !== recoveryRequestVersion
      ) return
      bumpProgressGeneration(sessionCacheKey(channel, chatID))
      this.progressVersion += 1
      if (!progress || progress.phase === 'done') {
        this.dispatch({
          type: 'progress_structured',
          channel,
          chat_id: chatID,
          progress: { phase: 'done', chat_id: chatID },
        })
        return
      }
      this.dispatch({
        type: 'progress_structured',
        channel,
        chat_id: chatID,
        progress: { ...progress, chat_id: progress.chat_id ?? chatID },
      })
    } catch {
      // The next native SSE reconnect or status poll gets another recovery chance.
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
 * connections. Each connection annotates events with its source identity, and
 * consumers require an exact channel+chat match.
 *
 * The "primary" connection (legacy `subscribe`/`disconnect`/`chatID`/`channel`)
 * is kept for backward compatibility — used by useSessionStore for ask_user
 * routing and by TerminalPanel for its own SSE lifecycle.
 */
export class MultiSSEManager implements WSConnection {
  private primary: SSEConnectionImpl
  private extra = new Map<string, { connection: SSEConnectionImpl; refs: number }>()
  private subscriptions = new Map<string, { kind: 'primary' | 'extra'; key: string }>()
  private primaryRefs = 0
  private subscriptionSeq = 0
  private legacyPrimaryActive = false
  private disposed = false

  // Track registered handlers so new connections can be subscribed to them.
  private messageHandlers = new Set<Handler<WSMessage>>()
  private sessionHandlers = new Set<Handler<SessionEvent>>()
  private progressHandlers = new Set<Handler<ProgressEvent>>()
  private connHandlers = new Set<Handler<boolean>>()

  constructor() {
    this.primary = new SSEConnectionImpl()
    this.attachConnection(this.primary, true)
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
    if (this.primaryRefs > 0 && (this.primary.chatID !== chatID || this.primary.channel !== channel)) {
      this.migratePrimarySubscriptions()
    }
    this.legacyPrimaryActive = true
    this.primary.subscribe(chatID, channel)
  }

  /** Legacy single-disconnect — delegates to the primary connection. */
  disconnect(): void {
    this.legacyPrimaryActive = false
    if (this.primaryRefs > 0) this.migratePrimarySubscriptions()
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
    const token = `subscription-${++this.subscriptionSeq}`
    const key = `${channel}:${chatID}`

    // If the primary connection is idle (no chatID), use it as the primary sub.
    if (!this.primary.chatID && !this.primary.channel) {
      this.primary.subscribe(chatID, channel)
      this.primaryRefs += 1
      this.subscriptions.set(token, { kind: 'primary', key })
      return token
    }

    // If the primary already targets this pair, share it with reference counting.
    if (this.primary.chatID === chatID && this.primary.channel === channel) {
      this.primaryRefs += 1
      this.subscriptions.set(token, { kind: 'primary', key })
      return token
    }

    const entry = this.ensureExtraConnection(key, channel, chatID)
    entry.refs += 1
    this.subscriptions.set(token, { kind: 'extra', key })
    return token
  }

  /** Remove a persistent SSE subscription by its ID. */
  removeSubscription(id: string): void {
    const subscription = this.subscriptions.get(id)
    if (!subscription) return
    this.subscriptions.delete(id)
    if (subscription.kind === 'primary') {
      this.primaryRefs = Math.max(0, this.primaryRefs - 1)
      if (this.primaryRefs === 0 && !this.legacyPrimaryActive) this.primary.disconnect()
      return
    }
    const entry = this.extra.get(subscription.key)
    if (!entry) return
    entry.refs = Math.max(0, entry.refs - 1)
    if (entry.refs === 0) {
      entry.connection.dispose()
      this.extra.delete(subscription.key)
    }
  }

  async send(msg: WSClientMessage): Promise<void> {
    return this.primary.send(msg)
  }

  rpc<T = unknown>(method: string, params?: unknown): Promise<T> {
    return this.primary.rpc(method, params)
  }

  setLastSeq(chatID: string, seq: number, channel?: string): void {
    const targetChannel = channel ?? this.primary.channel ?? 'web'
    if (this.primary.chatID === chatID && this.primary.channel === targetChannel) {
      this.primary.setLastSeq(chatID, seq, targetChannel)
      return
    }
    const target = this.extra.get(`${targetChannel}:${chatID}`)?.connection
    if (target) {
      target.setLastSeq(chatID, seq, targetChannel)
      return
    }
    this.primary.setLastSeq(chatID, seq, targetChannel)
  }

  onMessage = (handler: Handler<WSMessage>): (() => void) => {
    this.messageHandlers.add(handler)
    return () => this.messageHandlers.delete(handler)
  }

  onSession = (handler: Handler<SessionEvent>): (() => void) => {
    this.sessionHandlers.add(handler)
    return () => this.sessionHandlers.delete(handler)
  }

  onProgress = (handler: Handler<ProgressEvent>): (() => void) => {
    this.progressHandlers.add(handler)
    return () => this.progressHandlers.delete(handler)
  }

  onConnectionChange = (handler: Handler<boolean>): (() => void) => {
    this.connHandlers.add(handler)
    return () => this.connHandlers.delete(handler)
  }

  dispose(): void {
    if (this.disposed) return
    this.disposed = true
    this.primary.dispose()
    for (const entry of this.extra.values()) {
      entry.connection.dispose()
    }
    this.extra.clear()
    this.messageHandlers.clear()
    this.sessionHandlers.clear()
    this.progressHandlers.clear()
    this.connHandlers.clear()
    this.subscriptions.clear()
    this.primaryRefs = 0
  }

  private attachConnection(connection: SSEConnectionImpl, includeConnectionState: boolean): void {
    connection.onMessage((message) => this.messageHandlers.forEach((handler) => handler(message)))
    connection.onSession((event) => this.sessionHandlers.forEach((handler) => handler(event)))
    connection.onProgress((event) => this.progressHandlers.forEach((handler) => handler(event)))
    if (includeConnectionState) {
      connection.onConnectionChange((connected) => this.connHandlers.forEach((handler) => handler(connected)))
    }
  }

  private ensureExtraConnection(key: string, channel: string, chatID: string): { connection: SSEConnectionImpl; refs: number } {
    const existing = this.extra.get(key)
    if (existing) return existing
    const connection = new SSEConnectionImpl()
    this.attachConnection(connection, false)
    connection.subscribe(chatID, channel)
    const entry = { connection, refs: 0 }
    this.extra.set(key, entry)
    return entry
  }

  private migratePrimarySubscriptions(): void {
    const chatID = this.primary.chatID
    const channel = this.primary.channel
    if (!chatID || !channel || this.primaryRefs === 0) return
    const key = `${channel}:${chatID}`
    const entry = this.ensureExtraConnection(key, channel, chatID)
    entry.refs += this.primaryRefs
    for (const subscription of this.subscriptions.values()) {
      if (subscription.kind === 'primary') subscription.kind = 'extra'
    }
    this.primaryRefs = 0
    this.primary.disconnect()
  }
}

function sessionBody(msg: WSClientMessage): { channel?: string; chat_id?: string } {
  return { channel: msg.channel, chat_id: msg.chat_id }
}

function annotateSourceIdentity(msg: WSMessage, channel: string, chatID: string): void {
  msg.channel ??= channel
  msg.chat_id ??= chatID
  if (msg.session) {
    msg.session.channel ??= channel
    msg.session.chat_id ??= chatID
  }
  if (msg.progress) {
    msg.progress.chat_id ??= chatID
  }
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
  if (msg.type === 'resync_required') return true
  if (
    msg.type === 'stream_content' ||
    msg.type === 'progress_structured' ||
    msg.type === 'sync_progress' ||
    msg.type === 'text'
  ) return true
  if (msg.type !== 'session') return false
  return ['busy', 'idle', 'deleted', 'HistoryCompacted', 'history_rewound'].includes(msg.session?.action ?? '')
}

function isTerminalProgressEvent(msg: WSMessage): boolean {
  if (msg.type === 'resync_required') return true
  if (msg.type === 'text') return true
  if (msg.progress?.phase === 'done') return true
  if (msg.type !== 'session') return false
  return ['busy', 'idle', 'deleted', 'HistoryCompacted', 'history_rewound'].includes(msg.session?.action ?? '')
}
