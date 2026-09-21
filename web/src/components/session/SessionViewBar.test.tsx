/**
 * SessionViewBar — 分类切换（项目 / 状态 / 时间）+「全部折叠/展开」。
 *
 * 组件契约：项目分类排第一（它是默认组织方式）、切换走 store.setCategory、
 * 「全部折叠/展开」的判定与回调（键必须含 category）。桌面面板是否真的渲染了
 * 这个列——即回归守护的接线断言——由 builtinPanels.search.test.tsx 覆盖
 * （历史事故：分类切换器只在手机抽屉里，桌面切不到）。
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen } from '@testing-library/react'
import '@testing-library/jest-dom'

import i18n from '@/i18n'
import { renderWithProviders } from '@/test-utils'
import { SessionViewBar } from './SessionViewBar'

const spies = vi.hoisted(() => ({
  setCategory: vi.fn(),
  setGroupsCollapsed: vi.fn(),
  state: { category: 'path' as string, collapsed: new Set<string>() },
}))

vi.mock('@/hooks/useSessionStore', () => ({
  useSessionStore: () => ({
    category: spies.state.category,
    collapsedGroups: spies.state.collapsed,
    setCategory: spies.setCategory,
    setGroupsCollapsed: spies.setGroupsCollapsed,
  }),
}))

beforeEach(() => {
  spies.state.category = 'path'
  spies.state.collapsed = new Set<string>()
  spies.setCategory.mockClear()
  spies.setGroupsCollapsed.mockClear()
})

function categoryOrder(): string[] {
  return screen
    .getAllByTestId(/^session-category-/)
    .map((el) => el.getAttribute('data-testid') ?? '')
}

describe('SessionViewBar', () => {
  it('renders 项目 → 状态 → 时间，并高亮当前分类', () => {
    renderWithProviders(<SessionViewBar groupKeys={['/repo']} />)

    expect(categoryOrder()).toEqual(['session-category-path', 'session-category-status', 'session-category-time'])
    expect(screen.getByTestId('session-category-path')).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByTestId('session-category-time')).toHaveAttribute('aria-pressed', 'false')
  })

  it('switches the category through the store', () => {
    renderWithProviders(<SessionViewBar groupKeys={['/repo']} />)

    fireEvent.click(screen.getByTestId('session-category-time'))
    expect(spies.setCategory).toHaveBeenCalledWith('time')
  })

  it('「全部折叠」collapses every listed group — keys carry the category', () => {
    renderWithProviders(<SessionViewBar groupKeys={['/a', '/b']} />)

    const button = screen.getByTestId('session-collapse-all')
    expect(button).toHaveAttribute('title', i18n.t('session.collapseAllGroups'))

    fireEvent.click(button)
    expect(spies.setGroupsCollapsed).toHaveBeenCalledWith(['path:/a', 'path:/b'], true)
  })

  it('when everything is collapsed the button expands（全部展开）', () => {
    spies.state.collapsed = new Set(['path:/a', 'path:/b'])
    renderWithProviders(<SessionViewBar groupKeys={['/a', '/b']} />)

    const button = screen.getByTestId('session-collapse-all')
    expect(button).toHaveAttribute('title', i18n.t('session.expandAllGroups'))

    fireEvent.click(button)
    expect(spies.setGroupsCollapsed).toHaveBeenCalledWith(['path:/a', 'path:/b'], false)
  })

  it('hides the collapse toggle when there is no group yet', () => {
    renderWithProviders(<SessionViewBar groupKeys={[]} />)
    expect(screen.queryByTestId('session-collapse-all')).toBeNull()
  })
})
