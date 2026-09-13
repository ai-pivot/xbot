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

/**
 * 迭代块渲染隔离（perf：代价与 turn 内迭代数无关，2026-09-13 trace 归因）。
 *
 * trace 证据：App JS 不随时间增长，但浏览器侧 Layout ×1.70 / Paint ×1.69 /
 * RasterTask ×3.26 / GPUTask ×1.82，`Layout.dirtyObjects` 16→64（×4），
 * 7 次 `UpdateLayoutTree` 单次重算 ~4,700–4,800 元素（≈ 整个 turn 子树）。
 * 修复 = 每个迭代块独立 containment（离屏块跳过 style/layout/paint）：
 * 追加第 N+1 个迭代块的代价 O(1)，与 N 无关。
 * 断言选择器与关键声明本身（删掉任何一条 → 红灯），类名与 TurnBody 的渲染
 * 绑定由 components/agent/TurnBody.test.tsx 守护。
 */
describe('iteration block containment CSS (perf)', () => {
  const ruleBlock = (selector: string): string => {
    const idx = css.indexOf(selector)
    expect(idx, `${selector} rule missing from index.css`).toBeGreaterThan(-1)
    return css.slice(idx, css.indexOf('}', idx) + 1)
  }

  it('.iter-block declares layout+paint containment so invalidation stays inside the block', () => {
    const block = ruleBlock('.iter-block {')
    expect(block).toContain('contain: layout paint')
  })

  it('.iter-block skips off-screen rendering (content-visibility + intrinsic size)', () => {
    const block = ruleBlock('.iter-block {')
    expect(block).toContain('content-visibility: auto')
    // Remembered size first, conservative fallback second: never underestimate.
    expect(block).toContain('contain-intrinsic-size: auto 320px')
  })

  it('.iter-block-live stays always-rendered (typewriter/shimmer mutate it every frame)', () => {
    const block = ruleBlock('.iter-block-live {')
    expect(block).toContain('content-visibility: visible')
  })

  it('.virt-row keeps live-row growth from relayouting the whole list', () => {
    const block = ruleBlock('.virt-row {')
    expect(block).toContain('contain: layout')
  })
})
