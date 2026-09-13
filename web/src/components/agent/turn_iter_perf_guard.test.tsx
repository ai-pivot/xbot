/**
 * 守护（2026-09-13「每帧代价 ∝ 迭代数」根治）：**历史加载路径**下的流式帧代价。
 *
 * 与 turn_perf_pipeline.test.tsx 的差别（这就是上一版没拦住的覆盖缺口）：
 *   - turn_perf_pipeline.test.tsx 用 SSE `iteration` 事件 seed committed 行；
 *   - 本文件用 **`historyToReplaced`（/api/history 的真实入口）** seed committed 行，
 *     并在每个流式帧**幂等重放**一次 history（生产形态：useChatMessages 的 store
 *     每帧 notify → setMessages → historyMessages 换引用 → history_replaced）。
 *     这正是「加载的历史消息长了就卡」的那条路径 —— 也是窗口化观测竞态
 *     （首帧注册的块永不 observe → 永不 muted）唯一能暴露的路径。
 *
 * 探针：把 `IterationGroup` 换成**非 memo** 计数实现 —— CommittedTurn 一旦重渲染，
 * 它的 N 个子元素就会被重建/重执行，计数即"每帧为已提交迭代付出的工作量"。
 *
 * 不变量：
 *   1. 每个流式帧对已提交迭代的渲染次数 = 0（与 N 无关）；
 *   2. 未变更的 committed 行对象与 iterations 数组引用逐帧恒等（memo 边界成立）。
 */
import { memo, useMemo, useSyncExternalStore } from 'react'

import { act, render } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'

const counters = vi.hoisted(() => ({ committedIter: 0 }))

vi.mock('@/components/agent/IterationHistory', () => ({
  IterationGroup: ({ iteration }: { iteration: { iteration: number } }) => {
    counters.committedIter++
    return <div data-testid="committed-iter">{iteration.iteration}</div>
  },
}))
vi.mock('@/components/agent/LiveIteration', () => ({
  LiveIteration: () => <div data-testid="live-iter" />,
}))
vi.mock('@/components/agent/SubAgentProgressTree', () => ({
  SubAgentProgressTree: () => <div data-testid="sat" />,
}))

import { TurnBody } from '@/components/agent/TurnBody'
import { deriveRows } from '@/chat/derive'
import { historyToReplaced, liveProgressFromState, rowsToChatMessages } from '@/chat/integrate'
import { ChatStore } from '@/chat/store'
import type { DomainEvent } from '@/chat/types'
import { I18nProvider } from '@/providers/i18n'
import '@/i18n'
import type { ChatMessage, WebIteration } from '@/types/shared'

const ACTIVE_TURN = 42

function iter(n: number): WebIteration {
  return {
    iteration: n,
    content: `content-${n}`,
    reasoning: `reasoning-${n}`,
    tools: [{ name: 'Read', label: `f${n}.ts`, status: 'done' }],
    toolCount: 1,
  } as unknown as WebIteration
}

/** committed 历史：1 个 turn（N 迭代），带 dbID —— 走 historyToReplaced 的真实过滤。 */
function committedHistory(n: number): ChatMessage[] {
  let dbID = 0
  return [
    {
      id: `db-${++dbID}`, role: 'user', content: 'u1', iterations: [],
      timestamp: '', isPartial: false, turnID: 1, persisted: true, dbID,
    } as ChatMessage,
    {
      id: `db-${++dbID}`, role: 'assistant', content: 'a1',
      iterations: Array.from({ length: n }, (_, i) => iter(i + 1)),
      timestamp: '', isPartial: false, turnID: 1, persisted: true, dbID,
    } as ChatMessage,
  ]
}

function streamEvent(content: string, seq: number): DomainEvent {
  return {
    type: 'stream', turnID: ACTIVE_TURN, seq, iteration: null, content,
    reasoning: undefined, streamingTools: undefined, genui: undefined, streamStats: undefined,
  } as unknown as DomainEvent
}

const Driver = memo(function Driver({ store }: { store: ChatStore }) {
  const state = useSyncExternalStore(store.subscribe, store.getSnapshot, store.getSnapshot)
  const rows = useMemo(() => rowsToChatMessages(deriveRows(state)), [state])
  const liveProgress = useMemo(() => liveProgressFromState(state), [state])
  const live = rows.find((m) => m.isPartial)
  const committed = rows.find((m) => !m.isPartial && m.role === 'assistant')
  const iterations = live?.iterations?.length ? live.iterations : (committed?.iterations ?? [])
  return <TurnBody iterations={iterations} liveProgress={liveProgress} turnID={ACTIVE_TURN} />
})

async function measure(n: number, frames: number) {
  const store = new ChatStore('chat-1')
  const hist = committedHistory(n)
  store.dispatch(historyToReplaced(hist, null))
  store.dispatch({
    type: 'turn_started', turnID: ACTIVE_TURN, requestID: 'r1', trigger: 'user', content: null,
  } as unknown as DomainEvent)

  counters.committedIter = 0
  render(
    <I18nProvider>
      <Driver store={store} />
    </I18nProvider>,
  )
  const initial = counters.committedIter
  const before = counters.committedIter

  // 逐帧恒等性：**已提交行**对象 + iterations 引用必须稳定（live 行每帧变化是语义要求）。
  const snapCommitted = () => rowsToChatMessages(deriveRows(store.getSnapshot())).filter((m) => !m.isPartial)
  let prev = snapCommitted()
  let identityChurn = 0
  for (let f = 1; f <= frames; f++) {
    await act(async () => {
      store.dispatch(streamEvent(`live-${f}`, 100 + f))
      // 生产形态：historyMessages 每帧换引用 → 幂等重放
      store.dispatch(historyToReplaced(hist, null))
      await new Promise((r) => setTimeout(r, 20))
    })
    const next = snapCommitted()
    for (let i = 0; i < prev.length; i++) {
      if (next[i] !== prev[i]) identityChurn++
      else if (next[i].iterations !== prev[i].iterations) identityChurn++
    }
    prev = next
  }
  store.dispose()
  return { initial, perFrame: (counters.committedIter - before) / frames, identityChurn }
}

describe('守护：历史加载路径下，流式帧代价与 turn 迭代数无关', () => {
  it('已提交迭代在流式帧之间零渲染（N=20 与 N=200 相同）', async () => {
    const small = await measure(20, 5)
    const big = await measure(200, 5)

    // 首帧：全部已提交迭代各渲染一次（这是 O(N) 的一次性代价，允许）
    expect(small.initial).toBe(20)
    expect(big.initial).toBe(200)

    // 流式帧：已提交迭代零渲染 —— 每帧代价与 N 无关
    expect(small.perFrame).toBe(0)
    expect(big.perFrame).toBe(0)

    // 行对象 / iterations 引用逐帧恒等（memo 边界不被击穿）
    expect(small.identityChurn).toBe(0)
    expect(big.identityChurn).toBe(0)
  }, 30000)
})
