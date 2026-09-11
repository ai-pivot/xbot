/**
 * Tests for the collapsible / intermediate-process components.
 *
 * Model: FoldedLine (borderless ▸/▾ for reasoning), ToolGroup (every tool call
 * rendered as its own expanded card — NO folding, NO cross-iteration merge),
 * IterationGroup (T→O→C order), plus the content renderers.
 */
import { describe, expect, it } from 'vitest'
import { screen, fireEvent, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { FoldedLine } from '@/components/agent/FoldedLine'
import { ToolGroup } from '@/components/agent/ToolGroup'
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

describe('ToolGroup', () => {
  it('renders every tool as its own expanded card — no folding, no summary row', () => {
    const { container } = renderWithProviders(
      <ToolGroup
        tools={[
          makeTool({ name: 'CustomToolA', label: 'CustomToolA: a', detail: 'file A' }),
          makeTool({ name: 'CustomToolB', label: 'CustomToolB: foo', detail: 'match B' }),
        ]}
      />,
    )
    // Each tool's detail is directly visible (no click, no popover).
    expect(screen.getByText('file A')).toBeInTheDocument()
    expect(screen.getByText('match B')).toBeInTheDocument()
    // No fold arrow / no collapse container anywhere.
    expect(container.textContent).not.toContain('▸')
    expect(container.querySelector('.fold-container')).toBeNull()
    // Structural assertion (the old `queryAllByTestId('tool-pill')` was
    // vacuously true — nothing produces that testid anymore): exactly one
    // card per tool.
    expect(screen.getAllByTestId('tool-card')).toHaveLength(2)
  })

  it('renders each of many tools individually (no "+N" overflow badge)', () => {
    const tools = Array.from({ length: 10 }, (_, i) =>
      makeTool({ name: 'CustomTool', label: `CustomTool: f${i}`, detail: `detail ${i}` }),
    )
    renderWithProviders(<ToolGroup tools={tools} />)
    for (let i = 0; i < 10; i++) {
      expect(screen.getByText(`detail ${i}`)).toBeInTheDocument()
    }
    // All ten cards render — no "+N" overflow badge collecting the tail.
    expect(screen.getAllByTestId('tool-card')).toHaveLength(10)
  })

  it('renders nothing for empty tools', () => {
    const { container } = renderWithProviders(<ToolGroup tools={[]} />)
    expect(container.firstChild).toBeNull()
  })

  it('shows the tool name and elapsed time', () => {
    const { container } = renderWithProviders(
      <ToolGroup tools={[makeTool({ name: 'Shell', label: 'Shell: ls', elapsedMs: 1500 })]} />,
    )
    // Header carries name + short param (same info the old pill showed).
    expect(container.textContent).toContain('Shell')
    expect(container.textContent).toContain('ls')
    expect(screen.getByText('1.5s')).toBeInTheDocument()
  })

  it('generating tools show the raw tool name (no label parsing flicker)', () => {
    const { container } = renderWithProviders(
      <ToolGroup tools={[makeTool({ name: 'CustomTool', label: '思考中…', status: 'generating' })]} />,
    )
    // The raw tool name is used — the streaming label is NOT parsed for display.
    // (Running tools render through SweepText, which splits the text into spans,
    // so assert on textContent rather than a single text node.)
    expect(container.textContent).toContain('CustomTool')
    expect(container.textContent).not.toContain('思考中…')
  })

  // ── Sweep 语义矩阵（自旧的折叠实现移植，防止回归无人拦截） ──
  it.each(['pending', 'running', 'generating'] as const)(
    'in-progress status %s renders the header name with a sweep',
    (status) => {
      const { container } = renderWithProviders(
        <ToolGroup tools={[makeTool({ name: 'CustomTool', label: 'CustomTool: x', status })]} />,
      )
      const sweep = container.querySelector<HTMLElement>('.sweep-text')
      expect(sweep).not.toBeNull()
      // 运行中的颜色 token 必须是 --status-running（与 statusVisual 同源）
      expect(sweep!.style.getPropertyValue('--sweep-color')).toBe('var(--status-running)')
    },
  )

  it.each(['done', 'error'] as const)(
    'settled status %s renders the header name WITHOUT a sweep',
    (status) => {
      const { container } = renderWithProviders(
        <ToolGroup tools={[makeTool({ name: 'CustomTool', label: 'CustomTool: x', status })]} />,
      )
      expect(container.querySelector('.sweep-text')).toBeNull()
    },
  )

  it('a running SubAgent does NOT sweep (the progress card owns that animation)', () => {
    const { container } = renderWithProviders(
      <ToolGroup
        tools={[makeTool({ name: 'SubAgent', label: 'SubAgent: explore', status: 'running' })]}
      />,
    )
    expect(container.querySelector('.sweep-text')).toBeNull()
  })

  it('renders the status icon BEFORE the tool name', () => {
    const { container } = renderWithProviders(
      <ToolGroup tools={[makeTool({ name: 'CustomTool', label: 'CustomTool: x' })]} />,
    )
    const icon = container.querySelector('svg.tool-icon-single')
    expect(icon).not.toBeNull()
    const header = icon!.parentElement!
    const kids = Array.from(header.children)
    // 图标在第一个子元素里，文本在其后 —— 顺序回归会在这里被拦下。
    expect(kids[0].contains(icon!)).toBe(true)
    expect(kids.length).toBeGreaterThan(1)
    expect(kids[1].tagName.toLowerCase()).not.toBe('svg')
  })
})

describe('IterationGroup', () => {
  it('renders T → O → C in order (reasoning, text output, then tools)', () => {
    const iter = makeIteration({
      iteration: 1,
      reasoning: 'planning the approach',
      content: 'Here is the output',
      tools: [makeTool({ name: 'CustomTool', label: 'CustomTool: run' })],
      toolCount: 1,
    })
    const { container } = renderWithProviders(<IterationGroup iteration={iter} />)
    // Reasoning is a folded line with character count as title
    expect(screen.getByText(/Thought.*characters/)).toBeInTheDocument()
    expect(screen.getAllByTestId('tool-card')).toHaveLength(1)
    // O text from MarkdownRenderer
    expect(screen.getByText('Here is the output')).toBeInTheDocument()

    // 顺序断言（旧的用例名声称有顺序校验，实际只断言"三者都存在"）。
    // textContent 的顺序即 DOM 顺序：T 的标题 → O 的正文 → C 的卡片头。
    const text = container.textContent ?? ''
    const tIdx = text.indexOf('Thought')
    const oIdx = text.indexOf('Here is the output')
    const cIdx = text.indexOf('CustomTool')
    expect(tIdx).toBeGreaterThanOrEqual(0)
    expect(oIdx).toBeGreaterThan(tIdx)
    expect(cIdx).toBeGreaterThan(oIdx)
  })

  it('renders reasoning (T) as a folded line (collapsed by default)', () => {
    const { container } = renderWithProviders(
      <IterationGroup iteration={makeIteration({ iteration: 2, reasoning: 'deep thinking' })} />,
    )
    expect(screen.getByText(/Thought.*characters/)).toBeInTheDocument()
    expect(container.querySelector('.fold-container')).toBeNull()
  })

  it('renders O (text output) always visible', () => {
    const iter = makeIteration({ iteration: 3, content: 'Final answer here' })
    renderWithProviders(<IterationGroup iteration={iter} />)
    expect(screen.getByText('Final answer here')).toBeInTheDocument()
  })

  it('renders tools as independent expanded cards', () => {
    const iter = makeIteration({
      iteration: 1,
      tools: [
        makeTool({ name: 'CustomToolA', label: 'CustomToolA: a', detail: 'file A' }),
        makeTool({ name: 'CustomToolB', label: 'CustomToolB: foo', detail: 'match B' }),
      ],
      toolCount: 2,
    })
    const { container } = renderWithProviders(<IterationGroup iteration={iter} />)
    expect(container.textContent).not.toContain('▸')
    // Both tool outputs are visible without any interaction.
    expect(screen.getByText('file A')).toBeInTheDocument()
    expect(screen.getByText('match B')).toBeInTheDocument()
  })

  it('renders a hint when iteration is empty', () => {
    renderWithProviders(<IterationGroup iteration={makeIteration({ iteration: 1 })} />)
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
