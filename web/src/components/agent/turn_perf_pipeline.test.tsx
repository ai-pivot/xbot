/**
 * REPRO（长 turn 卡顿回归，2026-09-13）：真实状态机管线下的流式帧代价。
 *
 * 与 turn_perf.test.tsx 的区别（这就是上次没拦住的覆盖缺口）：
 *   - turn_perf.test.tsx 把 `iterations` 当**外部常量**喂给 TurnBody —— 它把
 *     "引用稳定"当**前提**，而不是**断言对象**；
 *   - 本文件走**线上同一条链路**：真实 ChatStore → reduce → deriveRows →
 *     rowsToChatMessages / liveProgressFromState → TurnBody。引用一旦在
 *     integrate 边界被拷坏（`[...iterations]`/每行新建对象），下面的计数就会红。
 *
 * 探针：`IterationGroup` 替换为**非 memo** 实现 —— CommittedTurn 一旦重渲染，
 * 它的 N 个子元素就会被重建/重执行，计数即"每帧为已提交迭代付出的工作量"。
 * （真实实现是 memo 的，所以线上代价是 N 次元素创建 + reconcile；探针测的是同一
 * 个 O(N) 事实。）
 *
 * 不变量：流式帧对已提交迭代的渲染次数 = 0，且与 N 完全无关。
 */
import { memo, useMemo, useSyncExternalStore, type ReactNode } from 'react'

import { act, render } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'

const counters = vi.hoisted(() => ({ committedIter: 0, liveIter: 0 }))

vi.mock('@/components/agent/IterationHistory', () => ({
  IterationGroup: ({ iteration }: { iteration: { iteration: number } }) => {
    counters.committedIter++
    return <div data-testid="committed-iter">{iteration.iteration}</div>
  },
}))
vi.mock('@/components/agent/MarkdownRenderer', () => ({
  MarkdownRenderer: ({ content }: { content: string }) => <div data-testid="md">{content}</div>,
}))
vi.mock('@/components/agent/ReasoningBlock', () => ({
  ReasoningBlock: () => <div data-testid="rb" />,
}))
vi.mock('@/components/agent/ThinkingLine', () => ({
  ThinkingLine: ({ children, label }: { children: ReactNode; label: ReactNode }) => (
    <div>
      {label}
      {children}
    </div>
  ),
}))
vi.mock('@/components/agent/FoldedToolGroup', () => ({
  FoldedToolGroup: () => <div data-testid="ftg" />,
}))
vi.mock('@/components/agent/GenUIPanel', () => ({
  GenUICollapsiblePanel: () => <div data-testid="genui" />,
}))
vi.mock('@/components/agent/SubAgentProgressTree', () => ({
  SubAgentProgressTree: () => <div data-testid="sat" />,
}))
vi.mock('@/components/agent/ShimmerThinking', () => ({
  ShimmerThinking: () => <div data-testid="shimmer" />,
}))
vi.mock('@/components/agent/SweepText', () => ({
  SweepText: ({ text }: { text: string }) => <span>{text}</span>,
}))

import { TurnBody } from '@/components/agent/TurnBody'
import { deriveRows } from '@/chat/derive'
import { liveProgressFromState, rowsToChatMessages, historyToReplaced } from '@/chat/integrate'
import { ChatStore } from '@/chat/store'
import type { DomainEvent } from '@/chat/types'
import { I18nProvider } from '@/providers/i18n'
import '@/i18n'
import type { WebIteration } from '@/types/shared'

const TURN = 41

function iter(n: number): WebIteration {
  return {
    iteration: n,
    content: `content-${n}`,
    reasoning: `reasoning-${n}`,
    tools: [{ name: 'Read', label: `f${n}.ts`, status: 'done' }],
    toolCount: 1,
  } as unknown as WebIteration
}

/** 真实事件序列：turn_started → iteration×N（每次携带该迭代增量）→ 流式帧。 */
function seed(store: ChatStore, n: number): void {
  store.dispatch({
    type: 'turn_started',
    turnID: TURN,
    requestID: 'r1',
    trigger: 'user',
    content: null,
  } as unknown as DomainEvent)
  for (let i = 1; i <= n; i++) {
    store.dispatch({
      type: 'iteration',
      turnID: TURN,
      phase: 'tool_exec',
      iter: i + 1,
      seq: i,
      content: undefined,
      reasoning: undefined,
      activeTools: [],
      completedTools: [],
      iterationsDelta: [iter(i)],
      todos: undefined,
      subAgents: undefined,
      tokenUsage: undefined,
      streamStats: undefined,
    } as unknown as DomainEvent)
  }
}

function streamEvent(content: string, seq: number): DomainEvent {
  return {
    type: 'stream',
    turnID: TURN,
    seq,
    iteration: null,
    content,
    reasoning: undefined,
    streamingTools: undefined,
    genui: undefined,
    streamStats: undefined,
  } as unknown as DomainEvent
}

/** 线上派生链（useAgentChatState 的三行）+ AssistantMessage 的迭代取值。 */
const PipelineDriver = memo(function PipelineDriver({ store }: { store: ChatStore }) {
  const state = useSyncExternalStore(store.subscribe, store.getSnapshot, store.getSnapshot)
  const messages = useMemo(() => rowsToChatMessages(deriveRows(state)), [state])
  const liveProgress = useMemo(() => liveProgressFromState(state), [state])
  const live = messages.find((m) => m.isPartial)
  const iterations = liveProgress.iterationHistory.length > 0
    ? liveProgress.iterationHistory
    : (live?.iterations ?? [])
  return <TurnBody iterations={iterations} liveProgress={liveProgress} turnID={TURN} />
})

async function measure(n: number, frames: number): Promise<{ initial: number; perFrame: number }> {
  const store = new ChatStore('chat-1')
  seed(store, n)
  // 生产接线形态：DB 历史行在 MessageStore 里对象/迭代引用稳定，每帧被重放
  // （useChatMessages 的 store 每帧 notify → setMessages → historyMessages 换引用
  // → history_replaced）。历史行必须有 dbID 才会进状态机（否则是空历史，测不到）。
  const dbHistory = rowsToChatMessages(deriveRows(store.getSnapshot()))
    .filter((m) => !m.isPartial)
    .map((m) => ({ ...m, dbID: 941 }))
  counters.committedIter = 0
  render(
    <I18nProvider>
      <PipelineDriver store={store} />
    </I18nProvider>,
  )
  const initial = counters.committedIter
  const before = counters.committedIter
  for (let f = 1; f <= frames; f++) {
    await act(async () => {
      store.dispatch(streamEvent(`live-${f}`, 100 + f))
      store.dispatch(historyToReplaced(dbHistory, null))
      await new Promise((r) => setTimeout(r, 25))
    })
  }
  store.dispose()
  return { initial, perFrame: (counters.committedIter - before) / frames }
}

describe('REPRO 长 turn 卡顿回归 —— 真实管线下流式帧代价与 iter 数无关', () => {
  it('已提交迭代在流式帧之间零渲染，且与 N 无关（N=20 与 N=200 相同）', async () => {
    const small = await measure(20, 4)
    const big = await measure(200, 4)

    // 首帧：全部已提交迭代各渲染一次
    expect(small.initial).toBe(20)
    expect(big.initial).toBe(200)

    // 流式帧：已提交迭代零渲染（修复前 = 每帧 N 次 → 代价 ∝ turn 长度）
    expect(small.perFrame).toBe(0)
    expect(big.perFrame).toBe(0)
    expect(big.perFrame).toBe(small.perFrame)
  }, 20000)
})
