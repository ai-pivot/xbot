/**
 * iterationHeight 单元守护（迭代级窗口化的地基）。
 *
 * 契约（两次真实事故后确立）：
 *   1. **实例作用域**（2026-09-13「切换 session 后出现空 tool iter」）：key 只能是
 *      `turnID:iteration`，而 turnID 每会话独立 —— 模块级缓存会让两个会话的同一 key
 *      撞车 → 新会话的块读到旧会话的"已结算高度" → 立刻冻结 → **空块**。
 *      ⇒ 每个 tracker 实例互相隔离，生存期 = 该行这次挂载。
 *   2. **瞬态测量不得结算**：单次测量（哪怕 26.66px）永不 settled；同值连续两次、
 *      间隔 ≥ SETTLE_MS 才算；只有 settled 才允许冻结内容。
 *   3. **高度变化立即解冻**（值变了必须重新稳定）。
 *   4. 估算只用于「从未渲染过」的块显示占位，且随内容单调增长；非法值忽略。
 */
import { beforeEach, describe, expect, it } from 'vitest'

import {
  ITERATION_HEIGHT_SETTLE_MS,
  createIterationHeightTracker,
  estimateIterationHeight,
  iterationHeightKey,
  type IterationHeightTracker,
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

describe('IterationHeightTracker（实例作用域 + settle 语义）', () => {
  let t: IterationHeightTracker
  const key = iterationHeightKey(1084, 810)

  beforeEach(() => {
    t = createIterationHeightTracker()
  })

  it('⛔ 单次瞬态测量永不结算（内容因此不会被错误高度冻结）', () => {
    const first = t.record(key, 26.6562, 1000)
    expect(first.changed).toBe(true)
    expect(first.settled).toBe(false)
    expect(t.isSettled(key)).toBe(false)
    // 时间过去但只测过一次 → 仍不结算
    expect(t.record(key, 26.6562, 1000 + ITERATION_HEIGHT_SETTLE_MS * 5).settled).toBe(true)
  })

  it('同值两次且间隔 ≥ SETTLE_MS 才结算（这才是可信高度）', () => {
    t.record(key, 812, 1000)
    expect(t.record(key, 812, 1000 + ITERATION_HEIGHT_SETTLE_MS - 1).settled).toBe(false)
    expect(t.record(key, 812.5, 1000 + ITERATION_HEIGHT_SETTLE_MS).settled).toBe(true)
    expect(t.isSettled(key)).toBe(true)
  })

  it('⚠️ 高度变化立即解冻（现场：先 26px 后变高）', () => {
    t.record(key, 26.6562, 1000)
    t.record(key, 26.6562, 1000 + ITERATION_HEIGHT_SETTLE_MS)
    expect(t.isSettled(key)).toBe(true)
    const res = t.record(key, 1434, 2000)
    expect(res.changed).toBe(true)
    expect(res.settled).toBe(false)
    expect(t.get(key)).toBe(1434)
  })

  it('显式解冻后必须重新稳定，不得立即再冻结', () => {
    t.record(key, 300, 0)
    t.record(key, 300, ITERATION_HEIGHT_SETTLE_MS)
    t.unsettle(key, 400)
    expect(t.isSettled(key)).toBe(false)
    expect(t.record(key, 300, 401).settled).toBe(false)
    expect(t.record(key, 300, 401 + ITERATION_HEIGHT_SETTLE_MS).settled).toBe(true)
  })

  it('±2px 内视为同值；超过即变化（必须重新稳定）', () => {
    t.record(key, 300, 0)
    expect(t.record(key, 301.5, ITERATION_HEIGHT_SETTLE_MS).settled).toBe(true)
    const t2 = createIterationHeightTracker()
    t2.record(key, 300, 0)
    expect(t2.record(key, 303, ITERATION_HEIGHT_SETTLE_MS).changed).toBe(true)
    expect(t2.isSettled(key)).toBe(false)
  })

  it('非法高度（0/负数/NaN/Infinity）被忽略', () => {
    for (const bad of [0, -5, Number.NaN, Number.POSITIVE_INFINITY]) {
      expect(t.record(key, bad, 0).changed).toBe(false)
    }
    expect(t.get(key)).toBeUndefined()
    expect(t.isSettled(key)).toBe(false)
  })

  it('⛔ 实例之间完全隔离（切换 session 的 key 撞车不会再冻结别人的高度）', () => {
    const sessionA = createIterationHeightTracker()
    sessionA.record(key, 1434, 0)
    sessionA.record(key, 1434, ITERATION_HEIGHT_SETTLE_MS)
    expect(sessionA.isSettled(key)).toBe(true)

    // 会话 B 的同名 key（turnID/iteration 相同但完全无关）必须"从未测量"
    const sessionB = createIterationHeightTracker()
    expect(sessionB.get(key)).toBeUndefined()
    expect(sessionB.isSettled(key)).toBe(false)
    // B 自己量到的才是 B 的
    sessionB.record(key, 320, 0)
    expect(sessionB.get(key)).toBe(320)
    expect(sessionA.get(key)).toBe(1434)
  })

  it('不同 turn / iteration 互不串味', () => {
    t.record(iterationHeightKey(1, 1), 100, 0)
    t.record(iterationHeightKey(2, 1), 200, 0)
    expect(t.get(iterationHeightKey(1, 1))).toBe(100)
    expect(t.get(iterationHeightKey(2, 1))).toBe(200)
  })
})
