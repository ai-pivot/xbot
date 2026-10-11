import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import type { ReactElement } from 'react'

import { AssistantMessage } from '@/components/agent/AssistantMessage'
import i18n from '@/i18n'
import { I18nProvider } from '@/providers/i18n'
import type { ChatMessage, WebIteration, WebToolProgress } from '@/types/shared'

// 复制菜单标签走 i18n（agent.copyMenu.*）⇒ 断言语言两侧钉死（jsdom 默认 en-US）。
beforeAll(async () => {
  await i18n.changeLanguage('zh-CN')
})

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

function iter(content: string, iteration = 1): WebIteration {
  return { iteration, content, reasoning: '', tools: [], toolCount: 0 }
}

describe('复制入口（电脑右键 / 手机长按；每个迭代独立）', () => {
  it('右键消息 → 四个变体菜单；"复制回复"写入剪贴板（iterations-only 也拿得到）', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.assign(navigator, { clipboard: { writeText } })
    // 顶层 content 为空、回复只在 iterations 里（老实现此处"按钮没了"）
    const m = msg({ content: '', iterations: [iter('先看一眼', 1), iter('## 今日要点', 2)] })
    renderMsg(<AssistantMessage message={m} />)
    const target = document.querySelector('[data-copy-target="message"]') as HTMLElement
    expect(target).toBeTruthy()
    fireEvent.contextMenu(target)
    const menu = screen.getByTestId('copy-menu')
    expect(menu.textContent).toContain('复制含思考')
    expect(menu.textContent).toContain('复制含工具调用')
    expect(menu.textContent).toContain('查看原始 Markdown')
    fireEvent.click(screen.getByText('复制回复'))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('## 今日要点'))
  })

  it('**每个迭代**都有独立的复制目标（用户：不是说每个 iter 都有吗）', () => {
    const m = msg({ content: 'r', iterations: [iter('a', 1), iter('b', 2), iter('c', 3)] })
    renderMsg(<AssistantMessage message={m} />)
    expect(document.querySelectorAll('[data-copy-target="iteration"]').length).toBe(3)
  })

  it('右键某个迭代 → "复制该迭代正文" 只复制该迭代（不串到别的迭代）', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.assign(navigator, { clipboard: { writeText } })
    renderMsg(<AssistantMessage message={msg({ content: '', iterations: [iter('first', 1), iter('second', 2)] })} />)
    const its = document.querySelectorAll('[data-copy-target="iteration"]')
    fireEvent.contextMenu(its[0] as HTMLElement)
    fireEvent.click(screen.getByText('复制该迭代正文'))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('first'))
  })

  it('工具级：该迭代的每个工具各一项（可单独复制该工具输出）', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.assign(navigator, { clipboard: { writeText } })
    const m = msg({
      content: 'r',
      iterations: [
        {
          iteration: 1,
          content: 'c',
          reasoning: '',
          tools: [{ name: 'Shell', label: 'Shell ls', detail: 'out1', status: 'done' } as unknown as WebToolProgress],
          toolCount: 1,
        },
      ],
    })
    renderMsg(<AssistantMessage message={m} />)
    const t = document.querySelector('[data-copy-target="tools"]') as HTMLElement
    expect(t).toBeTruthy()
    fireEvent.contextMenu(t)
    expect(screen.getByTestId('copy-menu').textContent).toContain('Shell ls')
    fireEvent.click(screen.getByText('复制：Shell ls'))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('out1'))
  })

  it('不再有常驻/悬浮工具条（用户：这个悬浮太丑了还挡着）', () => {
    renderMsg(<AssistantMessage message={msg({ content: 'reply' })} />)
    expect(screen.queryByTestId('msg-actions')).toBeNull()
    expect(screen.queryByTestId('msg-copy')).toBeNull()
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

  it('renders ONLY the compressing indicator (no "thinking…" stacked above it)', () => {
    // REPRO（用户报告截图：`thinking…` 叠在 `Compressing context…` 上方）：
    // 压缩期间 streaming=true 且无内容 → LiveIteration 的空内容分支渲染
    // ShimmerThinking（.sweep-text），与本组件的压缩指示器同时出现。
    // 不变量：每个状态下有且只有一个状态指示器 —— 压缩期间归压缩指示器。
    const m = msg({ isPartial: true, iterations: [] })
    const { container } = renderMsg(
      <AssistantMessage message={m} progress={compressing()} />,
    )
    expect(container.querySelectorAll('.sweep-text').length).toBe(0)
    expect(container.textContent).not.toMatch(/思考中|thinking/)
    expect(container.querySelector('.animate-spin')).not.toBeNull()
  })
})

describe('AssistantMessage 展示区域分隔条（D1）', () => {
  it('regionsBefore > 0 ⇒ 行顶渲染分隔条（count 来自 regions_before）', () => {
    const m = msg({
      turnID: 7,
      iterations: [iter('latest', 52)],
      regionsBefore: 137,
    })
    renderMsg(<AssistantMessage message={m} />)
    const divider = screen.getByTestId('regions-divider')
    expect(divider.getAttribute('data-regions-before')).toBe('137')
    expect(divider.textContent).toContain('137')
    // 分隔条在迭代之前（该 turn 所有已加载迭代的上方）
    const turnBody = document.querySelector('.iter-blocks')
    expect(turnBody).not.toBeNull()
    expect(divider.compareDocumentPosition(turnBody!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('regionsBefore 缺省/0 ⇒ 零新增 DOM（默认视图与现状零差异）', () => {
    renderMsg(<AssistantMessage message={msg({ iterations: [iter('only', 1)] })} />)
    expect(screen.queryByTestId('regions-divider')).toBeNull()
    // 旧「更早的 N 个迭代未加载」提示块已彻底移除（语义由 regionsBefore 取代）
    // —— 即便载荷仍带 iterationsTruncated，也不得再渲染任何提示节点。
    expect(document.querySelector('[data-testid="iterations-truncated"]')).toBeNull()
  })

  it('iterationsTruncated 残留字段不得再渲染死钩子（P2 移除守护）', () => {
    const m = msg({ iterations: [iter('latest', 52)], iterationsTruncated: 137 })
    renderMsg(<AssistantMessage message={m} />)
    expect(document.querySelector('[data-testid="iterations-truncated"]')).toBeNull()
    expect(document.body.textContent).not.toContain('未加载')
  })
})
