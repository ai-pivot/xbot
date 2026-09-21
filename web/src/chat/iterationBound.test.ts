import { describe, expect, it } from 'vitest'
import { normalizeEvent } from './normalize'

/**
 * ⛔ P0 不变量（用户 2026-09-21 定稿）：「不能有任何 gap，任何 gap 都是破坏线性一致性」。
 *
 * 客户端**不许**对迭代历史做任何截断。历史教训：2026-09-17 为了躲开"服务端旧二进制 /
 * 缓存 / 其它端点"下发 1964 个迭代的卡顿，客户端也在消费处截了尾部（SNAPSHOT_ITERATION_LIMIT=60）
 * —— 客户端与服务端各截一次会制造**两个不相邻的窗口** ⇒ 合并出 gap ⇒ 渲染只能在 gap 处
 * 截断 ⇒ 用户看到「历史停在旧位置 / 中间很多迭代不见 / 新迭代出现即消失」。
 *
 * 体积与渲染性能由渲染层解决（TurnBody 迭代级窗口化），不得丢数据。
 * 判别力：任何地方重新加回客户端截断 ⇒ 本例必红。
 */
function iter(n: number) {
  return { iteration: n, content: `c${n}`, reasoning: '', tools: [] }
}

describe('迭代历史：客户端不得截断（gap-free 不变量）', () => {
  it('结构化事件携带 N 个迭代 ⇒ 全部保留（N=200，远超曾经的上限 60）', () => {
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

  it('phase_done 携带 N 个迭代 ⇒ 最后一个迭代号就是真实的最新迭代号（没有被尾部截断顶替）', () => {
    const its = Array.from({ length: 200 }, (_, i) => iter(i + 1))
    const evs = normalizeEvent(
      {
        type: 'progress_structured',
        chat_id: 'web:chat-1',
        progress: { turn_id: 1, phase: 'done', iteration: 200, iteration_history: its },
      },
      'chat-1',
    )
    expect(evs).not.toBeNull()
    const done = (evs ?? []).find((e) => e.type === 'phase_done')
    expect(done).toBeDefined()
    if (done && done.type === 'phase_done') {
      expect(done.finalIteration?.iteration).toBe(200)
    }
  })
})
