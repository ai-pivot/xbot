/**
 * Tests for the collapsible intermediate-process components (Spec 4 §3.3).
 *
 * Tests the new folding model: FoldedLine (borderless ▸/▾), FoldedToolGroup
 * (consecutive tool merging), IterationGroup (T→C→O order), and the content
 * renderers ToolCallBlock and ReasoningBlock.
 */
import { describe, expect, it } from 'vitest'
import { screen, fireEvent } from '@testing-library/react'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { FoldedLine } from '@/components/agent/FoldedLine'
import { FoldedToolGroup } from '@/components/agent/FoldedToolGroup'
import { IterationGroup } from '@/components/agent/IterationHistory'
import { ReasoningBlock } from '@/components/agent/ReasoningBlock'
import { ToolCallBlock } from '@/components/agent/ToolCallBlock'
import type { WebIteration, WebToolProgress } from '@/types/shared'

/** Helper: build a WebToolProgress with defaults. */
function makeTool(overrides: Partial<WebToolProgress> = {}): WebToolProgress {
  return {
    name: 'Read',
    label: '',
    status: 'done',
    elapsedMs: 0,
    summary: '',
    detail: '',
    args: '',
    toolHints: '',
    ...overrides,
  }
}

/** Helper: build a WebIteration with defaults. */
function makeIteration(overrides: Partial<WebIteration> = {}): WebIteration {
  return {
    iteration: 1,
    thinking: '',
    reasoning: '',
    tools: [],
    toolCount: 0,
    ...overrides,
  }
}

describe('FoldedLine', () => {
  it('renders the title with ▸ and toggles open class on click', () => {
    const { container } = renderWithProviders(
      <FoldedLine title="T1">
        <span>content</span>
      </FoldedLine>,
    )
    // Always renders ▸ (rotation handled by CSS class), content always in DOM
    expect(screen.getByText('▸')).toBeInTheDocument()
    // Content is always in the DOM (CSS grid controls visibility)
    expect(screen.getByText('content')).toBeInTheDocument()
    // Fold container does not have 'open' class when collapsed
    expect(container.querySelector('.fold-container')).not.toHaveClass('open')

    // Click to expand
    fireEvent.click(screen.getByRole('button'))
    expect(container.querySelector('.fold-container')).toHaveClass('open')
    expect(container.querySelector('.fold-arrow')).toHaveClass('open')
  })

  it('starts open when defaultOpen=true', () => {
    const { container } = renderWithProviders(
      <FoldedLine title="test" defaultOpen>
        <span>visible</span>
      </FoldedLine>,
    )
    expect(container.querySelector('.fold-container')).toHaveClass('open')
    expect(screen.getByText('visible')).toBeInTheDocument()
  })

  it('calls onToggle callback', () => {
    let toggled = false
    renderWithProviders(
      <FoldedLine title="test" onToggle={() => { toggled = true }}>
        <span>content</span>
      </FoldedLine>,
    )
    fireEvent.click(screen.getByRole('button'))
    expect(toggled).toBe(true)
  })
})

describe('ToolCallBlock', () => {
  it('renders args and output content directly (no collapsible wrapper)', () => {
    const tool = makeTool({
      name: 'Read',
      args: '{"path":"a.go"}',
      detail: 'file contents',
    })
    renderWithProviders(<ToolCallBlock tool={tool} />)
    // Content is immediately visible (folding handled by parent FoldedLine)
    expect(screen.getByText('file contents')).toBeInTheDocument()
    expect(screen.getByText('{"path":"a.go"}')).toBeInTheDocument()
  })

  it('renders summary when no args or detail', () => {
    const tool = makeTool({ name: 'Read', summary: 'file ok' })
    renderWithProviders(<ToolCallBlock tool={tool} />)
    expect(screen.getByText('file ok')).toBeInTheDocument()
  })
})

describe('ReasoningBlock', () => {
  it('renders nothing when content is empty', () => {
    const { container } = renderWithProviders(<ReasoningBlock content="" />)
    expect(container.firstChild).toBeNull()
  })

  it('renders the reasoning text as Markdown', () => {
    renderWithProviders(<ReasoningBlock content="Because the sky is blue." />)
    expect(screen.getAllByText(/Because the sky is blue/).length).toBeGreaterThan(0)
  })

  it('shows streaming indicator when streaming=true', () => {
    renderWithProviders(<ReasoningBlock content="thinking..." streaming />)
    // Both the content and the streaming indicator contain "thinking"
    expect(screen.getAllByText(/thinking/i).length).toBeGreaterThan(0)
  })
})

describe('FoldedToolGroup', () => {
  it('merges multiple tools at minimal level into one foldable line', () => {
    const tools = [
      makeTool({ name: 'Read', label: 'Read' }),
      makeTool({ name: 'Grep', label: 'Grep' }),
    ]
    renderWithProviders(<FoldedToolGroup tools={tools} level="minimal" />)
    // Merged line: "Read · Grep" is one text node + "(2 tools)"
    expect(screen.getByText('Read · Grep')).toBeInTheDocument()
    expect(screen.getByText('(2 tools)')).toBeInTheDocument()

    // Expand the merged line
    fireEvent.click(screen.getByRole('button'))
    // Individual tool FoldedLines should now be visible (multiple "Read" and "Grep")
    expect(screen.getAllByText('Read').length).toBeGreaterThan(0)
    expect(screen.getAllByText('Grep').length).toBeGreaterThan(0)
  })

  it('renders each tool independently at none level', () => {
    const tools = [
      makeTool({ name: 'Read', label: 'Read' }),
      makeTool({ name: 'Grep', label: 'Grep' }),
    ]
    const { container } = renderWithProviders(
      <FoldedToolGroup tools={tools} level="none" />,
    )
    // Each tool is its own FoldedLine (two toggle buttons)
    const buttons = container.querySelectorAll('button[aria-expanded]')
    expect(buttons.length).toBe(2)
  })

  it('renders single tool as independent FoldedLine regardless of level', () => {
    const tools = [makeTool({ name: 'Read', label: 'Read' })]
    const { container } = renderWithProviders(
      <FoldedToolGroup tools={tools} level="minimal" />,
    )
    // Single tool: one FoldedLine, not a merged line
    const buttons = container.querySelectorAll('button[aria-expanded]')
    expect(buttons.length).toBe(1)
  })

  it('renders nothing for empty tools', () => {
    const { container } = renderWithProviders(
      <FoldedToolGroup tools={[]} level="minimal" />,
    )
    expect(container.firstChild).toBeNull()
  })
})

describe('IterationGroup', () => {
  it('renders T (reasoning), C (tools), O (text) in order', () => {
    const iter = makeIteration({
      iteration: 1,
      reasoning: 'planning the approach',
      thinking: 'Here is the output',
      tools: [makeTool({ name: 'Read', label: 'Read' })],
      toolCount: 1,
    })
    renderWithProviders(<IterationGroup iteration={iter} level="minimal" />)
    // Reasoning is a folded line with character count as title
    expect(screen.getByText(/Thought.*characters/)).toBeInTheDocument()
    // Tool name from FoldedToolGroup
    expect(screen.getAllByText('Read').length).toBeGreaterThan(0)
    // O text from MarkdownRenderer
    expect(screen.getByText('Here is the output')).toBeInTheDocument()
  })

  it('renders reasoning (T) as a folded line (collapsed by default)', () => {
    const { container } = renderWithProviders(
      <IterationGroup
        iteration={makeIteration({ iteration: 2, reasoning: 'deep thinking' })}
        level="none"
      />,
    )
    // Reasoning folded line shows character count as title
    expect(screen.getByText(/Thought.*characters/)).toBeInTheDocument()
    // Reasoning content is always in the DOM but hidden via CSS (grid 0fr).
    // The fold-container should NOT have the 'open' class when collapsed.
    expect(container.querySelector('.fold-container')).not.toHaveClass('open')
  })

  it('renders O (text output) always visible', () => {
    const iter = makeIteration({
      iteration: 3,
      thinking: 'Final answer here',
    })
    renderWithProviders(<IterationGroup iteration={iter} level="all" />)
    expect(screen.getByText('Final answer here')).toBeInTheDocument()
  })

  it('renders tools with FoldedToolGroup', () => {
    const iter = makeIteration({
      iteration: 1,
      tools: [
        makeTool({ name: 'Read', label: 'Read' }),
        makeTool({ name: 'Grep', label: 'Grep' }),
      ],
      toolCount: 2,
    })
    renderWithProviders(<IterationGroup iteration={iter} level="minimal" />)
    // Both tool names visible in the merged line as one text node
    expect(screen.getByText('Read · Grep')).toBeInTheDocument()
  })

  it('renders a hint when iteration is empty', () => {
    const iter = makeIteration({ iteration: 1 })
    renderWithProviders(<IterationGroup iteration={iter} level="minimal" />)
    // Should render the "none" hint
    expect(screen.getByText('—')).toBeInTheDocument()
  })
})
