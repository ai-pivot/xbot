/**
 * Unit tests for markdown frontmatter utilities (src/lib/markdown.ts).
 *
 * Covers: parsing `---`-delimited YAML frontmatter, stripping it from the
 * body, quoted values, CRLF line endings, and the no-frontmatter / unterminated
 * edge cases (must NOT strip a `---` horizontal rule mid-document).
 */
import { describe, expect, it } from 'vitest'

import { parseFrontmatter, stripFrontmatter } from '@/lib/markdown'

const SKILL_MD = `---
name: sglang-bench
description: "SGLang 推理服务 benchmark 方法论"
---

# 正文

A paragraph.`

describe('parseFrontmatter', () => {
  it('parses name/description from a SKILL.md frontmatter block', () => {
    const fm = parseFrontmatter(SKILL_MD)
    expect(fm.hasFrontmatter).toBe(true)
    expect(fm.values.name).toBe('sglang-bench')
    expect(fm.values.description).toBe('SGLang 推理服务 benchmark 方法论')
  })

  it('returns the markdown body with the frontmatter stripped', () => {
    const fm = parseFrontmatter(SKILL_MD)
    expect(fm.body).toBe('# 正文\n\nA paragraph.')
  })

  it('strips double and single quotes from values', () => {
    const fm = parseFrontmatter('---\nname: "quoted"\ndesc: \'single\'\n---\n\nbody')
    expect(fm.values.name).toBe('quoted')
    expect(fm.values.desc).toBe('single')
  })

  it('keeps a colon inside a quoted value', () => {
    const fm = parseFrontmatter('---\ndesc: "a: b: c"\n---\n\nbody')
    expect(fm.values.desc).toBe('a: b: c')
  })

  it('strips an unquoted trailing comment but keeps a quoted one', () => {
    const fm = parseFrontmatter('---\na: 1 # comment\nb: "x # kept"\n---\n\nbody')
    expect(fm.values.a).toBe('1')
    expect(fm.values.b).toBe('x # kept')
  })

  it('handles CRLF line endings', () => {
    const fm = parseFrontmatter('---\r\nname: crlf\r\ndesc: value\r\n---\r\n\r\nbody')
    expect(fm.hasFrontmatter).toBe(true)
    expect(fm.values.name).toBe('crlf')
    expect(fm.body).toBe('body')
  })

  it('handles an empty frontmatter block', () => {
    const fm = parseFrontmatter('---\n---\n\nbody')
    expect(fm.hasFrontmatter).toBe(true)
    expect(fm.values).toEqual({})
    expect(fm.body).toBe('body')
  })

  it('reports hasFrontmatter=false for plain markdown', () => {
    const src = '# Title\n\nA paragraph.\n\n---\n\nA horizontal rule.'
    const fm = parseFrontmatter(src)
    expect(fm.hasFrontmatter).toBe(false)
    expect(fm.values).toEqual({})
    expect(fm.body).toBe(src)
  })

  it('does NOT treat a document that merely contains --- mid-way as frontmatter', () => {
    const src = '# Title\n\n---\n\nrule'
    const fm = parseFrontmatter(src)
    expect(fm.hasFrontmatter).toBe(false)
    expect(fm.body).toBe(src)
  })

  it('does NOT strip an unterminated leading --- (could be a setext rule)', () => {
    const src = '---\nname: dangling\n\n# Title'
    const fm = parseFrontmatter(src)
    expect(fm.hasFrontmatter).toBe(false)
    expect(fm.body).toBe(src)
  })
})

describe('stripFrontmatter', () => {
  it('removes the frontmatter block and keeps the body', () => {
    expect(stripFrontmatter(SKILL_MD)).toBe('# 正文\n\nA paragraph.')
  })

  it('returns the source unchanged when there is no frontmatter', () => {
    const src = '# Title\n\nBody'
    expect(stripFrontmatter(src)).toBe(src)
  })
})
