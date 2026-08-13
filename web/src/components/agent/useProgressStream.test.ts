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
import { MessageStore } from '@/components/agent/messageStore'
import { clearWebCaches, progressSnapshotCache, sessionCacheKey } from '@/lib/webCache'
import { parseSSEDump } from '@/test-utils/sseReplay'

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

  it('cancel ack after turn completion does not commit duplicate', () => {
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
    // finalizedRef is still true from turn 1's text event → cancel ack
    // sees finalizedRef=true → returns early (line 608) → no duplicate commit
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
    expect(complete).toHaveBeenCalledWith('final answer', expect.any(Array), 42, undefined)
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
    expect(complete).toHaveBeenCalledWith('final answer', expect.any(Array), undefined, undefined)
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
    ]), undefined, undefined)
    expect(result.current.liveMessage).toBeNull()
  })

  it('defensively finalizes accumulated stream on session(idle)', () => {
    const complete = vi.fn()
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', onAssistantComplete: complete, ws: currentWS as unknown as WSConnection }),
    )
    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'streamed' } })
    emitAndFlush({ type: 'session', session: { action: 'idle', chat_id: 'c1' } })
    expect(complete).toHaveBeenCalledWith('streamed', expect.any(Array), undefined, undefined)
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
    ]), undefined, undefined)
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

  it('reload completing with active_progress=null must NOT wipe a streaming turn (turn DOM vanish)', () => {
    // User report: "agent turn 消失 — 消失之后那个 agent turn 的 dom 都没了（不是空
    // dom，而是直接没了）". Root cause: a reload() (triggered by SSE seq gap /
    // resync_required / rewind) completes while the turn is STILL STREAMING, and
    // fetchHistory returns active_progress=null (backend snapshot not registered
    // yet / rewind cleared it / fetch raced). The hydration effect then called
    // store.reset() unconditionally, wiping the entire live turn from the DOM.
    // Fix: only reset when the store is genuinely idle (not streaming, no active
    // phase) — a live turn must survive a stale null reload snapshot.
    const { result, rerender } = renderHook(
      (props: Partial<Parameters<typeof useProgressStream>[0]>) =>
        useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection, ...props }),
      { initialProps: { initialProgress: null } },
    )
    // Turn starts streaming (iter 1 reasoning + a running tool).
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 1, chat_id: 'web:c1' } })
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'thinking', iteration: 1, turn_id: 1, chat_id: 'web:c1' } })
    emitAndFlush({ type: 'progress_structured', progress: { iteration: 1, turn_id: 1, reasoning_stream_content: 'reasoning' } })
    emitAndFlush({
      type: 'progress_structured',
      progress: { phase: 'tool_exec', iteration: 1, turn_id: 1, active_tools: [{ name: 'Read', status: 'running' }] },
    })
    expect(result.current.liveMessage).not.toBeNull()

    // A reload that first returns a valid snapshot (hydrate) then a null one
    // (the turn is mid-stream on the server but the snapshot fetch raced).
    act(() => {
      rerender({ initialProgress: { phase: 'thinking', iteration: 1, seq: 1 } as unknown as null })
    })
    act(() => { rafCbs.splice(0, rafCbs.length).forEach((cb) => cb()) })
    expect(result.current.liveMessage).not.toBeNull()

    act(() => {
      rerender({ initialProgress: null })
    })
    act(() => { rafCbs.splice(0, rafCbs.length).forEach((cb) => cb()) })
    // The streaming turn must survive — no DOM vanish.
    expect(result.current.liveMessage).not.toBeNull()
    expect(result.current.isStreaming).toBe(true)
    expect(result.current.progressSnapshot.phase).toBe('tool_exec')
  })

  it('reload completing with active_progress=null DOES reset an idle store (turn already over)', () => {
    // Contrast test: when the store is genuinely idle (no streaming, no active
    // phase), a null reload snapshot legitimately clears stale state.
    const { result, rerender } = renderHook(
      (props: Partial<Parameters<typeof useProgressStream>[0]>) =>
        useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection, ...props }),
      { initialProps: { initialProgress: null } },
    )
    // A finished turn: stream content arrives, then the store is reset by the
    // text event (idle store, no streaming).
    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'done reply' } })
    expect(result.current.liveMessage).not.toBeNull()

    // Simulate text event finalization → store empty/idle.
    emitAndFlush({ type: 'text', chat_id: 'c1', content: 'done reply' })
    expect(result.current.liveMessage).toBeNull()

    // A stale null reload snapshot on an idle store must not resurrect anything.
    act(() => {
      rerender({ initialProgress: null })
    })
    act(() => { rafCbs.splice(0, rafCbs.length).forEach((cb) => cb()) })
    expect(result.current.liveMessage).toBeNull()
  })

  it('reload hydration must NOT overwrite a streaming store with a stale/laconic snapshot', () => {
    // User report: "回复显示到一半突然消失" (same as prior RENDER_LOSS_ROWS
    // rowsLen:0 reports). reload() completes MID-STREAM (resync_required /
    // replay_gap / seq-gap reload while the agent is still streaming), and the
    // server snapshot can be stale or laconic vs. the live SSE-driven store:
    // from_iteration delta filtering returns only NEW iterations, an
    // iteration-boundary snapshot has visible fields cleared by
    // historyProgressToLive, or the snapshot simply lags SSE. The hydration
    // effect's store.replace(live) OVERWROTE the streaming store with it —
    // leaving no visible fields → liveMessage null → the entire live turn
    // vanished (rowsLen:0). Same bug class as the reset guards: only hydrate
    // a genuinely idle store; let SSE events drive.
    const { result, rerender } = renderHook(
      (props: Partial<Parameters<typeof useProgressStream>[0]>) =>
        useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection, ...props }),
      { initialProps: { initialProgress: null as ProgressEvent | null } },
    )
    // Turn starts streaming (iter 1 thinking + a running tool + stream content).
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 1, chat_id: 'web:c1' } })
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'thinking', iteration: 1, turn_id: 1, chat_id: 'web:c1' } })
    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'half of the reply...' } })
    emitAndFlush({
      type: 'progress_structured',
      progress: { phase: 'tool_exec', iteration: 1, turn_id: 1, active_tools: [{ name: 'Read', status: 'running' }] },
    })
    expect(result.current.liveMessage).not.toBeNull()
    expect(result.current.progressSnapshot.streamContent).toContain('half of the reply')

    // reload completes mid-stream: server snapshot is LACONIC — phase set but
    // no visible fields (iteration-boundary snapshot / delta-filtered history).
    act(() => {
      rerender({ initialProgress: { phase: 'tool_exec', iteration: 2, turn_id: 1 } as ProgressEvent | null })
    })
    act(() => { rafCbs.splice(0, rafCbs.length).forEach((cb) => cb()) })

    // The streaming turn must survive — hydration must not replace the live store.
    expect(result.current.liveMessage).not.toBeNull()
    expect(result.current.progressSnapshot.streamContent).toContain('half of the reply')
    expect(result.current.isStreaming).toBe(true)
  })

  it('hydration REPLACES a streaming store when iterationHistory is broken (repair the gap — no reload loop)', () => {
    // User report: "turn 消失维持一个完整的迭代" — the iteration-gap detection
    // fired onIterationGap → reload, but hydration's storeActive guard SKIPPED
    // the replace → the broken iterationHistory never healed → onIterationGap
    // re-fired on every subsequent event → reload loop → live turn vanished for
    // a full iteration. Fix: the guard allows replace when hasIterationGapNow()
    // is true — the server snapshot carries the authoritative full history and
    // repairs the gap, terminating the loop.
    const onIterationGap = vi.fn()
    const { result, rerender } = renderHook(
      (props: Partial<Parameters<typeof useProgressStream>[0]>) =>
        useProgressStream({ chatID: 'c1', onIterationGap, ws: currentWS as unknown as WSConnection, ...props }),
      { initialProps: { initialProgress: null as ProgressEvent | null } },
    )
    // Turn streaming with complete history [1,2,3].
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 1, chat_id: 'web:c1' } })
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'tool_exec', iteration: 3, turn_id: 1, chat_id: 'web:c1', iteration_history: [{ iteration: 1 }, { iteration: 2 }, { iteration: 3 }] } })
    // Gap: iteration 5's delta arrives, iteration 4 was dropped → [1,2,3,5].
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'tool_exec', iteration: 5, turn_id: 1, chat_id: 'web:c1', iteration_history: [{ iteration: 5 }] } })
    expect(onIterationGap).toHaveBeenCalledTimes(1)
    expect(result.current.progressSnapshot.iterationHistory.map((i) => i.iteration)).toEqual([1, 2, 3, 5])

    // reload completes: server snapshot carries the authoritative FULL history.
    act(() => {
      rerender({
        initialProgress: {
          phase: 'tool_exec', iteration: 5, turn_id: 1,
          iteration_history: [{ iteration: 1 }, { iteration: 2 }, { iteration: 3 }, { iteration: 4 }, { iteration: 5 }],
        } as ProgressEvent | null,
      })
    })
    act(() => { rafCbs.splice(0, rafCbs.length).forEach((cb) => cb()) })

    // The gap must be REPAIRED (replace allowed despite streaming) — and the
    // onIterationGap one-shot re-arms only once the history is contiguous.
    expect(result.current.progressSnapshot.iterationHistory.map((i) => i.iteration)).toEqual([1, 2, 3, 4, 5])
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'tool_exec', iteration: 5, turn_id: 1, chat_id: 'web:c1', active_tools: [{ name: 'Shell', status: 'running', iteration: 5 }] } })
    expect(onIterationGap).toHaveBeenCalledTimes(1)
  })

  it('disabled toggle (subscription flake) must NOT wipe a streaming turn', () => {
    // User report: [RENDER_LOSS_ROWS] rowsLen:0, liveMessageId:null, busy:true
    // with a cli chatKey. The live store was blanked by the useLayoutEffect
    // ELSE branch (`store.reset()` on non-chatKey triggers) — `disabled`
    // (= !shouldSubscribe) toggles when the SSE subscription/connection state
    // briefly flips during reconnect / session-status jitter, even though the
    // chatKey is UNCHANGED. A mid-turn disabled flake wiped the entire live
    // store → liveMessage null → whole turn vanished. The live store is driven
    // by SSE events; a subscription-state toggle must NOT wipe it. Only reset
    // when genuinely idle (streaming=false, no active phase).
    const { result, rerender } = renderHook(
      (props: Partial<Parameters<typeof useProgressStream>[0]>) =>
        useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection, ...props }),
      { initialProps: { disabled: false } },
    )
    // Turn starts streaming (iter 1 thinking + a running tool).
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 1, chat_id: 'web:c1' } })
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'thinking', iteration: 1, turn_id: 1, chat_id: 'web:c1' } })
    emitAndFlush({ type: 'progress_structured', progress: { iteration: 1, turn_id: 1, reasoning_stream_content: 'reasoning' } })
    emitAndFlush({
      type: 'progress_structured',
      progress: { phase: 'tool_exec', iteration: 1, turn_id: 1, active_tools: [{ name: 'Read', status: 'running' }] },
    })
    expect(result.current.liveMessage).not.toBeNull()

    // Subscription flake: disabled flips true then false, chatKey unchanged.
    act(() => { rerender({ disabled: true }) })
    act(() => { rafCbs.splice(0, rafCbs.length).forEach((cb) => cb()) })
    act(() => { rerender({ disabled: false }) })
    act(() => { rafCbs.splice(0, rafCbs.length).forEach((cb) => cb()) })

    // The streaming turn must survive the disabled toggle.
    expect(result.current.liveMessage).not.toBeNull()
    expect(result.current.isStreaming).toBe(true)
    expect(result.current.progressSnapshot.phase).toBe('tool_exec')
  })

  it('disabled toggle DOES reset a genuinely idle store (turn already over)', () => {
    // Contrast: when the store is idle (turn finalized by text event), a
    // disabled toggle legitimately clears stale state.
    const { result, rerender } = renderHook(
      (props: Partial<Parameters<typeof useProgressStream>[0]>) =>
        useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection, ...props }),
      { initialProps: { disabled: false } },
    )
    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'done reply' } })
    emitAndFlush({ type: 'text', chat_id: 'c1', content: 'done reply' })
    expect(result.current.liveMessage).toBeNull()

    act(() => { rerender({ disabled: true }) })
    act(() => { rafCbs.splice(0, rafCbs.length).forEach((cb) => cb()) })
    expect(result.current.liveMessage).toBeNull()
  })

  it('liveMessage is null when initialProgress has phase=running but no active_tools (thinking indicator should show via busy placeholder)', () => {
    // BUG: switching to a busy session where the snapshot has no active_tools
    // (captured between iterations or during thinking). historyProgressToLive
    // sets streaming=true, which made hasVisibleProgress return true → liveMessage
    // non-null but empty → suppresses the "思考中…" busy placeholder.
    const { result } = renderHook(() =>
      useProgressStream({
        chatID: 'c1',
        initialProgress: {
          phase: 'running',
          iteration: 2,
          seq: 5,
          // NO active_tools, NO stream_content — agent is between iterations
        },
        ws: currentWS as unknown as WSConnection,
      }),
    )
    act(() => {
      rafCbs.splice(0, rafCbs.length).forEach((cb) => cb())
    })
    // liveMessage must be null so MessageList's busy placeholder shows "思考中…"
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

  it('PhaseDone with empty todos clears the store (todo_write([]) cleanup)', () => {
    // Bug: the PhaseDone branch filtered `p.todos.length > 0`, so a server
    // sending `todos: []` (turn-end cleanup of a fully-completed todo list)
    // was IGNORED — the frontend kept stale todos indefinitely, diverging
    // from the server's authoritative (cleared) state.
    const todos = [
      { id: 1, text: 'task A', done: true },
      { id: 2, text: 'task B', done: true },
    ]
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection }),
    )
    emitAndFlush({ type: 'session', session: { action: 'busy', chat_id: 'c1' } })
    emitAndFlush({
      type: 'progress_structured',
      progress: { chat_id: 'web:c1', seq: 1, phase: 'tool_exec', iteration: 1, todos } as ProgressEvent,
    })
    expect(result.current.progressSnapshot.todos).toHaveLength(2)

    // turn ends, server sends PhaseDone with empty todos (cleanupTodos cleared them)
    emitAndFlush({
      type: 'progress_structured',
      progress: { chat_id: 'web:c1', seq: 2, phase: 'done', todos: [] } as ProgressEvent,
    })
    expect(result.current.progressSnapshot.todos).toHaveLength(0)
  })

  it('PhaseDone with visible iterations but EMPTY streamContent (final text event lost) triggers reload', () => {
    // User report: "某个迭代结束 agent turn 消失了" with NO console error.
    // Root cause: the final reply travels ONLY in the text event; on a
    // stateless SSE stream that event can be dropped. streamContent was cleared
    // at the last iteration boundary, so after PhaseDone the store keeps only
    // iteration records — liveMessage stays NON-null (RENDER_LOSS_ROWS stays
    // silent!) but renders EMPTY → the reply "vanishes". Fix: PhaseDone with
    // visible progress + no accumulated reply text signals the lost text event
    // → reload from DB (authoritative complete reply).
    const onIterationGap = vi.fn()
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', onIterationGap, ws: currentWS as unknown as WSConnection }),
    )
    // Turn streams iterations; streamContent cleared at the 1→2 boundary.
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 1, chat_id: 'web:c1' } })
    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'partial reply...', turn_id: 1 } })
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'tool_exec', iteration: 1, turn_id: 1, chat_id: 'web:c1', iteration_history: [{ iteration: 1 }] } })
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'thinking', iteration: 2, turn_id: 1, chat_id: 'web:c1', iteration_history: [{ iteration: 2 }] } })
    expect(onIterationGap).not.toHaveBeenCalled()
    // PhaseDone: streamContent was cleared by the iteration boundary, no text
    // event arrived → the reply is lost → must trigger reload.
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'done', iteration: 2, turn_id: 1, chat_id: 'web:c1' } })
    expect(onIterationGap).toHaveBeenCalledTimes(1)
    expect(result.current.progressSnapshot.iterationHistory.map((i) => i.iteration)).toEqual([1, 2])
  })

  it('PhaseDone with todos present preserves them (not cleared)', () => {
    const todos = [
      { id: 1, text: 'task A', done: true },
      { id: 2, text: 'task B', done: false },
    ]
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection }),
    )
    emitAndFlush({ type: 'session', session: { action: 'busy', chat_id: 'c1' } })
    emitAndFlush({
      type: 'progress_structured',
      progress: { chat_id: 'web:c1', seq: 1, phase: 'tool_exec', iteration: 1, todos } as ProgressEvent,
    })
    emitAndFlush({
      type: 'progress_structured',
      progress: { chat_id: 'web:c1', seq: 2, phase: 'done', todos } as ProgressEvent,
    })
    expect(result.current.progressSnapshot.todos).toHaveLength(2)
  })

  it('todo_write([]) clearing todos propagates: mid-busy [] and PhaseDone [] both clear', () => {
    // Bug: notifyProgress only refreshed structuredProgress.Todos when
    // len(todos) > 0, and the PhaseDone branch skipped `todos.length > 0`
    // checks — a todo_write([]) (clear) never reached the frontend, so stale
    // todos lingered indefinitely (inconsistent with the server).
    const todos = [
      { id: 1, text: 'task A', done: true },
      { id: 2, text: 'task B', done: false },
    ]
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection }),
    )

    emitAndFlush({ type: 'session', session: { action: 'busy', chat_id: 'c1' } })
    emitAndFlush({
      type: 'progress_structured',
      progress: { chat_id: 'web:c1', seq: 1, phase: 'tool_exec', iteration: 1, todos } as ProgressEvent,
    })
    expect(result.current.progressSnapshot.todos).toHaveLength(2)

    // todo_write([]) → server sends todos: [] → must clear (not carry-forward)
    emitAndFlush({
      type: 'progress_structured',
      progress: { chat_id: 'web:c1', seq: 2, phase: 'tool_exec', iteration: 2, todos: [] } as ProgressEvent,
    })
    expect(result.current.progressSnapshot.todos).toHaveLength(0)

    // PhaseDone with empty todos must ALSO clear (turn end cleanup)
    emitAndFlush({
      type: 'progress_structured',
      progress: { chat_id: 'web:c1', seq: 3, phase: 'done', todos: [] } as ProgressEvent,
    })
    expect(result.current.progressSnapshot.todos).toHaveLength(0)

    // text finalize must not resurrect cleared todos
    emitAndFlush({ type: 'text', content: 'reply', chat_id: 'c1' })
    expect(result.current.progressSnapshot.todos).toHaveLength(0)
  })

  it('ignores stale streaming_tools from a completed iteration (catchup gap replay)', () => {
    const { result } = renderHook(() => useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection }))
    // Turn starts; iteration 1 generates a tool
    emitAndFlush({
      type: 'progress_structured',
      chat_id: 'c1',
      progress: { turn_id: 1, phase: 'tool_exec', iteration: 1 },
    })
    emitAndFlush({
      type: 'stream_content',
      chat_id: 'c1',
      progress: { iteration: 1, streaming_tools: [{ name: 'Bash', label: 'Bash run build', status: 'generating' }] },
    })
    expect(result.current.progressSnapshot.streamingTools).toHaveLength(1)
    // Iteration 2 begins — structured event advances lastIter and clears streamingTools
    emitAndFlush({
      type: 'progress_structured',
      chat_id: 'c1',
      progress: { turn_id: 1, phase: 'thinking', iteration: 2 },
    })
    expect(result.current.progressSnapshot.streamingTools).toHaveLength(0)
    // Stale stream_content from iteration 1 (SSE catchup gap replay / reorder)
    // must NOT restore the old generating tool into iteration 2.
    emitAndFlush({
      type: 'stream_content',
      chat_id: 'c1',
      progress: { iteration: 1, streaming_tools: [{ name: 'Bash', label: 'Bash run build', status: 'generating' }] },
    })
    expect(result.current.progressSnapshot.streamingTools).toHaveLength(0)
  })

  it('accepts streaming_tools for the CURRENT iteration (no regression guard false-positive)', () => {
    const { result } = renderHook(() => useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection }))
    emitAndFlush({
      type: 'progress_structured',
      chat_id: 'c1',
      progress: { turn_id: 1, phase: 'thinking', iteration: 2 },
    })
    // Current iteration's streaming_tools (iteration == lastIter) must apply
    emitAndFlush({
      type: 'stream_content',
      chat_id: 'c1',
      progress: { iteration: 2, streaming_tools: [{ name: 'Read', label: 'Read main.go', status: 'generating' }] },
    })
    expect(result.current.progressSnapshot.streamingTools).toHaveLength(1)
    expect(result.current.progressSnapshot.streamingTools[0].name).toBe('Read')
  })
})

describe('cancel ack: preserves live state without commit', () => {
  it('cancel does NOT call onAssistantComplete — keeps live progress as-is', () => {
    const complete = vi.fn()
    const cancelComplete = vi.fn()
    const { result } = renderHook(() =>
      useProgressStream({
        chatID: 'c1',
        onAssistantComplete: complete,
        onCancelComplete: cancelComplete,
        ws: currentWS as unknown as WSConnection,
      }),
    )

    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'partial reply' } })
    expect(result.current.liveMessage?.content).toBe('partial reply')

    emitAndFlush({ type: 'progress_structured', progress: { phase: 'done' } })

    // Cancel ack with server progress_history
    const serverHistory = JSON.stringify([{
      iteration: 1,
      tools: [
        { name: 'Read', status: 'done', summary: 'read file' },
        { name: 'user_cancelled', status: 'done', summary: 'cancelled by user' },
      ],
    }])
    emitAndFlush({ type: 'text', chat_id: 'c1', content: '', cancelled: true, progress_history: serverHistory })

    // Cancel does NOT commit — the live progress stays as-is
    expect(complete).not.toHaveBeenCalled()
    expect(cancelComplete).toHaveBeenCalledTimes(1)
    // Store is NOT reset — liveMessage content preserved
    expect(result.current.progressSnapshot.streamContent).toBe('partial reply')
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

    // Defensive finalize should NOT commit — PhaseDone already fired (phaseDoneRef=true)
    expect(complete).not.toHaveBeenCalled()

    // Cancel ack arrives — does NOT commit, keeps live state
    emitAndFlush({ type: 'text', chat_id: 'c1', content: '', cancelled: true })
    expect(complete).not.toHaveBeenCalled()
  })
})

describe('cancel: iteration preservation', () => {
  it('cancel preserves live state (no reset, no commit)', () => {
    const complete = vi.fn()
    const cancelComplete = vi.fn()
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', onAssistantComplete: complete, onCancelComplete: cancelComplete, ws: currentWS as unknown as WSConnection }),
    )

    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'partial reply' } })
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'done' } })

    const serverHistory = JSON.stringify([{
      iteration: 1,
      tools: [{ name: 'Read', status: 'done' }, { name: 'user_cancelled', status: 'done' }],
    }])
    emitAndFlush({ type: 'text', chat_id: 'c1', content: '', cancelled: true, progress_history: serverHistory })

    // Cancel does NOT commit and does NOT reset
    expect(complete).not.toHaveBeenCalled()
    expect(cancelComplete).toHaveBeenCalledTimes(1)
    // Store is preserved — streamContent stays
    expect(result.current.progressSnapshot.streamContent).toBe('partial reply')
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
    expect(complete).toHaveBeenCalledWith('bang output', [], undefined, undefined)

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
  it('cancel preserves live content without committing', () => {
    const complete = vi.fn()
    const cancelComplete = vi.fn()
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', onAssistantComplete: complete, onCancelComplete: cancelComplete, ws: currentWS as unknown as WSConnection }),
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

    // Cancel does NOT commit — live progress is preserved as-is
    expect(complete).not.toHaveBeenCalled()
    expect(cancelComplete).toHaveBeenCalledTimes(1)
    // Stream content is still visible
    expect(result.current.progressSnapshot.streamContent).toBe('I am working on')
  })

  it('does NOT commit when cancel has no content AND no iterations', () => {
    const complete = vi.fn()
    renderHook(() =>
      useProgressStream({ chatID: 'c1', onAssistantComplete: complete, ws: currentWS as unknown as WSConnection }),
    )

    // Cancel immediately — no stream content, no iterations
    emitAndFlush({ type: 'session', session: { action: 'busy', chat_id: 'c1' } })
    emitAndFlush({ type: 'progress_structured', seq: 1, progress: { phase: 'done' } })
    emitAndFlush({ type: 'text', chat_id: 'c1', content: '', cancelled: true })

    // onAssistantComplete should NOT be called
    expect(complete).not.toHaveBeenCalled()
  })

  // ── Turn-ID / Iteration-ID continuity assertions ──

  it('warns on TurnID regression (backwards)', () => {
    const warnSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    renderHook(() =>
      useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection }),
    )
    // First turn: TurnID=5
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 5, chat_id: 'web:c1' } })
    // Second turn: TurnID=3 (REGRESSION — stale turn_started)
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 3, chat_id: 'web:c1' } })
    // The stale guard drops the event with a TURN_ID_INVARIANT_VIOLATION error.
    expect(warnSpy).toHaveBeenCalledWith(
      expect.stringContaining('TURN_ID_INVARIANT_VIOLATION'),
      expect.objectContaining({ prev: 5, stale: 3 }),
    )
    warnSpy.mockRestore()
  })

  it('warns on TurnID gap (skipped number)', () => {
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {})
    renderHook(() =>
      useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection }),
    )
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 5, chat_id: 'web:c1' } })
    // Gap: 5 → 8 (skipped 6, 7)
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 8, chat_id: 'web:c1' } })
    expect(warnSpy).toHaveBeenCalledWith(
      expect.stringContaining('TURN_ID_GAP'),
      expect.objectContaining({ prev: 5, next: 8, gap: 2 }),
    )
    warnSpy.mockRestore()
  })

  it('warns on iteration gap within a turn', () => {
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {})
    renderHook(() =>
      useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection }),
    )
    // Start turn
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 1, chat_id: 'web:c1' } })
    // Iteration 1 (1-based)
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'thinking', iteration: 1, chat_id: 'web:c1' } })
    // Iteration 3 (GAP — skipped 2)
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'thinking', iteration: 3, chat_id: 'web:c1' } })
    expect(warnSpy).toHaveBeenCalledWith(
      expect.stringContaining('ITER_ID_GAP'),
      expect.objectContaining({ prev: 1, next: 3, gap: 1 }),
    )
    warnSpy.mockRestore()
  })

  it('does NOT warn on normal sequential iteration advance (1 → 2 → 3)', () => {
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    renderHook(() =>
      useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection }),
    )
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 1, chat_id: 'web:c1' } })
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'thinking', iteration: 1, chat_id: 'web:c1' } })
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'thinking', iteration: 2, chat_id: 'web:c1' } })
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'thinking', iteration: 3, chat_id: 'web:c1' } })
    expect(warnSpy).not.toHaveBeenCalled()
    expect(errorSpy).not.toHaveBeenCalled()
    warnSpy.mockRestore()
    errorSpy.mockRestore()
  })

  it('turn_started-lost fallback COMMITS old live content instead of wiping it (no flicker, no data loss)', () => {
    // BUG: when the old turn's text event AND the new turn's turn_started are
    // both lost (SSE coalescing/disconnect), the old turn's live content (tools,
    // stream text) is the ONLY display of the old reply. The 637-line fallback
    // called store.reset() directly — the content vanished from the UI in one
    // frame (flicker) and was lost until a history reload.
    const complete = vi.fn()
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', onAssistantComplete: complete, ws: currentWS as unknown as WSConnection }),
    )
    // Turn 1: turn_started → stream content + running tool. text event NEVER arrives.
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 1, chat_id: 'web:c1' } })
    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'old reply', turn_id: 1 } })
    emitAndFlush({
      type: 'progress_structured',
      progress: {
        phase: 'tool_exec', iteration: 1, seq: 2, turn_id: 1, chat_id: 'web:c1',
        active_tools: [{ name: 'Read', status: 'running', iteration: 1 }],
      },
    })
    expect(result.current.liveMessage).not.toBeNull()
    expect(result.current.progressSnapshot.activeTools).toHaveLength(1)

    // Turn 2 begins: turn_started(2) lost too. First structured event triggers
    // the fallback — it must hand the old content to the committed list
    // (onAssistantComplete) BEFORE resetting, so nothing flickers or vanishes.
    emitAndFlush({
      type: 'progress_structured',
      progress: { phase: 'thinking', iteration: 1, seq: 3, turn_id: 2, chat_id: 'web:c1' },
    })

    expect(complete).toHaveBeenCalledTimes(1)
    expect(complete.mock.calls[0][0]).toBe('old reply')
    // After commit, resetProgress calls store.freeze() — but turn_started
    // continues processing and sets phase='thinking' for the new turn.
    // The key assertion is that complete was called with the right content
    // (no data loss). The phase after turn_started is 'thinking' (new turn).
    expect(result.current.progressSnapshot.phase).not.toBe('done')
  })

  it('turn_started commit does NOT let text event create a duplicate (finalizedRef preserved)', () => {
    // BUG: turn_started called commitLiveProgressAndReset → onAssistantComplete
    // → resetProgress → finalizedRef=true. Then turn_started reset
    // finalizedRef=false (line 546). The text event arrived, saw
    // finalizedRef=false, and called onAssistantComplete AGAIN → duplicate.
    const complete = vi.fn()
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', onAssistantComplete: complete, ws: currentWS as unknown as WSConnection }),
    )
    // Turn 1: stream content
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 1, chat_id: 'web:c1' } })
    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'streaming reply', turn_id: 1 } })
    expect(result.current.liveMessage?.content).toBe('streaming reply')

    // Turn 2: turn_started fires BEFORE the text event for turn 1.
    // commitLiveProgressAndReset commits the live content.
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 2, chat_id: 'web:c1' } })
    expect(complete).toHaveBeenCalledTimes(1)
    expect(complete.mock.calls[0][0]).toBe('streaming reply')

    // Text event for turn 1 arrives AFTER turn_started(2).
    // Before fix: finalizedRef was reset to false → text event called
    // onAssistantComplete again → duplicate.
    // After fix: finalizedRef is preserved (true from commit) → text event
    // returns early → NO duplicate.
    emitAndFlush({ type: 'text', content: 'final reply', chat_id: 'c1', turn_id: 1 })
    expect(complete).toHaveBeenCalledTimes(1)
  })

  it('turn_started with empty store does NOT block text event', () => {
    const complete = vi.fn()
    renderHook(() =>
      useProgressStream({ chatID: 'c1', onAssistantComplete: complete, ws: currentWS as unknown as WSConnection }),
    )
    // Turn 1: turn_started, but NO streaming content (store is empty)
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 1, chat_id: 'web:c1' } })
    expect(complete).not.toHaveBeenCalled()

    // Text event arrives — should fire onAssistantComplete
    emitAndFlush({ type: 'text', content: 'reply', chat_id: 'c1', turn_id: 1 })
    expect(complete).toHaveBeenCalledTimes(1)
    expect(complete.mock.calls[0][0]).toBe('reply')
  })

  it('resume trigger resets finalizedRef so text event can fire', () => {
    const complete = vi.fn()
    renderHook(() =>
      useProgressStream({ chatID: 'c1', onAssistantComplete: complete, ws: currentWS as unknown as WSConnection }),
    )
    // Turn 1: stream + text (finalized)
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 1, chat_id: 'web:c1' } })
    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'partial', turn_id: 1 } })
    emitAndFlush({ type: 'text', content: 'first reply', chat_id: 'c1', turn_id: 1 })
    expect(complete).toHaveBeenCalledTimes(1)

    // Resume (AskUser answer) — same turnID
    emitAndFlush({
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 1, turn_start: { trigger: 'resume' }, chat_id: 'web:c1' },
    })

    // Text event for the resumed turn — should fire
    emitAndFlush({ type: 'text', content: 'resumed reply', chat_id: 'c1', turn_id: 1 })
    expect(complete).toHaveBeenCalledTimes(2)
    expect(complete.mock.calls[1][0]).toBe('resumed reply')
  })
})

describe('iterationHistory id gap → reload (incremental delta loss is REAL data loss)', () => {
  it('fires onIterationGap once when an internal iteration id is missing, and re-arms after repair', () => {
    const onIterationGap = vi.fn()
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', onIterationGap, ws: currentWS as unknown as WSConnection }),
    )

    // Iterations 1, 2, 3 arrive contiguously.
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'tool_exec', iteration: 3, turn_id: 1, chat_id: 'web:c1', iteration_history: [{ iteration: 1 }, { iteration: 2 }, { iteration: 3 }] } })
    expect(onIterationGap).not.toHaveBeenCalled()
    expect(result.current.progressSnapshot.iterationHistory.map((i) => i.iteration)).toEqual([1, 2, 3])

    // Iteration 5's delta arrives but 4's was DROPPED on the wire →
    // iterationHistory [1,2,3,5] has an internal id gap. SSE snapshots carry
    // only NEW iterations — no later snapshot can backfill iteration 4. The
    // DB is authoritative: reload (one-shot).
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'tool_exec', iteration: 5, turn_id: 1, chat_id: 'web:c1', iteration_history: [{ iteration: 5 }] } })
    expect(onIterationGap).toHaveBeenCalledTimes(1)
    expect(result.current.progressSnapshot.iterationHistory.map((i) => i.iteration)).toEqual([1, 2, 3, 5])

    // The gap persists on subsequent events — must NOT re-fire (reload storm).
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'tool_exec', iteration: 5, turn_id: 1, chat_id: 'web:c1', active_tools: [{ name: 'Shell', status: 'running', iteration: 5 }] } })
    expect(onIterationGap).toHaveBeenCalledTimes(1)

    // reload backfills iteration 4 → history contiguous → re-arm the one-shot.
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'tool_exec', iteration: 5, turn_id: 1, chat_id: 'web:c1', iteration_history: [{ iteration: 4 }] } })
    expect(onIterationGap).toHaveBeenCalledTimes(1)
    expect(result.current.progressSnapshot.iterationHistory.map((i) => i.iteration)).toEqual([1, 2, 3, 4, 5])

    // A NEW gap (7 missing 6) after repair fires again.
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'tool_exec', iteration: 7, turn_id: 1, chat_id: 'web:c1', iteration_history: [{ iteration: 7 }] } })
    expect(onIterationGap).toHaveBeenCalledTimes(2)
  })

  it('does NOT fire for a legitimately offset contiguous history ([5,6,7])', () => {
    const onIterationGap = vi.fn()
    renderHook(() =>
      useProgressStream({ chatID: 'c1', onIterationGap, ws: currentWS as unknown as WSConnection }),
    )
    // A history subset starting at 5 is contiguous (earlier iterations may have
    // been compressed/merged) — no internal jump, no reload.
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'tool_exec', iteration: 7, turn_id: 1, chat_id: 'web:c1', iteration_history: [{ iteration: 5 }, { iteration: 6 }, { iteration: 7 }] } })
    expect(onIterationGap).not.toHaveBeenCalled()
  })
})

describe('SSE dump replay (record → reproduce → pin regression)', () => {
  it('replays a recorded dump: liveMessage never vanishes; PhaseDone with EMPTY streamContent (lost text event) triggers reload', () => {
    // Pins the "某个迭代结束 agent turn 消失了" root cause (no console error,
    // RENDER_LOSS_ROWS silent because liveMessage stays non-null but renders
    // EMPTY). Reconstructed from /home/smith/src/mint-bench/2.ev essentials:
    // iteration 1 streams text → iteration 2 boundary CLEARS streamContent →
    // PhaseDone arrives with NO text event (dropped on the stateless SSE stream)
    // → store keeps only iteration records → reload must fire to recover the
    // authoritative complete reply from DB.
    const dump = [
      `id:100\nevent:progress_structured\ndata:${JSON.stringify({ type: 'progress_structured', seq: 100, progress: { phase: 'turn_started', turn_id: 1, chat_id: 'web:c1' } })}`,
      `id:101\nevent:stream_content\ndata:${JSON.stringify({ type: 'stream_content', seq: 101, progress: { stream_content: 'partial reply...', turn_id: 1 } })}`,
      `id:102\nevent:progress_structured\ndata:${JSON.stringify({ type: 'progress_structured', seq: 102, progress: { phase: 'tool_exec', iteration: 1, turn_id: 1, chat_id: 'web:c1', iteration_history: [{ iteration: 1 }] } })}`,
      `id:103\nevent:progress_structured\ndata:${JSON.stringify({ type: 'progress_structured', seq: 103, progress: { phase: 'thinking', iteration: 2, turn_id: 1, chat_id: 'web:c1', iteration_history: [{ iteration: 2 }] } })}`,
      `id:104\nevent:progress_structured\ndata:${JSON.stringify({ type: 'progress_structured', seq: 104, progress: { phase: 'done', iteration: 2, turn_id: 1, chat_id: 'web:c1' } })}`,
      ``,
    ].join('\n')
    const onIterationGap = vi.fn()
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', onIterationGap, ws: currentWS as unknown as WSConnection }),
    )
    for (const ev of parseSSEDump(dump)) {
      act(() => {
        currentWS.emit(ev)
        rafCbs.splice(0, rafCbs.length).forEach((cb) => cb())
      })
    }
    // The turn must NOT vanish from the DOM…
    expect(result.current.liveMessage).not.toBeNull()
    // …but the lost final text event must be detected → reload to recover the
    // complete reply (buildMessageRows' same-turn merge surfaces it).
    expect(onIterationGap).toHaveBeenCalledTimes(1)
    expect(result.current.progressSnapshot.iterationHistory.map((i) => i.iteration)).toEqual([1, 2])
  })
})

describe('turn-id ownership on turn boundary (turn duplication regression)', () => {
  it('commits old turn live to its OWN turnID (not the new turn) when text event was lost', () => {
    const complete = vi.fn()
    renderHook(() =>
      useProgressStream({ chatID: 'c1', onAssistantComplete: complete, ws: currentWS as unknown as WSConnection }),
    )
    // Turn 1 starts, then streams content via a stream_content event that does
    // NOT carry turn_id (the snapshot.turnID field is therefore still 0 — only
    // store.lastTurnID knows it belongs to turn 1).
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 1, chat_id: 'web:c1' } })
    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'old reply text' } })

    // Turn 1's text event was LOST (SSE drop). The user sends a new message →
    // turn_started(2). The commit of turn 1's live content must go to turnID 1,
    // NOT turnID 2 — otherwise the old turn re-renders below the new user msg
    // and the new turn's iterations attach to the duplicated old turn row.
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 2, chat_id: 'web:c1' } })

    expect(complete).toHaveBeenCalledTimes(1)
    // Args: (finalText, iterations, eventSeq, turnID, insertBeforeLastUser)
    expect(complete).toHaveBeenCalledWith(expect.any(String), expect.any(Array), undefined, 1, true)
  })

  it('does NOT restart streaming from a late progress_structured after PhaseDone (busy ghost)', () => {
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection }),
    )
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 1, chat_id: 'web:c1' } })
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'tool_exec', iteration: 1, turn_id: 1, chat_id: 'web:c1', active_tools: [{ name: 'Shell', status: 'running', iteration: 1 }] } })

    // PhaseDone ends the turn → stopStreaming (streaming=false).
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'done', turn_id: 1, chat_id: 'web:c1' } })
    expect(result.current.progressSnapshot.streaming).toBe(false)

    // A late progress_structured (SSE reorder) after PhaseDone must NOT set
    // streaming back to true — otherwise busy stays true and "思考中…" lingers
    // below the history even after the input box turned idle.
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'tool_exec', iteration: 2, turn_id: 1, chat_id: 'web:c1', active_tools: [{ name: 'Read', status: 'running', iteration: 2 }] } })
    expect(result.current.progressSnapshot.streaming).toBe(false)
  })

  it('stops streaming on session(idle) even when there is no visible progress to reset', () => {
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection }),
    )
    // Turn starts with phase='thinking' — sets streaming=true but has NO visible
    // progress yet (no iteration, no stream content, no tools).
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 1, chat_id: 'web:c1' } })
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'thinking', turn_id: 1, chat_id: 'web:c1' } })
    expect(result.current.progressSnapshot.streaming).toBe(true)

    // Turn ends abnormally (PhaseDone and text both lost) — only session(idle)
    // arrives. hasVisibleProgress is false (no content), so no reset happens —
    // but streaming MUST still be cleared, otherwise busy stays true and
    // "思考中…" lingers below the history after the input box turned idle.
    emitAndFlush({ type: 'session', session: { action: 'idle', chat_id: 'c1' } })
    expect(result.current.progressSnapshot.streaming).toBe(false)
  })
})

describe('late stream_content from a finalized turn must be dropped', () => {
  it('does NOT re-fill the store with a stale turn-1 stream_content after turn_started(2)', () => {
    // User report: "user1 agent1 → 发 user2 → user1 agent1 user2 agent2(processing) agent1".
    // A late stream_content from turn 1 (SSE reorder) arrives AFTER turn_started(2)
    // reset finalizedRef=false. stream_content lacked the finalizedTurnIDRef guard
    // that progress_structured has — it re-filled ProgressStore.streamContent and
    // wrote the OLD turn's slot, resurrecting agent1 as a live row below agent2.
    const { result } = renderHook(() =>
      useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection }),
    )
    // turn 1 completes
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 1, chat_id: 'web:c1' } })
    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'agent1 reply', turn_id: 1 } })
    emitAndFlush({ type: 'text', seq: 10, turn_id: 1, content: 'agent1 reply' })
    expect(result.current.progressSnapshot.streamContent).toBe('')

    // turn 2 begins (resets finalizedRef=false, finalizedTurnIDRef=1)
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 2, chat_id: 'web:c1' } })

    // late stream_content from turn 1 (stale, turn_id=1)
    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'late agent1 token', turn_id: 1 } })

    // The stale token must NOT resurrect the old turn's stream content.
    expect(result.current.progressSnapshot.streamContent).toBe('')
    expect(result.current.progressSnapshot.streaming).toBe(false)
  })
})

describe('text event turn_id from metadata.turn_id', () => {
  it('commits the final reply to metadata.turn_id when top-level turn_id is absent', () => {
    const complete = vi.fn()
    renderHook(() =>
      useProgressStream({ chatID: 'c1', ws: currentWS as unknown as WSConnection, onAssistantComplete: complete }),
    )
    emitAndFlush({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 1368, chat_id: 'web:c1' } })
    emitAndFlush({ type: 'stream_content', progress: { stream_content: 'reply text', turn_id: 1368 } })
    // The final reply's text event carries turn_id in metadata.turn_id (string),
    // NOT in the top-level turn_id field (omitempty, absent when 0).
    emitAndFlush({
      type: 'text',
      seq: 653,
      content: 'reply text',
      metadata: { turn_id: '1368' },
    })

    expect(complete).toHaveBeenCalledTimes(1)
    // Args: (finalText, iterations, eventSeq, turnID, insertBeforeLastUser)
    expect(complete).toHaveBeenCalledWith('reply text', expect.any(Array), 653, 1368)
  })
})

// ── historyReady gate：live progress 必须与 history 一起渲染 ──
describe('useProgressStream historyReady gate（live 不先于 history 渲染）', () => {
  it('historyReady=false（切换会话 loading）时 SSE live 事件不写入 MessageStore', () => {
    const ms = new MessageStore()
    renderHook(() =>
      useProgressStream({
        chatID: 'c1',
        ws: currentWS as unknown as WSConnection,
        messageStore: ms,
        historyReady: false, // 切换会话，fetchHistory 未完成
      }),
    )
    emitAndFlush({
      type: 'stream_content',
      chat_id: 'c1',
      progress: { turn_id: 1, stream_content: 'partial text', iteration: 1 },
    })
    // live 不得先于 history 渲染 —— MessageStore 无 live 行
    expect(ms.hasLive(1)).toBe(false)
    expect(ms.toRows().some((r) => r.id === 'turn-1-live')).toBe(false)
  })

  it('historyReady=true 时 SSE live 事件正常写入 MessageStore', () => {
    const ms = new MessageStore()
    renderHook(() =>
      useProgressStream({
        chatID: 'c1',
        ws: currentWS as unknown as WSConnection,
        messageStore: ms,
        historyReady: true,
      }),
    )
    emitAndFlush({
      type: 'stream_content',
      chat_id: 'c1',
      progress: { turn_id: 1, stream_content: 'partial text', iteration: 1 },
    })
    expect(ms.hasLive(1)).toBe(true)
    expect(ms.toRows().some((r) => r.id === 'turn-1-live')).toBe(true)
  })

  it('historyReady 从 false 变 true 后 SSE live 恢复写入（hydration 一起渲染）', () => {
    const ms = new MessageStore()
    const { rerender } = renderHook(
      ({ ready }) =>
        useProgressStream({
          chatID: 'c1',
          ws: currentWS as unknown as WSConnection,
          messageStore: ms,
          historyReady: ready,
        }),
      { initialProps: { ready: false } },
    )
    // loading 期间：live 不写入
    emitAndFlush({
      type: 'stream_content',
      chat_id: 'c1',
      progress: { turn_id: 1, stream_content: 'partial', iteration: 1 },
    })
    expect(ms.hasLive(1)).toBe(false)
    // history ready（fetchHistory 完成 + hydration）→ 后续 SSE 正常写入
    rerender({ ready: true })
    emitAndFlush({
      type: 'stream_content',
      chat_id: 'c1',
      progress: { turn_id: 1, stream_content: 'partial v2', iteration: 1 },
    })
    expect(ms.hasLive(1)).toBe(true)
    expect(ms.toRows().some((r) => r.id === 'turn-1-live')).toBe(true)
  })
})
