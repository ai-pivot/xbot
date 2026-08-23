/**
 * Rendering tests for MarkdownPreview with YAML frontmatter.
 *
 * Regression: SKILL.md's leading `---` block was parsed by CommonMark as a
 * setext heading underline — the frontmatter YAML lines rendered as an <h2>
 * ("yaml 被渲染成标题"). The preview must strip the block and render it as a
 * FrontmatterCard instead.
 */
import { describe, expect, it } from 'vitest'
import { render } from '@testing-library/react'
import '@testing-library/jest-dom'

import { MarkdownPreview } from '@/components/file/MarkdownPreview'

const SKILL_MD = `---
name: sglang-bench
description: "SGLang 推理服务 benchmark 方法论"
---

# 正文

A paragraph.`

describe('MarkdownPreview with YAML frontmatter', () => {
  it('renders the body but NOT the frontmatter as a heading (regression)', () => {
    const { container } = render(<MarkdownPreview source={SKILL_MD} />)
    // The real heading from the body renders…
    const h1 = container.querySelector('h1')
    expect(h1).toHaveTextContent('正文')
    // …but no h2 is created from the YAML lines (old bug: name/description → <h2>)
    const h2s = Array.from(container.querySelectorAll('h2'))
    expect(h2s.some((h) => h.textContent?.includes('sglang-bench'))).toBe(false)
    // The body paragraph is present.
    expect(container.textContent).toContain('A paragraph.')
  })

  it('shows a frontmatter card with name and description', () => {
    const { container } = render(<MarkdownPreview source={SKILL_MD} />)
    const card = container.querySelector('[data-testid="frontmatter-card"]')
    expect(card).not.toBeNull()
    expect(card!.textContent).toContain('sglang-bench')
    expect(card!.textContent).toContain('SGLang 推理服务 benchmark 方法论')
  })

  it('does not render a card for plain markdown without frontmatter', () => {
    const { container } = render(<MarkdownPreview source={'# Title\n\nPlain body.'} />)
    expect(container.querySelector('[data-testid="frontmatter-card"]')).toBeNull()
    expect(container.querySelector('h1')).toHaveTextContent('Title')
  })
})
