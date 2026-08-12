import type { WSMessage } from '@/types/shared'

/**
 * Replay-test infrastructure for the SSE recorder (DebugToolbar "REC" button).
 *
 * The recorder downloads the exact wire format the backend writes:
 *
 *   id:62281
 *   event:progress_structured
 *   data:{...json...}
 *
 * (blank line between events). `id` is the top-level WSMessage.Seq (transport
 * seq); `data` is the full WSMessage JSON. This parser mirrors handleEvent's
 * seq resolution (`msg.seq ?? parseSequence(lastEventId)`): a message whose
 * JSON omits the top-level seq gets the last `id:` value.
 *
 * Usage in a regression test:
 *   const events = parseSSEDump(fs.readFileSync('dump.ev', 'utf8'))
 *   const { result } = renderHook(() => useProgressStream({ ... }))
 *   events.forEach((ev) => act(() => { ws.emit(ev); flushRaf() }))
 */
export function parseSSEDump(content: string): WSMessage[] {
  const events: WSMessage[] = []
  let lastId = 0
  let pendingEvent = ''
  const lines = content.split('\n')
  for (const raw of lines) {
    const line = raw.trim()
    if (line.startsWith('id:')) {
      const v = Number.parseInt(line.slice(3), 10)
      if (Number.isFinite(v)) lastId = v
    } else if (line.startsWith('event:')) {
      pendingEvent = line.slice(6).trim()
    } else if (line.startsWith('data:')) {
      try {
        const msg = JSON.parse(line.slice(5)) as WSMessage
        // Mirror handleEvent: top-level seq wins; else fall back to the SSE id.
        if (typeof msg.seq !== 'number') msg.seq = lastId
        if (!msg.type && pendingEvent) msg.type = pendingEvent
        events.push(msg)
      } catch {
        // Malformed line — skip (defensive; recorded dumps are well-formed).
      }
    }
  }
  return events
}
