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

/** 流式事件（后端 stamp `iteration` —— 进行中迭代号）。 */
const evStream = (
  seq: number,
  iter: number,
  content: string | undefined,
  reasoning = '',
): DomainEvent => ({
  type: 'stream',
  turnID: T,
  seq: eventSeq(seq),
  iteration: iterNum(iter),
  content,
  reasoning,
  streamingTools: [],
  genui: '',
  streamStats: undefined,
})

/** DB 中间快照组成的 committed（无 active 快照）—— 运行中被折成 committed 的形态。 */
const evHistoryNoActive = (): DomainEvent => ({
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
          content: 'C',
          iterations: [mkIter(1, 'iter1'), mkIter(2, 'iter2')],
        },
      },
      requestID: 'r1',
    },
  ],
  legacy: [],
  lastSeq: null,
  active: null,
  todos: [],
})

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

// ─── 对称性：遮盖解除对 stream 事件必须同样成立 ──────────────────────────────
// 用户 2026-09-19 P0（手机熄屏解锁后 busy 会话 live 进度消失且**永远不再更新**）：
// `iteration` case 有 frozen/committed 遮蔽解除，`stream` case **没有** —— 非空壳
// frozen turn 的 `stream` 事件被整批 `return s` 丢弃。而 LLM 生成期（reasoning/
// content 流式）**只有** stream 事件（结构化事件只在迭代边界/工具状态变化时发），
// ⇒ 冻结后 live 进度永不回来（用户看到的"live 进度消失且永远不再更新"）。
// 证据标准与 `iteration` case 完全一致：`ev.iteration > maxIter`（后端绝不会对已
// 结束的 turn 发新迭代；进行中迭代号必然大于已落库的最大迭代号）。
describe('P0 对称性：冻结 / committed 的遮蔽解除对 stream 事件必须同样成立', () => {
  it('冻结后：进行中迭代的 stream 事件必须解冻并继续流式更新（不得整批丢弃）', () => {
    let s = reduce(initialChatState('web:chatX'), evTurnStarted())
    s = reduce(s, evIteration(2, 1, 'one'))
    s = reduce(s, evIteration(3, 2, 'two'))
    // 迭代 3 开始流式（后端 stamp iteration=3；DB iteration_history 只到 2）。
    s = reduce(s, evStream(4, 3, 'stream A'))
    expect(s.activeTurn).toBe(T)

    // 迟到/误传 idle（restoreActiveProgress 竞态 / SSE 重放）冻结了运行中的 turn。
    s = reduce(s, evIdle())
    expect(s.activeTurn).toBeNull()
    expect(s.turns.get(T)?.phase.kind).toBe('frozen')

    // ★ 进行中迭代的流式内容继续到达 —— 后端只对运行中的 turn 发流式事件。
    s = reduce(s, evStream(5, 3, 'stream B'))
    expect(s.activeTurn, 'stream 事件必须解冻（否则 live 进度永远不再更新）').toBe(T)
    const t = s.turns.get(T)
    if (t?.phase.kind !== 'live') throw new Error('frozen turn must be revived by an in-flight stream event')
    expect(t.phase.data.content).toBe('stream B')
    // 冻结前已渲染的迭代一个不少。
    expect(t.phase.data.iterations.map((i) => i.iteration)).toEqual([1, 2])

    // 后续流式继续更新（不是一次性的）。
    s = reduce(s, evStream(6, 3, 'stream C'))
    const t2 = s.turns.get(T)
    if (t2?.phase.kind !== 'live') throw new Error('live')
    expect(t2.phase.data.content).toBe('stream C')
  })

  it('committed + 只带 reasoning 的 stream：迭代号不得回退到 1（否则"迭代前进"会把已恢复内容清空）', () => {
    // DB 中间快照组成的 committed（迭代 1..2，content='C'），turn 仍在跑。
    let s = reduce(initialChatState('web:chatX'), evHistoryNoActive())
    s = reduce(s, evStream(10, 3, undefined, 'reasoning of iter 3'))
    expect(s.activeTurn).toBe(T)
    const t = s.turns.get(T)
    if (t?.phase.kind !== 'live') throw new Error('committed turn must be upgraded by a stream event')
    // 迭代号 = 进行中的 3（修复前是 EMPTY_LIVE.iter=1 ⇒ 下一帧被判"迭代前进"并清空）。
    expect(t.phase.data.iter).toBe(3)
    // 已恢复的 content 不得被清空（ev.content 为 undefined ⇒ 保留 prev）。
    expect(t.phase.data.content).toBe('C')
    expect(t.phase.data.reasoning).toBe('reasoning of iter 3')
  })
})
