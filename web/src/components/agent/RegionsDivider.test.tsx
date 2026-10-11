/**
 * RegionsDivider —— 「更早区域」分隔条三态 + 结构不变量（D1 线）。
 *
 * 守护点：
 *   ① 三态渲染（idle 计数 / loading spinner / error 重试）；
 *   ② **容器高度恒定**：三态根节点 class 完全一致（不得用估算高度占位，也不得
 *      在态之间改高度 —— 「加载前后滚动总高一致性」，gotchas: content-visibility 事故）；
 *   ③ 哨兵 ref 挂在根节点（IO 观察目标 = 分隔条本身）。
 */
import { fireEvent, render, screen } from '@testing-library/react'
import { beforeAll, describe, expect, it, vi } from 'vitest'

import { RegionsDivider } from '@/components/agent/RegionsDivider'
import i18n from '@/i18n'

beforeAll(async () => {
  await i18n.changeLanguage('zh-CN')
})

describe('RegionsDivider 三态', () => {
  it('idle：计数插值 + 上箭头；无 spinner / 无重试按钮', () => {
    render(<RegionsDivider count={12} status="idle" onRetry={() => {}} />)
    const root = screen.getByTestId('regions-divider')
    expect(root.getAttribute('data-state')).toBe('idle')
    expect(root.getAttribute('data-regions-before')).toBe('12')
    // i18next 插值必须是 {{count}} 双花括号（单括号会原样渲染 `{{count}}`）
    expect(screen.getByTestId('regions-divider-label').textContent).toBe('更早的 12 个区域')
    expect(screen.queryByTestId('regions-divider-spinner')).toBeNull()
    expect(screen.queryByTestId('regions-divider-retry')).toBeNull()
  })

  it('loading：spinner + 文案（不显示计数）', () => {
    render(<RegionsDivider count={12} status="loading" onRetry={() => {}} />)
    expect(screen.getByTestId('regions-divider').getAttribute('data-state')).toBe('loading')
    expect(screen.getByTestId('regions-divider-spinner')).toBeTruthy()
    expect(screen.getByText('正在加载更早区域…')).toBeTruthy()
    expect(screen.queryByTestId('regions-divider-label')).toBeNull()
  })

  it('error：重试按钮；点击 → onRetry', () => {
    const onRetry = vi.fn()
    render(<RegionsDivider count={12} status="error" onRetry={onRetry} />)
    const root = screen.getByTestId('regions-divider')
    expect(root.getAttribute('data-state')).toBe('error')
    const retry = screen.getByTestId('regions-divider-retry')
    expect(retry.textContent).toContain('加载失败 · 重试')
    fireEvent.click(retry)
    expect(onRetry).toHaveBeenCalledTimes(1)
  })

  it('三态根节点 class 完全一致（容器高度恒定 ⇒ 滚动总高可预测）', () => {
    const classes: string[] = []
    for (const status of ['idle', 'loading', 'error'] as const) {
      const { unmount } = render(<RegionsDivider count={5} status={status} onRetry={() => {}} />)
      classes.push(screen.getByTestId('regions-divider').className)
      unmount()
    }
    expect(classes[0]).toBe(classes[1])
    expect(classes[1]).toBe(classes[2])
    expect(classes[0]).toContain('h-7')
  })

  it('哨兵 ref 挂在根节点上（IO 观察目标就是分隔条本身）', () => {
    const ref = { current: null as HTMLDivElement | null }
    render(<RegionsDivider count={3} status="idle" onRetry={() => {}} sentinelRef={ref} />)
    expect(ref.current).toBe(screen.getByTestId('regions-divider'))
  })
})
