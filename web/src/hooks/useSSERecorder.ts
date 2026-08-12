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
 *
 * On START it prints the FULL store state as JSON (`[SSE_DUMP_STATE_START]`),
 * and on STOP the final state (`[SSE_DUMP_STATE]`) — both to the console.
 * Together the .ev dump (event stream) + the two state snapshots reproduce a
 * bug 100%: initialize a ProgressStore from the START snapshot, replay the
 * events, compare the replayed state against the END snapshot — the first
 * divergence pinpoints exactly which event broke the store. Without the START
 * snapshot the replayed store starts empty while the real one began mid-
 * session (hydration/initialProgress), so the comparison is wrong from the
 * first event.
 */
export function useSSERecorder(ws: WSConnection, getStateSnapshot?: () => unknown) {
  const [recording, setRecording] = useState(false)
  const [count, setCount] = useState(0)
  const eventsRef = useRef<WSMessage[]>([])
  const startedAtRef = useRef(0)
  // Keep the latest getter in a ref so STOP always reads the CURRENT store
  // state (the closure captured an older render's function otherwise).
  const getStateSnapshotRef = useRef(getStateSnapshot)
  getStateSnapshotRef.current = getStateSnapshot
  // START snapshot, captured once at start() and embedded into the .ev header
  // at stop() — the replay needs this baseline to initialize the store.
  const startStateRef = useRef<unknown>(null)

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
    // Capture the store state at recording START — the replay needs this
    // initial state to initialize a ProgressStore before feeding it the .ev
    // events; comparing the replayed end state against the STOP snapshot is
    // only meaningful when both start from the same baseline.
    let startState: unknown = null
    if (getStateSnapshotRef.current) {
      try {
        startState = getStateSnapshotRef.current()
        console.log('[SSE_DUMP_STATE_START]', JSON.stringify(startState))
      } catch (err) {
        console.error('[SSE_DUMP_STATE_START] serialization failed', err)
      }
    }
    startStateRef.current = startState
    setRecording(true)
  }, [])

  const stop = useCallback(() => {
    setRecording(false)
    const events = eventsRef.current
    if (events.length === 0) {
      setCount(0)
      return
    }
    // END snapshot: the replayed state after feeding all events must equal
    // this — the first divergence is the event that broke the store.
    let endState: unknown = null
    if (getStateSnapshotRef.current) {
      try {
        endState = getStateSnapshotRef.current()
        console.log('[SSE_DUMP_STATE]', JSON.stringify(endState))
      } catch (err) {
        console.error('[SSE_DUMP_STATE] serialization failed', err)
      }
    }
    // Embed BOTH snapshots into the .ev as SSE comment lines (a `:`-prefixed
    // line is a comment to EventSource and ignored by parseSSEDump, so the
    // event stream stays byte-identical to the wire) — a single downloaded
    // file fully reconstructs the frontend without copying console output.
    let header = ''
    let footer = ''
    if (startStateRef.current !== null) {
      try {
        header = `:state-start:${JSON.stringify(startStateRef.current)}\n\n`
      } catch (err) {
        console.error('[SSE_DUMP_STATE_START] serialize-to-file failed', err)
      }
    }
    if (endState !== null) {
      try {
        footer = `:state-end:${JSON.stringify(endState)}\n\n`
      } catch (err) {
        console.error('[SSE_DUMP_STATE] serialize-to-file failed', err)
      }
    }
    const content = header + events
      .map((ev) => {
        const id = typeof ev.seq === 'number' ? ev.seq : 0
        return `id:${id}\nevent:${ev.type}\ndata:${JSON.stringify(ev)}\n\n`
      })
      .join('') + footer
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
