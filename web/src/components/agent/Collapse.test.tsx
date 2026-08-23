/**
 * Tests for the collapsible intermediate-process components (Spec 4 §3.3).
 *
 * Tests the new folding model: FoldedLine (borderless ▸/▾), FoldedToolGroup
 * (consecutive tool merging), IterationGroup (T→C→O order), and the content
 * renderers ToolCallBlock and ReasoningBlock.
 */
import { describe, expect, it } from 'vitest'
import { screen, fireEvent, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { FoldedLine } from '@/components/agent/FoldedLine'
import { FoldedToolGroup } from '@/components/agent/FoldedToolGroup'
import { IterationGroup } from '@/components/agent/IterationHistory'
import { ReasoningBlock } from '@/components/agent/ReasoningBlock'
import { ToolCallBlock } from '@/components/agent/ToolCallBlock'
import { getToolIcon } from '@/components/agent/toolIcons'
import { SquareTerminal, FileText, Search, Sparkles, Wrench } from 'lucide-react'
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
    content: '',
    reasoning: '',
    tools: [],
    toolCount: 0,
    ...overrides,
  }
}

describe('FoldedLine', () => {
  it('renders the title with ▸ and toggles open class on click', async () => {
    const { container } = renderWithProviders(
      <FoldedLine title="T1">
        <span>content</span>
      </FoldedLine>,
    )
    // Collapsed lazy content is mounted only after first expansion.
    expect(screen.getByText('▸')).toBeInTheDocument()
    expect(screen.queryByText('content')).not.toBeInTheDocument()
    expect(container.querySelector('.fold-container')).toBeNull()

    // Click to expand
    fireEvent.click(screen.getByRole('button'))
    expect(screen.getByText('content')).toBeInTheDocument()
    await waitFor(() => expect(container.querySelector('.fold-container')).toHaveClass('open'))
    expect(container.querySelector('.fold-arrow')).toHaveClass('open')

    // Collapse again: content UNMOUNTS after the collapse animation (perf fix —
    // folded heavy content no longer participates in streaming re-renders).
    fireEvent.click(screen.getByRole('button'))
    await waitFor(() => expect(container.querySelector('.fold-container')).not.toHaveClass('open'))
    await waitFor(() => expect(screen.queryByText('content')).not.toBeInTheDocument())
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
    // Args are pretty-printed JSON (multi-line) — assert on the key content
    expect(screen.getByText(/"a\.go"/)).toBeInTheDocument()
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

  it('renders reasoning content without sweep text', () => {
    const { container } = renderWithProviders(<ReasoningBlock content="thinking..." />)
    expect(screen.getAllByText(/thinking/i).length).toBeGreaterThan(0)
    expect(container.querySelector('.sweep-text')).toBeNull()
  })

  it('renders completed reasoning without sweep', () => {
    const { container } = renderWithProviders(<ReasoningBlock content="finished thought" />)
    expect(container.querySelector('.sweep-text')).toBeNull()
  })
})

describe('FoldedToolGroup', () => {
  it('merges multiple tools at minimal level into one foldable line', () => {
    const tools = [
      makeTool({ name: 'Read', label: 'Read' }),
      makeTool({ name: 'Grep', label: 'Grep' }),
    ]
    const { container } = renderWithProviders(<FoldedToolGroup tools={tools} level="minimal" />)
    // Merged row shows icons in the button (title row); AnimatedCollapse also renders
    // icons in the hidden expanded content, so check the button specifically.
    const button = container.querySelector('button[aria-expanded="false"]')
    expect(button).not.toBeNull()
    const icons = button!.querySelectorAll('.tool-icon-single')
    expect(icons.length).toBe(2)

    // Expand the merged line
    fireEvent.click(screen.getByRole('button'))
    // Individual tool cards should now be visible — tool names appear in expanded cards
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
    // At 'none' level, each tool renders as an independent ToolCard (no toggle button)
    const cards = container.querySelectorAll('.tool-icon-single')
    expect(cards.length).toBe(2)
  })

  it.each(['pending', 'running', 'generating'] as const)(
    'uses an accent sweep in a folded %s tool title',
    (status) => {
      const { container } = renderWithProviders(
        <FoldedToolGroup tools={[makeTool({ status })]} level="minimal" />,
      )
      const title = container.querySelector('button[aria-expanded="false"]')
      const sweep = title?.querySelector<HTMLElement>('.sweep-text')
      expect(sweep).not.toBeNull()
      expect(sweep!.style.getPropertyValue('--sweep-color')).toBe('var(--accent)')
    },
  )

  it.each(['done', 'error'] as const)(
    'keeps a folded %s tool title static',
    (status) => {
      const { container } = renderWithProviders(
        <FoldedToolGroup tools={[makeTool({ status })]} level="minimal" />,
      )
      const title = container.querySelector('button[aria-expanded="false"]')
      expect(title?.querySelector('.sweep-text')).toBeNull()
    },
  )

  it.each(['pending', 'running', 'generating'] as const)(
    'uses an accent sweep in an expanded %s tool card',
    (status) => {
      const { container } = renderWithProviders(
        <FoldedToolGroup
          tools={[makeTool({ name: 'Read', label: 'Read: file.go', status })]}
          level="none"
        />,
      )
      const sweep = container.querySelector<HTMLElement>('.sweep-text')
      expect(sweep).not.toBeNull()
      expect(sweep).toHaveTextContent('Read')
      expect(sweep!.style.getPropertyValue('--sweep-color')).toBe('var(--accent)')
    },
  )

  it('keeps the SubAgent tool static because its progress card owns the sweep', () => {
    const { container } = renderWithProviders(
      <FoldedToolGroup
        tools={[makeTool({ name: 'SubAgent', label: 'SubAgent: review', status: 'running' })]}
        level="minimal"
      />,
    )

    expect(container.querySelector('.sweep-text')).toBeNull()
    fireEvent.click(screen.getByRole('button'))
    expect(container.querySelector('.sweep-text')).toBeNull()
  })

  it('uses one sweep per tool in a merged running-tool title', () => {
    const { container } = renderWithProviders(
      <FoldedToolGroup
        tools={[
          makeTool({ name: 'Read', label: 'Read', status: 'running' }),
          makeTool({ name: 'Grep', label: 'Grep', status: 'running' }),
        ]}
        level="minimal"
      />,
    )

    const title = container.querySelector('button[aria-expanded="false"]')
    // Each tool group gets its own SweepText (icon + name per group).
    // This ensures icons are positioned before their corresponding names.
    expect(title?.querySelectorAll('.sweep-text')).toHaveLength(2)
  })

  it('does not animate both the title and card for one expanded running tool', () => {
    const { container } = renderWithProviders(
      <FoldedToolGroup tools={[makeTool({ status: 'running' })]} level="minimal" />,
    )

    fireEvent.click(screen.getByRole('button'))
    expect(container.querySelectorAll('.sweep-text')).toHaveLength(1)
  })

  it.each(['done', 'error'] as const)(
    'keeps an expanded %s tool card title static',
    (status) => {
      const { container } = renderWithProviders(
        <FoldedToolGroup tools={[makeTool({ status })]} level="none" />,
      )
      expect(container.querySelector('.sweep-text')).toBeNull()
    },
  )

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

  // ── Icon position tests ──────────────────────────────────────────────
  // Regression: icons must appear BEFORE their corresponding tool name,
  // not grouped together before all names. This was broken when the running
  // branch rendered all icons in one span, then all text in a SweepText.
  // These tests ensure each icon is immediately followed by its tool name.

  /** Helper: check that within a span, the icon element comes before the text element. */
  function expectIconBeforeText(span: HTMLElement) {
    const icon = span.querySelector('.tool-icon-single')
    const text = span.querySelector('.sweep-text, .font-mono')
    expect(icon).not.toBeNull()
    expect(text).not.toBeNull()
    // Compare DOM position: icon should come before text
    // compareDocumentPosition returns bitmask; Node.DOCUMENT_POSITION_FOLLOWING = 4
    // If icon precedes text: text.compareDocumentPosition(icon) & 4 === 4 (icon is following text → wrong)
    // If icon precedes text: icon.compareDocumentPosition(text) & 4 === 4 (text follows icon → correct)
    const mask = icon!.compareDocumentPosition(text!)
    expect(mask & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy() // text follows icon
  }

  it('places each icon directly before its tool name in merged running tools', () => {
    const tools = [
      makeTool({ name: 'Fetch', label: 'Fetch', status: 'running' }),
      makeTool({ name: 'WebSearch', label: 'WebSearch', status: 'running' }),
    ]
    const { container } = renderWithProviders(
      <FoldedToolGroup tools={tools} level="minimal" />,
    )
    const button = container.querySelector('button[aria-expanded="false"]')
    expect(button).not.toBeNull()

    // Each tool group should be a span containing [icon, text] in that order.
    // We do NOT want all icons grouped in one span before all text.
    const toolSpans = button!.querySelectorAll('span.flex.items-center.gap-0\\.5')
    expect(toolSpans.length).toBe(2) // one span per tool group

    expectIconBeforeText(toolSpans[0] as HTMLElement) // Fetch
    expectIconBeforeText(toolSpans[1] as HTMLElement) // WebSearch
  })

  it('places each icon directly before its tool name in merged done tools', () => {
    const tools = [
      makeTool({ name: 'Read', label: 'Read', status: 'done' }),
      makeTool({ name: 'Grep', label: 'Grep', status: 'done' }),
    ]
    const { container } = renderWithProviders(
      <FoldedToolGroup tools={tools} level="minimal" />,
    )
    const button = container.querySelector('button[aria-expanded="false"]')
    expect(button).not.toBeNull()

    const toolSpans = button!.querySelectorAll('span.flex.items-center.gap-0\\.5')
    expect(toolSpans.length).toBe(2)

    for (const span of toolSpans) {
      expectIconBeforeText(span as HTMLElement)
    }
  })

  it('never groups all icons before all names (regression test)', () => {
    // This is the specific regression: a previous implementation rendered
    // all icons in one <span>, then all text in a <SweepText>, causing
    // [icon][icon][text text] instead of [icon text][icon text].
    const tools = [
      makeTool({ name: 'Fetch', label: 'Fetch', status: 'running' }),
      makeTool({ name: 'WebSearch', label: 'WebSearch', status: 'running' }),
    ]
    const { container } = renderWithProviders(
      <FoldedToolGroup tools={tools} level="minimal" />,
    )
    const button = container.querySelector('button[aria-expanded="false"]')
    expect(button).not.toBeNull()

    // Verify: no span contains 2+ icons without text between them.
    // Each tool group span should have exactly 1 icon.
    const toolSpans = button!.querySelectorAll('span.flex.items-center.gap-0\\.5')
    for (const span of toolSpans) {
      const icons = span.querySelectorAll(':scope > .tool-icon-single')
      expect(icons.length).toBe(1) // exactly 1 icon per tool group span
    }
  })
})

describe('IterationGroup', () => {
  it('renders T (reasoning), C (tools), O (text) in order', () => {
    const iter = makeIteration({
      iteration: 1,
      reasoning: 'planning the approach',
      content: 'Here is the output',
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
    expect(container.querySelector('.fold-container')).toBeNull()
  })

  it('renders O (text output) always visible', () => {
    const iter = makeIteration({
      iteration: 3,
      content: 'Final answer here',
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
    const { container } = renderWithProviders(<IterationGroup iteration={iter} level="minimal" />)
    // Merged line shows icons in the button (fold-container also renders hidden icons)
    const button = container.querySelector('button[aria-expanded="false"]')
    const icons = button!.querySelectorAll('.tool-icon-single')
    expect(icons.length).toBe(2)
  })

  it('renders a hint when iteration is empty', () => {
    const iter = makeIteration({ iteration: 1 })
    renderWithProviders(<IterationGroup iteration={iter} level="minimal" />)
    // Should render the "none" hint
    expect(screen.getByText('—')).toBeInTheDocument()
  })
})

describe('getToolIcon', () => {
  it('returns SquareTerminal for Shell', () => {
    expect(getToolIcon('Shell')).toBe(SquareTerminal)
  })

  it('returns FileText for Read', () => {
    expect(getToolIcon('Read')).toBe(FileText)
  })

  it('returns Search for Grep', () => {
    expect(getToolIcon('Grep')).toBe(Search)
  })

  it('returns Sparkles for SubAgent', () => {
    expect(getToolIcon('SubAgent')).toBe(Sparkles)
  })

  it('returns Wrench for unmapped tool names', () => {
    expect(getToolIcon('UnknownTool')).toBe(Wrench)
    expect(getToolIcon('')).toBe(Wrench)
  })
})
