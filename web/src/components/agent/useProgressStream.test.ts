/**
 * Hook-level integration tests for useProgressStream (Spec 4).
 *
 * Covers the WS event dispatch that the pure-component tests do not:
 *   stream_content → append, progress_structured → tools/reasoning/iteration,
 *   text → finalize (onAssistantComplete) + reset, session(idle) → defensive
 *   finalize, session/other-chat filtering, and initialProgress hydration.
 *
 * The WS connection is stubbed by mocking @/hooks/useWSConnection. rAF is
 * mocked so the store's throttled notify can be flushed deterministically
 * inside a single act() tick.
 */
import { act, renderHook } from '@testing-library/react'
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'

import type { ProgressEvent, WSMessage } from '@/types/shared'
import type { WSConnection } from '@/types/ws'
import { clearWebCaches, progressSnapshotCache, sessionCacheKey } from '@/lib/webCache'

// --- stub WS connection ----------------------------------------------------

type MessageHandler = (msg: WSMessage) => void

interface FakeWS {
  onMessage: (h: MessageHandler) => () => void
  onProgress: (h: (e: ProgressEvent) => void) => () => void
  send: (msg: unknown) => void
  connected: boolean
  chatID: string | null
  emit: (msg: WSMessage) => void
}

function makeFakeWS(): FakeWS & { handlers: Set<MessageHandler> } {
  const handlers = new Set<MessageHandler>()
  return {
    handlers,
    onMessage: (h) => {
      handlers.add(h)
      return () => handlers.delete(h)
    },
    onProgress: () => () => {},
    send: () => {},
    connected: true,
    chatID: null,
    emit: (msg) => handlers.forEach((h) => h(msg)),
  }
}

let currentWS: FakeWS
let rafCbs: Array<() => void>

beforeEach(() => {
  clearWebCaches()
  currentWS = makeFakeWS()
  rafCbs = []
  vi.spyOn(window, 'requestAnimationFrame').mockImplementation((cb) => {
    rafCbs.push(cb as () => void)
    return rafCbs.length
  })
})
afterEach(() => {
  vi.restoreAllMocks()
})

/** Emit a WS message and flush the store's throttled notify within one act. */
function emitAndFlush(msg: WSMessage) {
  act(() => {
    currentWS.emit(msg)
    const cbs = rafCbs.splice(0, rafCbs.length)
    cbs.forEach((cb) => cb())
  })
}

const { useProgressStream } = await import('@/hooks/useProgressStream')

describe('useProgressStream event dispatch', () => {
  it('sets cumulative stream_content to the live message', () => {
    const { result } = renderHook(() => useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection }))
    // Server sends cumulative values: first "Hello", then "Hello world"
    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'Hello' } })
    expect(result.current.liveMessage?.content).toBe('Hello')
    expect(result.current.isStreaming).toBe(true)
    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'Hello world' } })
    expect(result.current.liveMessage?.content).toBe('Hello world')
  })

  it('preserves in-flight stream content on a synthetic session(busy) recovery', () => {
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection }),
    )
    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'Hello world' } })
    expect(result.current.liveMessage?.content).toBe('Hello world')
    expect(result.current.isStreaming).toBe(true)

    // SSE reconnect recovery synthesizes a session(busy) — it must NOT wipe
    // the cumulative streamContent (which would restart the typewriter from 0).
    emitAndFlush({ type: 'session', session: { action: 'busy', chat_id: 'c1' } })

    expect(result.current.liveMessage?.content).toBe('Hello world')
    expect(result.current.isStreaming).toBe(true)
  })

  it('cancel ack (text with cancelled=true) does not call onAssistantComplete', () => {
    const complete = vi.fn()
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', onAssistantComplete: complete, ws: currentWS as unknown as WSConnection }),
    )
    // Simulate a normal turn completion first
    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'prev reply' } })
    emitAndFlush({ type: 'text', seq: 10, content: 'prev reply' })
    expect(complete).toHaveBeenCalledTimes(1)

    // User sends a new message then cancels before stream content arrives.
    // session(busy) must NOT reset finalizedRef (only stream_content does).
    emitAndFlush({ type: 'session', session: { action: 'busy', chat_id: 'c1' } })
    // Cancel ack: text event with cancelled=true and empty content
    emitAndFlush({ type: 'text', seq: 20, content: '', cancelled: true })
    // onAssistantComplete must NOT be called again — no duplicate message
    expect(complete).toHaveBeenCalledTimes(1)
    expect(result.current.liveMessage).toBeNull()
    expect(result.current.isStreaming).toBe(false)

    // session(idle) must not trigger defensive finalize either
    emitAndFlush({ type: 'session', session: { action: 'idle', chat_id: 'c1' } })
    expect(complete).toHaveBeenCalledTimes(1)
  })

  it('finalizes on text: calls onAssistantComplete and clears the stream', () => {
    const complete = vi.fn()
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', onAssistantComplete: complete, ws: currentWS as unknown as WSConnection }),
    )
    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'partial' } })
    expect(result.current.liveMessage?.content).toBe('partial')

    emitAndFlush({
      type: 'text',
      seq: 42,
      content: 'final answer',
      progress_history: '[{"iteration":1,"tools":[{"name":"Read","status":"done"}]}]',
    })
    expect(complete).toHaveBeenCalledTimes(1)
    expect(complete).toHaveBeenCalledWith('final answer', expect.any(Array), 42)
    expect(result.current.liveMessage).toBeNull()
    expect(result.current.isStreaming).toBe(false)
  })

  it('does not restore completed progress from a stale terminal cache snapshot', () => {
    const cacheKey = sessionCacheKey('web', 'c1')
    progressSnapshotCache.set(cacheKey, { phase: 'tool', completed_tools: [{ name: 'Read', status: 'done' }] })
    const complete = vi.fn()
    const { result, rerender } = renderHook(
      ({ chatID }) => useProgressStream({ chatID, onAssistantComplete: complete, ws: currentWS as unknown as WSConnection }),
      { initialProps: { chatID: 'c1' } },
    )
    act(() => {
      rafCbs.splice(0, rafCbs.length).forEach((cb) => cb())
    })
    // Cache hydration was intentionally removed — history's active_progress is
    // the single source. A stale cache entry must NOT restore live progress.
    expect(result.current.isStreaming).toBe(false)

    emitAndFlush({ type: 'text', chat_id: 'c1', content: 'done' })
    expect(progressSnapshotCache.has(cacheKey)).toBe(false)

    rerender({ chatID: 'c2' })
    rerender({ chatID: 'c1' })
    act(() => {
      rafCbs.splice(0, rafCbs.length).forEach((cb) => cb())
    })
    expect(result.current.liveMessage).toBeNull()
    expect(result.current.isStreaming).toBe(false)
  })

  it('clears the previous session progress before returning from a transition', () => {
    const { result, rerender } = renderHook(
      ({ chatID }) => useProgressStream({ chatID, ws: currentWS as unknown as WSConnection }),
      { initialProps: { chatID: 'c1' } },
    )
    emitAndFlush({ type: 'stream_content', chat_id: 'c1', progress: { stream_content: 'session A' } })
    expect(result.current.liveMessage?.content).toBe('session A')

    rerender({ chatID: 'c2' })

    expect(result.current.liveMessage).toBeNull()
    expect(result.current.isStreaming).toBe(false)
  })

  it('resets finalization when a later turn begins with structured tool progress', () => {
    const complete = vi.fn()
    renderHook(() =>
      useProgressStream({ chatID: 'c1', onAssistantComplete: complete, ws: currentWS as unknown as WSConnection }),
    )

    emitAndFlush({ type: 'text', chat_id: 'c1', content: 'first' })
    // session(busy) on a clean store resets finalizedRef for the new turn
    emitAndFlush({ type: 'session', session: { action: 'busy', chat_id: 'c1' } })
    emitAndFlush({
      type: 'progress_structured',
      chat_id: 'c1',
      progress: {
        phase: 'tool',
        iteration: 1,
        completed_tools: [{ name: 'Read', status: 'done' }],
      },
    })
    emitAndFlush({ type: 'text', chat_id: 'c1', content: 'second' })

    expect(complete).toHaveBeenCalledTimes(2)
    expect(complete.mock.calls.map((call) => call[0])).toEqual(['first', 'second'])
  })

  it('keeps live progress until a terminal event clears it (recovery phase=done)', () => {
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection }),
    )
    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'stale' } })
    expect(result.current.isStreaming).toBe(true)

    // phase=done alone does NOT clear the store — it dispatches agent-idle but
    // preserves iterations to avoid a flash. The text or session(idle) event
    // is responsible for finalization.
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'done' } })
    expect(result.current.liveMessage).not.toBeNull()

    emitAndFlush({ type: 'text', chat_id: 'c1', content: 'final' })
    expect(result.current.liveMessage).toBeNull()
    expect(result.current.isStreaming).toBe(false)
    expect(result.current.progressSnapshot.phase).toBe('')
  })

  it('commits text once when phase done arrives before the final text and idle', () => {
    const complete = vi.fn()
    renderHook(() =>
      useProgressStream({ chatID: 'c1', onAssistantComplete: complete, ws: currentWS as unknown as WSConnection }),
    )

    emitAndFlush({ type: 'progress_structured', chat_id: 'c1', progress: { phase: 'done' } })
    emitAndFlush({ type: 'text', chat_id: 'c1', content: 'final answer' })
    emitAndFlush({ type: 'session', session: { action: 'idle', chat_id: 'c1' } })

    expect(complete).toHaveBeenCalledTimes(1)
    expect(complete).toHaveBeenCalledWith('final answer', expect.any(Array), undefined)
  })

  it('handles session_reset text without appending assistant content', () => {
    const complete = vi.fn()
    const reset = vi.fn()
    const { result } = renderHook(() =>
      useProgressStream({
        chatID: 'c1',
        onAssistantComplete: complete,
        onSessionReset: reset,
        ws: currentWS as unknown as WSConnection,
      }),
    )
    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'partial' } })
    emitAndFlush({
      type: 'text',
      content: '会话已重置',
      metadata: { session_reset: 'true' },
    })
    expect(complete).not.toHaveBeenCalled()
    expect(reset).toHaveBeenCalledTimes(1)
    expect(result.current.liveMessage).toBeNull()
    expect(result.current.isStreaming).toBe(false)
  })

  it('parses progress_history iteration JSON into onAssistantComplete iterations', () => {
    const complete = vi.fn()
    renderHook(() =>
      useProgressStream({ chatID: 'c1', onAssistantComplete: complete, ws: currentWS as unknown as WSConnection }),
    )
    emitAndFlush({
      type: 'text',
      content: 'done',
      progress_history:
        '[{"iteration":1,"thinking":"t","tools":[{"name":"Read","status":"done","summary":"ok"}]}]',
    })
    expect(complete).toHaveBeenCalled()
    const [, iterations] = complete.mock.calls[0]
    expect(iterations).toHaveLength(1)
    expect(iterations[0].iteration).toBe(1)
    expect(iterations[0].tools[0].name).toBe('Read')
  })

  it('uses accumulated visible progress when final text has no progress history', () => {
    const complete = vi.fn()
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', onAssistantComplete: complete, ws: currentWS as unknown as WSConnection }),
    )
    emitAndFlush({
      type: 'progress_structured',
      progress: {
        chat_id: 'web:c1',
        iteration: 1,
        iteration_history: [
          { iteration: 1, completed_tools: [{ name: 'Read', status: 'done', summary: 'ok' }] },
        ],
      } as ProgressEvent,
    })
    expect(result.current.liveMessage).not.toBeNull()

    emitAndFlush({ type: 'text', content: '', progress_history: '[]' })

    expect(complete).toHaveBeenCalledWith('', expect.arrayContaining([
      expect.objectContaining({ iteration: 1 }),
    ]), undefined)
    expect(result.current.liveMessage).toBeNull()
  })

  it('defensively finalizes accumulated stream on session(idle)', () => {
    const complete = vi.fn()
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', onAssistantComplete: complete, ws: currentWS as unknown as WSConnection }),
    )
    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'streamed' } })
    emitAndFlush({ type: 'session', session: { action: 'idle', chat_id: 'c1' } })
    expect(complete).toHaveBeenCalledWith('streamed', expect.any(Array), undefined)
    expect(result.current.liveMessage).toBeNull()
  })

  it('defensively finalizes visible tool-only progress on session(idle)', () => {
    const complete = vi.fn()
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', onAssistantComplete: complete, ws: currentWS as unknown as WSConnection }),
    )
    emitAndFlush({
      type: 'progress_structured',
      progress: {
        chat_id: 'web:c1',
        iteration: 1,
        completed_tools: [{ name: 'Read', status: 'done', summary: 'ok' }],
        iteration_history: [
          { iteration: 1, completed_tools: [{ name: 'Read', status: 'done', summary: 'ok' }] },
        ],
      } as ProgressEvent,
    })
    expect(result.current.liveMessage).not.toBeNull()
    expect(result.current.isStreaming).toBe(true)

    emitAndFlush({ type: 'session', session: { action: 'idle', chat_id: 'c1' } })

    expect(complete).toHaveBeenCalledWith('', expect.arrayContaining([
      expect.objectContaining({ iteration: 1 }),
    ]), undefined)
    expect(result.current.liveMessage).toBeNull()
    expect(result.current.isStreaming).toBe(false)
  })

  it('ignores session(idle) from a different chat', () => {
    const complete = vi.fn()
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', onAssistantComplete: complete, ws: currentWS as unknown as WSConnection }),
    )
    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'ours' } })
    // a *different* chat goes idle — must not finalize ours
    emitAndFlush({ type: 'session', session: { action: 'idle', chat_id: 'other' } })
    expect(complete).not.toHaveBeenCalled()
    expect(result.current.liveMessage?.content).toBe('ours')
  })

  it('ignores stream_content from a different chat (top-level chat_id filter)', () => {
    const { result } = renderHook(() => useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection }))
    emitAndFlush({
      type: 'stream_content',
      chat_id: 'other',
      progress: { stream_content: 'not ours' },
    })
    expect(result.current.liveMessage).toBeNull()
  })

  it('hydrates from initialProgress when the session is busy', () => {
    const { result } = renderHook(() =>
      useProgressStream({
        chatID: 'c1',
        initialProgress: {
          phase: 'thinking',
          iteration: 3,
          stream_content: 'resumed stream',
          active_tools: [{ name: 'Shell', status: 'running' }],
          completed_tools: [{ name: 'Read', status: 'done', summary: 'ok' }],
          // active_progress iteration_history uses the slim histIterSnapshot
          // shape (completed_tools, not tools) — verify the fallback works.
          iteration_history: [
            { iteration: 1, completed_tools: [{ name: 'Grep', status: 'done' }] },
          ],
          sub_agents: [
            {
              role: 'review',
              instance: '1',
              status: 'running',
              desc: 'checking',
              children: [{ role: 'fix', status: 'pending' }],
            },
          ],
        },
        ws: currentWS as unknown as WSConnection,
      }),
    )
    // The hydrate runs in an effect and is throttled via rAF; flush it.
    act(() => {
      rafCbs.splice(0, rafCbs.length).forEach((cb) => cb())
    })
    expect(result.current.isStreaming).toBe(true)
    expect(result.current.liveMessage?.content).toBe('resumed stream')
    expect(result.current.progressSnapshot.activeTools).toHaveLength(1)
    expect(result.current.progressSnapshot.completedTools).toHaveLength(1)
    expect(result.current.progressSnapshot.iteration).toBe(3)
    expect(result.current.progressSnapshot.iterationHistory).toHaveLength(1)
    // normalizeIteration fell back to completed_tools:
    expect(result.current.progressSnapshot.iterationHistory[0].tools).toHaveLength(1)
    expect(result.current.progressSnapshot.iterationHistory[0].tools[0].name).toBe('Grep')
    expect(result.current.progressSnapshot.subAgents[0].role).toBe('review')
    expect(result.current.progressSnapshot.subAgents[0].children?.[0].role).toBe('fix')
  })

  it('installs a busy snapshot watermark and ignores replayed semantic logs', () => {
    const skillIteration = {
      iteration: 1,
      completed_tools: [
        { name: 'Skill', label: 'debug', status: 'done' },
        { name: 'Read', label: 'progressStore.ts', status: 'done' },
      ],
    }
    const { result } = renderHook(() =>
      useProgressStream({
        chatID: 'c1',
        initialProgress: {
          seq: 20,
          phase: 'thinking',
          iteration: 2,
          iteration_history: [skillIteration],
        },
        ws: currentWS as unknown as WSConnection,
      }),
    )
    act(() => {
      rafCbs.splice(0, rafCbs.length).forEach((cb) => cb())
    })

    emitAndFlush({
      type: 'progress_structured',
      progress: {
        seq: 20,
        phase: 'thinking',
        iteration: 2,
        iteration_history: [skillIteration],
      } as ProgressEvent,
    })

    expect(result.current.progressSnapshot.eventSeq).toBe(20)
    expect(result.current.progressSnapshot.iterationHistory).toHaveLength(1)
    expect(result.current.progressSnapshot.iterationHistory[0].tools).toHaveLength(2)
  })

  it('does not hydrate when initialProgress phase is done', () => {
    const { result } = renderHook(() =>
      useProgressStream({
        chatID: 'c1',
        initialProgress: { phase: 'done', stream_content: 'done text' },
        ws: currentWS as unknown as WSConnection,
      }),
    )
    expect(result.current.isStreaming).toBe(false)
    expect(result.current.liveMessage).toBeNull()
  })

  it('updates tools/reasoning/iteration from progress_structured', () => {
    const { result } = renderHook(() => useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection }))
    emitAndFlush({
      type: 'progress_structured',
      progress: {
        iteration: 2,
        phase: 'tool_exec',
        reasoning: 'because',
        active_tools: [{ name: 'Grep', status: 'running' }],
      } as ProgressEvent,
    })
    expect(result.current.progressSnapshot.iteration).toBe(2)
    expect(result.current.progressSnapshot.activeTools[0].name).toBe('Grep')
    expect(result.current.progressSnapshot.lastReasoning).toBe('because')
  })

  it('reloads when progress_structured reports history_compacted', () => {
    const compacted = vi.fn()
    const { result } = renderHook(() =>
      useProgressStream({
        chatID: 'c1',
        onHistoryCompacted: compacted,
        ws: currentWS as unknown as WSConnection,
      }),
    )
    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'partial' } })

    emitAndFlush({
      type: 'progress_structured',
      progress: {
        chat_id: 'web:c1',
        history_compacted: true,
      } as ProgressEvent,
    })

    expect(compacted).toHaveBeenCalledTimes(1)
    expect(result.current.liveMessage).toBeNull()
    expect(result.current.isStreaming).toBe(false)
  })

  it('renders a live message when progress_structured only contains sub_agents', () => {
    const { result } = renderHook(() => useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection }))
    emitAndFlush({
      type: 'progress_structured',
      progress: {
        chat_id: 'web:c1',
        sub_agents: [
          {
            role: 'review',
            instance: '1',
            status: 'running',
            desc: 'checking',
          },
        ],
      } as ProgressEvent,
    })
    expect(result.current.liveMessage).not.toBeNull()
    expect(result.current.isStreaming).toBe(true)
    expect(result.current.progressSnapshot.subAgents[0].role).toBe('review')
  })

  it('accepts channel-qualified progress chat_id for CLI sessions', () => {
    const { result } = renderHook(() =>
      useProgressStream({
        chatID: '/repo:Agent-main',
        channel: 'cli',
        ws: currentWS as unknown as WSConnection,
      }),
    )
    emitAndFlush({
      type: 'progress_structured',
      progress: {
        chat_id: 'cli:/repo:Agent-main',
        sub_agents: [{ role: 'review', status: 'running' }],
      } as ProgressEvent,
    })
    expect(result.current.isStreaming).toBe(true)
    expect(result.current.progressSnapshot.subAgents[0].role).toBe('review')
  })

  it('rejects another channel with the same raw progress chat_id', () => {
    const { result } = renderHook(() =>
      useProgressStream({
        chatID: 'shared',
        channel: 'cli',
        ws: currentWS as unknown as WSConnection,
      }),
    )
    emitAndFlush({
      type: 'progress_structured',
      progress: {
        chat_id: 'web:shared',
        sub_agents: [{ role: 'foreign', status: 'running' }],
      } as ProgressEvent,
    })
    expect(result.current.isStreaming).toBe(false)
  })

  it('regression: todos survive busy→PhaseDone→text lifecycle', () => {
    // Bug: PhaseDone discarded its todos (setStructuredTools early-returned on
    // phase==='done'), so todos were lost at the turn boundary and only
    // reappeared on the next history reload (idle) — never during busy.
    const todos = [
      { id: 1, text: 'task A', done: true },
      { id: 2, text: 'task B', done: false },
    ]
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection }),
    )

    emitAndFlush({ type: 'session', session: { action: 'busy', chat_id: 'c1' } })

    // mid-busy structured event carries todos (TodoWrite just ran)
    emitAndFlush({
      type: 'progress_structured',
      progress: { chat_id: 'web:c1', seq: 1, phase: 'tool_exec', iteration: 1, todos } as ProgressEvent,
    })
    expect(result.current.progressSnapshot.todos).toHaveLength(2)

    // PhaseDone carries todos too — must NOT be discarded
    emitAndFlush({
      type: 'progress_structured',
      progress: { chat_id: 'web:c1', seq: 2, phase: 'done', todos } as ProgressEvent,
    })
    expect(result.current.progressSnapshot.todos).toHaveLength(2)

    // text finalize preserves todos
    emitAndFlush({ type: 'text', content: 'reply', chat_id: 'c1' })
    expect(result.current.progressSnapshot.todos).toHaveLength(2)
  })
})

describe('cancel ack: commits via onAssistantComplete with server data', () => {
  it('commits server progress_history (includes user_cancelled) on cancel', () => {
    const complete = vi.fn()
    const { result } = renderHook(() =>
      useProgressStream({
        chatID: 'c1',
        onAssistantComplete: complete,
        ws: currentWS as unknown as WSConnection,
      }),
    )

    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'partial reply' } })
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'done' } })

    // Cancel ack with server progress_history (includes user_cancelled)
    const serverHistory = JSON.stringify([{
      iteration: 1,
      tools: [
        { name: 'Read', status: 'done', summary: 'read file' },
        { name: 'user_cancelled', status: 'done', summary: 'cancelled by user' },
      ],
    }])
    emitAndFlush({ type: 'text', chat_id: 'c1', content: '', cancelled: true, progress_history: serverHistory })

    expect(complete).toHaveBeenCalledTimes(1)
    const [, iterations] = complete.mock.calls[0]
    expect(iterations).toHaveLength(1)
    const allTools = iterations[0].tools.map((t: { name: string }) => t.name)
    expect(allTools).toContain('user_cancelled')
    expect(result.current.liveMessage).toBeNull()
  })

  it('merges live-only iterations with server data', () => {
    const complete = vi.fn()
    renderHook(() =>
      useProgressStream({ chatID: 'c1', onAssistantComplete: complete, ws: currentWS as unknown as WSConnection }),
    )

    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'hello' } })
    emitAndFlush({ type: 'progress_structured', progress: {
      phase: 'tool_exec', iteration: 2,
      iteration_history: [
        { iteration: 1, thinking: '', reasoning: '', tools: [], toolCount: 0 },
        { iteration: 2, thinking: '', reasoning: '', tools: [{ name: 'Grep', status: 'done' }], toolCount: 1 },
      ],
    } })

    const serverHistory = JSON.stringify([{
      iteration: 2,
      tools: [{ name: 'Grep', status: 'done' }, { name: 'user_cancelled', status: 'done' }],
    }])
    emitAndFlush({ type: 'text', chat_id: 'c1', content: '', cancelled: true, progress_history: serverHistory })

    expect(complete).toHaveBeenCalledTimes(1)
    const [, iterations] = complete.mock.calls[0]
    const iterNums = iterations.map((i: { iteration: number }) => i.iteration).sort()
    expect(iterNums).toContain(1)
    expect(iterNums).toContain(2)
  })
})

describe('cancel: no duplicate message', () => {
  it('session(idle) before cancel ack does NOT call onAssistantComplete', () => {
    const complete = vi.fn()
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', onAssistantComplete: complete, ws: currentWS as unknown as WSConnection }),
    )

    // LLM generated content
    emitAndFlush({ type: 'stream_content', progress: { stream_content: '已修复并部署' } })
    expect(result.current.liveMessage?.content).toBe('已修复并部署')

    // PhaseDone (progressFinalizer) — preserves streamContent
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'done' } })

    // session(idle) arrives BEFORE text(cancelled=true)
    emitAndFlush({ type: 'session', session: { action: 'idle', chat_id: 'c1' } })

    // BUG: defensive finalize commits the content — but this is a cancelled
    // turn, the backend already persisted it to DB. History reload will show
    // it again → duplicate.
    expect(complete).not.toHaveBeenCalled()

    // Cancel ack arrives
    emitAndFlush({ type: 'text', chat_id: 'c1', content: '', cancelled: true })
    expect(complete).not.toHaveBeenCalled()
    expect(result.current.liveMessage).toBeNull()
  })
})

describe('cancel: iteration preservation', () => {
  it('commits server progress_history + live content on cancel (no vanish)', () => {
    const complete = vi.fn()
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', onAssistantComplete: complete, ws: currentWS as unknown as WSConnection }),
    )

    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'partial reply' } })
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'done' } })

    const serverHistory = JSON.stringify([{
      iteration: 1,
      tools: [{ name: 'Read', status: 'done' }, { name: 'user_cancelled', status: 'done' }],
    }])
    emitAndFlush({ type: 'text', chat_id: 'c1', content: '', cancelled: true, progress_history: serverHistory })

    expect(complete).toHaveBeenCalledTimes(1)
    const [content, iterations] = complete.mock.calls[0]
    expect(content).toBe('partial reply')
    expect(iterations[0].tools.map((t: { name: string }) => t.name)).toContain('user_cancelled')
    expect(result.current.liveMessage).toBeNull()
  })
})

describe('bang command: text without PhaseDone clears busy', () => {
  it('dispatches agent-idle when text event arrives without PhaseDone', () => {
    const complete = vi.fn()
    const idleSpy = vi.fn()
    window.addEventListener('agent-idle', idleSpy)

    renderHook(() =>
      useProgressStream({ chatID: 'c1', onAssistantComplete: complete, ws: currentWS as unknown as WSConnection }),
    )

    // Bang command: session(busy) → text → session(idle)
    emitAndFlush({ type: 'session', session: { action: 'busy', chat_id: 'c1' } })
    emitAndFlush({ type: 'text', chat_id: 'c1', content: 'bang output' })

    // onAssistantComplete should be called with the bang output
    expect(complete).toHaveBeenCalledWith('bang output', [], undefined)

    // agent-idle should be dispatched (clears busy even without PhaseDone)
    expect(idleSpy).toHaveBeenCalled()

    window.removeEventListener('agent-idle', idleSpy)
  })
})

describe('busy: no iteration lost under packet loss', () => {
  it('preserves iteration 1 when its progress_structured delta is dropped', () => {
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection }),
    )

    // Iteration 1: tool running (no iteration_history yet — delta comes later)
    emitAndFlush({ type: 'progress_structured', seq: 1, progress: {
      phase: 'tool_exec', iteration: 1,
      active_tools: [{ name: 'Read', status: 'running', iteration: 1 }],
    } })
    // SIMULATE PACKET LOSS: the event that would carry iter 1's delta is dropped

    // Iteration 2 starts — carries iter 1 as a delta
    emitAndFlush({ type: 'progress_structured', seq: 3, progress: {
      phase: 'tool_exec', iteration: 2,
      active_tools: [{ name: 'Shell', status: 'running', iteration: 2 }],
      iteration_history: [{ iteration: 1, thinking: '', reasoning: '', tools: [{ name: 'Read', status: 'done', iteration: 1 }], toolCount: 1 }],
    } })

    // iter 1 must survive in iterationHistory
    const iter1 = result.current.progressSnapshot.iterationHistory.find(i => i.iteration === 1)
    expect(iter1).toBeDefined()
    expect(iter1?.tools[0]?.name).toBe('Read')
  })

  it('preserves all iterations when multiple delta events are dropped', () => {
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection }),
    )

    // Iteration 1
    emitAndFlush({ type: 'progress_structured', seq: 1, progress: {
      phase: 'tool_exec', iteration: 1,
      active_tools: [{ name: 'Read', status: 'running', iteration: 1 }],
    } })
    // Delta for iter 1 DROPPED

    // Iteration 2 (carries iter 1 delta)
    emitAndFlush({ type: 'progress_structured', seq: 3, progress: {
      phase: 'tool_exec', iteration: 2,
      active_tools: [{ name: 'Grep', status: 'running', iteration: 2 }],
      iteration_history: [{ iteration: 1, thinking: '', reasoning: '', tools: [{ name: 'Read', status: 'done', iteration: 1 }], toolCount: 1 }],
    } })
    // Delta for iter 2 DROPPED

    // Iteration 3 (carries iter 2 delta — server sends only 0-1 entries)
    emitAndFlush({ type: 'progress_structured', seq: 5, progress: {
      phase: 'tool_exec', iteration: 3,
      active_tools: [{ name: 'Shell', status: 'running', iteration: 3 }],
      iteration_history: [{ iteration: 2, thinking: '', reasoning: '', tools: [{ name: 'Grep', status: 'done', iteration: 2 }], toolCount: 1 }],
    } })

    const iters = result.current.progressSnapshot.iterationHistory.map(i => i.iteration).sort()
    // Both iter 1 and iter 2 must be present
    expect(iters).toContain(1)
    expect(iters).toContain(2)
  })

  it('recovers lost iteration via restoreActiveProgress (same seq, full history)', () => {
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection }),
    )

    // seq 1: iter 1 tool running
    emitAndFlush({ type: 'progress_structured', seq: 1, progress: {
      phase: 'tool_exec', iteration: 1,
      active_tools: [{ name: 'Read', status: 'running', iteration: 1 }],
    } })
    // seq 2: DROPPED (would have carried iter 1's completed_tools + delta)

    // seq 3: iter 2 starts (NO delta — server only sends 0-1 entries)
    emitAndFlush({ type: 'progress_structured', seq: 3, progress: {
      phase: 'tool_exec', iteration: 2,
      active_tools: [{ name: 'Shell', status: 'running', iteration: 2 }],
    } })

    // seq gap detected → restoreActiveProgress fires → fetches get_active_progress
    // The backend returns seq=3 (same as last event) WITH full iteration_history
    // (from a.iterationHistories, which has iter 1 even though the delta was dropped)
    emitAndFlush({ type: 'progress_structured', seq: 3, progress: {
      phase: 'tool_exec', iteration: 2,
      active_tools: [{ name: 'Shell', status: 'running', iteration: 2 }],
      iteration_history: [{ iteration: 1, thinking: '', reasoning: '', tools: [{ name: 'Read', status: 'done', iteration: 1 }], toolCount: 1 }],
    } })

    const snap = result.current.progressSnapshot
    // iter 1 MUST be recovered — the eventSeq check must NOT drop iterationHistory
    expect(snap.iterationHistory.find(i => i.iteration === 1)).toBeDefined()
    expect(snap.iterationHistory.find(i => i.iteration === 1)?.tools[0]?.name).toBe('Read')
  })
})

describe('cancel: assistant message must not vanish', () => {
  it('commits non-empty content + iterations on cancel (no empty assistant)', () => {
    const complete = vi.fn()
    renderHook(() =>
      useProgressStream({ chatID: 'c1', onAssistantComplete: complete, ws: currentWS as unknown as WSConnection }),
    )

    // session(busy) → stream_content → progress_structured → PhaseDone → cancel
    emitAndFlush({ type: 'session', session: { action: 'busy', chat_id: 'c1' } })
    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'I am working on' } })
    emitAndFlush({ type: 'progress_structured', seq: 1, progress: {
      phase: 'tool_exec', iteration: 1,
      active_tools: [{ name: 'Read', status: 'running', iteration: 1 }],
    } })
    emitAndFlush({ type: 'progress_structured', seq: 2, progress: { phase: 'done' } })
    emitAndFlush({ type: 'text', chat_id: 'c1', content: '', cancelled: true })

    // onAssistantComplete MUST be called with non-empty content
    expect(complete).toHaveBeenCalledTimes(1)
    const [content] = complete.mock.calls[0]
    expect(content).not.toBe('')
    expect(content).toBe('I am working on')
  })

  it('does NOT commit an empty assistant message when cancel has no content', () => {
    const complete = vi.fn()
    renderHook(() =>
      useProgressStream({ chatID: 'c1', onAssistantComplete: complete, ws: currentWS as unknown as WSConnection }),
    )

    // Cancel immediately — no stream content, no iterations
    emitAndFlush({ type: 'session', session: { action: 'busy', chat_id: 'c1' } })
    emitAndFlush({ type: 'progress_structured', seq: 1, progress: { phase: 'done' } })
    emitAndFlush({ type: 'text', chat_id: 'c1', content: '', cancelled: true })

    // onAssistantComplete should NOT be called — there's nothing to commit
    // (empty content + no iterations = no visible assistant message)
    expect(complete).not.toHaveBeenCalled()
  })
})
