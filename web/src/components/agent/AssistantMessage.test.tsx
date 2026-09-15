import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
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

function iter(content: string, iteration = 1): WebIteration {
  return { iteration, content, reasoning: '', tools: [], toolCount: 0 }
}

describe('AssistantMessage copy affordance (MessageActions)', () => {
  it('always mounts the copy button — 不再条件挂载（"突然冒出来/没了"的根因）', () => {
    // 用户 2026-09-15 报告：复制按钮"总是突然冒出来"、且某些消息下"没了"。
    // 根因是条件挂载 + 判据用错字段（`!isStreaming && !!content`）。新契约：按钮恒在 DOM 里，
    // 只是 hover 时才可见（absolute ⇒ 零占高），没有内容时 disabled。
    renderMsg(<AssistantMessage message={msg({ content: '' })} />)
    expect(screen.getByTestId('msg-copy')).toBeTruthy()
    expect(screen.getByTestId('msg-actions')).toBeTruthy()
  })

  it('copies the reply even when it only lives inside iterations（顶层 content 为空）', async () => {
    // v55 架构下回复常只存在于 iterations ⇒ 旧判据 `!!message.content` 为假、按钮不渲染
    //（用户："复制按钮怎么没了"）。新判定 resolveCopyText 回退到最后一迭代正文。
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.assign(navigator, { clipboard: { writeText } })
    const m = msg({ content: '', iterations: [iter('先看一眼', 1), iter('## 今日要点\n1. 扩容完成', 2)] })
    renderMsg(<AssistantMessage message={m} />)
    fireEvent.click(screen.getByTestId('msg-copy'))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('## 今日要点\n1. 扩容完成'))
  })

  it('disables copy when there is nothing to copy（display-only / 空消息）', () => {
    renderMsg(<AssistantMessage message={msg({ content: '', iterations: [], displayOnly: true })} />)
    expect((screen.getByTestId('msg-copy') as HTMLButtonElement).disabled).toBe(true)
  })

  it('exposes 含思考/含工具/原始 Markdown 变体菜单', () => {
    renderMsg(<AssistantMessage message={msg({ content: 'reply', iterations: [iter('reply')] })} />)
    fireEvent.click(screen.getByTestId('msg-more'))
    const menu = screen.getByTestId('msg-menu')
    expect(menu.textContent).toContain('复制含思考')
    expect(menu.textContent).toContain('复制含工具调用')
    expect(menu.textContent).toContain('查看原始 Markdown')
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
      { iteration: 1, content: 'a', reasoning: '', tools: [], toolCount: 0 },
      { iteration: 2, content: 'b', reasoning: '', tools: [], toolCount: 0 },
    ],
    subAgents: [],
    todos: [],
      goal: null,
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
          content: 'a',
          reasoning: '',
          tools: [{ name: 'Read', status: 'done' as const, iteration: 1, label: '', elapsedMs: 0, summary: '', detail: '', args: '', toolHints: '' }],
          toolCount: 1,
        },
        {
          iteration: 2,
          content: 'b',
          reasoning: '',
          tools: [{ name: 'FileReplace', status: 'done' as const, iteration: 2, label: '', elapsedMs: 0, summary: '', detail: '', args: '', toolHints: '' }],
          toolCount: 1,
        },
      ],
    })
    const { container } = renderMsg(
      <AssistantMessage message={m} progress={progress()} />,
    )
    expect(container.querySelectorAll('.sweep-text').length).toBe(1)
    // The single indicator is the LiveIteration one (inside data-iter-id="live"),
    // NOT a second one appended by AssistantMessage.
    const liveIndicators = container.querySelectorAll('[data-iter-id="live"] .sweep-text')
    expect(liveIndicators.length).toBe(1)
  })

  it('renders the thinking indicator when iterationHistory is EMPTY (first iteration / pre-first-SSE)', () => {
    // REPRO（切换会话后新 turn 空白）：M4 下 turn_started 立即建 live 行
    // （EMPTY_LIVE，无已完成迭代）→ 行外 busy placeholder（liveId !== null）
    // 不渲染 → 第一迭代的"思考中"必须由 LiveIteration 渲染（2026-09-04 修复：
    // 空内容分支去掉 iterationHistory.length > 0 限制）。AssistantMessage 的
    // 行级 indicator 已删除（与 LiveIteration 双渲染根治）。
    const m = msg({ isPartial: true, iterations: [] })
    const { container } = renderMsg(
      <AssistantMessage message={m} progress={progress({ iterationHistory: [], lastIter: 0 })} />,
    )
    expect(container.querySelectorAll('.sweep-text').length).toBe(1)
    // 唯一的 indicator 来自 LiveIteration（data-iter-id="live" 内部），
    // 不是 AssistantMessage 追加的第二个。
    expect(container.querySelectorAll('[data-iter-id="live"] .sweep-text').length).toBe(1)
  })

  it('does NOT render the thinking indicator when the turn has no live progress', () => {
    const m = msg({ content: 'final', iterations: [iter('done')] })
    const { container } = renderMsg(<AssistantMessage message={m} />)
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
      { iteration: 1, content: 'done work', reasoning: '', tools: [], toolCount: 0 },
    ],
    subAgents: [],
    todos: [],
      goal: null,
    ...over,
  })

  it('renders the compressing indicator AFTER turn content (tail), not at the top', () => {
    const m = msg({ isPartial: true, iterations: [] })
    const { container } = renderMsg(
      <AssistantMessage message={m} progress={compressing()} />,
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

describe('AssistantMessage truncated iterations notice', () => {
  it('renders "更早的 N 个迭代未加载" when history carried iterations_truncated', () => {
    // 用户 2026-09-15：历史响应按 turn 尾部截断迭代（加载时间随迭代数线性增长的修复）——
    // 丢弃数量必须显示出来，绝不静默缺块。
    const m = msg({ iterations: [iter('latest')], iterationsTruncated: 137 })
    renderMsg(<AssistantMessage message={m} />)
    const notice = screen.getByTestId('iterations-truncated')
    expect(notice.textContent).toContain('137')
  })

  it('renders no notice when nothing was truncated', () => {
    renderMsg(<AssistantMessage message={msg({ iterations: [iter('only')] })} />)
    expect(screen.queryByTestId('iterations-truncated')).toBeNull()
  })
})
