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
})
