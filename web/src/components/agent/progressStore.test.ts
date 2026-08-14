import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { ProgressStore, normalizeWebSubAgent, continuousIterations } from './progressStore'
import type { WebIteration, WebToolProgress } from '@/types/shared'

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

    // 1000 token deltas — each APPENDS (bandwidth optimization: backend pushes
    // O(n) deltas), so all accumulate
    for (let i = 0; i < 1000; i++) store.appendStreamContent('a')
    expect(calls).not.toHaveBeenCalled()
    flushRaf()
    expect(calls).toHaveBeenCalledTimes(1)
    expect(store.getSnapshot().streamContent).toBe('a'.repeat(1000))

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
    // appendStreamContent APPENDS deltas, so 'hi' + 'hi!' = 'hihi!'
    expect(c.streamContent).toBe('hihi!')

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

  it('appendReasoningContent appends deltas (delta/checkpoint scheme)', () => {
    const store = new ProgressStore()
    // Server pushes O(n) deltas: "foo " then "bar"
    store.appendReasoningContent('foo ')
    store.appendReasoningContent('bar')
    flushRaf()
    expect(store.getSnapshot().reasoningStreamContent).toBe('foo bar')
    // A checkpoint (setReasoningContent) replaces the accumulated text.
    store.setReasoningContent('full reasoning')
    flushRaf()
    expect(store.getSnapshot().reasoningStreamContent).toBe('full reasoning')
    store.dispose()
  })

  it('setIterationHistory appends snapshots', () => {
    const store = new ProgressStore()
    store.setIterationHistory([{ iteration: 1, content: '', reasoning: '', tools: [], toolCount: 0 }])
    store.setIterationHistory([{ iteration: 2, content: '', reasoning: '', tools: [tool({ name: 'Read' })], toolCount: 1 }])
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
        content: '',
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
      content: '',
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

describe('continuousIterations — linear-consistency guard (weak-network iteration gaps)', () => {
  function iters(nums: number[]): WebIteration[] {
    return nums.map((n) => ({ iteration: n, content: '', reasoning: '', tools: [], toolCount: 0 }))
  }

  it('keeps a fully contiguous sequence as-is', () => {
    expect(continuousIterations(iters([1, 2, 3])).map((i) => i.iteration)).toEqual([1, 2, 3])
  })

  it('truncates at the first gap (weak network dropped iteration 2)', () => {
    // delta for iteration 2 lost before restoreActiveProgress backfills it
    expect(continuousIterations(iters([1, 3, 4])).map((i) => i.iteration)).toEqual([1])
  })

  it('renders a contiguous sequence that does not start at 1 (partial history from compression)', () => {
    // History may contain only a subset of iterations (earlier ones compressed/merged).
    // A contiguous 2->3 is valid and should render.
    expect(continuousIterations(iters([2, 3])).map((i) => i.iteration)).toEqual([2, 3])
  })

  it('handles empty and single-iteration input', () => {
    expect(continuousIterations([])).toEqual([])
    expect(continuousIterations(iters([1])).map((i) => i.iteration)).toEqual([1])
  })

  it('preserves input order (no sorting — reordering after reconnect looks like duplication)', () => {
    // iterations arrive in order from appendIterations; continuousIterations
    // must NOT sort (sorting would reorder rows after a reconnect and make
    // old iterations appear near the latest progress)
    expect(continuousIterations(iters([1, 2, 3])).map((i) => i.iteration)).toEqual([1, 2, 3])
    expect(continuousIterations(iters([1, 3])).map((i) => i.iteration)).toEqual([1])
  })
})

describe('appendIterations — ordered union (reconnect out-of-order delivery)', () => {
  let rafCbs: Array<() => void>
  let rafSpy: ReturnType<typeof vi.spyOn>
  beforeEach(() => {
    rafCbs = []
    rafSpy = vi.spyOn(window, 'requestAnimationFrame').mockImplementation((cb) => {
      rafCbs.push(cb as () => void)
      return rafCbs.length
    })
  })
  afterEach(() => rafSpy.mockRestore())
  function flushRaf() {
    rafCbs.splice(0, rafCbs.length).forEach((cb) => cb())
  }
  function mkIter(n: number): WebIteration {
    return { iteration: n, content: '', reasoning: '', tools: [], toolCount: 0 }
  }

  it('sorts iterations regardless of arrival order (old 1 arriving between 100 and 101)', () => {
    const store = new ProgressStore()
    // new iteration 100 arrives first (reconnect recovery), then old 1, then 101
    store.replace({ iterationHistory: [mkIter(100)] })
    flushRaf()
    store.replace({ iterationHistory: [mkIter(1)] })
    flushRaf()
    store.replace({ iterationHistory: [mkIter(101)] })
    flushRaf()
    const hist = store.getSnapshot().iterationHistory.map((i) => i.iteration)
    expect(hist).toEqual([1, 100, 101])
    // continuousIterations truncates at the gap → only the contiguous prefix renders
    expect(continuousIterations(store.getSnapshot().iterationHistory).map((i) => i.iteration)).toEqual([1])
  })

  it('dedupes by iteration number and keeps order', () => {
    const store = new ProgressStore()
    store.replace({ iterationHistory: [mkIter(2), mkIter(1)] })
    flushRaf()
    store.replace({ iterationHistory: [mkIter(2)] })
    flushRaf()
    expect(store.getSnapshot().iterationHistory.map((i) => i.iteration)).toEqual([1, 2])
  })

  // ── SSE reconnect linear-consistency regression tests ──
  // After an SSE disconnect/reconnect, restoreActiveProgress can deliver a
  // STALE snapshot (seq <= current.eventSeq) while live events have already
  // advanced the store. The stale branch must append missing iterations but
  // must NOT roll back phase/iteration/content/activeTools — otherwise the
  // newer live state is overwritten by the older snapshot (linear-consistency
  // violation: history jumps backward after reconnect).

  it('stale snapshot (seq <= current) appends iterations but does NOT roll back newer live state', () => {
    const store = new ProgressStore()

    // Live events advance the store to iteration 2, seq 10 (arrived via SSE
    // before the recovery RPC returned).
    store.setStructuredTools({ eventSeq: 9, iteration: 1, phase: 'tool_exec',
      activeTools: [tool({ name: 'Read', status: 'done', iteration: 1 })] })
    flushRaf()
    store.setStructuredTools({ eventSeq: 10, iteration: 2, phase: 'tool_exec',
      content: 'NEW live content for iter 2',
      activeTools: [tool({ name: 'Shell', status: 'running', iteration: 2 })],
      iterationHistory: [mkIter(1)] })
    flushRaf()
    const before = store.getSnapshot()
    expect(before.iteration).toBe(2)
    expect(before.phase).toBe('tool_exec')
    expect(before.activeTools[0].name).toBe('Shell')

    // Stale recovery snapshot (seq 8 < current 10) from restoreActiveProgress.
    store.setStructuredTools({
      eventSeq: 8,
      phase: 'tool_exec',
      iteration: 1, // OLD iteration — must NOT roll back
      content: 'OLD content from stale snapshot',
      activeTools: [tool({ name: 'Read', status: 'done', iteration: 1 })],
      iterationHistory: [mkIter(3)], // NEW iteration — should still be appended
    })
    flushRaf()

    const snap = store.getSnapshot()
    // Phase/iteration/content must NOT be rolled back to the stale snapshot.
    expect(snap.phase).toBe('tool_exec')
    expect(snap.content).toBe('NEW live content for iter 2')
    expect(snap.activeTools[0].name).toBe('Shell')
    // Iteration history from the stale event is still appended (dedup by num).
    expect(snap.iterationHistory.map((i) => i.iteration)).toContain(3)
  })

  it('stale snapshot does NOT change streaming flag or revert to done', () => {
    const store = new ProgressStore()

    // Live event: turn running at iter 1, seq 5.
    store.setStructuredTools({ eventSeq: 5, iteration: 1, phase: 'running',
      content: 'live', activeTools: [] })
    flushRaf()
    expect(store.getSnapshot().phase).toBe('running')

    // Stale done-ish snapshot at seq 3 — must not turn the live state into done.
    store.setStructuredTools({ eventSeq: 3, phase: 'done', iteration: 0 })
    flushRaf()

    const snap = store.getSnapshot()
    expect(snap.phase).toBe('running') // NOT rolled back to 'done'
    expect(snap.streaming).toBe(true)
  })

  it('ignores iteration-regressed stream deltas (phase:undefined) — does NOT roll back active/completed/iteration', () => {
    // User report: "迭代到一半最新 turn 突然消失" + ITER_ID_INVARIANT_VIOLATION
    // {prev:4, next:2, phase:undefined}. A phase:undefined stream delta (Web
    // channel forwards stream_content as progress_structured) carries the
    // backend's CURRENT iteration, which can legitimately LAG the snapshot
    // (iter-2 stream text arriving after the snapshot advanced to 4). Applying
    // its activeTools/completedTools/iteration would roll the snapshot back to
    // the older iteration, wiping the newest iteration's tools.
    const store = new ProgressStore()
    // Iteration 2 active (structured)
    store.setStructuredTools({
      eventSeq: 1,
      phase: 'tool_exec',
      iteration: 2,
      activeTools: [tool({ name: 'Shell', status: 'running' })],
    })
    flushRaf()
    expect(store.getSnapshot().iteration).toBe(2)
    expect(store.getSnapshot().activeTools.map((t) => t.name)).toEqual(['Shell'])

    // A lagging stream delta for iteration 1 (phase:undefined, regressed)
    store.setStructuredTools({
      eventSeq: 2,
      iteration: 1,
      activeTools: [tool({ name: 'Read', status: 'running' })],
    })
    flushRaf()

    const snap = store.getSnapshot()
    expect(snap.iteration).toBe(2) // NOT rolled back to 1
    expect(snap.activeTools.map((t) => t.name)).toEqual(['Shell']) // NOT replaced by iter-1 tools
  })

  it('keeps already-rendered tools across the iteration boundary (no vanish window)', () => {
    // User report: "agent turn 消失然后又出现" — at the iteration boundary the
    // previous iteration's activeTools were cleared, but the clearing event is
    // often a phase:undefined stream delta carrying NO iteration_history, so
    // the tools vanished until the NEXT structured event appended the history
    // (an empty window lasting as long as SSE is slow). Already-rendered
    // content must never disappear: keep activeTools (mark running as done),
    // the new iteration's structured event replaces them.
    const store = new ProgressStore()
    // Iteration 1: active tool
    store.setStructuredTools({
      eventSeq: 1,
      phase: 'tool_exec',
      iteration: 1,
      activeTools: [tool({ name: 'Shell', status: 'running' })],
    })
    flushRaf()
    expect(store.getSnapshot().activeTools.map((t) => t.name)).toEqual(['Shell'])

    // Iteration 2 via a phase:undefined stream delta (no iteration_history) —
    // stream deltas do NOT advance lastIter (their iteration is the backend's
    // CURRENT iteration, which can lead the structured stream) and therefore do
    // NOT trigger the iteration boundary. Already-rendered tools stay as-is.
    store.setStructuredTools({ eventSeq: 2, iteration: 2 })
    flushRaf()

    const boundary = store.getSnapshot()
    expect(boundary.activeTools.length).toBe(1) // kept — not cleared
    expect(boundary.activeTools[0].name).toBe('Shell')
    expect(boundary.activeTools[0].status).toBe('running') // boundary NOT triggered by stream delta

    // New iteration's STRUCTURED event triggers the boundary (mark done) and replaces the old tool
    store.setStructuredTools({
      eventSeq: 3,
      phase: 'tool_exec',
      iteration: 2,
      activeTools: [tool({ name: 'Read', status: 'running' })],
    })
    flushRaf()
    expect(store.getSnapshot().activeTools.map((t) => t.name)).toEqual(['Read'])
  })

  it('does NOT advance lastIter from a phase:undefined stream delta — later structured iterations are NOT dropped as regressed', () => {
    // User report: committed assistant appeared with iter-range 1-1 after a
    // 1-second turn vanish (already-rendered iterations lost) + ITER_ID_
    // INVARIANT_VIOLATION prev:48 next:29. A stream delta carrying the backend's
    // CURRENT iteration (48, leading the structured stream) advanced lastIter,
    // so the later structured iteration 29 was treated as REGRESSED and dropped
    // from iterationHistory → commit had only the early iterations.
    const store = new ProgressStore()
    store.setStructuredTools({
      eventSeq: 1,
      phase: 'tool_exec',
      iteration: 1,
      activeTools: [tool({ name: 'Shell', status: 'running' })],
      iterationHistory: [{ iteration: 1, content: 't1', reasoning: '', tools: [], toolCount: 0 }],
    })
    flushRaf()
    expect(store.getSnapshot().lastIter).toBe(1)

    // A leading stream delta (phase:undefined, iteration=48)
    store.setStructuredTools({ eventSeq: 2, iteration: 48 })
    flushRaf()
    expect(store.getSnapshot().lastIter).toBe(1) // NOT advanced by the stream delta

    // The structured iteration 29 (which is > 1) must NOT be treated as regressed
    store.setStructuredTools({
      eventSeq: 3,
      phase: 'tool_exec',
      iteration: 29,
      activeTools: [tool({ name: 'Grep', status: 'running' })],
      iterationHistory: [{ iteration: 29, content: 't29', reasoning: '', tools: [], toolCount: 0 }],
    })
    flushRaf()
    const snap = store.getSnapshot()
    expect(snap.lastIter).toBe(29) // structured event advances lastIter
    expect(snap.iterationHistory.length).toBe(2) // iter 1 + iter 29 both kept
  })
})

describe('ProgressStore.dumpFullState', () => {
  let rafSpy: ReturnType<typeof vi.spyOn>
  let rafCallbacks: Array<() => void>

  beforeEach(() => {
    rafCallbacks = []
    rafSpy = vi.spyOn(window, 'requestAnimationFrame').mockImplementation((cb) => {
      rafCallbacks.push(cb as () => void)
      return rafCallbacks.length
    })
  })

  afterEach(() => {
    rafSpy.mockRestore()
  })

  it('exposes the un-throttled current state + store-level watermark fields', () => {
    const store = new ProgressStore()
    store.setStructuredTools({
      eventSeq: 3,
      phase: 'tool_exec',
      iteration: 2,
      activeTools: [tool({ name: 'Shell', status: 'running' })],
      iterationHistory: [{ iteration: 1, content: 't1', reasoning: '', tools: [], toolCount: 0 }],
    })
    // Do NOT flushRaf: the RAF-throttled snapshot is stale, but dumpFullState
    // must read the CURRENT internal state directly.
    const dump = store.dumpFullState()
    expect(dump.current.phase).toBe('tool_exec')
    expect(dump.current.iteration).toBe(2)
    expect(dump.current.iterationHistory).toHaveLength(1)
    expect(dump.current.activeTools?.[0]?.name).toBe('Shell')
    // lastTurnID is the store-level field NOT in the snapshot
    expect(typeof dump.lastTurnID).toBe('number')
    // The real iteration watermark lives in current.lastIter — advanced by the
    // structured event (2 > 0)
    expect(dump.current.lastIter).toBe(2)
  })

  it('is JSON-serializable (no circular refs) — the REC dump contract', () => {
    const store = new ProgressStore()
    store.setStructuredTools({
      eventSeq: 1,
      phase: 'thinking',
      iteration: 1,
      iterationHistory: [{ iteration: 1, content: 't', reasoning: '', tools: [], toolCount: 0 }],
    })
    const dump = store.dumpFullState()
    expect(() => JSON.stringify(dump)).not.toThrow()
    const round = JSON.parse(JSON.stringify(dump)) as ReturnType<ProgressStore['dumpFullState']>
    expect(round.current.phase).toBe('thinking')
    expect(round.current.iterationHistory).toHaveLength(1)
  })
})
