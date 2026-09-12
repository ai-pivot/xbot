/**
 * REPRO（Trace-20260912T100816）：超长 agent turn 越跑越卡。
 *
 * trace 证据：渲染主线程 88% busy、~10fps（每帧 ~88ms）、68.8% 采样在 React
 * reconcile 之下；`createLucideIcon`（ThinkingLine 的 Brain 图标）出现在
 * 79/96 帧 —— 即**每个流式帧都把该 turn 的全部已提交迭代重新渲染一遍**，
 * 代价 ∝ turn 迭代数（用户要求：与 turn 长度完全无关，永远常数）。
 *
 * 本测试用与线上一致的渲染拓扑：帧状态由 **TurnBody 之上的小 driver** 持有
 * （线上是 MessageList 订阅 store 后重渲染），i18n Provider 不随帧重渲染
 * （上下文引用抖动会击穿 memo，那是测试拓扑问题，不是产品行为）。
 *
 * 不变量：`iterations` 引用不变（快照 iterationHistory 在流式帧之间引用稳定）
 * 时，流式帧**不得重渲染任何已提交迭代** —— 渲染只允许发生在 LiveIteration
 * 子树内。
 */
import { createRef, forwardRef, useImperativeHandle, useState, type ReactNode } from 'react'

import { act, render } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'

const counters = vi.hoisted(() => ({ committed: 0 }))

vi.mock('@/components/agent/ThinkingLine', () => ({
  ThinkingLine: ({ label }: { label: ReactNode }) => {
    counters.committed++
    return <div data-testid="tl">{label}</div>
  },
}))
vi.mock('@/components/agent/FoldedToolGroup', () => ({
  FoldedToolGroup: () => {
    counters.committed++
    return <div data-testid="ftg" />
  },
}))
vi.mock('@/components/agent/MarkdownRenderer', () => ({
  MarkdownRenderer: () => <div data-testid="md" />,
}))
vi.mock('@/components/agent/ReasoningBlock', () => ({
  ReasoningBlock: () => <div data-testid="rb" />,
}))
vi.mock('@/components/agent/SubAgentProgressTree', () => ({
  SubAgentProgressTree: () => <div data-testid="sat" />,
}))
vi.mock('@/components/agent/GenUIPanel', () => ({
  GenUICollapsiblePanel: () => <div data-testid="genui" />,
}))

import { TurnBody } from '@/components/agent/TurnBody'
import { I18nProvider } from '@/providers/i18n'
import '@/i18n'
import type { ProgressSnapshot, WebIteration } from '@/types/shared'

function makeIterations(n: number): WebIteration[] {
  return Array.from({ length: n }, (_, i) => ({
    iteration: i + 1,
    content: `content-${i}`,
    reasoning: `reasoning-${i}`,
    tools: [{ name: 'Read', label: `f${i}.ts`, status: 'done' }],
    toolCount: 1,
  })) as unknown as WebIteration[]
}

function makeSnapshot(history: WebIteration[], frame: number): ProgressSnapshot {
  return {
    eventSeq: frame,
    phase: 'content',
    iteration: history.length + 1,
    streamContent: `live-frame-${frame}`,
    reasoningStreamContent: '',
    content: '',
    streaming: true,
    activeTools: [],
    completedTools: [],
    iterationHistory: history,
    streamingTools: [],
    genuiContent: '',
    lastIter: history.length,
    lastReasoning: '',
    todos: [],
    goal: null,
    subAgents: [],
    tokenUsage: null,
    turnID: 1,
  } as ProgressSnapshot
}

/** 帧 driver：只重渲染 TurnBody 子树（与线上 MessageList 的订阅重渲染同形）。
 *  通过 ref 暴露 push（useImperativeHandle）—— 不在 render 期间写外部变量。 */
const FrameDriver = forwardRef<{ push: (f: number) => void }, { iterations: WebIteration[] }>(
  function FrameDriver({ iterations }, ref) {
    const [frame, setFrame] = useState(1)
    useImperativeHandle(ref, () => ({ push: (f: number) => setFrame(f) }), [])
    return (
      <TurnBody
        iterations={iterations}
        liveProgress={makeSnapshot(iterations, frame)}
        level="minimal"
        mergeTools={true}
        turnID={1}
      />
    )
  },
)

function renderDriver(iterations: WebIteration[]) {
  const api = createRef<{ push: (f: number) => void }>()
  render(
    <I18nProvider>
      <FrameDriver ref={api} iterations={iterations} />
    </I18nProvider>,
  )
  return api
}

describe('REPRO Trace-20260912T100816 — 流式帧不得重渲染已提交迭代（代价与 turn 长度无关）', () => {
  it('liveProgress 换引用时，已提交迭代子组件零渲染；渲染次数不随迭代数增长', () => {
    const frames = 5
    const measure = (n: number): { initial: number; perFrame: number } => {
      counters.committed = 0
      const iterations = makeIterations(n)
      const api = renderDriver(iterations)
      // 首帧：全部已提交迭代各渲染一次（每个迭代：thinking + tools）
      const initial = counters.committed
      const before = counters.committed
      for (let f = 2; f <= frames + 1; f++) {
        act(() => api.current?.push(f))
      }
      const perFrame = (counters.committed - before) / frames
      return { initial, perFrame }
    }

    const small = measure(20)
    const big = measure(200)

    // 首帧必须渲染全部已提交迭代（每个迭代：thinking + tools 各一次）
    expect(small.initial).toBe(20 * 2)
    expect(big.initial).toBe(200 * 2)

    // 流式帧：不重渲染已提交迭代（修复前 = 每帧 n 次 → 代价 ∝ turn 长度）
    expect(small.perFrame).toBe(0)
    expect(big.perFrame).toBe(0)

    // 每帧渲染次数与迭代数完全无关
    expect(big.perFrame).toBe(small.perFrame)
  })
})
