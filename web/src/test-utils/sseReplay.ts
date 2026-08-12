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

/**
 * Extract the store-state snapshots the recorder captures — from EITHER the
 * .ev dump file (the `:state-start:` / `:state-end:` SSE comment lines the
 * recorder embeds, the authoritative source — a single downloaded file fully
 * reconstructs the frontend) OR the console lines it also prints
 * (`[SSE_DUMP_STATE_START]` / `[SSE_DUMP_STATE]`), e.g. when pasting console
 * output directly.
 *
 * Usage in a regression test:
 *   const { events, state } = parseSSEDumpWithState(dumpFileContent)
 *   const store = createProgressStore()
 *   applySnapshot(store, state.start)          // baseline = real pre-record state
 *   events.forEach((ev) => store.apply(ev))
 *   expect(store.snapshot()).toEqual(state.end)  // first divergence = root cause
 */
export function parseStateDump(content: string): { start: unknown; end: unknown } {
  const result: { start: unknown; end: unknown } = { start: null, end: null }
  // .ev embedded form: `:state-start:{json}` / `:state-end:{json}` (SSE comment).
  const evStart = content.match(/^:state-start:(.*)$/m)
  const evEnd = content.match(/^:state-end:(.*)$/m)
  // Console form: `[SSE_DUMP_STATE_START] {...}` / `[SSE_DUMP_STATE] {...}`.
  // Each snapshot is a single JSON.stringify line (no embedded newlines), so
  // anchor per-line with /m — a greedy `/s` match would swallow the rest of
  // the console text up to the LAST `}` (e.g. the END line) and fail to parse.
  const consoleStart = content.match(/^\[SSE_DUMP_STATE_START\]\s*(\{.*\})\s*$/m)
  const consoleEnd = content.match(/^\[SSE_DUMP_STATE\]\s*(\{.*\})\s*$/m)
  if (evStart) {
    try {
      result.start = JSON.parse(evStart[1])
    } catch {
      // malformed JSON — leave null
    }
  }
  if (evEnd) {
    try {
      result.end = JSON.parse(evEnd[1])
    } catch {
      // malformed JSON — leave null
    }
  }
  // Console lines are fallbacks only — the .ev embedded form wins when both
  // are present (it is the exact serialization written to the file).
  if (result.start === null && consoleStart) {
    try {
      result.start = JSON.parse(consoleStart[1])
    } catch {
      // malformed JSON — leave null
    }
  }
  if (result.end === null && consoleEnd) {
    try {
      result.end = JSON.parse(consoleEnd[1])
    } catch {
      // malformed JSON — leave null
    }
  }
  return result
}

/**
 * Full replay payload: the event list AND the embedded START/END state
 * snapshots from a single .ev dump. One call — no separate parseSSEDump +
 * parseStateDump bookkeeping.
 */
export function parseSSEDumpWithState(content: string): {
  events: WSMessage[]
  state: { start: unknown; end: unknown }
} {
  return {
    events: parseSSEDump(content),
    state: parseStateDump(content),
  }
}
