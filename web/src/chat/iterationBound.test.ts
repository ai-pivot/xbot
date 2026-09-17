import { describe, expect, it } from 'vitest'
import { boundIterationTail, SNAPSHOT_ITERATION_LIMIT } from './normalize'

/**
 * 2026-09-17 我自己的 Playwright + CDP trace 实测：切到 chat_07B68B101679 时
 * `/api/history` 的 active_progress 带了 **1964 个迭代（11.2MB）**，浏览器物化
 * 1961 个 `.iter-block` / **50,633 DOM 节点** ⇒ switchMs 7175ms、长任务 2997ms、
 * rAF 卡 3104ms。
 *
 * 契约：**在消费处截尾**（保留最新 N 个），让「服务端还是旧二进制 / 其它端点」
 * 都不可能把浏览器拖垮。短列表必须原样返回（零拷贝语义：同一引用）。
 */
describe('boundIterationTail（切会话性能护栏）', () => {
  it('1964 个迭代 ⇒ 只保留尾部 60，且保留最新（最后一个）迭代', () => {
    const big = Array.from({ length: 1964 }, (_, i) => ({ iteration: i + 1 }))
    const out = boundIterationTail(big)
    expect(out).toHaveLength(SNAPSHOT_ITERATION_LIMIT)
    expect(out[0].iteration).toBe(1964 - SNAPSHOT_ITERATION_LIMIT + 1) // 1905
    expect(out[out.length - 1].iteration).toBe(1964) // 最新保留
  })

  it('不超过上限 ⇒ 原样返回（同一引用，不做无谓拷贝）', () => {
    const small = Array.from({ length: 12 }, (_, i) => ({ iteration: i + 1 }))
    const out = boundIterationTail(small)
    expect(out).toBe(small)
  })

  it('恰好等于上限 ⇒ 不截断', () => {
    const exact = Array.from({ length: SNAPSHOT_ITERATION_LIMIT }, (_, i) => ({ iteration: i + 1 }))
    expect(boundIterationTail(exact)).toHaveLength(SNAPSHOT_ITERATION_LIMIT)
  })

  it('limit 可覆盖（后端策略变化时的旋钮）', () => {
    const big = Array.from({ length: 100 }, (_, i) => i)
    expect(boundIterationTail(big, 10)).toEqual(big.slice(90))
  })
})
