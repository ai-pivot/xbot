/**
 * integrate.test.ts — 渲染边界（ChatState → 渲染数据）的【引用稳定性】契约。
 *
 * REPRO（长 turn 卡顿回归）：`integrate.ts` 是状态机 → 渲染组件的唯一边界。
 * 它在**每一帧**用 `[...iterations]` / `[...activeTools]` 逐行拷贝数组、并
 * 为每一行新建 ChatMessage 对象 —— 把 reduce 辛苦保持的引用稳定性在渲染边界
 * 前一米处打穿。后果：TurnBody 的 `useMemo([iterations])`、CommittedTurn /
 * IterationGroup / MessageItem 的 memo 全部逐帧失效，流式帧代价重新变成
 * O(N)（N = turn 迭代数）+ 每帧重渲染全部可见行。
 *
 * 本文件断言的是**不变量本身**（而不是"人记住要保持引用"）：
 *   源引用不变（phase / iterations 数组 / turn）⇒ 派生结果的引用必须不变。
 * 没有这条，任何 memo 边界都是纸糊的。
 *
 * 真实事件序列由 reduce 驱动（与线上同一条链路）：
 *   turn_started → iteration（携带迭代增量）× N → stream（流式帧）
 */
import { describe, expect, it } from 'vitest'

import { deriveRows } from './derive'
import { liveProgressFromState, rowsToChatMessages, historyToReplaced } from './integrate'
import { reduce } from './reduce'
import { initialChatState, type ChatState, type DomainEvent } from './types'
import type { ChatMessage, WebIteration } from '@/types/shared'

const T1 = 41
const T2 = 42

function iter(n: number): WebIteration {
  return {
    iteration: n,
    content: `content-${n}`,
    reasoning: `reasoning-${n}`,
    tools: [{ name: 'Read', label: `f${n}.ts`, status: 'done' }],
    toolCount: 1,
  } as unknown as WebIteration
}

/** turn_started：无 user 行（trigger=user + content=null → 未绑定乐观行）。 */
function startTurn(s: ChatState, turnID: number): ChatState {
  return reduce(s, {
    type: 'turn_started',
    turnID,
    requestID: `r${turnID}`,
    trigger: 'user',
    content: null,
  } as unknown as DomainEvent)
}

/** 结构化迭代事件：携带该迭代增量 + 迭代号推进（后端 snapshotCompletedIteration 形态）。 */
function iterEvent(s: ChatState, turnID: number, n: number, seq: number): ChatState {
  return reduce(s, {
    type: 'iteration',
    turnID,
    phase: 'tool_exec',
    iter: n + 1,
    seq,
    content: undefined,
    reasoning: undefined,
    activeTools: [],
    completedTools: [],
    iterationsDelta: [iter(n)],
    todos: undefined,
    subAgents: undefined,
    tokenUsage: undefined,
    streamStats: undefined,
  } as unknown as DomainEvent)
}

function seedTurn(s: ChatState, turnID: number, n: number): ChatState {
  let next = startTurn(s, turnID)
  for (let i = 1; i <= n; i++) next = iterEvent(next, turnID, i, i)
  return next
}

/** 流式帧：累积 content（打字机），iterations 不变（后端 delta push 关闭下的全量帧）。 */
function stream(s: ChatState, turnID: number, content: string, seq: number): ChatState {
  return reduce(s, {
    type: 'stream',
    turnID,
    seq,
    iteration: null,
    content,
    reasoning: undefined,
    streamingTools: undefined,
    genui: undefined,
    streamStats: undefined,
  } as unknown as DomainEvent)
}

function textFinal(s: ChatState, turnID: number): ChatState {
  return reduce(s, {
    type: 'text_final',
    turnID,
    content: 'final' as never,
    progressHistory: [],
    cancelled: false,
  } as unknown as DomainEvent)
}

/** 一帧派生（与 useAgentChatState 完全同一链路）。 */
function derive1(s: ChatState): { messages: ChatMessage[]; live: ReturnType<typeof liveProgressFromState> } {
  const rows = deriveRows(s)
  return { messages: rowsToChatMessages(rows), live: liveProgressFromState(s) }
}

describe('integrate 渲染边界 —— 引用稳定性（流式帧代价与 iter 数无关的必要条件）', () => {
  it('流式帧必须保持 liveProgress 的迭代/工具数组引用（LiveIteration 字段级 memo 的依据）', () => {
    const s1 = stream(seedTurn(initialChatState('chat-1'), T1, 5), T1, 'a', 100)
    const s2 = stream(s1, T1, 'ab', 101)
    const p1 = liveProgressFromState(s1)
    const p2 = liveProgressFromState(s2)

    // 打字机：内容必须前进
    expect(p2.streamContent).toBe('ab')
    expect(p2.streamContent).not.toBe(p1.streamContent)

    // 迭代/工具：源引用未变 ⇒ 派生引用必须不变（否则每帧 O(N) 重算 tools）
    expect(p2.iterationHistory).toBe(p1.iterationHistory)
    expect(p2.activeTools).toBe(p1.activeTools)
    expect(p2.streamingTools).toBe(p1.streamingTools)
    expect(p2.subAgents).toBe(p1.subAgents)
    expect(p2.todos).toBe(p1.todos)
  })

  it('流式帧必须保持未变更行的 ChatMessage 对象恒等（MessageItem memo 的依据）', () => {
    const s1 = stream(seedTurn(initialChatState('chat-1'), T1, 3), T1, 'a', 100)
    const s2 = stream(s1, T1, 'ab', 101)
    const m1 = derive1(s1).messages
    const m2 = derive1(s2).messages

    expect(m1.length).toBeGreaterThan(0)
    expect(m2.length).toBe(m1.length)
    for (let i = 0; i < m1.length; i++) {
      // live 行：内容逐帧前进 → 必须重建（打字机本身）。其余行（committed /
      // user / legacy）引用不变 ⇒ 输出对象必须恒等，否则整列表逐帧重渲染。
      if (m1[i].isPartial) continue
      expect(m2[i]).toBe(m1[i])
    }
  })

  it('live 行 / 快照的 iterations 引用在流式帧之间稳定（TurnBody→CommittedTurn memo 的依据）', () => {
    const s1 = stream(seedTurn(initialChatState('chat-1'), T1, 3), T1, 'a', 100)
    const s2 = stream(s1, T1, 'ab', 101)
    const d1 = derive1(s1)
    const d2 = derive1(s2)

    const live1 = d1.messages.find((m) => m.isPartial)
    const live2 = d2.messages.find((m) => m.isPartial)
    expect(live1).toBeDefined()
    expect(live2).toBeDefined()
    expect(live2!.iterations).toBe(live1!.iterations)
    // AssistantMessage 渲染迭代取 progress.iterationHistory —— 必须与 live 行同源同引用
    expect(d1.live.iterationHistory).toBe(live1!.iterations)
    expect(d2.live.iterationHistory).toBe(live2!.iterations)
  })

  it('上一个 turn 已 committed，新 turn 流式帧不得重建 committed 行（不能整列表逐帧重渲染）', () => {
    // turn 41 完成（committed），turn 42 开始流式
    let s = seedTurn(initialChatState('chat-1'), T1, 4)
    s = textFinal(s, T1)
    s = stream(seedTurn(s, T2, 2), T2, 'x', 200)
    const m1 = derive1(s).messages

    const committed1 = m1.filter((m) => !m.isPartial)
    expect(committed1.length).toBeGreaterThan(0)

    // turn 42 的流式帧
    const s2 = stream(s, T2, 'xy', 201)
    const m2 = derive1(s2).messages
    const committed2 = m2.filter((m) => !m.isPartial)
    expect(committed2.length).toBe(committed1.length)
    for (let i = 0; i < committed1.length; i++) {
      expect(committed2[i]).toBe(committed1[i])
      expect(committed2[i].iterations).toBe(committed1[i].iterations)
    }
  })

  it('缩放：每帧"引用被替换的派生对象数"与 iter 数无关（N=200 与 N=20 相同）', () => {
    const measure = (n: number): number => {
      let s = stream(seedTurn(initialChatState('chat-1'), T1, n), T1, 'f0', 100)
      let prev = derive1(s)
      let changed = 0
      for (let f = 1; f <= 10; f++) {
        s = stream(s, T1, `f${f}`, 100 + f)
        const next = derive1(s)
        changed += next.messages.filter((m, i) => m !== prev.messages[i]).length
        if (next.live.iterationHistory !== prev.live.iterationHistory) changed += 1
        if (next.live.activeTools !== prev.live.activeTools) changed += 1
        if (next.live.streamingTools !== prev.live.streamingTools) changed += 1
        prev = next
      }
      return changed
    }

    const small = measure(20)
    const big = measure(200)
    // 每帧只允许"live 行本身"重建（1 个），与 iter 数无关
    expect(big).toBe(small)
  })

  it('history_replaced 幂等：同一事件重放必须返回原 state（不得重建 Turn 对象）', () => {
    // 状态机不变量：同一转移重放 = no-op。这是"每帧重放不得击穿 memo"的
    // 状态机侧保证（接线侧另见 useAgentChatState 的 DB 历史 gate 测试）。
    let s = seedTurn(initialChatState('chat-1'), T1, 2)
    s = textFinal(s, T1)
    s = stream(seedTurn(s, T2, 1), T2, 'x', 200)

    const history = [
      {
        id: 'h41',
        role: 'assistant',
        content: 'final',
        iterations: [iter(1), iter(2)],
        timestamp: '',
        isPartial: false,
        turnID: T1,
        dbID: 941,
      },
    ] as unknown as ChatMessage[]

    const ev = historyToReplaced(history, null)
    const s1 = reduce(s, ev)
    const s2 = reduce(s1, ev)
    // 第二次重放：逐项恒等 ⇒ 必须返回同一个 state 引用（零通知、零渲染）
    expect(s2).toBe(s1)
  })

  it('history_replaced 携带未变更历史时不得重建 committed turn（引用稳定的输入）', () => {
    let s = seedTurn(initialChatState('chat-1'), T1, 2)
    s = textFinal(s, T1)
    s = stream(seedTurn(s, T2, 1), T2, 'x', 200)

    // 真实形态：DB 历史行由 MessageStore 复用（行对象与 iterations 引用稳定）
    const rows = derive1(s).messages
    const history = rows
      .filter((m) => !m.isPartial)
      .map((m) => ({ ...m, dbID: 941, turnID: T1 })) as unknown as ChatMessage[]

    const s2 = reduce(s, historyToReplaced(history, null))
    // 历史内容未变 ⇒ turn 对象必须恒等（否则 deriveRows 的行 memo 每帧失效）
    expect(s2.turns.get(T1 as never)).toBe(s.turns.get(T1 as never))
    // 正在流式的 turn 引用也不得被重建
    expect(s.turns.get(T2 as never)).toBeDefined()
    expect(s2.turns.get(T2 as never)).toBe(s.turns.get(T2 as never))
  })
})
