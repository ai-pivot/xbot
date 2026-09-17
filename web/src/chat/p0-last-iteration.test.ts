/**
 * P0 回归（2026-09-16 用户报告）：
 *   「一个会话从 busy 跑到 idle 输出完毕后，我从别的会话切过去，可能看不到**最后一个
 *     迭代**的内容，刷新后才出现。」
 *
 * 根因（explore 探查 + 代码实证）：
 *   最后一个迭代的**唯一权威载体**是 `text.progressHistory`（后端 recordFinalIteration
 *   补记 —— 它没有"下一迭代"事件带动，只能靠 text 顶层的 progress_history 带回）与
 *   `phase_done.finalIteration`。
 *   而切会话时面板是 **mounted** 的（只切可见性 → SSE 订阅被移除），本地 turn 可能已按
 *   **过时快照** committed（history_replaced / session(idle) 定格）；SSE 用
 *   last_event_id 回放的那条 text_final/phase_done 一到，就被 reduce 的**幂等短路**
 *   无条件丢弃：
 *     - `chat/reduce.ts` text_final：`if (t.phase.kind === 'committed') return s`
 *     - `chat/reduce.ts` phase_done：`if (target !== s.activeTurn) return …`
 *   ⇒ 唯一载体丢失 ⇒ 最后迭代内容永久缺失，只有下一次完整 fetch（F5 刷新）才补回。
 *
 * 契约（本文件钉死）：
 *   ① 已 committed 的 turn 收到权威 `text_final(progressHistory)` → **并入**最后迭代；
 *   ② 非 active 的 turn 收到权威 `phase_done(finalIteration)` → **并入**最后迭代；
 *   ③ 幂等：无实际变化时返回原 state 引用（零渲染，不引入重放抖动）。
 */

import { describe, expect, it } from 'vitest'
import { historyToReplaced } from './integrate'
import { reduce } from './reduce'
import { initialChatState, turnID, type ChatState, type DomainEvent } from './types'
import type { ChatMessage, WebIteration } from '@/types/shared'

const T9 = turnID(9)

const mkIter = (n: number, content: string): WebIteration =>
  ({ iteration: n, content, reasoning: '', tools: [], toolCount: 0 }) as unknown as WebIteration

const row = (o: Partial<ChatMessage>): ChatMessage =>
  ({
    id: 'db-x',
    role: 'assistant',
    content: '',
    turnID: 0,
    iterations: [],
    timestamp: '',
    isPartial: false,
    persisted: true,
    ...o,
  }) as ChatMessage

/**
 * 现场还原：切会话时 DB 快照先落地 —— 该 turn 已 committed，但**缺最后一个迭代**
 * （因为它只存在于回放的那条 text_final 里）。
 */
function committedWithoutLastIteration(): ChatState {
  return reduce(
    initialChatState('chat-1'),
    historyToReplaced(
      [
        row({ id: 'db-1', role: 'user', content: '跑一下这条', turnID: 9, dbID: 1 }),
        row({
          id: 'db-2',
          role: 'assistant',
          content: '旧回复',
          turnID: 9,
          dbID: 2,
          iterations: [mkIter(1, 'i1'), mkIter(2, 'i2')],
        }),
      ],
      null,
    ),
  )
}

const iterationsOf = (s: ChatState, t: ReturnType<typeof turnID>): readonly WebIteration[] => {
  const ph = s.turns.get(t)?.phase
  if (!ph) return []
  if (ph.kind === 'committed') return ph.payload.iterations ?? []
  return ph.data.iterations ?? []
}

describe('P0: 最后迭代的权威载体不得被幂等短路丢弃', () => {
  it('① 已 committed 的 turn + text_final(progressHistory) → 最后迭代必须并入', () => {
    const s0 = committedWithoutLastIteration()
    expect(s0.turns.get(T9)?.phase.kind).toBe('committed') // 前置：本地已 committed
    expect(iterationsOf(s0, T9)).toHaveLength(2) // 前置：缺最后一个迭代

    const next = reduce(s0, {
      type: 'text_final',
      turnID: T9,
      content: '最终回复',
      progressHistory: [mkIter(1, 'i1'), mkIter(2, 'i2'), mkIter(3, 'i3-final')],
      cancelled: false,
      seq: 99,
    } as unknown as DomainEvent)

    expect(next.turns.get(T9)?.phase.kind).toBe('committed')
    const its = iterationsOf(next, T9)
    expect(its).toHaveLength(3) // 修前 = 2（text_final 短路把 progressHistory 丢了）
    expect(its[its.length - 1].content).toBe('i3-final')
  })

  it('② 非 active（已 committed）的 turn + phase_done(finalIteration) → 最后迭代必须并入', () => {
    const s0 = committedWithoutLastIteration()
    const next = reduce(s0, {
      type: 'phase_done',
      turnID: T9,
      finalIteration: mkIter(3, 'i3-final'),
      todos: undefined,
      goal: undefined,
      seq: 100,
    } as unknown as DomainEvent)

    const its = iterationsOf(next, T9)
    expect(its).toHaveLength(3) // 修前 = 2（phase_done 的 target!==activeTurn 短路）
    expect(its[its.length - 1].content).toBe('i3-final')
  })

  it('③ 幂等：无新增内容的重放返回原 state 引用（零渲染）', () => {
    const s0 = committedWithoutLastIteration()
    const replay = {
      type: 'text_final',
      turnID: T9,
      content: '旧回复',
      progressHistory: [mkIter(1, 'i1'), mkIter(2, 'i2')],
      cancelled: false,
      seq: 99,
    } as unknown as DomainEvent
    expect(reduce(s0, replay)).toBe(s0)
  })
})
