import { describe, expect, it } from 'vitest'

import { parseSSEDump } from '@/test-utils/sseReplay'

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
})
