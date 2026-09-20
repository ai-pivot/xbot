/**
 * P0 回归（2026-09-20 用户报告）：
 * 「手机熄屏很久之后回来：idle/busy 正常了，但**新迭代 live 时会渲染、完成后立刻
 *   消失**，前端显示的历史永久卡死在熄屏前的进度。」
 *
 * 根因（`chat/reduce.ts` 的 I5 seq gate）：`ProgressEvent.Seq` 是 **per-Run** 水位
 * （后端 `buildMainRunConfig` 每次 Run 新建一个 `atomic.Uint64`），而**同一个 turn 的
 * Run 会被重启** —— 最典型的是服务端重启后的 resume（`resolveResumeTurnID` 复用被中断
 * turn 的 turn_id + `IterationStart = K+1` 续接迭代号），此时**新 Run 的 Seq 从 1 重新
 * 计数**。
 *
 * 客户端在熄屏/断线期间保留的是**旧 Run** 的水位（`ChatState.lastSeq`）⇒ 恢复后新 Run
 * 的 structured 事件（seq 1..N ≤ 旧水位）**全部被判成"重放"丢弃**；而 `stream` 事件
 * **没有** seq gate（累积全量推送）⇒ 打字机照常更新。于是：
 *   · live 帧看得到内容（stream 生效）；
 *   · 迭代推进时，下一帧 stream 的 `advanced` 清空流式内容，而携带该迭代 delta 的
 *     structured 事件被吞 ⇒ 迭代**出现即消失**；
 *   · `iterations` 永不增长 ⇒ **历史卡死**（刷新才从 DB 恢复）。
 *
 * 判据（与遮蔽解除同一证据标准）：seq ≤ 水位 **且** 事件不携带任何**新迭代信息**
 * 才算重放 —— 迭代号在 turn 域内单调、后端绝不对更早的迭代重发更大号，故携带更大
 * 迭代号（或我们还没有的迭代 delta）的事件不可能是"已应用过的重放"。
 */
import { describe, expect, it } from 'vitest'

import { reduce } from './reduce'
import {
  commitViaFold,
  EMPTY_LIVE,
  initialChatState,
  iterNum,
  turnID,
  type ChatState,
  type DomainEvent,
  type Turn,
} from './types'
import type { WebIteration } from '@/types/shared'

const T = turnID(9)

const mkIter = (n: number, c = `i${n}`): WebIteration => ({
  iteration: n,
  content: c,
  reasoning: '',
  tools: [],
  toolCount: 0,
})

/** DB 快照里的 turn（已有迭代 ⇒ 折成 committed）。 */
const committedTurn = (its: readonly WebIteration[]): Turn => ({
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
  phase: { kind: 'committed', payload: commitViaFold(its as never, '') },
  requestID: null,
})

const started = (turn: ReturnType<typeof turnID>): DomainEvent => ({
  type: 'turn_started',
  turnID: turn,
  requestID: null,
  trigger: 'resume',
  content: null,
})

/** 结构化 iteration 事件（iter/seq/delta 可指定）。 */
function iterEvent(opts: {
  turn?: ReturnType<typeof turnID>
  iter: number
  seq: number | null
  delta?: readonly WebIteration[]
  content?: string
}): DomainEvent {
  return {
    type: 'iteration',
    turnID: opts.turn ?? T,
    iter: iterNum(opts.iter),
    seq: opts.seq as never,
    content: opts.content,
    reasoning: undefined,
    activeTools: [],
    completedTools: [],
    iterationsDelta: opts.delta ?? [],
    todos: undefined,
    goal: undefined,
    subAgents: undefined,
    tokenUsage: undefined,
    streamStats: undefined,
  } as DomainEvent
}

function streamEvent(opts: { iter: number; seq: number | null; content?: string }): DomainEvent {
  return {
    type: 'stream',
    turnID: T,
    seq: opts.seq as never,
    iteration: iterNum(opts.iter),
    content: opts.content,
    reasoning: undefined,
    streamingTools: undefined,
    genui: undefined,
    streamStats: undefined,
  } as DomainEvent
}

/** 熄屏前：同一个 turn 已在跑（旧 Run 水位 12、当前迭代 3、已落迭代 1..2）。 */
function preScreenOff(): ChatState {
  let s = reduce(initialChatState('chat-1'), started(T))
  s = reduce(s, iterEvent({ iter: 3, seq: 10, delta: [mkIter(1), mkIter(2)] }))
  s = reduce(s, iterEvent({ iter: 3, seq: 12, content: '熄屏前的流式文本' }))
  return s
}

function liveOf(s: ChatState) {
  const t = s.turns.get(T)
  if (t?.phase.kind !== 'live') throw new Error('turn must be live')
  return t.phase.data
}

const iters = (s: ChatState) => liveOf(s).iterations.map((i) => i.iteration)

describe('P0 熄屏恢复：turn 的 Run 重启后（新 seq 从 1）structured 事件不得被 stale 水位吞掉', () => {
  it('REPRO: 新 Run 的 iteration 事件（seq=1，迭代号续接）必须应用并把水位切到新 Run', () => {
    const s0 = preScreenOff()
    expect(s0.lastSeq).toBe(12)
    expect(iters(s0)).toEqual([1, 2])

    // 服务端重启 resume：同一个 turn、新 Run（seq 重新从 1 计数）、迭代号续接 4。
    const s1 = reduce(s0, iterEvent({ iter: 4, seq: 1, delta: [mkIter(3)] }))

    // ① 刚完成的迭代（delta）必须落进 iterations —— 修复前被 I5 gate 丢弃 ⇒
    //    紧接着的 stream `advanced` 会清空它的内容 ⇒「iter 出现即消失」。
    expect(iters(s1)).toEqual([1, 2, 3])
    // ② 迭代号必须前进（live 渲染新迭代）。
    expect(liveOf(s1).iter).toBe(4)
    // ③ 水位切到新 Run（后续 seq=2.. 照常通过）。
    expect(s1.lastSeq).toBe(1)
    const s2 = reduce(s1, iterEvent({ iter: 4, seq: 2, delta: [mkIter(4)] }))
    expect(iters(s2)).toEqual([1, 2, 3, 4])
  })

  it('REPRO: 恢复水合后 stream 先到、structured delta 后到 —— 迭代 content 不得消失', () => {
    // 熄屏期间服务端重启并 resume：DB 已落 iteration 1..3，新 Run 从迭代 4 继续。
    const hydrated = reduce(preScreenOff(), {
      type: 'history_replaced',
      legacy: [],
      turns: [committedTurn([mkIter(1), mkIter(2), mkIter(3)])],
      active: {
        turnID: T,
        snapshot: {
          ...EMPTY_LIVE,
          iter: iterNum(4),
          streaming: true,
          iterations: [mkIter(1), mkIter(2), mkIter(3)],
        },
      },
      lastSeq: null,
      todos: [],
    })
    expect(iters(hydrated)).toEqual([1, 2, 3])
    // 旧 Run 的水位被保留（同一个 turn）—— 正是把新 Run 事件误判成重放的来源。
    expect(hydrated.lastSeq).toBe(12)

    // 新 Run（seq 从 1 重新计数）：迭代 4 的 stream 帧先到（无 seq gate）。
    const s1 = reduce(hydrated, streamEvent({ iter: 4, seq: 1, content: '迭代4 的流式文本' }))
    expect(liveOf(s1).iter).toBe(4)
    // 迭代 4 完成：carry delta[4] 的 structured 事件（seq=3 ≤ 旧水位 12）必须落盘。
    const s2 = reduce(s1, iterEvent({ iter: 5, seq: 3, delta: [mkIter(4, '迭代4 的最终文本')] }))
    expect(iters(s2)).toEqual([1, 2, 3, 4])
    expect(liveOf(s2).iterations.find((i) => i.iteration === 4)?.content).toBe('迭代4 的最终文本')
    expect(s2.lastSeq).toBe(3)
  })

  it('REPRO: 新 Run 的 phase_done 携带的最后迭代（尚未持有）必须并入', () => {
    const s0 = preScreenOff()
    const done: DomainEvent = {
      type: 'phase_done',
      turnID: T,
      seq: 1 as never,
      finalIteration: mkIter(3, '最后迭代文本'),
      todos: undefined,
      goal: undefined,
    } as DomainEvent
    expect(iters(reduce(s0, done))).toEqual([1, 2, 3])
  })

  it('对照（不得回归）：真·重放（seq ≤ 水位 + 无新信息）仍必须丢弃', () => {
    const s0 = preScreenOff()
    // 重放同一迭代号的旧事件：seq 11 ≤ 12、iter 3 不大于 live.iter 3、
    // delta 是已持有的 1..2 ⇒ 必须原样返回（零渲染）。
    expect(reduce(s0, iterEvent({ iter: 3, seq: 11, delta: [mkIter(1), mkIter(2)] }))).toBe(s0)
    // 携带【旧内容】的重放不得回退已渲染的流式文本。
    const s2 = reduce(s0, iterEvent({ iter: 3, seq: 5, content: '过期内容' }))
    expect(liveOf(s2).content).toBe('熄屏前的流式文本')
    // 已持有的 finalIteration 重放 ⇒ 幂等（不重建 state）。
    const withFinal = reduce(s0, {
      type: 'phase_done',
      turnID: T,
      seq: 20 as never,
      finalIteration: mkIter(3, '最后迭代文本'),
      todos: undefined,
      goal: undefined,
    } as DomainEvent)
    expect(iters(withFinal)).toEqual([1, 2, 3])
    expect(
      reduce(withFinal, {
        type: 'phase_done',
        turnID: T,
        seq: 1 as never,
        finalIteration: mkIter(3, '最后迭代文本'),
        todos: undefined,
        goal: undefined,
      } as DomainEvent),
    ).toBe(withFinal)
  })

  it('对照：同 Run 内 seq 单调的内容覆盖语义不变（seq 更大才覆盖 content）', () => {
    const s0 = preScreenOff()
    expect(liveOf(reduce(s0, iterEvent({ iter: 3, seq: 13, content: '同 Run 的新内容' }))).content).toBe(
      '同 Run 的新内容',
    )
  })
})
