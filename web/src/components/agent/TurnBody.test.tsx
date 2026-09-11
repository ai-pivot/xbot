/**
 * TurnBody tests — per-iteration rendering.
 *
 * 每个 iteration 独立渲染（无 turn 级折叠、无跨迭代工具合并）。
 * reasoning 的字符数必须是**真实值**（`iteration.reasoning.length`），
 * 不允许 /4 之类的估算（用户要求"永远显示正确的多少 char"）。
 *
 * 另含 committed / live 两处 SubAgent 树的去重守卫。
 */
import { describe, expect, it } from 'vitest'
import '@testing-library/jest-dom'

import { TurnBody } from '@/components/agent/TurnBody'
import { renderWithProviders } from '@/test-utils'
import type { ProgressSnapshot, WebIteration, WebSubAgentProgress } from '@/types/shared'

/** 构造合法的 ProgressSnapshot（字段与 types/shared.ts 一致）。 */
function makeSnapshot(overrides: Partial<ProgressSnapshot> = {}): ProgressSnapshot {
  return {
    eventSeq: 0,
    phase: 'thinking',
    iteration: 1,
    streamContent: '',
    content: '',
    reasoningStreamContent: '',
    streaming: true,
    activeTools: [],
    completedTools: [],
    iterationHistory: [],
    streamingTools: [],
    genuiContent: '',
    lastIter: 0,
    lastReasoning: '',
    todos: [],
    goal: null,
    subAgents: [],
    tokenUsage: null,
    turnID: 0,
    ...overrides,
  }
}

function makeIteration(overrides: Partial<WebIteration> = {}): WebIteration {
  return { iteration: 1, content: '', reasoning: '', tools: [], toolCount: 0, ...overrides }
}

describe('TurnBody per-iteration rendering (real char count, not a /4 estimate)', () => {
  it('REPRO: committed reasoning label shows REAL char count，不是 Math.ceil(len/4) 估算', () => {
    // 旧代码：Math.ceil(670/4)=168 → "思考 168 字"（用户 DOM 实测 167）
    // 修复后：真实 670 → 精确的 i18n 文案（测试环境 locale 为 en）
    const reasoning = 'x'.repeat(670)
    const { container } = renderWithProviders(
      <TurnBody iterations={[makeIteration({ reasoning })]} turnID={3159} />,
    )
    const text = container.textContent ?? ''
    // 精确断言：真实字符数出现在 i18n 文案里（避免 /2/ 这类近乎恒真的匹配）
    expect(text).toContain('670')
    expect(text).toMatch(/Thought\s+670\s+characters/i)
  })

  it('短 reasoning（2 字符）显示真实值 —— 不四舍五入到 0/1', () => {
    const { container } = renderWithProviders(
      <TurnBody iterations={[makeIteration({ reasoning: 'ab' })]} turnID={1} />,
    )
    // 真实的 2 字符（旧 /4 估算 Math.ceil(2/4)=1）
    expect(container.textContent ?? '').toMatch(/Thought\s+2\s+characters/i)
  })
})

describe('TurnBody — committed SubAgent 树与 live 区去重', () => {
  it('REPRO: 同一 untagged 节点不得同时渲染在 committed 与 live 两处', () => {
    // iteration 未打标的 legacy 节点：LiveIteration 的过滤条件
    // (n.iteration === undefined || n.iteration === currentIter) 会放行它，
    // 而它也可能同时存在于 committed iteration 冻结的 subAgents 里 ——
    // 两处都渲染 = 同一张卡片出现两次 + 虚拟列表行高翻倍。
    const node: WebSubAgentProgress = { role: 'explore', instance: 'mem-1', status: 'running' }
    const iterations: WebIteration[] = [makeIteration({ subAgents: [node] })]
    const live = makeSnapshot({ iteration: 1, subAgents: [node], iterationHistory: [] })

    const { container } = renderWithProviders(
      <TurnBody iterations={iterations} liveProgress={live} />,
    )
    const occurrences = (container.textContent ?? '').split('explore').length - 1
    expect(occurrences).toBe(1)
  })

  it('live 区没有该节点时，committed 侧照常渲染（去重不能误杀）', () => {
    // 注意：SubAgentProgressTree 不渲染**顶层 done** 节点（该组件只展示
    // running / 有子节点的树），所以这里用 running。
    const node: WebSubAgentProgress = { role: 'explore', instance: 'mem-2', status: 'running' }
    const iterations: WebIteration[] = [makeIteration({ subAgents: [node] })]
    const live = makeSnapshot({ iteration: 2, subAgents: [], iterationHistory: [] })

    const { container } = renderWithProviders(
      <TurnBody iterations={iterations} liveProgress={live} />,
    )
    const occurrences = (container.textContent ?? '').split('explore').length - 1
    expect(occurrences).toBeGreaterThan(0)
  })
})
