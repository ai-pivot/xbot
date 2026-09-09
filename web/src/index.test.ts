// Composer CSS regression guards (index.css).
//
// These assert the SELECTOR SHAPE of two rules whose interaction caused the
// "粘贴图片后图片后面出现小黑色正方形，输入文字就消失" bug:
//
//   ProseMirror injects an EMPTY <img class="ProseMirror-separator" alt=""> after
//   an inline node (e.g. a pasted image) as a zero-width caret anchor. It has no
//   src and alt="", so the image-loading-placeholder rule
//   (`img[alt=""]` / `img:not([src])` → 24×24 gradient square) styled it as a
//   visible broken-image block until ProseMirror removed it on typing.
//
// The fix has two halves that must stay in place:
//   1. the placeholder rule EXCLUDES .ProseMirror-separator, and
//   2. the separator gets its own zero-sized, invisible rule.
// @ts-expect-error node builtin — test-only file; the web tsconfig has no node types
import { readFileSync } from 'node:fs'
// @ts-expect-error node builtin — test-only file
import { dirname, resolve } from 'node:path'
// @ts-expect-error node builtin — test-only file
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const here = dirname(fileURLToPath(import.meta.url))
const css = readFileSync(resolve(here, 'index.css'), 'utf8')

describe('composer ProseMirror-separator CSS', () => {
  it('image placeholder rule excludes the separator', () => {
    // Both placeholder selectors must carry the :not(.ProseMirror-separator) guard.
    expect(css).toMatch(/img\[alt=""\]:not\(\.ProseMirror-separator\)/)
    expect(css).toMatch(/img:not\(\[src\]\):not\(\.ProseMirror-separator\)/)
  })

  it('separator has a zero-sized invisible rule that keeps display:inline', () => {
    const idx = css.indexOf('.ProseMirror.xbot-editor img.ProseMirror-separator')
    expect(idx, 'separator rule missing from index.css').toBeGreaterThan(-1)
    const block = css.slice(idx, css.indexOf('}', idx) + 1)
    expect(block).toContain('display: inline !important')
    expect(block).toContain('width: 0 !important')
    expect(block).toContain('height: 0 !important')
    expect(block).toContain('min-height: 0 !important')
    expect(block).toContain('background: none !important')
    expect(block).toContain('opacity: 0')
    // display:none would break caret positioning next to the inline image.
    expect(block).not.toContain('display: none')
  })
})
