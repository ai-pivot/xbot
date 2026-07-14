/**
 * WSConnectionImpl — the imperative WebSocket connection used by WSProvider.
 *
 * Owns a single WebSocket to `/ws` (browser cookie auth, no token needed).
 * Responsibilities (Spec 2 §3.4):
 *   - auto-connect on create, exponential-backoff reconnect on close/error
 *   - keepalive relies on the server's 30s ping (channel/web/web.go writePump);
 *     the browser auto-replies Pong, refreshing the 120s read deadline. No
 *     application-layer heartbeat is needed — KISS.
 *   - message fan-out: text → onMessage, session → onSession,
 *     progress_structured/stream_content → onProgress, rpc_response → pending RPC
 *   - RPC: generate unique id, send {type:'rpc',id,method,params}, resolve by id, 30s timeout
 *   - subscribe: send {type:'subscribe',chat_id}, track current chatID
 *
 * This is a plain class (no React) so it survives re-renders and is testable.
 * The provider creates one instance and tears it down on unmount.
 */
import type {
  ProgressEvent,
  SessionEvent,
  WSClientMessage,
  WSMessage,
} from '@/types/shared'
import type { WSConnection } from '@/types/ws'

const RPC_TIMEOUT_MS = 30_000
const RECONNECT_BASE_MS = 500
const RECONNECT_MAX_MS = 15_000

interface PendingRPC {
  resolve: (value: unknown) => void
  reject: (reason: Error) => void
  timer: ReturnType<typeof setTimeout>
}

type Handler<T> = (payload: T) => void

/** Build the absolute ws:// or wss:// URL for `/ws` from the current location. */
function buildWSUrl(): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${window.location.host}/ws`
}

/** Monotonic RPC id generator (no Math.random to stay deterministic). */
let rpcSeq = 0
function nextRpcId(): string {
  rpcSeq += 1
  return `rpc-${Date.now()}-${rpcSeq}`
}

export class WSConnectionImpl implements WSConnection {
  private ws: WebSocket | null = null
  private _connected = false
  private _chatID: string | null = null
  private _lastSeq = 0

  private reconnectTimer: ReturnType<typeof setTimeout> | null = null
  private reconnectAttempt = 0
  private disposed = false

  private pending = new Map<string, PendingRPC>()
  /** Messages queued while WS is not OPEN; flushed on reconnect. */
  private pendingMessages: WSClientMessage[] = []

  private messageHandlers = new Set<Handler<WSMessage>>()
  private sessionHandlers = new Set<Handler<SessionEvent>>()
  private progressHandlers = new Set<Handler<ProgressEvent>>()
  private connHandlers = new Set<Handler<boolean>>()

  constructor() {
    this.connect()
  }

  /* ── public WSConnection surface ── */

  get connected(): boolean {
    return this._connected
  }

  get chatID(): string | null {
    return this._chatID
  }

  setLastSeq(_chatID: string, seq: number, _channel?: string): void {
    this._lastSeq = seq
  }

  async send(msg: WSClientMessage): Promise<void> {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
      // Buffer message — will be flushed on reconnect (prevents silent drops).
      // Dedup: subscribe/sync are idempotent; only buffer 'message' type.
      if (msg.type === 'message') {
        this.pendingMessages.push(msg)
      }
      return
    }
    this.ws.send(JSON.stringify(msg))
  }

  subscribe(chatID: string): void {
    this._chatID = chatID
    this.send({ type: 'subscribe', chat_id: chatID })
  }

  get channel(): string | null {
    return 'web'
  }

  disconnect(): void {
    if (this.ws) {
      this.ws.onclose = null
      this.ws.onerror = null
      this.ws.onmessage = null
      this.ws.onopen = null
      try { this.ws.close() } catch { /* ignore */ }
      this.ws = null
    }
    this._connected = false
  }

  // Stubs for multi-subscription interface (WSConnectionImpl is single-connection).
  addSubscription(_chatID: string, _channel: string): string {
    throw new Error('WSConnectionImpl does not support multi-subscriptions')
  }
  removeSubscription(_id: string): void {
    // no-op
  }

  rpc<T = unknown>(method: string, params?: unknown): Promise<T> {
    return new Promise<T>((resolve, reject) => {
      const id = nextRpcId()
      const timer = setTimeout(() => {
        this.pending.delete(id)
        reject(new Error(`RPC "${method}" timed out after ${RPC_TIMEOUT_MS}ms`))
      }, RPC_TIMEOUT_MS)
      this.pending.set(id, { resolve: resolve as (v: unknown) => void, reject, timer })
      this.send({ type: 'rpc', id, method, params: params ?? {} })
    })
  }

  onMessage = (h: Handler<WSMessage>) => this.subscribeHandler(this.messageHandlers, h)
  onSession = (h: Handler<SessionEvent>) => this.subscribeHandler(this.sessionHandlers, h)
  onProgress = (h: Handler<ProgressEvent>) => this.subscribeHandler(this.progressHandlers, h)
  onConnectionChange = (h: Handler<boolean>) => this.subscribeHandler(this.connHandlers, h)

  /* ── lifecycle ── */

  /** Tear everything down. Safe to call once. */
  dispose(): void {
    this.disposed = true
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer)
      this.reconnectTimer = null
    }
    if (this.ws) {
      this.ws.onclose = null
      this.ws.onerror = null
      this.ws.onmessage = null
      this.ws.onopen = null
      try { this.ws.close() } catch { /* ignore */ }
      this.ws = null
    }
    this.pending.forEach((p) => { clearTimeout(p.timer); p.reject(new Error('connection disposed')) })
    this.pending.clear()
    this.setConnected(false, /* notify */ false)
    this.messageHandlers.clear()
    this.sessionHandlers.clear()
    this.progressHandlers.clear()
    this.connHandlers.clear()
  }

  /* ── internals ── */

  private subscribeHandler<T>(set: Set<Handler<T>>, h: Handler<T>): () => void {
    set.add(h)
    return () => set.delete(h)
  }

  private connect(): void {
    if (this.disposed) return
    if (typeof WebSocket === 'undefined') return // SSR / test guard
    let ws: WebSocket
    try {
      ws = new WebSocket(buildWSUrl())
    } catch {
      this.scheduleReconnect()
      return
    }
    this.ws = ws

    ws.onopen = () => {
      this.reconnectAttempt = 0
      this.setConnected(true)
      // Send sync handshake with last_seq for incremental replay.
      // Omitted or 0 = full replay (backward compatible with old server).
      this.send({ type: 'sync', last_seq: this._lastSeq || undefined })
      // Re-establish subscription after reconnect so events resume.
      if (this._chatID) this.send({ type: 'subscribe', chat_id: this._chatID })
      // Flush any messages that were queued while WS was disconnected.
      if (this.pendingMessages.length > 0) {
        const pending = this.pendingMessages.splice(0)
        for (const msg of pending) {
          if (this.ws && this.ws.readyState === WebSocket.OPEN) {
            this.ws.send(JSON.stringify(msg))
          }
        }
      }
    }
    ws.onmessage = (ev) => this.handleMessage(ev)
    ws.onerror = () => {
      // The close handler drives reconnect; errors only signal failure.
    }
    ws.onclose = () => {
      this.setConnected(false)
      this.failPending('connection closed')
      this.scheduleReconnect()
    }
  }

  private scheduleReconnect(): void {
    if (this.disposed) return
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer)
    const delay = Math.min(
      RECONNECT_BASE_MS * 2 ** this.reconnectAttempt,
      RECONNECT_MAX_MS,
    )
    this.reconnectAttempt += 1
    this.reconnectTimer = setTimeout(() => this.connect(), delay)
  }

  private setConnected(value: boolean, notify = true): void {
    if (this._connected === value) return
    this._connected = value
    if (notify) this.connHandlers.forEach((h) => h(value))
  }

  private failPending(reason: string): void {
    this.pending.forEach((p, id) => {
      clearTimeout(p.timer)
      this.pending.delete(id)
      p.reject(new Error(reason))
    })
  }

  private handleMessage(ev: MessageEvent): void {
    let msg: WSMessage
    try {
      msg = JSON.parse(typeof ev.data === 'string' ? ev.data : String(ev.data))
    } catch {
      return // ignore malformed frames
    }
    this.dispatch(msg)
  }

  private dispatch(msg: WSMessage): void {
    switch (msg.type) {
      case '__pong__':
        return // heartbeat ack — no fan-out

      case 'rpc_response': {
        const pending = msg.id ? this.pending.get(msg.id) : undefined
        if (!pending) return
        clearTimeout(pending.timer)
        this.pending.delete(msg.id!)
        if (msg.error) pending.reject(new Error(msg.error))
        else pending.resolve(msg.result)
        return
      }

      case 'session':
        if (msg.session) this.sessionHandlers.forEach((h) => h(msg.session!))
        this.messageHandlers.forEach((h) => h(msg))
        return

      case 'progress_structured':
      case 'stream_content':
        // Both types carry their payload in msg.progress (per protocol/ws.go).
        // stream_content's content is in progress.stream_content, not top-level.
        if (msg.progress) this.progressHandlers.forEach((h) => h(msg.progress!))
        this.messageHandlers.forEach((h) => h(msg))
        return

      default:
        this.messageHandlers.forEach((h) => h(msg))
    }
  }
}
