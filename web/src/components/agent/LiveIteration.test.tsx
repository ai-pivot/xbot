/**
 * Tests for LiveIteration (Spec A §2 — typewriter cursor position).
 *
 * Verifies:
 *  - Streaming content renders with streaming-content class
 *  - Typewriter cursor (CSS ::after) is applied when streaming
 *  - No streaming-content class when not streaming
 *  - SubAgent tree renders when subAgents present
 */
import { describe, expect, it } from 'vitest'
import '@testing-library/jest-dom'

import { LiveIteration } from '@/components/agent/LiveIteration'
import { renderWithProviders } from '@/test-utils'
import type { ProgressSnapshot } from '@/types/shared'

function makeSnapshot(overrides: Partial<ProgressSnapshot> = {}): ProgressSnapshot {
  return {
    eventSeq: 0,
    phase: 'thinking',
    iteration: 1,
    streamContent: '',
    content: '',
    reasoningStreamContent: '',
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
    ...overrides,
  }
}

describe('LiveIteration — typewriter cursor', () => {
  it('renders streaming content with streaming-content class when streaming', () => {
    const snapshot = makeSnapshot({
      streamContent: 'Hello world',
      streaming: true,
    })
    const { container } = renderWithProviders(<LiveIteration progress={snapshot} level="minimal" />)
    const streamingDiv = container.querySelector('.streaming-content')
    expect(streamingDiv).not.toBeNull()
    // Typewriter starts empty; content appears after the 50ms interval tick.
    // The test verifies the CSS class is applied, not the full text (which
    // depends on timer advancement).
  })

  it('does NOT apply streaming-content class when not streaming', () => {
    const snapshot = makeSnapshot({
      streamContent: 'Final text',
      streaming: false,
    })
    const { container } = renderWithProviders(<LiveIteration progress={snapshot} level="minimal" />)
    const streamingDiv = container.querySelector('.streaming-content')
    expect(streamingDiv).toBeNull()
  })

  it('does not render streaming content section when streamContent is empty (thinking phase)', () => {
    const snapshot = makeSnapshot({
      streamContent: '',
      reasoningStreamContent: 'thinking about something',
      streaming: true,
    })
    const { container } = renderWithProviders(<LiveIteration progress={snapshot} level="minimal" />)
    const streamingDiv = container.querySelector('.streaming-content')
    expect(streamingDiv).toBeNull()
  })

  it('sweeps the in-progress thought character count without a second reasoning sweep', () => {
    const snapshot = makeSnapshot({
      reasoningStreamContent: 'thinking about something',
      streaming: true,
      phase: 'thinking',
    })
    const { container } = renderWithProviders(<LiveIteration progress={snapshot} level="minimal" />)
    const sweep = container.querySelector<HTMLElement>('.sweep-text')

    expect(sweep).not.toBeNull()
    expect(sweep).toHaveTextContent(String(snapshot.reasoningStreamContent.length))
    expect(container.querySelectorAll('.sweep-text')).toHaveLength(1)
  })

  it.each(['pending', 'generating', 'running'] as const)(
    'hides the reasoning sweep while a %s tool is in progress',
    (status) => {
      const snapshot = makeSnapshot({
        reasoningStreamContent: 'thinking about something',
        streamingTools: [{
          name: 'Read',
          label: 'Read',
          status,
          elapsedMs: 0,
          summary: '',
          detail: '',
          args: '',
          toolHints: '',
        }],
      })
      const { container } = renderWithProviders(<LiveIteration progress={snapshot} level="minimal" />)

      expect(container.querySelectorAll('.sweep-text')).toHaveLength(1)
      expect(container.querySelector('.sweep-text')).toHaveTextContent('Read')
    },
  )

  it('renders SubAgent tree when subAgents present', () => {
    const snapshot = makeSnapshot({
      streamContent: '',
      streaming: false,
      subAgents: [
        { role: 'explore', instance: 'sub-1', status: 'running', desc: 'searching' },
      ],
    })
    const { container } = renderWithProviders(<LiveIteration progress={snapshot} level="minimal" />)
    expect(container.textContent).toContain('explore:sub-1')
    expect(container.textContent).toContain('searching')
  })

  it('returns null when no content to show', () => {
    const snapshot = makeSnapshot({
      streamContent: '',
      reasoningStreamContent: '',
      streaming: true,
      phase: '',
    })
    const { container } = renderWithProviders(<LiveIteration progress={snapshot} level="minimal" />)
    // Should render nothing meaningful (empty)
    expect(container.querySelector('.streaming-content')).toBeNull()
  })

  it('does NOT filter out running activeTools that share name+label with a completed iteration', () => {
    // BUG: LiveIteration filtered ALL tools (including activeTools) by name+label
    // against iterationHistory. If the same tool (e.g. Shell) appeared in both a
    // completed iteration and the current running iteration, the running tool was
    // filtered out — making it disappear from the UI.
    const snapshot = makeSnapshot({
      streaming: true,
      phase: 'tool_exec',
      iteration: 2,
      // Iteration 1 is completed — has Shell(done)
      iterationHistory: [{
        iteration: 1,
        thinking: '',
        reasoning: '',
        tools: [{
          name: 'Shell',
          label: 'Shell echo hello',
          status: 'done',
          elapsedMs: 100,
          summary: '',
          detail: '',
          args: '',
          toolHints: '',
        }],
        toolCount: 1,
      }],
      // Current iteration 2 — Shell is running (same name+label!)
      activeTools: [{
        name: 'Shell',
        label: 'Shell echo hello',
        status: 'running',
        elapsedMs: 0,
        summary: '',
        detail: '',
        args: '',
        toolHints: '',
        iteration: 2,
      }],
    })
    const { container } = renderWithProviders(<LiveIteration progress={snapshot} level="minimal" />)
    // The running Shell must NOT be filtered out — it renders with a SweepText
    // (the animated "running" indicator). Check for the tool name + sweep.
    expect(container.textContent).toContain('Shell')
    // SweepText is shown when a tool is running (status === 'running')
    expect(container.querySelector('.sweep-text')).not.toBeNull()
  })
})

describe('LiveIteration thinking placeholder (reuses ShimmerThinking — iteration boundary)', () => {
  it('shows the EXISTING thinking placeholder at a NON-first iteration boundary (prev iter done, next not arrived)', () => {
    // liveMessage is non-null here → MessageList's busy placeholder is
    // suppressed. Reusing ShimmerThinking keeps the "思考中…" visible during
    // the boundary wait (user: "之前那个思考中有些情况没显示"). Requires a
    // predecessor iteration (iterationHistory non-empty) — the FIRST iteration
    // is special: busy placeholder covers the pre-first-iter window.
    const { container } = renderWithProviders(
      <LiveIteration
        progress={makeSnapshot({
          lastIter: 2,
          iterationHistory: [{ iteration: 1, thinking: 't1', reasoning: '', tools: [], toolCount: 0 }],
        })}
        level="all"
      />,
    )
    expect(container.textContent).toMatch(/思考中|thinking/)
  })

  it('returns null for the FIRST iteration (iterationHistory empty — busy placeholder covers it)', () => {
    // User: "第一个 iter 是特殊的" — no predecessor iteration, so the busy
    // placeholder (MessageList ShimmerThinking) covers the window; rendering
    // ShimmerThinking here would show TWO thinking indicators.
    const { container } = renderWithProviders(
      <LiveIteration progress={makeSnapshot({ lastIter: 1 })} level="all" />,
    )
    expect(container.textContent).not.toMatch(/思考中|thinking/)
  })

  it('returns null in the pre-iteration phase (lastIter=0 — busy placeholder covers it)', () => {
    const { container } = renderWithProviders(
      <LiveIteration progress={makeSnapshot({ lastIter: 0 })} level="all" />,
    )
    expect(container.textContent).not.toMatch(/思考中|thinking/)
  })

  it('returns null when the turn is not streaming (ended — committed reply replaces the row)', () => {
    const { container } = renderWithProviders(
      <LiveIteration
        progress={makeSnapshot({
          lastIter: 2,
          streaming: false,
          iterationHistory: [{ iteration: 1, thinking: 't1', reasoning: '', tools: [], toolCount: 0 }],
        })}
        level="all"
      />,
    )
    expect(container.textContent).not.toMatch(/思考中|thinking/)
  })
})
