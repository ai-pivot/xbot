/**
 * WSConnection — compatibility-named REST + SSE surface exposed by the provider.
 *
 * The name remains stable for existing consumers; no browser WebSocket backs it.
 */
import type {
  ProgressEvent,
  SessionEvent,
  WSClientMessage,
  WSMessage,
} from './shared'

export interface WSConnection {
  /** True while the active session's SSE stream is open. */
  connected: boolean
  /** Map a client operation to its REST endpoint. */
  send: (msg: WSClientMessage) => Promise<void>
  /** Open one EventSource for a business chatID, closing the previous stream. */
  subscribe: (chatID: string, channel?: string) => void
  /** Close the active EventSource. */
  disconnect: () => void
  /** Issue a REST RPC and return its unwrapped data. */
  rpc: <T = unknown>(method: string, params?: unknown) => Promise<T>
  /** The chatID currently subscribed, if any. */
  chatID: string | null
  /** The channel of the current SSE subscription, if any. */
  channel: string | null
  /** Set the last event seq from the history snapshot for SSE deduplication. */
  setLastSeq: (chatID: string, seq: number, channel?: string) => void

  /** Stream subscriptions; each returns an unsubscribe function. */
  onMessage: (handler: (msg: WSMessage) => void) => () => void
  onSession: (handler: (event: SessionEvent) => void) => () => void
  onProgress: (handler: (event: ProgressEvent) => void) => () => void
  /** Fired whenever the connection state flips. */
  onConnectionChange: (handler: (connected: boolean) => void) => () => void
}
