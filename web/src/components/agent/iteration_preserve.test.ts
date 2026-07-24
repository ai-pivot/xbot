import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { ProgressStore } from '@/components/agent/progressStore'
import type { WebToolProgress, WebIteration } from '@/types/shared'

let rafSpy: ReturnType<typeof vi.spyOn>
let rafCallbacks: Array<() => void>

beforeEach(() => {
  rafCallbacks = []
  rafSpy = vi.spyOn(window, 'requestAnimationFrame').mockImplementation((cb) => {
    rafCallbacks.push(cb as () => void)
    return rafCallbacks.length
  })
})
afterEach(() => rafSpy.mockRestore())

function flushRaf() { rafCallbacks.splice(0, rafCallbacks.length).forEach((cb) => cb()) }

function tool(name: string, status: string, iter?: number): WebToolProgress {
  return { name, label: name, status: status as WebToolProgress['status'], elapsedMs: 0, summary: '', detail: '', args: '', iteration: iter, toolHints: '' }
}

function iter(n: number, tools: WebToolProgress[]): WebIteration {
  return { iteration: n, thinking: '', reasoning: '', tools, toolCount: tools.length }
}

describe('iterationHistory preservation — NEVER disappear during tool execution', () => {
  it('preserves completedTools within the same iteration when a new tool starts running', () => {
    const store = new ProgressStore()

    // Iteration 1: tool 1 starts running
    store.setStructuredTools({ eventSeq: 1, iteration: 1, phase: 'tool_exec',
      activeTools: [tool('Read', 'running', 1)], completedTools: [] })
    flushRaf()

    // Tool 1 done, tool 2 starts
    store.setStructuredTools({ eventSeq: 2, iteration: 1, phase: 'tool_exec',
      activeTools: [tool('Grep', 'running', 1)],
      completedTools: [tool('Read', 'done', 1)] })
    flushRaf()

    const snap = store.getSnapshot()
    expect(snap.activeTools.length).toBe(1)
    expect(snap.activeTools[0].name).toBe('Grep')
    expect(snap.completedTools.length).toBe(1)
    expect(snap.completedTools[0].name).toBe('Read')
  })

  it('preserves iterationHistory when a new iteration begins with tools', () => {
    const store = new ProgressStore()

    // Iteration 1: tools complete
    store.setStructuredTools({ eventSeq: 1, iteration: 1, phase: 'tool_exec',
      activeTools: [], completedTools: [tool('Read', 'done', 1)] })
    flushRaf()

    // Iteration 2: new tool starts (beginIteration clears + initToolProgress)
    store.setStructuredTools({ eventSeq: 2, iteration: 2, phase: 'tool_exec',
      activeTools: [tool('Shell', 'pending', 2)],
      completedTools: [],
      iterationHistory: [iter(1, [tool('Read', 'done', 1)])] })
    flushRaf()

    const snap = store.getSnapshot()
    expect(snap.iterationHistory.length).toBe(1)
    expect(snap.iterationHistory[0].iteration).toBe(1)
    expect(snap.iterationHistory[0].tools.length).toBe(1)
    expect(snap.activeTools.length).toBe(1)
    expect(snap.activeTools[0].name).toBe('Shell')
    // completedTools from iteration 2 (empty) must not wipe iteration 1 history
    expect(snap.completedTools.length).toBe(0)
  })

  it('resetStreamingState preserves iterationHistory (regression: session idle)', () => {
    const store = new ProgressStore()

    store.setStructuredTools({ eventSeq: 1, iteration: 1, phase: 'tool_exec',
      activeTools: [tool('Read', 'running', 1)],
      iterationHistory: [iter(0, [tool('Grep', 'done', 0)])] })
    flushRaf()

    // Simulate session(idle) calling resetStreamingState
    store.resetStreamingState()
    flushRaf()

    const snap = store.getSnapshot()
    expect(snap.iterationHistory.length).toBe(1) // MUST survive
    expect(snap.activeTools.length).toBe(0) // cleared
    expect(snap.completedTools.length).toBe(0) // cleared
  })

  it('replace preserves existing iterationHistory when replacement has none', () => {
    const store = new ProgressStore()

    // SSE builds iterationHistory incrementally
    store.setStructuredTools({ eventSeq: 1, iteration: 1, phase: 'tool_exec',
      activeTools: [tool('Read', 'running', 1)],
      iterationHistory: [iter(0, [tool('Grep', 'done', 0)])] })
    flushRaf()

    // History reload arrives late with a snapshot that has NO iterationHistory
    store.replace({ phase: 'tool_exec', iteration: 1, eventSeq: 2 })
    flushRaf()

    const snap = store.getSnapshot()
    expect(snap.iterationHistory.length).toBe(1) // MUST survive replace
  })
})
