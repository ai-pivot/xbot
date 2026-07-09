/**
 * WSConnection — the connection surface exposed by useWSConnection (Spec 2).
 *
 * Mirrors the shared contract in the main design spec §6.3. Lives in a separate
 * type module so both the hook and the provider can import it without pulling
 * React (kept KISS — no barrel re-exports).
 */
import type {
  ProgressEvent,
  SessionEvent,
  WSClientMessage,
  WSMessage,
} from './shared'

export interface WSConnection {
  /** True when the WebSocket is open and authenticated. */
  connected: boolean
  /** Send a client → server message. No-ops when disconnected. */
  send: (msg: WSClientMessage) => void
  /** Subscribe the connection to a business chatID (server routes events). */
  subscribe: (chatID: string) => void
  /** Issue an RPC; resolves with the server result or rejects on error/timeout. */
  rpc: <T = unknown>(method: string, params?: unknown) => Promise<T>
  /** The chatID currently subscribed, if any. */
  chatID: string | null
  /** Set the last event seq (from history API) for incremental WS reconnect replay. */
  setLastSeq: (seq: number) => void

  /** Stream subscriptions; each returns an unsubscribe function. */
  onMessage: (handler: (msg: WSMessage) => void) => () => void
  onSession: (handler: (event: SessionEvent) => void) => () => void
  onProgress: (handler: (event: ProgressEvent) => void) => () => void
  /** Fired whenever the connection state flips. */
  onConnectionChange: (handler: (connected: boolean) => void) => () => void
}
