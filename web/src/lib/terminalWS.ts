/**
 * terminalWS — one terminal data channel over `/ws/terminal?tid=<tid>`.
 *
 * Plain class (no React) so it survives re-renders and is testable. Owns a
 * single WebSocket to the backend PTY pump. The TerminalPanel creates one
 * instance on mount and disposes it on unmount.
 *
 * Responsibilities:
 *   - connect on create, exponential-backoff reconnect on transient drop
 *   - stdin: base64-encode bytes the user types → `{ type:'stdin', data }`
 *   - stdout/stderr: base64-decode PTY bytes → Uint8Array for xterm.write()
 *   - resize: forward `{ type:'resize', cols, rows }` when the panel resizes
 *   - close: send `{ type:'close' }` (server destroys the PTY) then close socket
 *
 * The backend keeps a PTY alive for an idle grace window after a transient WS
 * disconnect (channel/web/web_pty.go serveTerminalWS), so reconnecting within
 * that window resumes the same terminal. On an explicit `close` or PTY `exit`
 * the backend destroys the terminal and no reconnect is attempted.
 *
 * base64 is used for data because PTY output may contain non-UTF-8 bytes that
 * would be mangled by JSON string encoding.
 */
import type {
  TerminalClientMessage,
  TerminalServerMessage,
} from '@/types/terminal'

const RECONNECT_BASE_MS = 500
const RECONNECT_MAX_MS = 15_000
/** Cap reconnect attempts so a permanently-gone terminal doesn't spin forever. */
const MAX_RECONNECT_ATTEMPTS = 6

/** Callbacks the panel wires up to drive xterm + status display. */
export interface TerminalWSCallbacks {
  /** PTY produced bytes (decoded). Write straight to xterm. */
  onStdout: (data: Uint8Array) => void
  /** PTY stderr bytes (decoded), if the server ever splits stderr. */
  onStderr: (data: Uint8Array) => void
  /** PTY process exited with `code`. Terminal is done. */
  onExit: (code: number) => void
  /** Server reported an error (e.g. "terminal not found" after idle reap). */
  onError: (message: string) => void
  /** Connection opened (or re-opened after a transient drop). */
  onOpen: () => void
  /** Connection closed. `willReconnect` is true if a reconnect is scheduled. */
  onClose: (willReconnect: boolean) => void
}

/** Build the absolute ws:// or wss:// URL for `/ws/terminal?tid=<tid>`. */
function buildTerminalWSUrl(tid: string): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${window.location.host}/ws/terminal?tid=${encodeURIComponent(tid)}`
}

/* ── base64 helpers (binary-safe; atob/btoa are Latin-1) ── */

/** Encode a byte array (or string of UTF-8) to a base64 string. */
export function bytesToBase64(input: Uint8Array | string): string {
  const bytes =
    typeof input === 'string'
      ? new TextEncoder().encode(input)
      : input
  let bin = ''
  for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i])
  return btoa(bin)
}

/** Decode a base64 string to a byte array (for xterm.write, which is binary-safe). */
export function base64ToBytes(b64: string): Uint8Array {
  const bin = atob(b64)
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out
}

export class TerminalWS {
  private ws: WebSocket | null = null
  private tid: string
  private cbs: TerminalWSCallbacks
  private disposed = false
  /** True after the PTY exited — suppresses reconnect. */
  private exited = false
  /** True after an explicit close() — suppresses reconnect. */
  private explicitClose = false
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null
  private reconnectAttempt = 0
  /** Pending resize sent before the socket opened; flushed on connect. */
  private pendingResize: { cols: number; rows: number } | null = null

  constructor(tid: string, cbs: TerminalWSCallbacks) {
    this.tid = tid
    this.cbs = cbs
    this.connect()
  }

  /** Whether the underlying socket is open and ready for data. */
  get isOpen(): boolean {
    return this.ws !== null && this.ws.readyState === WebSocket.OPEN
  }

  /** Send stdin bytes (from xterm.onData). No-ops when not open. */
  sendStdin(data: Uint8Array | string): void {
    if (!this.isOpen) return
    const msg: TerminalClientMessage = { type: 'stdin', data: bytesToBase64(data) }
    this.ws!.send(JSON.stringify(msg))
  }

  /** Send a resize. Buffered until the socket is open (initial fit before WS). */
  resize(cols: number, rows: number): void {
    if (this.isOpen) {
      const msg: TerminalClientMessage = { type: 'resize', cols, rows }
      this.ws!.send(JSON.stringify(msg))
    } else {
      this.pendingResize = { cols, rows }
    }
  }

  /** Explicitly close: tell the server to destroy the PTY, then close. */
  close(): void {
    this.explicitClose = true
    this.cancelReconnect()
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      try {
        this.ws.send(JSON.stringify({ type: 'close' } satisfies TerminalClientMessage))
      } catch {
        /* ignore */
      }
    }
    this.teardownSocket()
  }

  /** Disconnect WS without sending close to backend (terminal persists). */
  disconnect(): void {
    this.explicitClose = true // suppress reconnect
    this.cancelReconnect()
    this.teardownSocket()
  }

  /** Tear everything down without sending a close frame (e.g. parent unmount). */
  dispose(): void {
    this.disposed = true
    this.explicitClose = true
    this.cancelReconnect()
    this.teardownSocket()
  }

  /* ── internals ── */

  private connect(): void {
    if (this.disposed || this.exited || this.explicitClose) return
    if (typeof WebSocket === 'undefined') return // SSR / test guard
    let ws: WebSocket
    try {
      ws = new WebSocket(buildTerminalWSUrl(this.tid))
    } catch {
      this.scheduleReconnect()
      return
    }
    this.ws = ws

    ws.onopen = () => {
      this.reconnectAttempt = 0
      this.cbs.onOpen()
      // Flush a resize that was queued before the socket opened.
      if (this.pendingResize) {
        this.resize(this.pendingResize.cols, this.pendingResize.rows)
        this.pendingResize = null
      }
    }
    ws.onmessage = (ev) => this.handleMessage(ev)
    ws.onerror = () => {
      // The close handler drives reconnect; errors only signal failure.
    }
    ws.onclose = () => {
      this.ws = null
      if (this.disposed || this.explicitClose || this.exited) {
        this.cbs.onClose(false)
        return
      }
      this.cbs.onClose(true)
      this.scheduleReconnect()
    }
  }

  private scheduleReconnect(): void {
    if (this.disposed || this.exited || this.explicitClose) return
    if (this.reconnectAttempt >= MAX_RECONNECT_ATTEMPTS) return
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer)
    const delay = Math.min(
      RECONNECT_BASE_MS * 2 ** this.reconnectAttempt,
      RECONNECT_MAX_MS,
    )
    this.reconnectAttempt += 1
    this.reconnectTimer = setTimeout(() => this.connect(), delay)
  }

  private cancelReconnect(): void {
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer)
      this.reconnectTimer = null
    }
  }

  private teardownSocket(): void {
    if (this.ws) {
      this.ws.onopen = null
      this.ws.onmessage = null
      this.ws.onerror = null
      this.ws.onclose = null
      try {
        if (
          this.ws.readyState === WebSocket.OPEN ||
          this.ws.readyState === WebSocket.CONNECTING
        ) {
          this.ws.close()
        }
      } catch {
        /* ignore */
      }
      this.ws = null
    }
  }

  private handleMessage(ev: MessageEvent): void {
    let msg: TerminalServerMessage
    try {
      msg = JSON.parse(typeof ev.data === 'string' ? ev.data : String(ev.data))
    } catch {
      return // ignore malformed frames
    }
    switch (msg.type) {
      case 'stdout':
        this.cbs.onStdout(base64ToBytes(msg.data))
        return
      case 'stderr':
        this.cbs.onStderr(base64ToBytes(msg.data))
        return
      case 'exit':
        this.exited = true
        this.cbs.onExit(msg.code)
        return
      case 'error':
        // "terminal not found" after idle reap → stop reconnecting.
        if (/not found|no longer/i.test(msg.message)) this.exited = true
        this.cbs.onError(msg.message)
        return
    }
  }
}
