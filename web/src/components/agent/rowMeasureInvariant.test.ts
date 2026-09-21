import { describe, expect, it } from 'vitest'
import { noDegenerateMeasureElement } from './MessageList'

/**
 * P0 不变式（2026-09-18）：**行高永远不允许是 0**。
 *
 * 行是 `position:absolute + translateY(start)`；某行 size=0 ⇒ 下一行与它共享 start
 * ⇒ 两层内容画在同一位置（用户报「偶发消息正文互相穿插」）。旧实现在
 * `el.isConnected && el.offsetParent !== null`（= 看起来可见）时**直接把 0 当测量结果返回**，
 * 这正是那条口子 —— 下面第一条用例在旧实现下会得到 0（红灯），修复后得到记住的高度。
 */
const makeEl = (index: number, opts: { connected?: boolean; layoutable?: boolean; rectH?: number } = {}) =>
  ({
    dataset: { index: String(index) },
    isConnected: opts.connected ?? true,
    // 非 null = 有布局（旧实现据此认为"可见 ⇒ 0 可信"）
    offsetParent: (opts.layoutable ?? true) ? {} : null,
    // ⚠️ TanStack 的默认 measureElement 读的是 element.offsetHeight/offsetWidth
    // （不是 getBoundingClientRect），桩必须给全，否则"真实测量"这条分支测不到。
    offsetHeight: opts.rectH ?? 0,
    offsetWidth: 0,
    getBoundingClientRect: () => ({ height: opts.rectH ?? 0, width: 0, top: 0, bottom: 0, left: 0, right: 0 }),
  }) as unknown as HTMLElement

const makeInstance = (opts: { remembered?: number; estimate?: number } = {}) => {
  const size = opts.remembered ?? 0
  return {
    options: { horizontal: false, estimateSize: () => opts.estimate ?? 0 },
    measurementsCache: [{ key: 'k0', size }],
    itemSizeCache: new Map<unknown, number>([['k0', size]]),
  } as unknown as Parameters<typeof noDegenerateMeasureElement>[2]
}

describe('noDegenerateMeasureElement — 行高永不为 0（防两层文字重叠）', () => {
  it('可见元素量到 0：不得返回 0，退回记住的实测高度', () => {
    const got = noDegenerateMeasureElement(makeEl(0), undefined, makeInstance({ remembered: 500 }))
    expect(got).toBe(500)
  })

  it('无历史高度：退回该行估算高度', () => {
    const got = noDegenerateMeasureElement(makeEl(0), undefined, makeInstance({ estimate: 120 }))
    expect(got).toBe(120)
  })

  it('既无历史也无估算：至少 1px 占位（绝不 0）', () => {
    const got = noDegenerateMeasureElement(makeEl(0), undefined, makeInstance({}))
    expect(got).toBeGreaterThan(0)
    expect(got).toBe(1)
  })

  it('隐藏元素（offsetParent=null）同样不得返回 0', () => {
    const got = noDegenerateMeasureElement(makeEl(0, { layoutable: false }), undefined, makeInstance({ remembered: 300 }))
    expect(got).toBe(300)
  })

  it('真实测量 > 0 时原样返回（不干扰正常路径）', () => {
    const got = noDegenerateMeasureElement(makeEl(0, { rectH: 432 }), undefined, makeInstance({ remembered: 500 }))
    expect(got).toBe(432)
  })
})
