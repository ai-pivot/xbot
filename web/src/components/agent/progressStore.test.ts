import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { ProgressStore, dedupMessages, normalizeWebSubAgent } from './progressStore'
import type { WebToolProgress } from '@/types/shared'

// Helper: create a tool with defaults
function tool(opts: Partial<WebToolProgress>): WebToolProgress {
  return {
    name: opts.name ?? 'Read',
    label: opts.label ?? '',
    status: opts.status ?? 'done',
    elapsedMs: opts.elapsedMs ?? 0,
    summary: opts.summary ?? '',
    detail: opts.detail ?? '',
    args: opts.args ?? '',
    toolHints: opts.toolHints ?? '',
  }
}

// ── Basic ProgressStore tests ──
describe('ProgressStore basic', () => {
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

  function flushRaf() {
    rafCallbacks.splice(0, rafCallbacks.length).forEach((cb) => cb())
  }

  it('coalesces many mutations into one notify per frame', () => {
    const store = new ProgressStore()
    const calls = vi.fn()
    const unsub = store.subscribe(calls)

    // 1000 token appends — each is a cumulative SET, last one wins
    for (let i = 0; i < 1000; i++) store.appendStreamContent('a')
    expect(calls).not.toHaveBeenCalled()
    flushRaf()
    expect(calls).toHaveBeenCalledTimes(1)
    // appendStreamContent uses assignment (=), so last value wins
    expect(store.getSnapshot().streamContent).toBe('a')

    unsub()
    store.dispose()
  })

  it('returns a stable snapshot reference between notifies', () => {
    const store = new ProgressStore()
    const unsub = store.subscribe(() => {})

    store.appendStreamContent('hi')
    flushRaf()
    const a = store.getSnapshot()
    const b = store.getSnapshot()
    expect(a).toBe(b)

    store.appendStreamContent('hi!')
    flushRaf()
    const c = store.getSnapshot()
    expect(c).not.toBe(a)
    // appendStreamContent is assignment, so 'hi!' replaces 'hi'
    expect(c.streamContent).toBe('hi!')

    unsub()
    store.dispose()
  })

  it('reset clears accumulated content synchronously', () => {
    const store = new ProgressStore()
    store.appendStreamContent('abc')
    flushRaf()
    store.reset()
    // reset() now synchronously updates snapshot — no flushRaf needed
    expect(store.getSnapshot().streamContent).toBe('')
    expect(store.getSnapshot().streaming).toBe(false)
    store.dispose()
  })

  it('appendReasoningContent sets cumulative reasoning value', () => {
    const store = new ProgressStore()
    // Server sends cumulative values: first "foo ", then "foo bar"
    store.appendReasoningContent('foo ')
    store.appendReasoningContent('foo bar')
    flushRaf()
    expect(store.getSnapshot().reasoningStreamContent).toBe('foo bar')
    store.dispose()
  })

  it('setIterationHistory appends snapshots', () => {
    const store = new ProgressStore()
    store.setIterationHistory([{ iteration: 1, thinking: '', reasoning: '', tools: [], toolCount: 0 }])
    store.setIterationHistory([{ iteration: 2, thinking: '', reasoning: '', tools: [tool({ name: 'Read' })], toolCount: 1 }])
    flushRaf()
    expect(store.getSnapshot().iterationHistory).toHaveLength(1)
    store.dispose()
  })

  it('does not notify after dispose', () => {
    const store = new ProgressStore()
    store.dispose()
    const calls = vi.fn()
    store.subscribe(calls)
    store.appendStreamContent('z')
    flushRaf()
    expect(calls).not.toHaveBeenCalled()
  })
})

describe('normalizeWebSubAgent', () => {
  it('normalizes session_key recursively', () => {
    expect(normalizeWebSubAgent({
      role: 'orchestrator',
      status: 'running',
      session_key: 'cli:main/orchestrator:1',
      children: [{
        role: 'review',
        status: 'running',
        session_key: 'cli:main/orchestrator:1/review:2',
      }],
    })).toMatchObject({
      sessionKey: 'cli:main/orchestrator:1',
      children: [{ sessionKey: 'cli:main/orchestrator:1/review:2' }],
    })
  })
})

// ── Stream-only patch + carry-forward + iteration snapshot ──
describe('ProgressStore stream-only patch + carry-forward', () => {
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

  function flushRaf() {
    rafCallbacks.splice(0, rafCallbacks.length).forEach((cb) => cb())
  }

  it('carry-forward: structured event preserves streamContent within same iteration', () => {
    const store = new ProgressStore()
    // Server sends cumulative values
    store.appendStreamContent('Hello world')
    flushRaf()
    expect(store.getSnapshot().streamContent).toBe('Hello world')

    // Structured event arrives in the same iteration — streamContent preserved
    store.setStructuredTools({
      phase: 'tool_exec',
      iteration: 1,
      activeTools: [tool({ name: 'Read', status: 'running' })],
    })
    flushRaf()

    const snap = store.getSnapshot()
    expect(snap.streamContent).toBe('Hello world')
    expect(snap.phase).toBe('tool_exec')
    expect(snap.iteration).toBe(1)
    expect(snap.activeTools[0].name).toBe('Read')
    store.dispose()
  })

  it('carry-forward: structured event preserves reasoningStreamContent within same iteration', () => {
    const store = new ProgressStore()
    store.appendReasoningContent('thinking deeply')
    flushRaf()

    // Same iteration — reasoningStreamContent should be preserved
    store.setStructuredTools({ phase: 'thinking', iteration: 1 })
    flushRaf()

    expect(store.getSnapshot().reasoningStreamContent).toBe('thinking deeply')
    store.dispose()
  })

  it('iteration advance consumes only backend log entries and clears stream fields', () => {
    const store = new ProgressStore()
    // First iteration
    store.setStructuredTools({ phase: 'thinking', iteration: 1 })
    store.appendStreamContent('iter1 text')
    store.setStructuredTools({
      phase: 'tool_exec',
      iteration: 1,
      reasoning: 'iter1 reasoning',
      completedTools: [tool({ name: 'Read', status: 'done', summary: 'ok' })],
    })
    flushRaf()

    // Second iteration carries the authoritative completed-iteration log delta.
    store.setStructuredTools({
      phase: 'thinking',
      iteration: 2,
      iterationHistory: [{
        iteration: 1,
        thinking: '',
        reasoning: 'iter1 reasoning',
        tools: [tool({ name: 'Read', status: 'done', summary: 'ok' })],
        toolCount: 1,
      }],
    })
    flushRaf()

    const snap = store.getSnapshot()
    expect(snap.iterationHistory).toHaveLength(1)
    expect(snap.iterationHistory[0].iteration).toBe(1)
    expect(snap.iterationHistory[0].reasoning).toBe('iter1 reasoning')
    expect(snap.iterationHistory[0].tools).toHaveLength(1)
    expect(snap.iterationHistory[0].tools[0].name).toBe('Read')
    // Stream fields should be cleared for the new iteration
    expect(snap.streamContent).toBe('')
    expect(snap.reasoningStreamContent).toBe('')
    expect(snap.streamingTools).toHaveLength(0)
    expect(snap.activeTools).toHaveLength(0)
    expect(snap.completedTools).toHaveLength(0)
    expect(snap.subAgents).toHaveLength(0)
    store.dispose()
  })

  it('does not synthesize a semantic log entry when only iteration advances', () => {
    const store = new ProgressStore()
    store.setStructuredTools({ phase: 'thinking', iteration: 1 })
    flushRaf()
    expect(store.getSnapshot().iterationHistory).toHaveLength(0)
    expect(store.getSnapshot().lastIter).toBe(1)
    store.dispose()
  })

  it('does not duplicate installed snapshot log when the same backend delta is replayed', () => {
    const store = new ProgressStore()
    const skillIteration = {
      iteration: 1,
      thinking: '',
      reasoning: '',
      tools: [
        tool({ name: 'Skill', label: 'debug', status: 'done' }),
        tool({ name: 'Read', label: 'progressStore.ts', status: 'done' }),
        tool({ name: 'Grep', label: 'iterationHistory', status: 'done' }),
      ],
      toolCount: 3,
    }
    store.replace({
      phase: 'thinking',
      iteration: 2,
      eventSeq: 10,
      lastIter: 2,
      iterationHistory: [skillIteration],
    })
    flushRaf()

    // Busy reconnect can replay the delta already included in the installed
    // active-progress snapshot. Iteration is the semantic log watermark.
    store.setStructuredTools({
      eventSeq: 10,
      phase: 'thinking',
      iteration: 2,
      iterationHistory: [skillIteration],
    })
    flushRaf()

    expect(store.getSnapshot().iterationHistory).toEqual([skillIteration])
    store.dispose()
  })

  it('stale streamingTools filtered when structured event brings matching activeTools', () => {
    const store = new ProgressStore()
    // stream_content sets generating tool
    store.setStreamOnlyFields({ streamingTools: [tool({ name: 'Read', status: 'generating' })] })
    flushRaf()
    expect(store.getSnapshot().streamingTools).toHaveLength(1)

    // progress_structured brings the same tool as running — stale generating should be filtered
    store.setStructuredTools({
      phase: 'tool_exec',
      iteration: 1,
      activeTools: [tool({ name: 'Read', label: 'file.go', status: 'running' })],
    })
    flushRaf()

    const snap = store.getSnapshot()
    expect(snap.streamingTools).toHaveLength(0) // filtered out!
    expect(snap.activeTools).toHaveLength(1)
    expect(snap.activeTools[0].name).toBe('Read')
    store.dispose()
  })

  it('carries subAgents forward when structured frames omit the field', () => {
    const store = new ProgressStore()
    store.setStructuredTools({
      phase: 'tool_exec',
      iteration: 1,
      subAgents: [{ role: 'review', instance: '1', status: 'running', desc: 'checking' }],
    })
    flushRaf()
    expect(store.getSnapshot().subAgents[0].role).toBe('review')

    store.setStructuredTools({ phase: 'thinking', iteration: 1 })
    flushRaf()
    expect(store.getSnapshot().subAgents).toHaveLength(1)

    store.dispose()
  })

  it('merges SubAgent progress like TUI to avoid desc and children flicker', () => {
    const store = new ProgressStore()
    store.setStructuredTools({
      phase: 'tool_exec',
      iteration: 1,
      subAgents: [{
        role: 'review',
        instance: '1',
        status: 'running',
        desc: 'checking',
        children: [{ role: 'fix', status: 'running', desc: 'patching' }],
      }],
    })
    flushRaf()
    store.setStructuredTools({
      phase: 'tool_exec',
      iteration: 1,
      subAgents: [{ role: 'review', instance: '1', status: 'running' }],
    })
    flushRaf()
    const node = store.getSnapshot().subAgents[0]
    expect(node.desc).toBe('checking')
    expect(node.children?.[0].desc).toBe('patching')
    store.dispose()
  })

  it('preserves completed SubAgent nodes in progress tree', () => {
    const store = new ProgressStore()
    store.setStructuredTools({
      phase: 'tool_exec',
      iteration: 1,
      subAgents: [{ role: 'review', status: 'running' }],
    })
    flushRaf()
    store.setStructuredTools({
      phase: 'tool_exec',
      iteration: 1,
      subAgents: [{ role: 'review', status: 'done' }],
    })
    flushRaf()
    // Done nodes are preserved (not filtered) — they show the final status
    expect(store.getSnapshot().subAgents).toHaveLength(1)
    expect(store.getSnapshot().subAgents[0].status).toBe('done')
    store.dispose()
  })

  it('clears subAgents when iteration changes', () => {
    const store = new ProgressStore()
    store.setStructuredTools({
      phase: 'tool_exec',
      iteration: 1,
      subAgents: [{ role: 'review', status: 'running' }],
    })
    flushRaf()
    store.setStructuredTools({ phase: 'thinking', iteration: 2 })
    flushRaf()
    expect(store.getSnapshot().subAgents).toHaveLength(0)
    store.dispose()
  })
})

// ── Tool dedup ──
describe('ProgressStore tool dedup', () => {
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

  function flushRaf() {
    rafCallbacks.splice(0, rafCallbacks.length).forEach((cb) => cb())
  }

  it('dedupTools: generating tools are never deduped', () => {
    const store = new ProgressStore()
    store.setStructuredTools({
      phase: 'tool_exec',
      iteration: 1,
      activeTools: [
        tool({ name: 'Read', status: 'generating' }),
        tool({ name: 'Read', status: 'generating' }),
        tool({ name: 'Read', status: 'generating' }),
      ],
    })
    flushRaf()
    expect(store.getSnapshot().activeTools).toHaveLength(3)
    store.dispose()
  })

  it('dedupTools: running/done/error tools dedup by name+label', () => {
    const store = new ProgressStore()
    store.setStructuredTools({
      phase: 'tool_exec',
      iteration: 1,
      completedTools: [
        tool({ name: 'Read', label: 'file1.go', status: 'done' }),
        tool({ name: 'Read', label: 'file1.go', status: 'done' }), // dup
        tool({ name: 'Read', label: 'file2.go', status: 'done' }), // different label
        tool({ name: 'Grep', label: '', status: 'done' }),          // different name
      ],
    })
    flushRaf()
    expect(store.getSnapshot().completedTools).toHaveLength(3)
    store.dispose()
  })
})

// ── Message dedup ──
describe('dedupMessages', () => {
  it('keeps only the last message with the same turnID+role', () => {
    const msgs = [
      { turnID: 1, role: 'assistant', id: 'a1' },
      { turnID: 1, role: 'user', id: 'u1' },
      { turnID: 1, role: 'assistant', id: 'a2' },
    ]
    const result = dedupMessages(msgs)
    expect(result).toHaveLength(2)
    expect(result.find((m) => m.role === 'assistant')!.id).toBe('a2')
  })

  it('keeps all history messages with turnID=0 and DB ids', () => {
    const msgs = [
      { turnID: 0, role: 'user', id: '1' },
      { turnID: 0, role: 'assistant', id: '2', content: 'hello' },
      { turnID: 0, role: 'user', id: '3' },
      { turnID: 0, role: 'assistant', id: '4', content: 'hello' }, // same content, different DB id
    ]
    const result = dedupMessages(msgs)
    expect(result).toHaveLength(4) // all kept — DB ids don't start with 'asst-'
  })

  it('does not infer identity from generated ids or repeated content', () => {
    const msgs = [
      { turnID: 0, role: 'assistant', id: 'asst-100-0', content: 'hello' },
      { turnID: 0, role: 'assistant', id: 'asst-101-1', content: 'hello' },
      { turnID: 0, role: 'assistant', id: 'asst-102-2', content: 'world' },
    ]
    const result = dedupMessages(msgs)
    expect(result).toEqual(msgs)
  })
})

// ── Todo real-time update tests ──
// Bug: todos only appear after busy→idle transition, not immediately when
// the TodoWrite tool fires a progress_structured event.
describe('ProgressStore todos real-time update', () => {
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

  function flushRaf() {
    rafCallbacks.splice(0, rafCallbacks.length).forEach((cb) => cb())
  }

  it('todos appear immediately after setStructuredTools (no flush needed between)', () => {
    const store = new ProgressStore()

    // Simulate: agent starts thinking (busy)
    store.setStructuredTools({ phase: 'thinking', iteration: 1 })
    flushRaf()
    expect(store.getSnapshot().todos).toHaveLength(0)

    // Simulate: TodoWrite fires → next progress_structured carries todos
    store.setStructuredTools({
      phase: 'tool_exec',
      iteration: 1,
      todos: [
        { id: 1, text: 'task A', done: false },
        { id: 2, text: 'task B', done: false },
      ],
    })
    flushRaf()

    // Todos should be visible NOW, not after idle
    expect(store.getSnapshot().todos).toHaveLength(2)
    expect(store.getSnapshot().todos[0].text).toBe('task A')
    store.dispose()
  })

  it('todos survive reset() — preserved across turn boundary', () => {
    const store = new ProgressStore()

    store.setStructuredTools({
      phase: 'tool_exec',
      iteration: 1,
      todos: [{ id: 1, text: 'task A', done: false }],
    })
    flushRaf()

    // Turn ends → reset() clears streaming state but keeps todos
    store.reset()
    flushRaf()

    expect(store.getSnapshot().todos).toHaveLength(1)
    store.dispose()
  })

  it('PhaseDone event applies its todos (TodoWrite-as-last-tool scenario)', () => {
    const store = new ProgressStore()

    // TodoWrite is often the last tool → its todos ride on the PhaseDone
    // event. When the mid-busy push events are dropped (SSE backpressure /
    // coalescing), PhaseDone is the only carrier. It MUST apply its todos.
    store.setStructuredTools({
      phase: 'done',
      todos: [{ id: 1, text: 'task A', done: false }],
    })
    flushRaf()

    expect(store.getSnapshot().todos).toHaveLength(1)
    expect(store.getSnapshot().todos[0].text).toBe('task A')
    store.dispose()
  })

  it('empty todos array clears previous todos (todo_write([]))', () => {
    const store = new ProgressStore()

    store.setStructuredTools({
      phase: 'tool_exec',
      iteration: 1,
      todos: [{ id: 1, text: 'task A', done: false }],
    })
    flushRaf()
    expect(store.getSnapshot().todos).toHaveLength(1)

    // todo_write([]) → backend sends todos: []
    store.setStructuredTools({
      phase: 'tool_exec',
      iteration: 1,
      todos: [],
    })
    flushRaf()

    expect(store.getSnapshot().todos).toHaveLength(0)
    store.dispose()
  })
})
