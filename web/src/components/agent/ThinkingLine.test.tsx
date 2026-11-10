/**
 * 思考块形态守护：
 *   1. 只有一种"思考中"渲染 —— brain 图标 + ThinkingLine；不得出现 `▸` 折叠行。
 *   2. live（流式）与 committed（历史）两种形态控件完全一致（同组件、同 className、同交互）。
 *   3. 展开态**不做任何缓存**（用户 2026-10-20：进程内 Map 缓存导致没点过的迭代
 *      也展开，且"刷新就没了"）。remount 后回到默认折叠。
 */
import { describe, expect, it } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom'

import { IterationGroup } from '@/components/agent/IterationHistory'
import { ThinkingLine } from '@/components/agent/ThinkingLine'
import { I18nProvider } from '@/providers/i18n'
import type { WebIteration } from '@/types/shared'

const wrapper = ({ children }: { children: React.ReactNode }) => <I18nProvider>{children}</I18nProvider>

const iter = (reasoning: string): WebIteration =>
  ({ iteration: 1, content: '', reasoning, tools: [], toolCount: 0 }) as unknown as WebIteration

describe('思考块：唯一形态 + 两形态完全一致', () => {
  it('committed 迭代用 brain 图标 ThinkingLine，不再有 ▸ 折叠行 / fold-arrow', () => {
    const { container } = render(<IterationGroup iteration={iter('abcdef')} />, { wrapper })
    expect(screen.getByTestId('thinking-line')).toBeInTheDocument()
    expect(container.querySelector('.lucide-brain')).toBeInTheDocument()
    expect(container.querySelector('.fold-arrow')).toBeNull()
    expect(container.querySelector('.fold-container')).toBeNull()
    expect(container.textContent).toContain('Thought 6 chars')
  })

  it('live 与 committed 的思考控件 className/结构逐字一致', () => {
    const live = render(
      <ThinkingLine label="Thought 6 chars">
        <span>live body</span>
      </ThinkingLine>,
      { wrapper },
    )
    const liveBtn = live.getByTestId('thinking-line')
    const liveClass = liveBtn.className
    const liveStyle = liveBtn.getAttribute('style')
    live.unmount()

    const committed = render(
      <IterationGroup iteration={iter('abcdef')} />,
      { wrapper },
    )
    const committedBtn = committed.container.querySelector('[data-testid="thinking-line"]') as HTMLElement

    expect(committedBtn.className).toBe(liveClass)
    expect(committedBtn.getAttribute('style')).toBe(liveStyle)
    expect(liveClass).toContain('w-fit')
    expect(committedBtn.className).toContain('w-fit')
  })
})

describe('思考块展开态不缓存', () => {
  it('默认收起（内容不挂载）', () => {
    render(
      <ThinkingLine label="X">
        <span>body</span>
      </ThinkingLine>,
      { wrapper },
    )
    expect(screen.queryByText('body')).toBeNull()
  })

  it('点击可展开 / 再点收起', async () => {
    render(
      <ThinkingLine label="X">
        <span>body</span>
      </ThinkingLine>,
      { wrapper },
    )
    const btn = screen.getByTestId('thinking-line')
    fireEvent.click(btn)
    expect(screen.getByText('body')).toBeInTheDocument()
    fireEvent.click(btn)
    // 收起走 AnimatedCollapse 的 180ms 过渡 + unmountOnClose，需等待卸载
    await waitFor(() => expect(screen.queryByText('body')).toBeNull())
  })

  it('remount（live → committed 形态切换）后回到折叠：不记忆展开态', () => {
    const first = render(
      <ThinkingLine label="X">
        <span>body</span>
      </ThinkingLine>,
      { wrapper },
    )
    fireEvent.click(first.getByTestId('thinking-line'))
    expect(screen.getByText('body')).toBeInTheDocument()
    first.unmount()

    render(<IterationGroup iteration={iter('abcdef')} />, { wrapper })
    // 新实例默认折叠 —— 2026-10-20 修复的核心：展开态不再跨实例/跨 turn 泄漏
    expect(screen.queryByText('abcdef')).toBeNull()
  })
})
