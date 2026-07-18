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

  it('clears terminal cache so A to B to A cannot restore completed progress', () => {
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
    expect(result.current.isStreaming).toBe(true)

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

  it('resets visible progress when recovery reports a terminal phase', () => {
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection }),
    )
    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'stale' } })
    expect(result.current.isStreaming).toBe(true)

    emitAndFlush({ type: 'progress_structured', progress: { phase: 'done' } })

    expect(result.current.liveMessage).toBeNull()
    expect(result.current.isStreaming).toBe(false)
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
})
