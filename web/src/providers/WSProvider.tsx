/**
 * WSProvider — compatibility-named provider for the REST + SSE connection.
 *
 * Wrap the app once (inside ThemeProvider/I18nProvider). The active session
 * opens the EventSource; native reconnects update `connected` while the
 * underlying connection instance remains stable across renders.
 *
 * CRITICAL: The `value` object is created ONCE and never changes identity.
 * Reactive properties (`connected`, `chatID`, `channel`) use getters that read
 * from refs updated by the connection lifecycle. This prevents all downstream
 * hooks from re-running their effects on every connection state change —
 * which caused SSE handler re-registration gaps (missing TODO events) and
 * spurious SSE subscription churn.
 */
import {
  createContext,
  useContext,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from 'react'
import { MultiSSEManager } from '@/providers/sseConnection'
import type { WSConnection } from '@/types/ws'

export const WSContext = createContext<WSConnection | undefined>(undefined)

export function WSProvider({ children }: { children: ReactNode }) {
  // One connection for the provider's lifetime; never recreated on re-render.
  const connRef = useRef<MultiSSEManager | null>(null)
  if (connRef.current === null) {
    connRef.current = new MultiSSEManager()
  }
  const conn = connRef.current

  // Reactive state — triggers re-renders so consumers read fresh values.
  // These are NOT included in the value object's identity.
  const [connected, setConnected] = useState(conn.connected)
  const [, setTargetTick] = useState(0)

  // Refs that the stable value object reads from.
  const stateRef = useRef({ connected: conn.connected, chatID: conn.chatID, channel: conn.channel })
  stateRef.current.connected = connected
  stateRef.current.chatID = conn.chatID
  stateRef.current.channel = conn.channel

  useEffect(() => {
    const offConn = conn.onConnectionChange((v) => {
      stateRef.current.connected = v
      setConnected(v)
    })
    setConnected(conn.connected)
    return () => {
      offConn()
      conn.dispose()
      connRef.current = null
    }
  }, [conn])

  // Create the value object ONCE. Its identity never changes.
  // Methods delegate to `conn` (stable). Reactive properties use getters.
  const valueRef = useRef<WSConnection | null>(null)
  if (valueRef.current === null) {
    valueRef.current = {
      get connected() { return stateRef.current.connected },
      get chatID() { return stateRef.current.chatID },
      get channel() { return stateRef.current.channel },
      send: (msg) => conn.send(msg),
      subscribe: (id, channel) => {
        conn.subscribe(id, channel)
        stateRef.current.chatID = id
        stateRef.current.channel = channel ?? 'web'
        setTargetTick((t) => t + 1)
      },
      disconnect: () => {
        conn.disconnect()
        stateRef.current.chatID = null
        stateRef.current.channel = null
        setTargetTick((t) => t + 1)
      },
      addSubscription: (chatID: string, channel: string) => conn.addSubscription(chatID, channel),
      removeSubscription: (id: string) => conn.removeSubscription(id),
      rpc: (method, params) => conn.rpc(method, params),
      setLastSeq: (chatID: string, seq: number, channel?: string) => conn.setLastSeq(chatID, seq, channel),
      onMessage: conn.onMessage,
      onSession: conn.onSession,
      onProgress: conn.onProgress,
      onConnectionChange: conn.onConnectionChange,
    } satisfies WSConnection
  }

  return <WSContext.Provider value={valueRef.current}>{children}</WSContext.Provider>
}

export function useWSConnection(): WSConnection {
  const ctx = useContext(WSContext)
  if (!ctx) {
    throw new Error('useWSConnection must be used within a <WSProvider>')
  }
  return ctx
}
