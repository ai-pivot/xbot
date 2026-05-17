import { useEffect, useRef, useState, useCallback } from 'react'

export interface WebSocketMessage {
  type: string
  [key: string]: unknown
}

export interface UseWebSocketOptions {
  /** Called for every message that passes seq dedup. */
  onMessage: (data: WebSocketMessage) => void
  /** Initial seq value (e.g. from history API's last_seq). Updated internally. */
  initialSeq?: number
  /** External ref for last_seq coordination. If omitted, an internal ref is used. */
  lastSeqRef?: React.MutableRefObject<number>
}

export interface UseWebSocketReturn {
  connected: boolean
  reconnecting: boolean
  serverStopped: boolean
  /** Send a JSON message through the WebSocket. No-op if not connected. */
  send: (data: WebSocketMessage) => void
  /** Manually initiate a WebSocket connection. */
  connect: () => void
  /** Close the connection intentionally (no reconnect). */
  disconnect: () => void
  /** Read/write the current last_seq value for sync handshakes. */
  lastSeqRef: React.MutableRefObject<number>
}

export function useWebSocket(options: UseWebSocketOptions): UseWebSocketReturn {
  const { onMessage, initialSeq, lastSeqRef: externalLastSeqRef } = options

  const [connected, setConnected] = useState(false)
  const [reconnecting, setReconnecting] = useState(true) // true = initial connecting state
  const [serverStopped, setServerStopped] = useState(false)

  const wsRef = useRef<WebSocket | null>(null)
  const intentionalCloseRef = useRef(false)
  const reconnectDelayRef = useRef(1000)
  const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const internalLastSeqRef = useRef<number>(initialSeq ?? 0)
  const lastSeqRef = externalLastSeqRef ?? internalLastSeqRef

  // Stable ref for onMessage to avoid re-creating connect on every render.
  const onMessageRef = useRef(onMessage)
  onMessageRef.current = onMessage

  const connect = useCallback(() => {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const wsUrl = `${protocol}//${window.location.host}/ws`
    const ws = new WebSocket(wsUrl)
    wsRef.current = ws

    ws.onopen = () => {
      setConnected(true)
      setReconnecting(false)
      setServerStopped(false)
      intentionalCloseRef.current = false
      reconnectDelayRef.current = 1000
      // Send sync handshake with last_seq from history API.
      ws.send(JSON.stringify({ type: 'sync', last_seq: lastSeqRef.current }))
      if (reconnectTimerRef.current) {
        clearTimeout(reconnectTimerRef.current)
        reconnectTimerRef.current = null
      }
    }

    ws.onerror = (e) => {
      console.warn('[WS] error', e)
    }

    ws.onclose = (e) => {
      setConnected(false)

      // Normal closure (1000) or going away (1001) = server shutdown, don't reconnect.
      // Skip if this is an intentional close (logout / component unmount).
      if (e.code === 1000 || e.code === 1001) {
        if (!intentionalCloseRef.current) {
          setServerStopped(true)
        }
        setReconnecting(false)
        return
      }

      setReconnecting(true)

      // Exponential backoff reconnect with jitter
      if (reconnectTimerRef.current) {
        clearTimeout(reconnectTimerRef.current)
      }
      const jitter = Math.random() * 0.5 + 0.5 // 0.5x - 1.0x random factor
      const delay = Math.round(reconnectDelayRef.current * jitter)
      reconnectTimerRef.current = setTimeout(() => {
        connect()
      }, delay)
      reconnectDelayRef.current = Math.min(reconnectDelayRef.current * 2, 30000)
    }

    ws.onmessage = (e) => {
      try {
        const data = JSON.parse(e.data)

        // Seq-based dedup: ignore events we've already processed.
        if (data.seq && data.seq <= lastSeqRef.current) {
          return
        }
        if (data.seq) {
          lastSeqRef.current = data.seq
        }

        onMessageRef.current(data)
      } catch {
        // ignore parse errors
      }
    }
  }, [])

  // Connect on mount, cleanup on unmount
  useEffect(() => {
    connect()
    return () => {
      if (reconnectTimerRef.current) {
        clearTimeout(reconnectTimerRef.current)
      }
      intentionalCloseRef.current = true
      wsRef.current?.close()
    }
  }, [connect])

  const send = useCallback((data: WebSocketMessage) => {
    if (!wsRef.current || wsRef.current.readyState !== WebSocket.OPEN) return
    wsRef.current.send(JSON.stringify(data))
  }, [])

  const disconnect = useCallback(() => {
    if (reconnectTimerRef.current) {
      clearTimeout(reconnectTimerRef.current)
    }
    intentionalCloseRef.current = true
    wsRef.current?.close()
  }, [])

  return {
    connected,
    reconnecting,
    serverStopped,
    send,
    connect,
    disconnect,
    lastSeqRef,
  }
}
