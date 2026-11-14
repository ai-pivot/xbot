/**
 * P0 回归：reasoning 绝不能渲染在 content 之后。
 *
 * 2026-11-03 用户截图实证：一轮 turn 结束后，正文（content）显示在前，
 * 折叠的「思考 N 字」出现在 turn 的**最末尾**。根因：
 *   - turn 提交后 `liveProgress` 仍非 null，`reasoningStreamContent` 是历史
 *     iteration 的增量快照；
 *   - 原抑制判据是**全等比较**（`historyTextIndex.reasonings.has(reasoningContent)`），
 *     快照长度不同 ⇒ 判 false ⇒ 漏抑制；
 *   - TurnBody 先渲染 CommittedTurn（content 在前），再渲染 LiveIteration
 *     （残留 reasoning 在后）⇒ 思考出现在正文之后。
 *
 * 修复：`!progress.streaming && iterationHistory.length > 0` ⇒ live 的
 * reasoning 一律不渲染（它必然属于某个已提交迭代）。
 */
import { describe, expect, it } from 'vitest'
import '@testing-library/jest-dom'

import { LiveIteration } from '@/components/agent/LiveIteration'
import { renderWithProviders } from '@/test-utils'
import type { ProgressSnapshot } from '@/types/shared'

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

const committedIteration = {
  iteration: 1,
  content: '所以 11ms 里有 10ms 是 D1/D2 的真实开销，不是武装税。',
  reasoning: '思考内容（历史里的完整版本，长度与 live 快照不同）',
  tools: [],
  toolCount: 0,
}

describe('LiveIteration — P0 reasoning 不得出现在 content 之后', () => {
  it('turn 已提交（streaming=false）且历史非空时，残留 reasoning 必须被抑制', () => {
    const snapshot = makeSnapshot({
      streaming: false,
      // live 快照是历史的**增量前缀**，长度不同 ⇒ 全等比较判 false
      reasoningStreamContent: '思考内容',
      lastReasoning: '思考内容',
      streamContent: '',
      content: '',
      iterationHistory: [committedIteration],
      lastIter: 1,
    })
    const { container } = renderWithProviders(<LiveIteration progress={snapshot} />)
    // 不得渲染思考折叠行
    expect(container.querySelector('[data-testid="thinking-line"]')).toBeNull()
    // turn 已结束后 live 块应整体不渲染（也绝不显示「思考中…」占位）
    expect(container.innerHTML).toBe('')
  })

  it('同一份 reasoning 全等时仍然抑制（原判据保持）', () => {
    const snapshot = makeSnapshot({
      streaming: false,
      reasoningStreamContent: committedIteration.reasoning,
      iterationHistory: [committedIteration],
      lastIter: 1,
    })
    const { container } = renderWithProviders(<LiveIteration progress={snapshot} />)
    expect(container.querySelector('[data-testid="thinking-line"]')).toBeNull()
  })

  it('仍在流式（streaming=true）时 reasoning 正常渲染，不被误杀', () => {
    const snapshot = makeSnapshot({
      streaming: true,
      phase: 'thinking',
      reasoningStreamContent: '正在思考的这一轮内容',
      iterationHistory: [committedIteration],
      lastIter: 1,
      iteration: 2,
    })
    const { container } = renderWithProviders(<LiveIteration progress={snapshot} />)
    expect(container.querySelector('[data-testid="thinking-line"]')).not.toBeNull()
  })

  it('第一轮（历史为空）且已不流式时，reasoning 仍渲染（不误杀）', () => {
    const snapshot = makeSnapshot({
      streaming: false,
      reasoningStreamContent: '第一轮的思考',
      iterationHistory: [],
      lastIter: 0,
    })
    const { container } = renderWithProviders(<LiveIteration progress={snapshot} />)
    expect(container.querySelector('[data-testid="thinking-line"]')).not.toBeNull()
  })
})
