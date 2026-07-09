/**
 * WSProvider — owns one WSConnectionImpl and exposes it via context.
 *
 * Wrap the app once (inside ThemeProvider/I18nProvider). The connection
 * auto-connects on mount and auto-reconnects on drop; children read it through
 * `useWSConnection()`. The provider re-renders on `connected` flips so the UI
 * can show live status, but the underlying connection instance is stable
 * across renders.
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
import { WSConnectionImpl } from '@/providers/wsConnection'
import type { WSConnection } from '@/types/ws'

export const WSContext = createContext<WSConnection | undefined>(undefined)

export function WSProvider({ children }: { children: ReactNode }) {
  // One connection for the provider's lifetime; never recreated on re-render.
  const connRef = useRef<WSConnectionImpl | null>(null)
  if (connRef.current === null) {
    connRef.current = new WSConnectionImpl()
  }
  const conn = connRef.current

  // Re-render on connection-state flips so consumers can read live status.
  const [connected, setConnected] = useState(conn.connected)
  const [, setChatID] = useState<string | null>(conn.chatID)

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

  // Keep chatID reactive for the `chatID` field on the context value.
  useEffect(() => {
    const off = conn.onMessage((m) => {
      if (m.type === 'session' && m.session?.chat_id) setChatID(m.session.chat_id)
    })
    return off
  }, [conn])

  const value = useMemo<WSConnection>(
    () => ({
      connected,
      send: (msg) => conn.send(msg),
      subscribe: (id) => {
        conn.subscribe(id)
        setChatID(id)
      },
      rpc: (method, params) => conn.rpc(method, params),
      chatID: conn.chatID,
      setLastSeq: (seq: number) => conn.setLastSeq(seq),
      onMessage: conn.onMessage,
      onSession: conn.onSession,
      onProgress: conn.onProgress,
      onConnectionChange: conn.onConnectionChange,
    }),
    [conn, connected],
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
