/**
 * TurnBody tests — thinking char count（逐迭代渲染路径）。
 *
 * REPRO（2026-09-04 用户报告："committed 之后数字不对，应该永远显示正确的多少 char"）：
 * reasoning label 曾用估算（670 字符显示"思考 167 字"），与 IterationHistory 路径
 * （`iteration.reasoning.length` 真实值）语义分裂。
 * 修复：统一为 i18n `agent.thoughtChars`（真实 iteration.reasoning.length；与流式态同一 key/同一组件）。
 * 折叠/合并路径已彻底删除（2026-09-12）—— 只剩逐迭代渲染这一条路径。
 */
import { describe, expect, it } from 'vitest'
import '@testing-library/jest-dom'

import { TurnBody } from '@/components/agent/TurnBody'
import { renderWithProviders } from '@/test-utils'
import type { ProgressSnapshot, WebIteration } from '@/types/shared'

describe('TurnBody thinking char count (真实 char 数，不是 /4 估算)', () => {
  it('REPRO: committed reasoning label shows REAL char count（reasoning.length），不是 Math.ceil(len/4) 估算', () => {
    // 旧代码：Math.ceil(670/4)=168 → "思考 168 字"（用户 DOM 实测 167）
    // 修复后：真实 670 → 与流式态统一为 agent.thoughtChars（en: "Thought 670 chars"）
    const reasoning = 'x'.repeat(670)
    const iterations: WebIteration[] = [
      { iteration: 1, content: '', reasoning, tools: [], toolCount: 0 },
    ]
    const { container } = renderWithProviders(
      <TurnBody iterations={iterations} turnID={3159} />,
    )
    const text = container.textContent ?? ''
    // 完整 i18n 文案必须出现（测试环境默认 en: 'Thought {{count}} chars'）——
    // 只断言数字会漏估算值回归（168 也含 "16"），所以断言整串。
    expect(text).toContain('Thought 670 chars')
    // /4 估算值（168）不得出现
    expect(text).not.toMatch(/16[0-9]\b/)
  })

  it('短 reasoning（<4 字符）也显示真实值 —— 不四舍五入到 0/1', () => {
    const iterations: WebIteration[] = [
      { iteration: 1, content: '', reasoning: 'ab', tools: [], toolCount: 0 },
    ]
    const { container } = renderWithProviders(
      <TurnBody iterations={iterations} turnID={1} />,
    )
    // 真实 2 字符（旧 /4 估算 Math.ceil(2/4)=1）—— 必须断言完整文案，
    // `toMatch(/2/)` 这种弱断言任何含 "2" 的文本都会通过（假绿）。
    expect(container.textContent).toContain('Thought 2 chars')
  })
})

/**
 * 渲染隔离（**性能与 turn 内迭代数解耦**，2026-09-13 trace 归因）：
 * trace 显示 App JS 不随时间增长，但浏览器侧 Layout/Paint/Raster 涨 1.7–3.3×、
 * `Layout.dirtyObjects` 16→64（×4）、7 次 `UpdateLayoutTree` 单次重算 ~4800 个元素
 * （整个 turn 子树）—— 迭代块之间没有 containment，失效范围随迭代数膨胀。
 * 修复：每个迭代块 `iter-block`（contain + content-visibility:auto 离屏跳过），
 * 进行中迭代 `iter-block-live`（恒渲染，不吃跳过机制）。
 * 规则本体在 index.css，选择器断言在 index.test.ts。
 */
function makeSnapshot(history: WebIteration[], iteration: number): ProgressSnapshot {
  return {
    eventSeq: 1,
    phase: 'content',
    iteration,
    streamContent: '',
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
    turnID: 7,
  } as ProgressSnapshot
}

describe('迭代块渲染隔离（perf：代价与 turn 内迭代数无关）', () => {
  it('每个已提交迭代块都带 iter-block（独立 containment 上下文）', () => {
    const iterations: WebIteration[] = [1, 2, 3].map((n) => ({
      iteration: n,
      content: `c${n}`,
      reasoning: '',
      tools: [],
      toolCount: 0,
    }))
    const { container } = renderWithProviders(
      <TurnBody iterations={iterations} turnID={7} />,
    )
    const blocks = Array.from(container.querySelectorAll('[data-iter-id]'))
    expect(blocks.length).toBe(3)
    for (const b of blocks) {
      expect(b.classList.contains('iter-block')).toBe(true)
    }
  })

  it('进行中迭代块带 iter-block-live（恒渲染，离屏跳过不作用于它）', () => {
    const iterations: WebIteration[] = [
      { iteration: 1, content: 'c1', reasoning: '', tools: [], toolCount: 0 },
    ]
    const { container } = renderWithProviders(
      <TurnBody
        iterations={iterations}
        liveProgress={makeSnapshot(iterations, 2)}
        turnID={7}
      />,
    )
    const live = container.querySelector('[data-iter-id="live"]')
    expect(live).not.toBeNull()
    expect(live!.classList.contains('iter-block')).toBe(true)
    expect(live!.classList.contains('iter-block-live')).toBe(true)
  })
})
