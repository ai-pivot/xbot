/**
 * Tests for the collapsible intermediate-process components (Spec 4 §3.3).
 *
 * Tests the new folding model: FoldedLine (borderless ▸/▾), FoldedToolGroup
 * (consecutive tool merging), IterationGroup (T→C→O order), and the content
 * renderers ToolCallBlock and ReasoningBlock.
 */
import { describe, expect, it } from 'vitest'
import { screen, fireEvent, waitFor, within } from '@testing-library/react'
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

// radix Popover（@floating-ui 定位）在 jsdom 里需要 ResizeObserver。
// 精简 stub：只要构造函数与三方法存在即可（测试不依赖真实测量）。
class ROStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
;(window as unknown as { ResizeObserver: unknown }).ResizeObserver = ROStub
;(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = ROStub

/** FoldedToolGroup 折叠行的 Popover 浮层容器（radix Portal → document.body）。 */
function popoverContent(): HTMLElement | null {
  return document.querySelector('[data-slot="popover-content"]')
}

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
  it('opens a per-tool popover with summary and args when a pill is clicked', () => {
    const tools = [
      makeTool({ name: 'Read', label: 'Read', summary: 'read a config file', args: '{"path":"/tmp/a.go"}' }),
      makeTool({ name: 'Grep', label: 'Grep', summary: 'grep the tree' }),
    ]
    const { container } = renderWithProviders(<FoldedToolGroup tools={tools} level="minimal" />)
    // 折叠行：无 ▸ 箭头；每个 pill 是独立 trigger，原地无浮窗
    expect(container.textContent).not.toContain('▸')
    const pills = screen.getAllByTestId('tool-pill')
    expect(pills).toHaveLength(2)
    expect(popoverContent()).toBeNull()

    // 点哪个 pill 弹哪个工具的浮窗：summary + 参数 + 渲染，不含第二个工具内容
    fireEvent.click(pills[0]!)
    const content = popoverContent()
    expect(content).not.toBeNull()
    // summary 在浮窗中至少出现一次（ToolPopoverDetail 的 summary 块；ToolCallBlock
    // 在 args 存在时不重复渲染 summary）。参数块断言用 hljs 内容（「参数」标签走
    // i18n，jsdom 无词典——以渲染实质为准）。
    expect(within(content as HTMLElement).getAllByText('read a config file').length).toBeGreaterThan(0)
    expect(within(content as HTMLElement).getByText(/"path"/)).toBeInTheDocument()
    expect(within(content as HTMLElement).queryByText('grep the tree')).toBeNull()
  })

  it('aggregates pills when more than 8 tools: first 7 pills + "+N" badge opens the overflow list', () => {
    const tools = Array.from({ length: 9 }, (_, i) =>
      makeTool({ name: `T${i}`, label: `T${i}` }),
    )
    renderWithProviders(<FoldedToolGroup tools={tools} level="minimal" />)
    // 折叠行：仅前 7 个 pill（T0..T6）+ "+2" 徽标；第 8 个工具（T7）不内联渲染
    expect(screen.getByTestId('tool-pill-more')).toHaveTextContent('+2')
    expect(screen.queryByText('T7')).not.toBeInTheDocument()
    expect(screen.getAllByTestId('tool-pill')).toHaveLength(7)

    // "+N" 浮层 = 溢出的 2 条列表
    fireEvent.click(screen.getByTestId('tool-pill-more'))
    const content = popoverContent()
    expect(content).not.toBeNull()
    expect(within(content as HTMLElement).getAllByTestId('tool-row')).toHaveLength(2)
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
      renderWithProviders(
        <FoldedToolGroup tools={[makeTool({ status })]} level="minimal" />,
      )
      const pill = screen.getByTestId('tool-pill')
      const sweep = pill.querySelector<HTMLElement>('.sweep-text')
      expect(sweep).not.toBeNull()
      expect(sweep!.style.getPropertyValue('--sweep-color')).toBe('var(--accent)')
    },
  )

  it.each(['done', 'error'] as const)(
    'keeps a folded %s tool title static',
    (status) => {
      renderWithProviders(
        <FoldedToolGroup tools={[makeTool({ status })]} level="minimal" />,
      )
      const pill = screen.getByTestId('tool-pill')
      expect(pill.querySelector('.sweep-text')).toBeNull()
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
    fireEvent.click(screen.getByTestId('tool-pill'))
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

    // Each pill gets its own SweepText (icon + name per pill).
    expect(container.querySelectorAll('.sweep-text')).toHaveLength(2)
  })

  it('does not add a second sweep when the running tool popover opens', () => {
    const { container } = renderWithProviders(
      <FoldedToolGroup tools={[makeTool({ status: 'running' })]} level="minimal" />,
    )

    expect(container.querySelectorAll('.sweep-text')).toHaveLength(1)
    fireEvent.click(screen.getByTestId('tool-pill'))
    // 折叠行 pill 保持 1 个流光；浮窗（Portal，container 外）header 用静态文本，无流光
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

  it('renders single tool as an independent pill with its own popover regardless of level', () => {
    const tools = [makeTool({ name: 'Read', label: 'Read' })]
    const { container } = renderWithProviders(
      <FoldedToolGroup tools={tools} level="minimal" />,
    )
    // Single tool: one pill trigger with its own per-tool popover (no row trigger)
    const pills = screen.getAllByTestId('tool-pill')
    expect(pills.length).toBe(1)
    expect(container.querySelector('button[aria-expanded]')).toBeNull()
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

  /**
   * Helper: check that within a pill, the status icon element comes before
   * the mono name text element. Pill DOM: [icon (pulse span | lucide svg), text].
   */
  function expectIconBeforeText(pill: HTMLElement) {
    const icon = pill.firstElementChild
    const text = pill.querySelector('.font-mono')
    expect(icon).not.toBeNull()
    expect(text).not.toBeNull()
    // Compare DOM position: icon should come before text.
    // icon.compareDocumentPosition(text) & DOCUMENT_POSITION_FOLLOWING(4) → text follows icon (correct)
    const mask = icon!.compareDocumentPosition(text!)
    expect(mask & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy() // text follows icon
  }

  /** Helper: fold row pill triggers → the per-tool pill elements (rounded-full). */
  function foldedPills(root: HTMLElement): HTMLElement[] {
    const triggers = root.querySelectorAll('[data-testid="tool-pill"]')
    return Array.from(triggers).map((trigger) => {
      const pill = trigger.querySelector('span.rounded-full')
      expect(pill).not.toBeNull()
      return pill as HTMLElement
    })
  }

  it('places each status icon directly before its tool name in merged running pills', () => {
    const tools = [
      makeTool({ name: 'Fetch', label: 'Fetch', status: 'running' }),
      makeTool({ name: 'WebSearch', label: 'WebSearch', status: 'running' }),
    ]
    const { container } = renderWithProviders(
      <FoldedToolGroup tools={tools} level="minimal" />,
    )

    // Each tool is its own pill containing [icon, text] in that order.
    const pills = foldedPills(container)
    expect(pills.length).toBe(2)

    expectIconBeforeText(pills[0]) // Fetch
    expectIconBeforeText(pills[1]) // WebSearch
  })

  it('places each status icon directly before its tool name in merged done pills', () => {
    const tools = [
      makeTool({ name: 'Read', label: 'Read', status: 'done' }),
      makeTool({ name: 'Grep', label: 'Grep', status: 'done' }),
    ]
    const { container } = renderWithProviders(
      <FoldedToolGroup tools={tools} level="minimal" />,
    )

    const pills = foldedPills(container)
    expect(pills.length).toBe(2)

    for (const pill of pills) {
      expectIconBeforeText(pill)
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

    // Each pill must pair its own icon with its own name (never 2 icons in one pill).
    const pills = foldedPills(container)
    expect(pills.length).toBe(2)
    for (const pill of pills) {
      expectIconBeforeText(pill)
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

  it('renders tools with FoldedToolGroup (pill row + popover)', () => {
    const iter = makeIteration({
      iteration: 1,
      tools: [
        makeTool({ name: 'Read', label: 'Read' }),
        makeTool({ name: 'Grep', label: 'Grep' }),
      ],
      toolCount: 2,
    })
    const { container } = renderWithProviders(<IterationGroup iteration={iter} level="minimal" />)
    // Folded row: 2 pills（各自独立浮窗），无 ▸ 箭头，原地无浮窗内容
    expect(container.textContent).not.toContain('▸')
    const pills = screen.getAllByTestId('tool-pill')
    expect(pills).toHaveLength(2)
    expect(popoverContent()).toBeNull()

    // 点第一个 pill → 该工具的浮窗（summary/参数/渲染），非全量列表
    fireEvent.click(pills[0]!)
    const content = popoverContent()
    expect(content).not.toBeNull()
    expect(within(content as HTMLElement).queryAllByTestId('tool-row')).toHaveLength(0)
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
