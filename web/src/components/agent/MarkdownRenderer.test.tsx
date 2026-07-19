/**
 * Rendering tests for MarkdownRenderer (Spec 4 §3.6).
 *
 * Verifies GFM (tables, lists, strikethrough), math (KaTeX), code blocks with
 * highlight.js tokens, inline code, links, and that the component memoizes on
 * content equality.
 */
import { describe, expect, it } from 'vitest'
import { render } from '@testing-library/react'
import '@testing-library/jest-dom'

import { MarkdownRenderer } from '@/components/agent/MarkdownRenderer'

describe('MarkdownRenderer', () => {
  it('renders headings, paragraphs, and lists', () => {
    const { container } = render(
      <MarkdownRenderer content={'# Title\n\nA paragraph.\n\n- a\n- b\n'} />,
    )
    expect(container.querySelector('h1')).toHaveTextContent('Title')
    expect(container.querySelectorAll('li')).toHaveLength(2)
  })

  it('renders a GFM table', () => {
    const { container } = render(
      <MarkdownRenderer content={'| a | b |\n| --- | --- |\n| 1 | 2 |\n'} />,
    )
    const table = container.querySelector('table')
    expect(table).not.toBeNull()
    expect(table!.querySelectorAll('tbody tr')).toHaveLength(1)
  })

  it('renders a highlighted code block with a language label', () => {
    const { container } = render(
      <MarkdownRenderer content={'```ts\nconst x: number = 1\n```'} />,
    )
    const pre = container.querySelector('pre')
    expect(pre).not.toBeNull()
    // language label is rendered
    expect(container.textContent).toContain('ts')
    // highlight.js emits token spans (hljs-* classes)
    const code = pre!.querySelector('code')
    expect(code?.className).toContain('hljs')
  })

  it('renders inline code', () => {
    const { container } = render(<MarkdownRenderer content={'use `foo()` please'} />)
    const inline = container.querySelector('code')
    expect(inline).not.toBeNull()
    expect(inline).toHaveTextContent('foo()')
  })

  it('renders inline math and display math (KaTeX)', () => {
    const { container } = render(
      <MarkdownRenderer content={'Inline $a^2 + b^2 = c^2$ here.\n\n$$\nE=mc^2\n$$'} />,
    )
    // KaTeX renders elements with class katex / katex-display
    expect(container.querySelectorAll('.katex').length).toBeGreaterThan(0)
    expect(container.querySelector('.katex-display')).not.toBeNull()
  })

  it('renders LaTeX-style inline and display delimiters', () => {
    const { container } = render(
      <MarkdownRenderer content={'Inline \\(x^2\\) here.\n\n\\[\nE=mc^2\n\\]'} />,
    )
    expect(container.querySelectorAll('.katex').length).toBeGreaterThan(0)
    expect(container.querySelector('.katex-display')).not.toBeNull()
  })

  it('does not convert LaTeX delimiters inside fenced code', () => {
    const { container } = render(
      <MarkdownRenderer content={'```tex\n\\[not math\\]\n```'} />,
    )
    expect(container.querySelector('.katex')).toBeNull()
    expect(container.querySelector('code')).toHaveTextContent('\\[not math\\]')
  })

  it('renders links with safe target/rel', () => {
    const { container } = render(
      <MarkdownRenderer content={'[xbot](https://example.com)'} />,
    )
    const link = container.querySelector('a')
    expect(link).toHaveAttribute('target', '_blank')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
  })

  it('renders strikethrough via GFM', () => {
    const { container } = render(<MarkdownRenderer content={'~~deleted~~'} />)
    expect(container.querySelector('del')).not.toBeNull()
  })

  it('clips already-rendered markdown on typewriter ticks without replacing the tree', () => {
    const { container, rerender } = render(
      <MarkdownRenderer content={'Hello **world**'} streaming visibleChars={5} />,
    )
    const paragraph = container.querySelector('p')
    expect(container.textContent).toBe('Hello')

    rerender(<MarkdownRenderer content={'Hello **world**'} streaming visibleChars={11} />)

    expect(container.querySelector('p')).toBe(paragraph)
    expect(container.textContent).toBe('Hello world')
    expect(container.querySelector('strong')).toHaveTextContent('world')
  })

  it('memoizes: re-render with same content keeps the same DOM text', () => {
    const { container, rerender } = render(<MarkdownRenderer content={'hello'} />)
    const before = container.innerHTML
    rerender(<MarkdownRenderer content={'hello'} />)
    expect(container.innerHTML).toBe(before)
  })
})
