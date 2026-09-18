/**
 * P0 回归（2026-09-18 用户报告）：
 * 「手机端 busy 会话的时候熄屏然后再打开，loading 后状态不对，明明是 busy 但是会话
 *   看不到最新进度且也不更新了」
 *
 * 机制（`chat/reduce.ts` 的 `history_replaced` 第 3 步）：
 *   锁屏期间 agent 继续跑，把中间 assistant/tool 行与 iteration_history 增量落库；
 *   解锁后面板 `reloadChat()`（用户看到的那次 loading）拿到 DB 快照 ——
 *   `historyToReplaced` 把**仍在跑的 turn** 折成 `committed`（因为已有迭代）。
 *   而 hydration 恢复 live 的条件只有 `!existing || isHollowFrozen(existing)`：
 *   `committed`（正是"跑了一半"的常态）被原样保留 ⇒ `activeTurn = null`：
 *     · `liveProgressFromState` 返回 EMPTY ⇒ **看不到 live 进度**
 *     · 后续 iteration/stream 事件被「committed 遮蔽」规则丢弃（`ev.iter` 不大于已
 *       落库 maxIter 时 `return s`）⇒ **界面再也不更新**
 *   修复：服务端 `active_progress`（同一份快照，phase ∉ {done,frozen} 才构造）明确
 *   声明该 turn 仍在跑 ⇒ 必须升级回 live（DB 迭代 ∪ 快照迭代，快照同号权威）。
 */
import { describe, expect, it } from 'vitest'

import { reduce } from './reduce'
import {
  commitViaFold,
  EMPTY_LIVE,
  initialChatState,
  iterNum,
  turnID,
  type Turn,
} from './types'
import type { WebIteration } from '@/types/shared'

const T = turnID(9)
const mkIter = (n: number, c: string): WebIteration => ({
  iteration: n,
  content: c,
  reasoning: '',
  tools: [],
  toolCount: 0,
})

const runningTool = (name: string) => ({
  name,
  label: '',
  status: 'running',
  elapsedMs: 0,
  summary: '',
  detail: '',
  args: '',
  toolHints: '',
})

/** DB 快照里的在跑 turn：已有中间行 ⇒ 被折成 committed（1..3 迭代）。 */
const committedInFlightTurn = (): Turn => ({
  id: T,
  user: {
    id: 'db-u9',
    content: 'user msg' as never,
    timestamp: 't',
    isNotification: false,
    queued: false,
    sending: false,
    requestID: null,
    turnHint: 9,
    dbID: 9,
  },
  phase: {
    kind: 'committed',
    payload: commitViaFold([mkIter(1, 'i1'), mkIter(2, 'i2'), mkIter(3, 'i3')] as never, ''),
  },
  requestID: null,
})

describe('P0 手机锁屏恢复：busy turn 必须从 DB 快照 + active_progress 恢复 live', () => {
  it('REPRO: DB 已有在跑 turn 的中间行（committed）+ active 快照声明仍在跑 ⇒ 必须 live 且继续更新', () => {
    const s = reduce(initialChatState('chat-1'), {
      type: 'history_replaced',
      legacy: [],
      turns: [committedInFlightTurn()],
      active: {
        turnID: T,
        // 服务端 active_progress：turn 仍在跑（tool_exec，迭代 4，Shell 正在执行）。
        snapshot: {
          ...EMPTY_LIVE,
          iter: iterNum(4),
          streaming: true,
          iterations: [mkIter(1, 'i1'), mkIter(2, 'i2'), mkIter(3, 'i3')],
          activeTools: [runningTool('Shell')] as never,
        },
      },
      lastSeq: null,
      todos: [],
    })

    // ① 必须恢复 live（修复前 activeTurn = null ⇒ liveProgressFromState 返回 EMPTY
    //    ⇒ 「看不到最新进度」）。
    expect(s.activeTurn).toBe(T)
    const t = s.turns.get(T)
    if (t?.phase.kind !== 'live') throw new Error('busy turn must be restored as live')
    // 进行中的工具必须在 live 数据里（否则恢复后连"Shell 在跑"都看不到）。
    expect(t.phase.data.activeTools.map((x) => x.name)).toEqual(['Shell'])
    // DB 已落库的迭代必须保留（union，不丢）。
    expect(t.phase.data.iterations.map((i) => i.iteration)).toEqual([1, 2, 3])

    // ② 后续 live 事件必须被接受（修复前：committed 遮蔽 —— ev.iter(3) 不大于
    //    maxIter(3) ⇒ return s ⇒ 「也不更新了」）。
    const s2 = reduce(s, {
      type: 'iteration',
      turnID: T,
      iter: iterNum(3),
      seq: 20 as never,
      content: '工具输出回来了',
      reasoning: undefined,
      activeTools: [],
      completedTools: [],
      iterationsDelta: [],
      todos: undefined,
    } as never)
    const t2 = s2.turns.get(T)
    if (t2?.phase.kind !== 'live') throw new Error('restored live turn must keep updating')
    expect(t2.phase.data.content).toBe('工具输出回来了')
  })

  it('对照：没有 active 快照（turn 真结束）时，DB 的 committed 不得被复活成 live', () => {
    const s = reduce(initialChatState('chat-1'), {
      type: 'history_replaced',
      legacy: [],
      turns: [committedInFlightTurn()],
      active: null,
      lastSeq: null,
      todos: [],
    })
    expect(s.activeTurn).toBeNull()
    const t = s.turns.get(T)
    expect(t?.phase.kind).toBe('committed')
  })

  it('对照：active 快照与 DB 迭代 union（DB 更旧/更多时都不丢，同号快照权威）', () => {
    const s = reduce(initialChatState('chat-1'), {
      type: 'history_replaced',
      legacy: [],
      turns: [committedInFlightTurn()],
      active: {
        turnID: T,
        snapshot: {
          ...EMPTY_LIVE,
          iter: iterNum(4),
          streaming: true,
          // 快照尾部只带 3..4（旧迭代被尾部截断），且 3 号是新鲜内容（权威覆盖）。
          iterations: [mkIter(3, 'i3-fresh'), mkIter(4, 'i4')],
          activeTools: [runningTool('Shell')] as never,
        },
      },
      lastSeq: null,
      todos: [],
    })
    const t = s.turns.get(T)
    if (t?.phase.kind !== 'live') throw new Error('live')
    expect(t.phase.data.iterations.map((i) => i.iteration)).toEqual([1, 2, 3, 4])
    // 同号快照权威（服务端 live 比 DB 行新）。
    expect(t.phase.data.iterations.find((i) => i.iteration === 3)?.content).toBe('i3-fresh')
  })

  it('对照（线性一致性红线）：committed turn 收到【同号 + 在跑工具】的迟到重放 ⇒ 不得升级 live', () => {
    // c3cb2f02 回归：把「同号 + running 工具」当活动证据升级成 live ⇒ live 行与
    // committed 渲染分叉 ⇒ 上下文视图缺迭代（刷新才恢复）。committed 的迭代前缀
    // 是持久化权威：只有【新迭代】（ev.iter > maxIter）才能解除遮蔽 —— 迟到重放
    // （事件在工具完成前捕获，天然带 running 工具）必须丢弃。
    const s = reduce(initialChatState('chat-1'), {
      type: 'history_replaced',
      legacy: [],
      turns: [committedInFlightTurn()],
      active: null,
      lastSeq: null,
      todos: [],
    })
    const s2 = reduce(s, {
      type: 'iteration',
      turnID: T,
      iter: iterNum(3),
      seq: 30 as never,
      content: undefined,
      reasoning: undefined,
      activeTools: [runningTool('Shell')] as never,
      completedTools: [],
      iterationsDelta: [],
      todos: undefined,
    } as never)
    const t2 = s2.turns.get(T)
    expect(t2?.phase.kind).toBe('committed')
    expect(s2.activeTurn).toBeNull()
    // 线性一致性：一个迭代都不能少。
    if (t2?.phase.kind === 'committed') expect(t2.phase.payload.iterations).toHaveLength(3)
  })
})
