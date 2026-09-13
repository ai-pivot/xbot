/**
 * iterationHeight 单元守护（迭代级窗口化的地基）。
 *
 * 关键契约（2026-09-13「手机上 iter 多了还是很卡」根治）：
 *   1. 高度只能来自「实测缓存」或「内容估算」——**绝不允许常数占位**
 *      （曾用 `contain-intrinsic-size: auto 320px` → 真实块远高于 320px →
 *       向上滚动鬼打墙：总高边滚边涨）；
 *   2. 估算必须随内容量单调增长（±20% 量级），不能退化成常数；
 *   3. 实测缓存跨挂载复用（卸载/重挂载后高度仍精确 → 滚动不漂）。
 */
import { beforeEach, describe, expect, it } from 'vitest'

import {
  clearIterationHeightCache,
  estimateIterationHeight,
  getCachedIterationHeight,
  iterationHeightKey,
  setCachedIterationHeight,
} from '@/components/agent/iterationHeight'
import type { WebIteration } from '@/types/shared'

const iter = (over: Partial<WebIteration>): WebIteration =>
  ({ iteration: 1, content: '', reasoning: '', tools: [], toolCount: 0, ...over }) as WebIteration

describe('estimateIterationHeight', () => {
  it('随内容量单调增长（不是常数占位）', () => {
    const small = estimateIterationHeight(iter({ content: 'x'.repeat(200) }))
    const big = estimateIterationHeight(iter({ content: 'x'.repeat(4000) }))
    expect(big).toBeGreaterThan(small * 2)
  })

  it('随 reasoning / 工具数增长', () => {
    const base = estimateIterationHeight(iter({ content: 'x'.repeat(500) }))
    const withReasoning = estimateIterationHeight(
      iter({ content: 'x'.repeat(500), reasoning: 'y'.repeat(2000) }),
    )
    const withTools = estimateIterationHeight(
      iter({
        content: 'x'.repeat(500),
        tools: Array.from({ length: 8 }, () => ({ name: 'Shell', status: 'done' })) as unknown as WebIteration['tools'],
      }),
    )
    expect(withReasoning).toBeGreaterThan(base)
    expect(withTools).toBeGreaterThan(base)
  })

  it('有下限与上限（极端输入不产生 0 或无穷）', () => {
    expect(estimateIterationHeight(iter({}))).toBeGreaterThanOrEqual(140)
    expect(estimateIterationHeight(iter({ content: 'x'.repeat(500000) }))).toBeLessThanOrEqual(6000)
  })
})

describe('iterationHeightCache', () => {
  beforeEach(() => clearIterationHeightCache())

  it('实测高度可写可读，跨「卸载/重挂载」复用（key 只由 turnID+iteration 决定）', () => {
    const key = iterationHeightKey(7, 3)
    expect(getCachedIterationHeight(key)).toBeUndefined()
    expect(setCachedIterationHeight(key, 812)).toBe(true)
    expect(getCachedIterationHeight(key)).toBe(812)
    // 同 key 再报一次相同高度 → 不算变化（避免无意义重渲染）
    expect(setCachedIterationHeight(key, 812.4)).toBe(false)
  })

  it('±1px 内的抖动不触发更新，超过则更新（滚动时高度必须精确）', () => {
    const key = iterationHeightKey(1, 1)
    setCachedIterationHeight(key, 300)
    expect(setCachedIterationHeight(key, 300.8)).toBe(false)
    expect(setCachedIterationHeight(key, 345)).toBe(true)
    expect(getCachedIterationHeight(key)).toBe(345)
  })

  it('非法高度（0/负数/NaN）被忽略', () => {
    const key = iterationHeightKey(1, 2)
    expect(setCachedIterationHeight(key, 0)).toBe(false)
    expect(setCachedIterationHeight(key, -5)).toBe(false)
    expect(setCachedIterationHeight(key, Number.NaN)).toBe(false)
    expect(getCachedIterationHeight(key)).toBeUndefined()
  })

  it('不同 turn / iteration 互不串味', () => {
    setCachedIterationHeight(iterationHeightKey(1, 1), 100)
    setCachedIterationHeight(iterationHeightKey(2, 1), 200)
    expect(getCachedIterationHeight(iterationHeightKey(1, 1))).toBe(100)
    expect(getCachedIterationHeight(iterationHeightKey(2, 1))).toBe(200)
  })
})
