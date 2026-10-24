import { describe, expect, it, vi } from 'vitest'
import { fireEvent, screen } from '@testing-library/react'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { CoreSessionsPanel } from './builtinPanels'
import type { SessionStore } from '@/hooks/useSessionStore'
import type { SessionInfo } from '@/types/shared'

const panelSpies = vi.hoisted(() => ({ setCategory: vi.fn() }))

// 需求（用户）：会话搜索是低频操作——搜索框默认收起，点击按钮才展开；
// 输入框与「新建会话」在**同一行**内（展开时横向挤压新建会话按钮），
// **不纵向撑开列表**。

function session(overrides: Partial<SessionInfo> & { chatID: string; channel: string; label: string }): SessionInfo {
  return {
    chatID: overrides.chatID,
    channel: overrides.channel,
    label: overrides.label,
    lastActive: overrides.lastActive ?? '2026-07-08T00:00:00Z',
    preview: overrides.preview ?? '',
    status: overrides.status ?? 'idle',
    isCurrent: overrides.isCurrent ?? false,
    type: overrides.type,
    role: overrides.role,
    instance: overrides.instance,
    parentChatID: overrides.parentChatID,
    parentChannel: overrides.parentChannel,
    running: overrides.running ?? false,
    children: overrides.children,
  }
}

const mainSession = session({ chatID: 'web:chat-abc', channel: 'web', label: '我的会话', type: 'main' })

vi.mock('@/hooks/useSessionStore', () => ({
  useSessionStore: (): SessionStore => ({
    sessions: [mainSession],
    groups: [{ key: 'today', sessions: [mainSession] }],
    sortedSessions: [mainSession],
    activeSessionId: null,
    activeSession: null,
    starredIds: [],
    category: 'time',
    unreadIds: [],
    activeChannel: null,
    loading: false,
    error: null,
    subAgents: [],
    askUserPrompts: new Map(),
    setCategory: panelSpies.setCategory,
    collapsedGroups: new Set<string>(),
    toggleGroupCollapsed: vi.fn(),
    setGroupsCollapsed: vi.fn(),
    setActiveChannel: vi.fn(),
    markRead: vi.fn(),
    refresh: vi.fn(),
    toggleStar: vi.fn(),
    createSession: vi.fn(),
    forkSession: vi.fn(),
    switchSession: vi.fn(),
    activateSession: vi.fn(),
    renameSession: vi.fn(),
    deleteSession: vi.fn(),
    clearAskUserPrompt: vi.fn(),
    markAskUserAnswered: vi.fn(),
    hydrateAskUserPrompt: vi.fn(),
    reorderSessions: vi.fn(),
    setStatus: vi.fn(),
    hasMore: false,
    loadMore: vi.fn(),
  }),
}))

vi.mock('@/components/ui/tooltip', () => ({
  Tooltip: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  TooltipTrigger: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  TooltipContent: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}))

vi.mock('@/components/ui/scroll-area', () => ({
  ScrollArea: ({ children, className }: { children: React.ReactNode; className?: string }) => (
    <div className={className}>{children}</div>
  ),
}))

const tabManagerMock = { openTab: vi.fn(), closeTab: vi.fn(), setActiveTab: vi.fn() }
const ctx = { tabManager: tabManagerMock } as never

describe('CoreSessionsPanel — global search entry', () => {
  it('keeps the new-session label and search button without an inline input', () => {
    renderWithProviders(<CoreSessionsPanel ctx={ctx} />)
    const toolbar = screen.getByTestId('session-list-toolbar')
    expect(toolbar.querySelector('input')).toBeNull()
    expect(toolbar).toContainElement(screen.getByTestId('session-search-toggle'))
    expect(toolbar).toContainElement(screen.getByRole('button', { name: /新建会话|New Session|新しいセッション/ }))
  })
  it('dispatches the global search request instead of expanding the sidebar', () => {
    const show = vi.fn()
    window.addEventListener('xbot:session-search', show)
    renderWithProviders(<CoreSessionsPanel ctx={ctx} />)
    fireEvent.click(screen.getByTestId('session-search-toggle'))
    expect(show).toHaveBeenCalledOnce()
    expect(screen.queryByRole('textbox')).toBeNull()
    window.removeEventListener('xbot:session-search', show)
  })
})

// ⚠️ 渠道下拉现在在**标题行**（PanelChrome 的 headerExtra 槽），不在面板主体里 ⇒
// 这里只断言主体工具条【没有】下拉（位置契约），下拉本身由 ChannelPicker.test.tsx
// 与 PanelLayout.test.tsx（headerExtra 渲染在 header）守护。
describe('会话面板主体工具条：不放渠道下拉（位置契约）', () => {
  it('工具条只有新建会话 + 搜索开关，不含 channel-picker', () => {
    renderWithProviders(<CoreSessionsPanel ctx={{ tabManager: undefined } as never} />)
    expect(screen.queryByTestId('channel-picker')).toBeNull()
    expect(screen.getByTestId('session-list-toolbar').querySelector('[data-testid="session-search-toggle"]')).toBeTruthy()
  })
})

// 需求（用户）：「会话列表按项目组织」——桌面会话面板必须能切分类（默认项目）。
// 历史事故：分类切换器只存在于手机抽屉 SessionSidebar，桌面换成 CoreSessionsPanel
// 后再也切不到分类（与「渠道下拉只在手机里」同类遗漏）。此断言即回归守护。
describe('桌面会话面板：分类切换器必须在（回归守护）', () => {
  it('渲染分类切换器并能切到「项目」，工具条与列表之间', () => {
    renderWithProviders(<CoreSessionsPanel ctx={ctx} />)

    const bar = screen.getByTestId('session-view-bar')
    expect(bar).toBeInTheDocument()
    // 三个分类都在，且项目在最前（默认组织方式）。
    expect(
      screen.getAllByTestId(/^session-category-/).map((el) => el.getAttribute('data-testid')),
    ).toEqual(['session-category-path', 'session-category-status', 'session-category-time'])
    // 「全部折叠/展开」也在（有组时可点）。
    expect(screen.getByTestId('session-collapse-all')).toBeInTheDocument()

    fireEvent.click(screen.getByTestId('session-category-path'))
    expect(panelSpies.setCategory).toHaveBeenCalledWith('path')
  })
})
