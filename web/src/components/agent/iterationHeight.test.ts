/**
 * iterationHeight 单元守护（迭代级窗口化的地基）。
 *
 * 契约（2026-09-13 首版窗口化事故后确立）：
 *   1. **瞬态测量不得结算**：单次测量（哪怕值是 26.66px）永远不 settled ——
 *      只有同值连续两次、间隔 ≥ ITERATION_HEIGHT_SETTLE_MS 才 settled；
 *      只有 settled 的高度才允许冻结内容（否则用户现场那种
 *      `data-window-muted="true" style="height: 26.6562px"` 空块会出现：
 *      内容被错误的过小高度卸载后再无测量机会 → 永久消失）；
 *   2. **高度变化立即解冻**：变高/变矮 → settled 清除 → 必须重新稳定；
 *   3. 估算只用于「从未渲染过」的块显示占位，且必须随内容单调增长；
 *   4. 非法高度（0/负数/NaN）被忽略。
 */
import { beforeEach, describe, expect, it } from 'vitest'

import {
  ITERATION_HEIGHT_SETTLE_MS,
  clearIterationHeightCache,
  estimateIterationHeight,
  getCachedIterationHeight,
  isIterationHeightSettled,
  iterationHeightKey,
  recordIterationHeight,
  unsettleIterationHeight,
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

describe('recordIterationHeight / settle 语义', () => {
  beforeEach(() => clearIterationHeightCache())
  const key = iterationHeightKey(1084, 810)

  it('⛔ 单次瞬态测量永远不结算（内容因此不会被错误高度冻结）', () => {
    const first = recordIterationHeight(key, 26.6562, 1000)
    expect(first.changed).toBe(true)
    expect(first.settled).toBe(false)
    expect(isIterationHeightSettled(key)).toBe(false)
    // 时间过去了但只测过一次 → 仍不结算
    expect(recordIterationHeight(key, 26.6562, 1000 + ITERATION_HEIGHT_SETTLE_MS * 5).settled).toBe(true)
  })

  it('同值两次且间隔 ≥ SETTLE_MS 才结算（这才是可信高度）', () => {
    recordIterationHeight(key, 812, 1000)
    expect(recordIterationHeight(key, 812, 1000 + ITERATION_HEIGHT_SETTLE_MS - 1).settled).toBe(false)
    expect(recordIterationHeight(key, 812.5, 1000 + ITERATION_HEIGHT_SETTLE_MS).settled).toBe(true)
    expect(isIterationHeightSettled(key)).toBe(true)
  })

  it('⚠️ 高度变化立即解冻（用户现场：先 26px 后变高）', () => {
    recordIterationHeight(key, 26.6562, 1000)
    recordIterationHeight(key, 26.6562, 1000 + ITERATION_HEIGHT_SETTLE_MS) // settled
    expect(isIterationHeightSettled(key)).toBe(true)
    const changed = recordIterationHeight(key, 812, 2000) // 内容定形后真实高度
    expect(changed.changed).toBe(true)
    expect(changed.settled).toBe(false)
    expect(isIterationHeightSettled(key)).toBe(false)
    expect(getCachedIterationHeight(key)).toBe(812)
  })

  it('解冻后必须重新稳定，不得立即再冻结', () => {
    recordIterationHeight(key, 300, 0)
    recordIterationHeight(key, 300, ITERATION_HEIGHT_SETTLE_MS)
    unsettleIterationHeight(key, 400)
    expect(isIterationHeightSettled(key)).toBe(false)
    expect(recordIterationHeight(key, 300, 401).settled).toBe(false)
    expect(recordIterationHeight(key, 300, 401 + ITERATION_HEIGHT_SETTLE_MS).settled).toBe(true)
  })

  it('±2px 内视为同值（子像素抖动不阻止结算）', () => {
    recordIterationHeight(key, 300, 0)
    expect(recordIterationHeight(key, 301.5, ITERATION_HEIGHT_SETTLE_MS).settled).toBe(true)
  })

  it('超过 ±2px 视为变化（必须重新稳定）', () => {
    recordIterationHeight(key, 300, 0)
    expect(recordIterationHeight(key, 303, ITERATION_HEIGHT_SETTLE_MS).changed).toBe(true)
    expect(isIterationHeightSettled(key)).toBe(false)
  })

  it('非法高度（0/负数/NaN/Infinity）被忽略', () => {
    expect(recordIterationHeight(key, 0, 0).changed).toBe(false)
    expect(recordIterationHeight(key, -5, 0).changed).toBe(false)
    expect(recordIterationHeight(key, Number.NaN, 0).changed).toBe(false)
    expect(recordIterationHeight(key, Number.POSITIVE_INFINITY, 0).changed).toBe(false)
    expect(getCachedIterationHeight(key)).toBeUndefined()
    expect(isIterationHeightSettled(key)).toBe(false)
  })

  it('不同 turn / iteration 互不串味', () => {
    recordIterationHeight(iterationHeightKey(1, 1), 100, 0)
    recordIterationHeight(iterationHeightKey(2, 1), 200, 0)
    expect(getCachedIterationHeight(iterationHeightKey(1, 1))).toBe(100)
    expect(getCachedIterationHeight(iterationHeightKey(2, 1))).toBe(200)
  })
})
