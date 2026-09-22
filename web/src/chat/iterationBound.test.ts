import { describe, expect, it } from 'vitest'
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
  return { iteration: n, content: `c${n}`, reasoning: '', tools: [] }
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
