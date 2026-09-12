import { describe, expect, it, vi } from 'vitest'
import { fireEvent, screen } from '@testing-library/react'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { CoreSessionsPanel } from './builtinPanels'
import type { SessionStore } from '@/hooks/useSessionStore'
import type { SessionInfo } from '@/types/shared'

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
    setCategory: vi.fn(),
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

describe('CoreSessionsPanel — 会话搜索收起/展开（同行横向挤压）', () => {
  it('默认收起：输入框不在可达性树中；新建会话按钮与开关在同一行工具栏内', () => {
    renderWithProviders(<CoreSessionsPanel ctx={ctx} />)

    // 收起：输入框不在可达性树（aria-hidden + tabIndex=-1，实际是被 0 宽裁切）。
    expect(screen.queryByRole('textbox')).toBeNull()

    const toolbar = screen.getByTestId('session-list-toolbar')
    const toggle = screen.getByRole('button', { expanded: false })
    const newBtn = screen.getByRole('button', { name: /新建会话|New Session|新しいセッション/ })

    // 同一行：新建会话按钮 / 搜索框槽位 / 开关都在工具栏容器内。
    // 输入框若被移到工具栏【之外】（纵向展开成独立一行）此断言即失败。
    expect(toolbar).toContainElement(newBtn)
    expect(toolbar).toContainElement(toggle)
    expect(toolbar).toContainElement(screen.getByLabelText(/^搜索$|^Search$/i, { selector: 'input' }))
  })

  it('点击按钮展开输入框并自动聚焦；再次点击收起', () => {
    renderWithProviders(<CoreSessionsPanel ctx={ctx} />)

    fireEvent.click(screen.getByRole('button', { expanded: false }))

    const input = screen.getByRole('textbox')
    expect(input).toBeInTheDocument()
    // 展开即聚焦（点击后可直接输入）。
    expect(document.activeElement).toBe(input)
    expect(screen.getByRole('button', { expanded: true })).toBeInTheDocument()

    // 再次点击 → 收起。
    fireEvent.click(screen.getByRole('button', { expanded: true }))
    expect(screen.queryByRole('textbox')).toBeNull()
  })

  it('Esc 收起且清空查询（隐藏的过滤条件不能让列表莫名变短）', () => {
    renderWithProviders(<CoreSessionsPanel ctx={ctx} />)

    fireEvent.click(screen.getByRole('button', { expanded: false }))
    const input = screen.getByRole('textbox')
    fireEvent.change(input, { target: { value: '我的' } })
    expect(input).toHaveValue('我的')

    fireEvent.keyDown(input, { key: 'Escape' })
    expect(screen.queryByRole('textbox')).toBeNull()

    // 重新展开 → 查询已清空（复位为未过滤状态）。
    fireEvent.click(screen.getByRole('button', { expanded: false }))
    expect(screen.getByRole('textbox')).toHaveValue('')
  })
})
