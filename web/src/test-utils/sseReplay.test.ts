import { describe, expect, it } from 'vitest'

import { parseSSEDump, parseSSEDumpWithState, parseStateDump } from '@/test-utils/sseReplay'

describe('parseSSEDump', () => {
  it('parses the recorder/backend SSE wire format (id/event/data lines)', () => {
    const dump = [
      `id:62281\nevent:progress_structured\ndata:${JSON.stringify({ type: 'progress_structured', seq: 62281, progress: { iteration: 1, chat_id: 'web:c1' } })}`,
      `id:62282\nevent:stream_content\ndata:${JSON.stringify({ type: 'stream_content', seq: 62282, progress: { stream_content: 'hi', turn_id: 1 } })}`,
      ``,
    ].join('\n')
    const events = parseSSEDump(dump)
    expect(events).toHaveLength(2)
    expect(events[0].type).toBe('progress_structured')
    expect(events[0].seq).toBe(62281)
    expect((events[0].progress as { iteration?: number }).iteration).toBe(1)
    expect(events[1].type).toBe('stream_content')
    expect((events[1].progress as { stream_content?: string }).stream_content).toBe('hi')
  })

  it('fills top-level seq from the SSE id line when the JSON omits it (mirrors handleEvent)', () => {
    // A stream delta may omit the top-level seq in data but still carry an id line.
    const dump = [
      `id:63793\nevent:progress_structured\ndata:${JSON.stringify({ type: 'progress_structured', progress: { iteration: 3, turn_id: 1 } })}`,
      ``,
    ].join('\n')
    const events = parseSSEDump(dump)
    expect(events).toHaveLength(1)
    expect(events[0].seq).toBe(63793)
  })

  it('skips malformed data lines without aborting the parse', () => {
    const dump = [
      `id:1\nevent:progress_structured\ndata:{not json`,
      `id:2\nevent:text\ndata:${JSON.stringify({ type: 'text', content: 'ok' })}`,
      ``,
    ].join('\n')
    const events = parseSSEDump(dump)
    expect(events).toHaveLength(1)
    expect(events[0].type).toBe('text')
  })

  it('ignores :state-start/:state-end comment lines (SSE comments are not events)', () => {
    const state = { progress: { iteration: 5 }, chat: { messages: [] } }
    const dump = [
      `:state-start:${JSON.stringify(state)}`,
      ``,
      `id:9\nevent:progress_structured\ndata:${JSON.stringify({ type: 'progress_structured', seq: 9, progress: { iteration: 6 } })}`,
      ``,
      `:state-end:${JSON.stringify({ progress: { iteration: 6 }, chat: { messages: [] } })}`,
      ``,
    ].join('\n')
    const events = parseSSEDump(dump)
    expect(events).toHaveLength(1)
    expect(events[0].seq).toBe(9)
  })
})

describe('parseStateDump', () => {
  it('extracts START/END snapshots from the .ev embedded comment lines', () => {
    const start = { progress: { iteration: 1 }, chat: { messages: [{ id: 'a' }] } }
    const end = { progress: { iteration: 4 }, chat: { messages: [] } }
    const dump = [
      `:state-start:${JSON.stringify(start)}`,
      ``,
      `id:1\nevent:progress_structured\ndata:${JSON.stringify({ type: 'progress_structured', seq: 1 })}`,
      ``,
      `:state-end:${JSON.stringify(end)}`,
      ``,
    ].join('\n')
    const state = parseStateDump(dump)
    expect(state.start).toEqual(start)
    expect(state.end).toEqual(end)
  })

  it('falls back to the console [SSE_DUMP_STATE_*] lines when no comment lines exist', () => {
    const start = { progress: { iteration: 0 }, lastIter: 0 }
    const end = { progress: { iteration: 2 }, lastIter: 2 }
    const consoleText = [
      `[SSE_DUMP_STATE_START] ${JSON.stringify(start)}`,
      `some other log line`,
      `[SSE_DUMP_STATE] ${JSON.stringify(end)}`,
    ].join('\n')
    const state = parseStateDump(consoleText)
    expect(state.start).toEqual(start)
    expect(state.end).toEqual(end)
  })

  it('prefers the .ev embedded form over the console form when both are present', () => {
    const evStart = { from: 'ev', iteration: 7 }
    const consoleStart = { from: 'console', iteration: 0 }
    const dump = [
      `:state-start:${JSON.stringify(evStart)}`,
      ``,
      `[SSE_DUMP_STATE_START] ${JSON.stringify(consoleStart)}`,
    ].join('\n')
    const state = parseStateDump(dump)
    expect(state.start).toEqual(evStart)
  })

  it('returns null for missing/malformed snapshots', () => {
    expect(parseStateDump('no state here')).toEqual({ start: null, end: null })
    expect(parseStateDump(':state-start:not json\n\n')).toEqual({ start: null, end: null })
  })
})

describe('parseSSEDumpWithState', () => {
  it('returns events + embedded state snapshots from one .ev dump', () => {
    const start = { progress: { iteration: 1 } }
    const dump = [
      `:state-start:${JSON.stringify(start)}`,
      ``,
      `id:5\nevent:progress_structured\ndata:${JSON.stringify({ type: 'progress_structured', seq: 5, progress: { iteration: 2 } })}`,
      ``,
    ].join('\n')
    const parsed = parseSSEDumpWithState(dump)
    expect(parsed.events).toHaveLength(1)
    expect(parsed.events[0].seq).toBe(5)
    expect(parsed.state.start).toEqual(start)
    expect(parsed.state.end).toBeNull()
  })
})
