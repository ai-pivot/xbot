/**
 * 思考块唯一形态守护（2026-09-12 用户要求）：
 *   1. 只有**一种**"思考中"渲染 —— brain 图标 + ThinkingLine（w-fit 点击热区、
 *      AnimatedCollapse 动画）；不得再出现 `▸` 折叠行（FoldedLine 已删除）。
 *   2. live（流式迭代）与 committed（历史迭代）两种形态的控件**必须完全一致**：
 *      同一组件、同一 className、同一交互（点击切换）。
 *   3. commit（形态切换必然 remount）**不得自动收起**已展开的思考块 —— 展开态由
 *      `reasoningOpenState` 按 `turnID:iteration` 共享。
 */
import { describe, expect, it, beforeEach } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import '@testing-library/jest-dom'

import { IterationGroup } from '@/components/agent/IterationHistory'
import { ThinkingLine, iterationReasoningKey } from '@/components/agent/ThinkingLine'
import { __resetReasoningOpenState, getReasoningOpen, reasoningKey } from '@/components/agent/reasoningOpenState'
import { I18nProvider } from '@/providers/i18n'
import type { WebIteration } from '@/types/shared'

const wrapper = ({ children }: { children: React.ReactNode }) => <I18nProvider>{children}</I18nProvider>

const iter = (reasoning: string): WebIteration =>
  ({ iteration: 1, content: '', reasoning, tools: [], toolCount: 0 }) as unknown as WebIteration

beforeEach(() => __resetReasoningOpenState())

describe('思考块：唯一形态 + 两形态完全一致', () => {
  it('committed 迭代用 brain 图标 ThinkingLine，不再有 ▸ 折叠行 / fold-arrow', () => {
    const { container } = render(<IterationGroup iteration={iter('abcdef')} reasoningStateKey="7:1" />, { wrapper })
    expect(screen.getByTestId('thinking-line')).toBeInTheDocument()
    expect(container.querySelector('.lucide-brain')).toBeInTheDocument()
    expect(container.querySelector('.fold-arrow')).toBeNull()
    expect(container.querySelector('.fold-container')).toBeNull()
    // 文案与流式态同一 key（agent.thoughtChars）
    expect(container.textContent).toContain('Thought 6 chars')
  })

  it('live 与 committed 的思考控件 className/结构逐字一致（同组件 → 交互无差别）', () => {
    const live = render(
      <ThinkingLine label="Thought 6 chars" stateKey={reasoningKey(7, 1)}>
        <span>live body</span>
      </ThinkingLine>,
      { wrapper },
    )
    const liveBtn = live.getByTestId('thinking-line')
    const liveClass = liveBtn.className
    const liveStyle = liveBtn.getAttribute('style')
    live.unmount()

    const committed = render(<IterationGroup iteration={iter('abcdef')} reasoningStateKey={iterationReasoningKey(7, 1)} />, {
      wrapper,
    })
    const committedBtn = committed.container.querySelector('[data-testid="thinking-line"]') as HTMLElement

    expect(committedBtn.className).toBe(liveClass)
    expect(committedBtn.getAttribute('style')).toBe(liveStyle)
    // 热区形状一致（w-fit）：两者都不占满整行
    expect(liveClass).toContain('w-fit')
    expect(committedBtn.className).toContain('w-fit')
  })
})

describe('思考块展开态跨形态保留（commit 不自动收起）', () => {
  it('展开 → remount（模拟 live→committed 形态切换）→ 仍保持展开', () => {
    const key = reasoningKey(7, 1)
    const first = render(
      <ThinkingLine label="Thought 6 chars" stateKey={key}>
        <span>body</span>
      </ThinkingLine>,
      { wrapper },
    )
    // 初始收起（lazy + unmountOnClose：内容未挂载）
    expect(screen.queryByText('body')).toBeNull()
    fireEvent.click(first.getByTestId('thinking-line'))
    expect(screen.getByText('body')).toBeInTheDocument()
    expect(getReasoningOpen(key)).toBe(true)

    // 形态切换：卸载后用**同一 key** 重新挂载（committed IterationGroup 走同 key）
    first.unmount()
    render(<IterationGroup iteration={iter('abcdef')} reasoningStateKey={key} />, { wrapper })
    expect(screen.getByText('abcdef')).toBeInTheDocument()
  })

  it('不同迭代的 key 互不影响', () => {
    const a = render(
      <ThinkingLine label="A" stateKey={reasoningKey(7, 1)}>
        <span>body-a</span>
      </ThinkingLine>,
      { wrapper },
    )
    fireEvent.click(a.getByTestId('thinking-line'))
    expect(getReasoningOpen(reasoningKey(7, 1))).toBe(true)
    expect(getReasoningOpen(reasoningKey(7, 2))).toBeUndefined()

    render(
      <ThinkingLine label="B" stateKey={reasoningKey(7, 2)}>
        <span>body-b</span>
      </ThinkingLine>,
      { wrapper },
    )
    // 第二个迭代默认收起（不被第一个的展开态污染）
    expect(screen.queryByText('body-b')).toBeNull()
  })

  it('无 stateKey 时保持原语义（默认收起，点击可展开）', () => {
    const { getByTestId } = render(
      <ThinkingLine label="X">
        <span>body-x</span>
      </ThinkingLine>,
      { wrapper },
    )
    expect(screen.queryByText('body-x')).toBeNull()
    fireEvent.click(getByTestId('thinking-line'))
    expect(screen.getByText('body-x')).toBeInTheDocument()
  })
})
