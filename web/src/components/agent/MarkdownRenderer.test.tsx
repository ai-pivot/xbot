/**
 * Rendering tests for MarkdownRenderer (Spec 4 §3.6).
 *
 * Verifies GFM (tables, lists, strikethrough), math (KaTeX), code blocks with
 * highlight.js tokens, inline code, links, and that the component memoizes on
 * content equality.
 */
import { describe, expect, it } from 'vitest'
import { render, waitFor } from '@testing-library/react'
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

  it('renders a highlighted code block with a language label', async () => {
    const { container } = render(
      <MarkdownRenderer content={'```ts\nconst x: number = 1\n```'} />,
    )
    const pre = container.querySelector('pre')
    expect(pre).not.toBeNull()
    // language label is rendered
    expect(container.textContent).toContain('ts')
    // highlight.js is async — wait for the hljs class to appear
    await waitFor(() => {
      const code = pre!.querySelector('code')
      expect(code?.className).toContain('hljs')
    })
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

  it('streaming: an unclosed trailing $$ block is clipped, not leaked as literal markers', () => {
    // Regression (mermaid-style guard for math): during streaming the content
    // snapshot can end mid-formula. remark-math only recognizes PAIRED $$ —
    // the unclosed delimiter renders as a literal "$$" and the partial TeX
    // body (\begin{aligned}, \\, &, \item) leaks through the markdown parser
    // as hard breaks / empty lists / stray markers that pile up at the bottom.
    const partial = '推导如下：\n\n$$\n\\begin{aligned}\nx &= 1 \\\\\ny &= 2 \\\\'
    const { container } = render(<MarkdownRenderer content={partial} streaming visibleChars={9999} />)
    const text = container.textContent ?? ''
    expect(text).not.toContain('$$')
    expect(text).not.toContain('\\begin{aligned}')
    // 完整的前置文本仍在
    expect(text).toContain('推导如下：')
  })

  it('streaming: a CLOSED $$ block before the unclosed tail still renders as KaTeX', () => {
    const partial = '完整公式 $E=mc^2$ 渲染。\n\n$$\n\\begin{aligned}\nx &= 1 \\\\'
    const { container } = render(<MarkdownRenderer content={partial} streaming visibleChars={9999} />)
    // 已闭合的行内公式正常渲染
    expect(container.querySelectorAll('.katex').length).toBeGreaterThan(0)
    // 尾部未闭合块被截掉
    const text = container.textContent ?? ''
    expect(text).not.toContain('$$')
    expect(text).not.toContain('\\begin{aligned}')
  })

  it('streaming: unclosed \\[ display delimiter (normalized to $$) is clipped too', () => {
    const partial = '推导：\n\n\\[\nE = mc^2\n\\begin{aligned}'
    const { container } = render(<MarkdownRenderer content={partial} streaming visibleChars={9999} />)
    const text = container.textContent ?? ''
    expect(text).not.toContain('$$')
    expect(text).not.toContain('E = mc^2')
    expect(text).toContain('推导：')
  })

  it('streaming: an unclosed trailing inline $ is clipped to end of line', () => {
    const partial = '前文完整。行内公式 $x^2 + '
    const { container } = render(<MarkdownRenderer content={partial} streaming visibleChars={9999} />)
    const text = container.textContent ?? ''
    expect(text).toContain('前文完整')
    expect(text).not.toContain('$')
    expect(text).not.toContain('x^2')
  })

  it('streaming: $$ inside fenced code does not trigger clipping', () => {
    const partial = '```bash\necho $$\n```\n\n尾部普通文本'
    const { container } = render(<MarkdownRenderer content={partial} streaming visibleChars={9999} />)
    const text = container.textContent ?? ''
    expect(text).toContain('echo $')
    expect(text).toContain('尾部普通文本')
  })

  it('non-streaming: unclosed math is NOT clipped (finished content is authoritative)', () => {
    // 完成态（流结束/历史消息）内容即权威 —— 不截断。remark-math 的 math-flow
    // 会把未闭合 $$ 到结尾吞进 KaTeX（现状行为），与 streaming 截断形成对照。
    const partial = '完成但未闭合：\n\n$$\nx = 1'
    const { container } = render(<MarkdownRenderer content={partial} />)
    expect(container.querySelector('.katex')).not.toBeNull()
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

  // --- Streaming inline-code regression tests ---
  // Reproduces the bug where inline <code> blocks render empty during SSE
  // streaming. The root cause: when content grows past a backtick pair, the
  // markdown structure changes (a <code> element appears). clipTextNodes walks
  // all Text nodes in document order and blanks those beyond visibleChars. If
  // sourceRef fails to restore the code text on the next typewriter tick, the
  // code stays empty.

  it('streaming one char at a time: inline code is not empty after stream passes it', () => {
    const full = 'use `foo()` please'
    const { container, rerender } = render(
      <MarkdownRenderer content={full.charAt(0)} streaming visibleChars={1} />,
    )
    for (let i = 2; i <= full.length; i++) {
      rerender(<MarkdownRenderer content={full.slice(0, i)} streaming visibleChars={i} />)
    }
    const code = container.querySelector('code')
    expect(code).not.toBeNull()
    expect(code).toHaveTextContent('foo()')
  })

  it('typewriter catches up past a single inline code: code is not empty', () => {
    // content already complete (SSE pushed full text), visibleChars lags behind
    const full = 'ab `cd` ef'
    const { container, rerender } = render(
      <MarkdownRenderer content={full} streaming visibleChars={2} />,
    )
    for (let v = 3; v <= 20; v++) {
      rerender(<MarkdownRenderer content={full} streaming visibleChars={v} />)
    }
    const code = container.querySelector('code')
    expect(code).not.toBeNull()
    expect(code).toHaveTextContent('cd')
  })

  it('streaming through multiple inline codes: none should be empty after full visible', () => {
    // Mirrors the real-world report: several inline codes, earlier ones
    // rendered empty while later ones had content.
    const full = 'a `b` c `d` e'
    const { container, rerender } = render(
      <MarkdownRenderer content={full} streaming visibleChars={0} />,
    )
    for (let v = 1; v <= 20; v++) {
      rerender(<MarkdownRenderer content={full} streaming visibleChars={v} />)
    }
    const codes = container.querySelectorAll('p code')
    expect(codes).toHaveLength(2)
    expect(codes[0]).toHaveTextContent('b')
    expect(codes[1]).toHaveTextContent('d')
  })

  it('content grows mid-stream to introduce a code block: code not empty after catch-up', () => {
    // Simulate: content starts as plain text, then grows to include a closed
    // backtick pair. visibleChars continues advancing past the code.
    const { container, rerender } = render(
      <MarkdownRenderer content={'hello world'} streaming visibleChars={5} />,
    )
    // content grows to introduce inline code
    rerender(<MarkdownRenderer content={'hello `world` end'} streaming visibleChars={5} />)
    // typewriter catches up past the code
    for (let v = 6; v <= 30; v++) {
      rerender(<MarkdownRenderer content={'hello `world` end'} streaming visibleChars={v} />)
    }
    const code = container.querySelector('code')
    expect(code).not.toBeNull()
    expect(code).toHaveTextContent('world')
  })

  it('SSE char-by-char with visibleChars lagging: inline code not empty after catch-up', () => {
    // Simulate real SSE: content grows one char per event, visibleChars lags
    // behind by a few chars (typewriter animation delay).
    const full = '已部署。`在 state 变化时不重新处理 DOM` 现在依赖 `[html, storeVersion]`'
    const { container, rerender } = render(
      <MarkdownRenderer content={full.slice(0, 1)} streaming visibleChars={1} />,
    )
    for (let i = 2; i <= full.length; i++) {
      const content = full.slice(0, i)
      // visibleChars lags 3 chars behind content length (typewriter delay)
      const vc = Math.max(0, i - 3)
      rerender(<MarkdownRenderer content={content} streaming visibleChars={vc} />)
    }
    // typewriter catches up fully
    for (let v = full.length; v <= full.length + 5; v++) {
      rerender(<MarkdownRenderer content={full} streaming visibleChars={v} />)
    }
    const codes = container.querySelectorAll('p code')
    expect(codes.length).toBeGreaterThanOrEqual(2)
    for (const code of codes) {
      expect(code.textContent).not.toBe('')
    }
  })

  it('content grows and visibleChars catches up: inline code not empty after backtick closes', () => {
    // Reproduces the user-reported bug: SSE pushes content incrementally,
    // visibleChars lags behind. When the closing backtick arrives, the code
    // span is formed. But if clipTextNodes blanks the code text and sourceRef
    // fails to restore it on the next tick, the code stays empty.
    const { container, rerender } = render(
      <MarkdownRenderer content={'ab `cd'} streaming visibleChars={3} />,
    )
    // SSE pushes the closing backtick; content now has a complete code span.
    // visibleChars still lags (typewriter hasn't caught up).
    rerender(<MarkdownRenderer content={'ab `cd` ef'} streaming visibleChars={3} />)
    // Typewriter catches up past the code.
    for (let v = 4; v <= 20; v++) {
      rerender(<MarkdownRenderer content={'ab `cd` ef'} streaming visibleChars={v} />)
    }
    const code = container.querySelector('code')
    expect(code).not.toBeNull()
    expect(code).toHaveTextContent('cd')
  })

  it('content and visibleChars interleave: no empty code after full catch-up', () => {
    // Simulate realistic SSE + typewriter interleaving: content grows by a
    // few chars, then visibleChars catches up, then content grows again.
    const { container, rerender } = render(
      <MarkdownRenderer content={'hello'} streaming visibleChars={0} />,
    )
    const steps: [string, number][] = [
      ['hello `wo', 2],
      ['hello `wo', 5],
      ['hello `world`', 5],
      ['hello `world` end', 8],
      ['hello `world` end', 20],
    ]
    for (const [content, vc] of steps) {
      rerender(<MarkdownRenderer content={content} streaming visibleChars={vc} />)
    }
    const code = container.querySelector('code')
    expect(code).not.toBeNull()
    expect(code).toHaveTextContent('world')
  })

  it('strips YAML frontmatter instead of rendering it as a heading (regression)', () => {
    const src = `---
name: sglang-bench
description: "SGLang 推理服务 benchmark 方法论"
---

# 正文

A paragraph.`
    const { container } = render(<MarkdownRenderer content={src} />)
    // Body renders normally…
    expect(container.querySelector('h1')).toHaveTextContent('正文')
    expect(container.textContent).toContain('A paragraph.')
    // …but the frontmatter is NOT swallowed into an <h2> (old setext bug).
    const h2s = Array.from(container.querySelectorAll('h2'))
    expect(h2s.some((h) => h.textContent?.includes('sglang-bench'))).toBe(false)
  })
})
