/**
 * RegionsDivider —— 「更早区域」取回哨兵（D1 线）。
 *
 * ⛔⛔ 不变量（用户 2026-10-11 明确要求）：**消息不连续零容忍**。渲染出的历史任何时候
 * 都必须连续；绝不允许在可见流里出现 `iter x → 加载哨兵 → iter x+y（y≠1）` 这种「中间
 * 跳过 y 条」的结构 —— 也**不允许**出现 `content - 折叠 - content` 的可见折叠条。
 *
 * 因此哨兵**不可见**：idle / loading 都不渲染任何可见物（无折线、无「更早的 N 个区域」
 * 文字、无 spinner）；只有显式失败态才给一个小的「重试」出口。
 */
import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { RegionsDivider } from '@/components/agent/RegionsDivider'

describe('RegionsDivider —— 不可见哨兵（消息不连续零容忍）', () => {
  it('idle：不渲染任何可见物（无计数文案 / 无 spinner / 无重试）', () => {
    render(<RegionsDivider count={12} status="idle" onRetry={() => {}} />)
    const root = screen.getByTestId('regions-divider')
    expect(root.getAttribute('data-state')).toBe('idle')
    expect(root.getAttribute('data-regions-before')).toBe('12')
    // 关键：DOM 里不得出现任何可见的「更早的 N 个区域」/ spinner / 折线文案。
    expect(root.textContent ?? '').toBe('')
    expect(screen.queryByTestId('regions-divider-label')).toBeNull()
    expect(screen.queryByTestId('regions-divider-spinner')).toBeNull()
    expect(screen.queryByTestId('regions-divider-retry')).toBeNull()
  })

  it('loading：同样不产生可见物（视觉零新增）', () => {
    render(<RegionsDivider count={12} status="loading" onRetry={() => {}} />)
    const root = screen.getByTestId('regions-divider')
    expect(root.getAttribute('data-state')).toBe('loading')
    expect(root.textContent ?? '').toBe('')
  })

  it('error：唯一的例外 —— 给出重试按钮；点击 → onRetry', () => {
    const onRetry = vi.fn()
    render(<RegionsDivider count={12} status="error" onRetry={onRetry} />)
    const retry = screen.getByTestId('regions-divider-retry')
    fireEvent.click(retry)
    expect(onRetry).toHaveBeenCalledTimes(1)
  })

  it('idle / loading 根节点 class 完全一致且高度为 1px（不可见、布局可预测）', () => {
    const classes: string[] = []
    for (const status of ['idle', 'loading'] as const) {
      const { unmount } = render(<RegionsDivider count={5} status={status} onRetry={() => {}} />)
      classes.push(screen.getByTestId('regions-divider').className)
      unmount()
    }
    expect(classes[0]).toBe(classes[1])
    expect(classes[0]).toContain('h-px')
    expect(classes[0]).toContain('bg-transparent')
  })

  it('哨兵 ref 挂在根节点上（IO 观察目标就是哨兵本身）', () => {
    const ref = { current: null as HTMLDivElement | null }
    render(<RegionsDivider count={3} status="idle" onRetry={() => {}} sentinelRef={ref} />)
    expect(ref.current).toBe(screen.getByTestId('regions-divider'))
  })
})
