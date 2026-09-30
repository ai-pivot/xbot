import { describe, expect, it } from 'vitest'
import {
  assertIterationContinuity,
  continuousIterations,
  hasIterationGap,
} from '@/components/agent/progressStore'
import { normalizeEvent } from './normalize'

/**
 * ⛔ 不变量（用户 2026-09-21 定稿）：「**不能有任何 gap，任何 gap 都是破坏线性一致性**」。
 *
 * 客户端**不许**对迭代历史做任何截断。历史教训（commit `1d6e98ff`，2026-09-17）：为了躲开
 * "服务端旧二进制 / 历史缓存 / 其它端点"下发 1964 个迭代的卡顿，客户端在消费处又截了一次
 * 尾部（`SNAPSHOT_ITERATION_LIMIT = 60`）—— 与服务端的 60（`BoundHistoryIterations`）叠加
 * 会制造**两个不相邻的窗口** ⇒ 拼接出 gap ⇒ 渲染层只能在 gap 处截断 ⇒ 用户看到「历史停在旧
 * 位置 / 中间很多迭代不见」，而且**取不回来**（全 history 搜索 `before_iteration` 零命中）。
 *
 * 体积与渲染性能由**渲染层**解决（TurnBody 迭代级窗口化），不得丢数据。
 * 判别力：任何地方重新加回客户端截断 ⇒ 本例必红。
 */
function iter(n: number) {
  return { iteration: n, content: `c${n}`, reasoning: '', tools: [], toolCount: 0 }
}

describe('迭代历史：客户端不得截断（gap-free 不变量）', () => {
  it('结构化事件携带 200 个迭代 ⇒ 全部保留（远超曾经的 60 上限）', () => {
    const its = Array.from({ length: 200 }, (_, i) => iter(i + 1))
    const evs = normalizeEvent(
      {
        type: 'progress_structured',
        chat_id: 'web:chat-1',
        progress: { turn_id: 1, phase: 'tool_exec', iteration: 200, iteration_history: its },
      },
      'chat-1',
    )
    expect(evs).not.toBeNull()
    const total = (evs ?? []).reduce((n, e) => {
      const delta = 'iterationsDelta' in e && Array.isArray(e.iterationsDelta) ? e.iterationsDelta.length : 0
      return n + delta
    }, 0)
    expect(total).toBe(200)
  })
})

/**
 * 演进（方案 `docs/plan-history-fold-windowing.md` §2.3 / T2）：
 * **窗口任意起点合法** + **轻字段迭代参与 union 不产生新洞**。
 *
 * 区域窗口（D2）下发的迭代序列是**尾部区间**（如 52..66），不含 1..51 ——
 * 渲染层的连续性判据（`progressStore.continuousIterations` / `hasIterationGap` /
 * `assertIterationContinuity`）只看序列**内部**断号，从任意起点开始都合法
 * （既有注释原文："does NOT require iteration 1 … A contiguous sequence starting
 * at any number (e.g. 12→13→14) is valid"）。轻字段化（`tools_folded`，D3）只动
 * 工具详情载荷，**不动迭代号** ⇒ 不产生新洞。
 *
 * 判别力：任何地方重新加回「必须从 1 开始 / 截断窗口」的判据 ⇒ 本例必红。
 */
describe('窗口任意起点合法（区域窗口 52..66）+ 轻字段不产生新洞', () => {
  it('任意起点连续序列（12→13→14）全部保留（渲染层不要求从 1 开始）', () => {
    const its = [12, 13, 14].map(iter)
    expect(continuousIterations(its)).toHaveLength(3)
    expect(hasIterationGap(its)).toBe(false)
    expect(assertIterationContinuity(its)).toBe(false)
  })

  it('区域窗口 52..66（尾部区间）：内部连续 ⇒ 全渲染、无 gap', () => {
    const its = Array.from({ length: 15 }, (_, i) => iter(52 + i))
    expect(continuousIterations(its)).toHaveLength(15)
    expect(hasIterationGap(its)).toBe(false)
    expect(assertIterationContinuity(its)).toBe(false)
  })

  it('轻字段（tools_folded）迭代与完整迭代在连续性判据下等价 —— 不产生新洞', () => {
    // 轻字段形态（详情省略）—— 迭代号与完整形态完全一致（D3 只瘦身载荷）。
    const folded = Array.from({ length: 15 }, (_, i) => ({ ...iter(52 + i), toolsFolded: true }))
    expect(hasIterationGap(folded)).toBe(false)
    expect(continuousIterations(folded)).toHaveLength(15)
    // union 产物形态：同号「轻 ∪ 完整」混排（浮层 hydrate 后）仍连续。
    const mixed = [...folded.slice(0, 7), ...Array.from({ length: 8 }, (_, i) => ({ ...iter(59 + i), toolsFolded: false }))]
    expect(hasIterationGap(mixed)).toBe(false)
    expect(continuousIterations(mixed)).toHaveLength(15)
  })
})
