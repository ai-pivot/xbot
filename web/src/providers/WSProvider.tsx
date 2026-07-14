/**
 * WSProvider — compatibility-named provider for the REST + SSE connection.
 *
 * Wrap the app once (inside ThemeProvider/I18nProvider). The active session
 * opens the EventSource; native reconnects update `connected` while the
 * underlying connection instance remains stable across renders.
 */
import {
  createContext,
  useContext,
  useEffect,
  useMemo,
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

  // Re-render on connection-state flips so consumers can read live status.
  const [connected, setConnected] = useState(conn.connected)
  const [target, setTarget] = useState(() => ({ chatID: conn.chatID, channel: conn.channel }))

  useEffect(() => {
    const offConn = conn.onConnectionChange(setConnected)
    // The connection is created eagerly; track its initial state too.
    setConnected(conn.connected)
    return () => {
      offConn()
      conn.dispose()
      connRef.current = null
    }
  }, [conn])

  const value = useMemo<WSConnection>(
    () => ({
      connected,
      send: (msg) => conn.send(msg),
      subscribe: (id, channel) => {
        conn.subscribe(id, channel)
        const next = { chatID: id, channel: channel ?? 'web' }
        setTarget((current) => (
          current.chatID === next.chatID && current.channel === next.channel ? current : next
        ))
      },
      disconnect: () => {
        conn.disconnect()
        setTarget((current) => (
          current.chatID === null && current.channel === null ? current : { chatID: null, channel: null }
        ))
      },
      addSubscription: (chatID: string, channel: string) => conn.addSubscription(chatID, channel),
      removeSubscription: (id: string) => conn.removeSubscription(id),
      rpc: (method, params) => conn.rpc(method, params),
      chatID: target.chatID,
      channel: target.channel,
      setLastSeq: (chatID: string, seq: number, channel?: string) => conn.setLastSeq(chatID, seq, channel),
      onMessage: conn.onMessage,
      onSession: conn.onSession,
      onProgress: conn.onProgress,
      onConnectionChange: conn.onConnectionChange,
    }),
    [conn, connected, target],
  )

  return <WSContext.Provider value={value}>{children}</WSContext.Provider>
}

export function useWSConnection(): WSConnection {
  const ctx = useContext(WSContext)
  if (!ctx) {
    throw new Error('useWSConnection must be used within a <WSProvider>')
  }
  return ctx
}
