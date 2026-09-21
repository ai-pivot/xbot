/**
 * P0 回归（2026-09-21 用户报告）：
 *   「我切到这个会话的时候，它的历史永远是这样，停在这个 +32 工具的位置。然后新迭代
 *    进行中的时候会渲染出来，执行完毕后就消失，恢复图里这个样子，看起来进度一直卡在
 *    这里。刷新了一下就正常了。」
 *
 * 机制（DB 取证：tenant 229017 turn 5 当时 510 个迭代、连续无缺号；前端渲染窗口停在
 * 第 93 个迭代 = 用户切走那一刻的进度）：
 *   1. 用户切走时，状态机里的 turn 持有当时的窗口 [1..93]（更早的读取 + 当时收到的 delta）；
 *   2. 切回来时 fetchHistory 返回的 DB 权威是**有界尾部窗口**（服务端
 *      BoundHistoryIterations / 客户端 boundIterationTail 只保留最近 60 个迭代）
 *      ⇒ incoming = [451..510]；
 *   3. history_replaced 的 union 合并（I4 append-only）把两个**不相邻**的窗口拼在一起
 *      ⇒ [1..93] ∪ [451..510] 出现 357 宽的 gap；
 *   4. 渲染层的线性一致性守卫 `continuousIterations` 在第一个 gap 处截断 ⇒ 永远只渲染
 *      [1..93]，**最新窗口永久不可见**；
 *   5. 新迭代：stream 事件（stream case 的遮蔽解除）让它在 live 行里**瞬时**渲染出来
 *      （打字机），迭代完成时 delta 落进那个带 gap 的数组 ⇒ 立刻被守卫隐藏 ⇒
 *      「出现即消失」；历史窗口再也不前进。
 *   6. 整页刷新（丢掉状态机里的旧窗口）→ 只剩 DB 尾部窗口 → 连续 ⇒ 正常。
 *
 * 判据（与渲染同源）：`continuousIterations(turn.iterations)` 必须能看到**最新**的迭代
 * —— 窗口可以是有界的（更早的迭代由「更早的 N 个迭代未加载」承载），但绝不能停留在
 * 过期窗口上。
 */

import { describe, expect, it } from 'vitest'
import { continuousIterations } from '@/components/agent/progressStore'
import { reduce } from './reduce'
import { commitViaFold, initialChatState, iterNum, turnID, type ChatState, type DomainEvent, type LiveSnapshot, type Turn } from './types'
import type { WebIteration } from '@/types/shared'

const T5 = turnID(5)
const WINDOW = 60

/** [from..to] 的迭代窗口（内容刻意用 iteration 号编码，便于断言"看到的是哪一段"）。 */
function window_(from: number, to: number): WebIteration[] {
  const out: WebIteration[] = []
  for (let i = from; i <= to; i++) {
    out.push({ iteration: i, content: `iter-${i}`, reasoning: '', tools: [], toolCount: 0 })
  }
  return out
}

function committedTurn(its: WebIteration[], truncated: number): Turn {
  return {
    id: T5,
    user: null,
    phase: { kind: 'committed', payload: commitViaFold(its as never, '', truncated) },
    requestID: null,
  }
}

function liveSnapshot(its: WebIteration[], iter: number): LiveSnapshot {
  return {
    iter: iterNum(iter),
    streaming: true,
    content: '',
    reasoning: '',
    iterations: its,
    activeTools: [],
    streamingTools: [],
    genui: '',
    subAgents: [],
    todos: [],
    tokenUsage: null,
    streamStats: null,
  }
}

const historyReplaced = (
  turns: readonly Turn[],
  active: DomainEvent extends never ? never : { turnID: ReturnType<typeof turnID>; snapshot: LiveSnapshot } | null,
): DomainEvent => ({ type: 'history_replaced', legacy: [], turns, active, lastSeq: null, todos: [] })

/** 状态机里 turn 5 当前持有的迭代（三态取数）。 */
function iterationsOf(s: ChatState): readonly WebIteration[] {
  const t = s.turns.get(T5)
  if (!t) return []
  return t.phase.kind === 'committed' ? t.phase.payload.iterations : t.phase.data.iterations
}

/** 渲染层实际会画的迭代（连续前缀守卫 —— 与 TurnBody 同源）。 */
function renderedOf(s: ChatState): readonly WebIteration[] {
  return continuousIterations([...iterationsOf(s)])
}

function truncatedOf(s: ChatState): number {
  const t = s.turns.get(T5)
  if (!t) return 0
  if (t.phase.kind === 'committed') return t.phase.payload.iterationsTruncated ?? 0
  if (t.phase.kind === 'live') return t.phase.data.iterationsTruncated ?? 0
  return t.phase.data.iterationsTruncated ?? 0
}

describe('P0（2026-09-21）切回会话：过期窗口 ∪ 有界尾部窗口 → gap → 最新进度永久不可见', () => {
  it('REPRO: 状态机持过期窗口 [1..93]，DB 权威是有界尾部 [451..510] ⇒ 渲染窗口必须前进到最新', () => {
    // 1. 用户切走那一刻：状态机持有 [1..93]（当时的进度 + 收到的 delta）。
    let s: ChatState = initialChatState('chat-1')
    s = reduce(s, historyReplaced([committedTurn(window_(1, 93), 0)], null))
    expect(renderedOf(s).at(-1)?.iteration).toBe(93)

    // 2. 切回来：fetchHistory 的 DB 权威 = 有界尾部窗口 [451..510]（+ active 快照同窗口）。
    s = reduce(
      s,
      historyReplaced(
        [committedTurn(window_(451, 510), 510 - WINDOW)],
        { turnID: T5, snapshot: liveSnapshot(window_(451, 510), 510) },
      ),
    )

    // 3. 渲染层的守卫必须能看到最新迭代 —— 而不是被 gap 截断在 93。
    const rendered = renderedOf(s)
    expect(rendered.at(-1)?.iteration).toBe(510)
    expect(rendered[0].iteration).toBe(451)
    // 保留的窗口告诉用户更早的迭代未加载（绝不静默缺块）。
    expect(truncatedOf(s)).toBe(510 - WINDOW + 1 - 1) // 450 个更早的迭代
  })

  it('REPRO: 有界尾部窗口之后的新迭代（delta）必须渲染出来 —— 不能只"出现即消失"', () => {
    let s: ChatState = initialChatState('chat-1')
    s = reduce(s, historyReplaced([committedTurn(window_(1, 93), 0)], null))
    s = reduce(
      s,
      historyReplaced(
        [committedTurn(window_(451, 510), 450)],
        { turnID: T5, snapshot: liveSnapshot(window_(451, 510), 510) },
      ),
    )
    // 迭代 511 完成（delta 落进状态机）。
    s = reduce(s, {
      type: 'iteration',
      turnID: T5,
      iter: iterNum(511),
      seq: 900 as never,
      content: undefined,
      reasoning: undefined,
      activeTools: [],
      completedTools: [],
      iterationsDelta: window_(511, 511),
      todos: undefined,
      subAgents: undefined,
      tokenUsage: undefined,
      streamStats: undefined,
    })
    expect(renderedOf(s).at(-1)?.iteration).toBe(511)
  })

  it('对照：相邻窗口（resume 竞态：live [4] + DB [1..3]）仍必须 union —— 修复不得回退该语义', () => {
    let s: ChatState = initialChatState('chat-1')
    s = reduce(s, historyReplaced([], { turnID: T5, snapshot: liveSnapshot(window_(4, 4), 4) }))
    s = reduce(s, historyReplaced([committedTurn(window_(1, 3), 0)], null))
    expect(iterationsOf(s).map((i) => i.iteration)).toEqual([1, 2, 3, 4])
  })
})
