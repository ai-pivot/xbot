/**
 * PERF-4：脏帧的决策重算必须只落在**受影响的 chunk** 上，而不是整个 turn。
 *
 * 用户问题（2026-09-18）："高度估算…不能彻底避免吗？这种 async 的高度计算会导致抖动。
 * 我不理解为什么这玩意计算这么慢，理论上只要计算倒数的几十个迭代确保占满可视窗口
 * 不就可以了吗？应该快如闪电吧"
 *
 * 根因：`invalidate()` 被 RO 的**每次尺寸变化**触发，而流式期间尾部块每帧都在长高
 * ⇒ 每帧都是"脏帧"；旧实现的"脏帧"是**整帧**语义 ⇒ 每帧为**全部 N 个迭代**重算决策
 * 并分配 N 长度的 `heights` 数组 ⇒ 代价 ∝ turn 总长（"turn 越长越慢"）。
 *
 * 判别力：修复前（整帧脏）一次尾部高度变化 → 重算 N 个块；修复后 → ≤ 一个 chunk。
 */
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { act } from '@testing-library/react'
import '@testing-library/jest-dom'

import { TurnBody, __turnBodyDecisionCompute } from '@/components/agent/TurnBody'
import { __resetSharedIterationHeightTrackers } from '@/components/agent/iterationHeight'
import { renderWithProviders } from '@/test-utils'
import type { WebIteration } from '@/types/shared'

const roCallbacks: ResizeObserverCallback[] = []

class FakeResizeObserver {
  constructor(cb: ResizeObserverCallback) {
    roCallbacks.push(cb)
  }
  observe() {}
  unobserve() {}
  disconnect() {}
}

class FakeIntersectionObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
  takeRecords(): IntersectionObserverEntry[] {
    return []
  }
}

const N = 200

function iterations(): WebIteration[] {
  return Array.from({ length: N }, (_, i) => ({
    iteration: i + 1,
    content: `iteration ${i + 1} body`,
    reasoning: '',
    tools: [],
    toolCount: 0,
  }))
}

beforeEach(() => {
  roCallbacks.length = 0
  ;(globalThis as unknown as Record<string, unknown>).ResizeObserver = FakeResizeObserver
  ;(globalThis as unknown as Record<string, unknown>).IntersectionObserver = FakeIntersectionObserver
  // jsdom 没有布局引擎：让 isLayoutable 认为元素有渲染盒（否则 RO 路径被整段忽略，
  // 测的就不是决策重算了）。
  Object.defineProperty(HTMLElement.prototype, 'offsetParent', {
    configurable: true,
    get() {
      return document.body
    },
  })
  __resetSharedIterationHeightTrackers()
})

afterEach(() => {
  delete (globalThis as unknown as Record<string, unknown>).ResizeObserver
  delete (globalThis as unknown as Record<string, unknown>).IntersectionObserver
})

describe('PERF-4: 脏帧只重算受影响的 chunk（与 turn 总迭代数无关）', () => {
  it('一次尾部高度变化不会重算整个 turn（N=200）', async () => {
    const { container } = renderWithProviders(
      <TurnBody iterations={iterations()} turnID={42} heightScope="perf4|800" />,
    )
    const last = container.querySelector(`[data-iter-id="${N}"]`) as HTMLElement | null
    expect(last).not.toBeNull()

    // 挂载完成后再计数：只观察"尾部一次高度变化"引起的决策重算。
    __turnBodyDecisionCompute.value = 0
    // 触发一次 RO 回调（模拟"尾部块长高"）——必须包在 act 里，否则 React 的更新
    // 不会被 flush（断言会看到 0）。
    await act(async () => {
      for (const cb of roCallbacks) {
        cb(
          [{ target: last, contentRect: { height: 120, width: 800 } } as unknown as ResizeObserverEntry],
          {} as ResizeObserver,
        )
      }
      // ⛔ 脏标记现在**合并到一次 rAF flush**（2026-09-18 trace 8.gz 性能修复：
      // IO/RO 回调逐个同步更新曾是主线程满载的根因）⇒ 必须等一帧再断言，
      // 否则看到的是 flush 前的计数 0（这正是修复前 0 个重算的假象）。
      await new Promise((resolve) => requestAnimationFrame(() => resolve(null)))
    })

    // 修复前：整帧脏 ⇒ 重算 N=200 个块。修复后：只有尾部一个 chunk（≤64）。
    expect(__turnBodyDecisionCompute.value).toBeGreaterThan(0)
    expect(__turnBodyDecisionCompute.value).toBeLessThanOrEqual(64)
  })
})
