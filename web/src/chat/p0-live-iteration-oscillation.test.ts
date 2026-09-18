/**
 * P0 线性一致性回归（2026-09-18 用户报告）：
 * 「web 切换 session 后：新的迭代不断出现然后消失。稳定的历史迭代一直不变，只有
 *   live iter 不断出现消失。刷新后恢复正常。」
 *
 * 机制（`chat/reduce.ts`）：turn 结尾信号族（`session(idle)` / `session_idle`）会把
 * live turn **冻结**并清 `activeTurn`。冻结后，该 turn 的后续 `iteration` 事件因
 * `phase.kind !== 'live'` 被**整批丢弃** —— 直到某次带 `active` 快照的
 * `history_replaced` 把它升级回 live ⇒ 出现；再次冻结 ⇒ 消失……
 *
 * 后端的顺序保证：一个 turn 的迭代事件只会出现在它的 idle 之前，idle 之后绝不会再有。
 * 因此「冻结之后收到**更大迭代号**」本身就证明那个 idle 是陈旧/误传的 —— 状态机必须
 * 解冻（与 committed 遮蔽解除同一规则、同一 `ev.iter > maxIter` 证据标准），而不是
 * 把事件整批丢弃（数据永远丢失，直到快照/刷新兜底 = 线性不一致）。
 *
 * 本测试断言：对运行中的 turn 任意交错 [iteration / session(idle) / history_replaced]，
 * `liveProgressFromState(...).iterationHistory` 必须**单调不回退**，且迟到 idle 之后
 * 的新迭代必须恢复 live 更新。
 */
import { describe, expect, it } from 'vitest'

import { liveProgressFromState } from './integrate'
import { reduce } from './reduce'
import { eventSeq, initialChatState, iterNum, turnID } from './types'
import type { DomainEvent } from './types'
import type { WebIteration } from '@/types/shared'

const T = turnID(9)

const mkIter = (n: number, c: string): WebIteration => ({
  iteration: n,
  content: c,
  reasoning: '',
  tools: [],
  toolCount: 0,
})

const evTurnStarted = (): DomainEvent => ({
  type: 'turn_started',
  turnID: T,
  requestID: 'r1',
  trigger: 'user',
  content: 'running task',
})

const evIteration = (seq: number, iter: number, content: string): DomainEvent => ({
  type: 'iteration',
  turnID: T,
  phase: 'tool_exec',
  iter: iterNum(iter),
  seq: eventSeq(seq),
  content,
  reasoning: '',
  activeTools: [],
  completedTools: [],
  iterationsDelta: [mkIter(iter, content)],
  todos: undefined,
  goal: undefined,
  subAgents: undefined,
  tokenUsage: undefined,
  streamStats: undefined,
})

const evIdle = (): DomainEvent => ({ type: 'session', busy: false })

/** 历史对账（reload 后）：DB 已落库到 toIter，active 快照声明 turn 仍在跑。 */
const evHistory = (activeIter: number): DomainEvent => ({
  type: 'history_replaced',
  turns: [
    {
      id: T,
      user: {
        id: 'db-u9',
        content: 'running task' as never,
        timestamp: 't',
        isNotification: false,
        queued: false,
        sending: false,
        requestID: 'r1',
        turnHint: undefined,
        dbID: 900,
      },
      phase: {
        kind: 'committed',
        payload: {
          via: 'fold',
          content: '',
          iterations: [mkIter(1, 'iter1'), mkIter(2, 'iter2')],
        },
      },
      requestID: 'r1',
    },
  ],
  legacy: [],
  lastSeq: null,
  active: {
    turnID: T,
    snapshot: {
      iter: iterNum(activeIter),
      content: '',
      reasoning: '',
      streaming: true,
      activeTools: [],
      streamingTools: [],
      iterations: Array.from({ length: activeIter }, (_, i) => mkIter(i + 1, `iter${i + 1}`)),
      genui: '',
      todos: [],
      subAgents: [],
      tokenUsage: null,
      streamStats: null,
    },
  },
  todos: [],
})

const liveIters = (state: ReturnType<typeof reduce>): number[] =>
  liveProgressFromState(state).iterationHistory.map((it) => it.iteration)

describe('P0 线性一致性：迟到 idle 冻结不得吞掉运行中 turn 的新迭代', () => {
  it('session(idle) 冻结后，更大迭代号的事件必须解冻并继续 live（不得丢弃）', () => {
    let s = reduce(initialChatState('web:chatX'), evTurnStarted())
    s = reduce(s, evIteration(2, 1, 'one'))
    s = reduce(s, evIteration(3, 2, 'two'))
    s = reduce(s, evIteration(4, 3, 'three'))
    expect(liveIters(s)).toEqual([1, 2, 3])

    // 迟到/伪 idle（restoreActiveProgress 竞态 / SSE 重放）：turn 实际仍在跑。
    s = reduce(s, evIdle())
    expect(s.activeTurn).toBeNull() // idle 语义保持（收尾兜底）

    // ★ 新迭代到达 —— 后端不会对已结束的 turn 发迭代 ⇒ 这个事件证明 idle 是陈旧的。
    s = reduce(s, evIteration(5, 4, 'four'))

    // 解冻恢复 live：迭代 [1,2,3,4] 全在，live 更新继续。
    const p1 = liveProgressFromState(s)
    expect(s.activeTurn).toBe(T)
    expect(p1.iterationHistory.map((i) => i.iteration)).toEqual([1, 2, 3, 4])

    s = reduce(s, evIteration(6, 5, 'five'))
    expect(liveIters(s)).toEqual([1, 2, 3, 4, 5])
  })

  it('冻结 + history 对账反复交错，迭代列表单调不回退（用户看到的"出现/消失"）', () => {
    let s = reduce(initialChatState('web:chatX'), evTurnStarted())
    s = reduce(s, evIteration(2, 1, 'one'))
    s = reduce(s, evIteration(3, 2, 'two'))

    let prevIters = liveIters(s)
    let seq = 10
    // 多轮「冻结 → 对账复活 → 新迭代」，模拟用户报告的持续振荡。
    for (let round = 0; round < 4; round++) {
      s = reduce(s, evIdle())
      s = reduce(s, evHistory(2 + round))
      s = reduce(s, evIteration(seq++, 3 + round, `iter${3 + round}`))

      const iters = liveIters(s)
      // 线性一致性：迭代列表只增不减（monotonic）。
      expect(
        iters.length,
        `round ${round}: 迭代列表回退（${prevIters.length} → ${iters.length}）`,
      ).toBeGreaterThanOrEqual(prevIters.length)
      for (let i = 0; i < Math.min(prevIters.length, iters.length); i++) {
        expect(iters[i], `round ${round}: 第 ${i} 个迭代回退`).toBe(prevIters[i])
      }
      prevIters = iters
    }
    // 收尾：turn 必须仍处于 live 且迭代完整（1..6）。
    expect(prevIters).toEqual([1, 2, 3, 4, 5, 6])
  })
})
