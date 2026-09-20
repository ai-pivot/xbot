/**
 * live 区不得重复渲染已 committed 的同一迭代。
 *
 * 现场（2026-09-17 用户报告 + 真实 DOM 实证）：
 *   <div class="iter-block" data-iter-id="24">…Thought 384 chars + 正文 + tools…</div>
 *   <div class="iter-block" data-iter-id="live" data-iter-num="24">
 *      <button data-testid="thinking-line">Thought 384 chars</button>   ← 只有标题 ⇒ 红框那片空白
 *   </div>
 *
 * 触发条件：AskUser 在迭代 N 内部调用 ⇒ 迭代 N 已进 iteration_history（committed），
 * 而 turn 暂停在 WaitingUser（没有下一次迭代推进）⇒ live 的 iteration 仍是 N，
 * 且 live 仍持有同一段 reasoning（reasoningStreamContent / lastReasoning）
 * ⇒ live 区又渲染一个思考块（折叠态 ⇒ 只有标题 + 空白）。
 *
 * 契约：live 区只渲染「尚未成为历史记录的进行中部分」——reasoning（以及文本）与
 * 最后一个已完成迭代**相同**时不得再渲染（与既有的 effectiveStreamContent 同源）。
 *
 * 判别力：把 LiveIteration 里 `effectiveReasoning` 的抑制去掉 ⇒ 本用例必红（2 个思考块）。
 */
import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import '@testing-library/jest-dom'

import { TurnBody } from '@/components/agent/TurnBody'
import { I18nProvider } from '@/providers/i18n'
import type { ProgressSnapshot, WebIteration } from '@/types/shared'

const REASONING = 'R'.repeat(384)

const iters = [
  { iteration: 1, content: '正文内容', reasoning: REASONING, tools: [], toolCount: 0 },
] as unknown as WebIteration[]

const liveSnapshot = {
  turnID: 1,
  iteration: 1,
  phase: 'tool_exec',
  streaming: false,
  // live 仍持有与 committed 迭代**完全相同**的 reasoning（现场实测 384 chars）
  reasoningStreamContent: REASONING,
  lastReasoning: REASONING,
  streamContent: '',
  content: '',
  iterationHistory: iters,
  activeTools: [],
  completedTools: [],
  streamingTools: [],
  subAgents: [],
  todos: [],
} as unknown as ProgressSnapshot

const wrap = (children: React.ReactNode) => <I18nProvider>{children}</I18nProvider>

describe('live 区与 committed 迭代的去重', () => {
  it('iter N 已 committed 且 live 仍是 N（AskUser 暂停）⇒ 思考块只能有一个', () => {
    render(wrap(<TurnBody iterations={iters} liveProgress={liveSnapshot} turnID={1} />))
    expect(screen.getAllByTestId('thinking-line')).toHaveLength(1)
  })

  it('live 的 reasoning 与 committed 不同（真正的新迭代内容）⇒ 两个块都要渲染（不误杀）', () => {
    const liveNewReasoning = {
      ...liveSnapshot,
      reasoningStreamContent: 'Q'.repeat(120),
      lastReasoning: 'Q'.repeat(120),
    } as unknown as ProgressSnapshot
    render(wrap(<TurnBody iterations={iters} liveProgress={liveNewReasoning} turnID={1} />))
    expect(screen.getAllByTestId('thinking-line')).toHaveLength(2)
  })
})
