import { useCallback, useEffect, useRef, useState } from 'react'

import type { WSConnection } from '@/types/ws'
import type { WSMessage } from '@/types/shared'

/**
 * Records every WS/SSE message of a session into an SSE-format dump file
 * (`id:N\nevent:TYPE\ndata:{json}\n\n` lines) — the SAME wire format the
 * backend writes and the replay-test infrastructure consumes
 * (src/test-utils/sseReplay.ts). Developer tool: start recording, reproduce a
 * bug, stop → download the dump → replay it in a vitest to pin the regression.
 *
 * The recorder registers its OWN ws.onMessage handler (registration-based API,
 * returns an unsubscribe) so it never interferes with useProgressStream /
 * useChatMessages handlers. `ev.seq` (top-level WSMessage.Seq, the transport
 * seq the backend stamps as the SSE `id:`) is used as the dump id line so the
 * file matches what a real EventSource would see — including the interplay
 * between transport seq and progress.seq that previously caused mis-diagnosis.
 */
export function useSSERecorder(ws: WSConnection) {
  const [recording, setRecording] = useState(false)
  const [count, setCount] = useState(0)
  const eventsRef = useRef<WSMessage[]>([])
  const startedAtRef = useRef(0)

  useEffect(() => {
    if (!recording) return
    const off = ws.onMessage((msg) => {
      eventsRef.current.push(msg)
      setCount(eventsRef.current.length)
    })
    return off
  }, [ws, recording])

  const start = useCallback(() => {
    eventsRef.current = []
    setCount(0)
    startedAtRef.current = Date.now()
    setRecording(true)
  }, [])

  const stop = useCallback(() => {
    setRecording(false)
    const events = eventsRef.current
    if (events.length === 0) {
      setCount(0)
      return
    }
    const content = events
      .map((ev) => {
        const id = typeof ev.seq === 'number' ? ev.seq : 0
        return `id:${id}\nevent:${ev.type}\ndata:${JSON.stringify(ev)}\n\n`
      })
      .join('')
    const blob = new Blob([content], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    const ts = new Date(startedAtRef.current).toISOString().replace(/[:.]/g, '-')
    a.href = url
    a.download = `sse-dump-${ts}.ev`
    a.click()
    URL.revokeObjectURL(url)
    setCount(0)
  }, [])

  return { recording, count, start, stop }
}
