import { describe, expect, it } from 'vitest'

import { hasVisibleProgress } from '@/hooks/useProgressStream'
import type { ProgressSnapshot } from '@/types/shared'

function snap(over: Partial<ProgressSnapshot>): ProgressSnapshot {
  return {
    eventSeq: 0,
    phase: '',
    iteration: 0,
    streamContent: '',
    reasoningStreamContent: '',
    content: '',
    streaming: true,
    activeTools: [],
    completedTools: [],
    iterationHistory: [],
    streamingTools: [],
    genuiContent: '',
    lastIter: 0,
    lastReasoning: '',
    todos: [],
    subAgents: [],
    tokenUsage: null,
    turnID: 0,
    ...over,
  }
}

describe('hasVisibleProgress', () => {
  it('returns true at the iteration boundary — all visible fields cleared, lastIter > 0', () => {
    // User report: "agent turn 消失然后又出现" — a new iteration started, the
    // previous iteration's active/completed tools were JUST cleared (the
    // clearing event is a phase:undefined stream delta carrying NO
    // iteration_history), and the new iteration's iterationHistory delta has
    // not arrived yet. Every visible field is momentarily empty. Without the
    // lastIter>0 guard the live row VANISHES for a frame until the next
    // structured event appends the iteration history.
    expect(hasVisibleProgress(snap({ lastIter: 3 }))).toBe(true)
  })

  it('returns false for a fresh pre-iteration thinking phase (lastIter=0, nothing visible)', () => {
    // Turn just started, nothing rendered yet — the busy placeholder shows.
    expect(hasVisibleProgress(snap({}))).toBe(false)
  })

  it('returns false after the turn ended (store reset → lastIter=0)', () => {
    // text event → resetProgress → store.reset() clears lastIter; no live row.
    expect(hasVisibleProgress(snap({ streaming: false, phase: 'done' }))).toBe(false)
  })

  it('returns true with only iterationHistory present', () => {
    expect(
      hasVisibleProgress(
        snap({
          iterationHistory: [
            { iteration: 1, thinking: 't', reasoning: '', tools: [], toolCount: 0 },
          ],
        }),
      ),
    ).toBe(true)
  })
})
