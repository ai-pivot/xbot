import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { ReactElement } from 'react'

import { AssistantMessage } from '@/components/agent/AssistantMessage'
import { I18nProvider } from '@/providers/i18n'
import type { ChatMessage, WebIteration, WebToolProgress } from '@/types/shared'

function renderMsg(node: ReactElement) {
  return render(node, { wrapper: ({ children }) => <I18nProvider>{children}</I18nProvider> })
}

function msg(over: Partial<ChatMessage>): ChatMessage {
  return {
    id: 'a1',
    role: 'assistant',
    content: '',
    iterations: [],
    timestamp: '2026-08-11T00:00:00Z',
    isPartial: false,
    turnID: 1,
    ...over,
  }
}

// The copy button's title is localized (en: 'Copy Markdown', zh-CN: '复制 Markdown').
function copyButton() {
  return screen.queryByTitle(/Copy Markdown|复制 Markdown/)
}

function iter(thinking: string, iteration = 1): WebIteration {
  return { iteration, thinking, reasoning: '', tools: [], toolCount: 0 }
}

describe('AssistantMessage copy button (showActions)', () => {
  it('shows the copy button when content duplicates an iteration thinking (render-dedup case)', () => {
    // User report: "最终消息没有复制按钮（一开始有，然后消失）" — the copy button
    // used `!!finalContent`, and finalContent becomes '' when the final reply
    // duplicates an iteration's thinking (dedup: same text on both paths).
    // The final reply is still the user's content and MUST be copyable.
    const m = msg({
      content: 'final reply',
      iterations: [iter('final reply')], // thinking === content
    })
    renderMsg(<AssistantMessage message={m} collapseLevel="none" />)
    expect(copyButton()).not.toBeNull()
  })

  it('shows the copy button for a normal final reply', () => {
    const m = msg({
      content: 'final reply',
      iterations: [iter('reasoning text')],
    })
    renderMsg(<AssistantMessage message={m} collapseLevel="none" />)
    expect(copyButton()).not.toBeNull()
  })

  it('hides the copy button for an empty message', () => {
    const m = msg({ content: '', iterations: [] })
    renderMsg(<AssistantMessage message={m} collapseLevel="none" />)
    expect(copyButton()).toBeNull()
  })

  it('hides the copy button for a display-only message (cancel marker)', () => {
    const m = msg({ content: 'partial', iterations: [], displayOnly: true })
    renderMsg(<AssistantMessage message={m} collapseLevel="none" />)
    expect(copyButton()).toBeNull()
  })
})

describe('AssistantMessage thinking indicator (mutual exclusion with LiveIteration)', () => {
  const progress = (over: Record<string, unknown> = {}) => ({
    eventSeq: 0,
    phase: 'thinking',
    iteration: 3,
    lastIter: 2,
    streaming: true,
    streamContent: '',
    content: '',
    reasoningStreamContent: '',
    genuiContent: '',
    lastReasoning: '',
    streamTokens: 0,
    tokenUsage: null,
    turnID: 1,
    activeTools: [] as WebToolProgress[],
    completedTools: [] as WebToolProgress[],
    streamingTools: [] as WebToolProgress[],
    iterationHistory: [
      { iteration: 1, thinking: 'a', reasoning: '', tools: [], toolCount: 0 },
      { iteration: 2, thinking: 'b', reasoning: '', tools: [], toolCount: 0 },
    ],
    subAgents: [],
    todos: [],
    ...over,
  })

  it('renders only ONE "思考中…" when iterationHistory is non-empty (LiveIteration owns the placeholder)', () => {
    // User report: "切换会话后渲染两个思考中..." — after a session switch the
    // snapshot has completed iterations (iterationHistory=[1,2], lastIter=2,
    // streaming=true) but progress.completedTools is EMPTY (the tools live
    // inside iterationHistory's iterations, not in completedTools). The old
    // showThinkingIndicator condition (`!hasAnyTools`) was satisfied, so BOTH
    // AssistantMessage (here) AND LiveIteration (inside TurnBody) rendered a
    // ShimmerThinking → two "思考中…" stacked. LiveIteration is in charge of
    // the boundary placeholder whenever it has a completed predecessor — this
    // component must not render a second one.
    const m = msg({
      isPartial: true,
      iterations: [
        {
          iteration: 1,
          thinking: 'a',
          reasoning: '',
          tools: [{ name: 'Read', status: 'done' as const, iteration: 1, label: '', elapsedMs: 0, summary: '', detail: '', args: '', toolHints: '' }],
          toolCount: 1,
        },
        {
          iteration: 2,
          thinking: 'b',
          reasoning: '',
          tools: [{ name: 'FileReplace', status: 'done' as const, iteration: 2, label: '', elapsedMs: 0, summary: '', detail: '', args: '', toolHints: '' }],
          toolCount: 1,
        },
      ],
    })
    const { container } = renderMsg(
      <AssistantMessage message={m} collapseLevel="none" progress={progress()} />,
    )
    expect(container.querySelectorAll('.sweep-text').length).toBe(1)
    // The single indicator is the LiveIteration one (inside data-iter-id="live"),
    // NOT a second one appended by AssistantMessage.
    const liveIndicators = container.querySelectorAll('[data-iter-id="live"] .sweep-text')
    expect(liveIndicators.length).toBe(1)
  })

  it('renders the thinking indicator when iterationHistory is EMPTY (first iteration / pre-first-SSE)', () => {
    // The FIRST iteration has no predecessor — LiveIteration returns null
    // (needs iterationHistory.length > 0). AssistantMessage's own indicator is
    // the only one here (matches the busy-placeholder window).
    const m = msg({ isPartial: true, iterations: [] })
    const { container } = renderMsg(
      <AssistantMessage message={m} collapseLevel="none" progress={progress({ iterationHistory: [], lastIter: 0 })} />,
    )
    expect(container.querySelectorAll('.sweep-text').length).toBe(1)
  })

  it('does NOT render the thinking indicator when the turn has no live progress', () => {
    const m = msg({ content: 'final', iterations: [iter('done')] })
    const { container } = renderMsg(<AssistantMessage message={m} collapseLevel="none" />)
    expect(container.querySelectorAll('.sweep-text').length).toBe(0)
  })

  it('does NOT render "思考中…" for a stale isPartial live row whose turn is already idle (streaming=false)', () => {
    // User report: "idle之后（思考中…）渲染在最新turn的agent消息第一行".
    // A turn that started with thinking but produced nothing (PhaseDone/text both
    // lost) leaves an EMPTY live shell in MessageStore → toRows() emits an
    // isPartial assistant row. The progress snapshot has already been reset
    // (streaming=false, empty content), but isStreaming = message.isPartial=true
    // made showThinkingIndicator true → a ghost "思考中…" on the first line of
    // the agent message. showThinkingIndicator must require progress.streaming.
    const m = msg({ isPartial: true, iterations: [] })
    const { container } = renderMsg(
      <AssistantMessage
        message={m}
        collapseLevel="none"
        progress={progress({ phase: '', streaming: false, iterationHistory: [], lastIter: 0 })}
      />,
    )
    expect(container.querySelectorAll('.sweep-text').length).toBe(0)
  })
})

describe('AssistantMessage compressing indicator position', () => {
  const compressing = (over: Record<string, unknown> = {}) => ({
    eventSeq: 0,
    phase: 'compressing',
    iteration: 1,
    lastIter: 1,
    streaming: true,
    streamContent: '',
    content: '',
    reasoningStreamContent: '',
    genuiContent: '',
    lastReasoning: '',
    streamTokens: 0,
    tokenUsage: null,
    turnID: 1,
    activeTools: [] as WebToolProgress[],
    completedTools: [] as WebToolProgress[],
    streamingTools: [] as WebToolProgress[],
    iterationHistory: [
      { iteration: 1, thinking: 'done work', reasoning: '', tools: [], toolCount: 0 },
    ],
    subAgents: [],
    todos: [],
    ...over,
  })

  it('renders the compressing indicator AFTER turn content (tail), not at the top', () => {
    const m = msg({ isPartial: true, iterations: [] })
    const { container } = renderMsg(
      <AssistantMessage message={m} collapseLevel="none" progress={compressing()} />,
    )
    // The compressing indicator (Loader2 + "compressing") must come after the
    // turn body content. Its container is a sibling placed at the tail of
    // .group/msg, so its index must be greater than the TurnBody's content.
    const group = container.querySelector('.group\\/msg')
    expect(group).not.toBeNull()
    const children = Array.from(group!.children)
    const compressingIdx = children.findIndex((el) => el.textContent?.includes('compressing') || el.querySelector('.animate-spin'))
    // There must be a child rendered for the turn body before the indicator.
    expect(compressingIdx).toBeGreaterThan(0)
    // The indicator must NOT be the FIRST child (that would be the "turn top").
    expect(compressingIdx).not.toBe(0)
  })
})
