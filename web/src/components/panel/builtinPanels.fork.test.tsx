import { describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { CoreSessionsPanel } from './builtinPanels'
import type { SessionStore } from '@/hooks/useSessionStore'
import type { SessionInfo } from '@/types/shared'

// REPRO: 用户报告 web 端"点 fork conv 完全没用——没发 RPC 也没交互"。
// 根因：web 主界面的会话面板是 CoreSessionsPanel（core.sessions 面板，
// builtinPanels.tsx），它渲染 SessionList 时漏传 onFork——onFork 是
// optional prop，SessionItem 的 `{onFork && ...}` 条件不满足 → 右键菜单
// 里根本没有"分叉会话/Fork"项（SessionSidebar 接线了但主面板没接）。
// 修复前：菜单无 Fork 项（红灯复现）；修复后：渲染 + 点击触发
// store.forkSession（POST /api/chats/fork）。

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

const forkSession = vi.fn().mockResolvedValue('web:chat-new')

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
    forkSession,
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

describe('CoreSessionsPanel fork（REPRO: 主会话面板的 SessionList 漏传 onFork）', () => {
  it('右键点"分叉会话"→ 对话框预填名 → 确认创建 → forkSession(id, channel, label) + 桌面 tab', async () => {
    forkSession.mockClear()
    forkSession.mockResolvedValue('web:chat-new')
    const openTab = tabManagerMock.openTab
    openTab.mockClear()
    renderWithProviders(<CoreSessionsPanel ctx={ctx} />)

    // 右键会话项打开 ContextMenu。
    const item = await screen.findByText('我的会话')
    fireEvent.contextMenu(item)

    // 菜单必须包含 Fork 项（修复前：CoreSessionsPanel 漏传 onFork →
    // SessionItem 的 {onFork && ...} 不渲染 → 用户右键菜单里没有 Fork，
    // "点 fork conv 完全没用"）。
    const forkEntry = await screen.findByRole('menuitem', { name: /分叉会话|Fork/ })
    expect(forkEntry).toBeInTheDocument()

    // 点击 Fork 项 → 打开 Fork 对话框（用户确认新会话名——不自动创建）。
    fireEvent.click(forkEntry)
    // 对话框出现且 Input 预填 "{源会话名} fork"（用户可改）。
    const nameInput = await screen.findByDisplayValue('我的会话 fork')
    expect(nameInput).toBeInTheDocument()

    // 确认（创建并切换）→ store.forkSession(chatID, channel, label) 被调用
    // （→ POST /api/chats/fork → switchSession 完整切换 + 桌面 tab 打开）。
    const confirmBtn = await screen.findByRole('button', { name: /创建并切换|Create & Switch/ })
    fireEvent.click(confirmBtn)
    await waitFor(() => {
      expect(forkSession).toHaveBeenCalledWith('web:chat-abc', 'web', '我的会话 fork')
    })
    // fork 成功后打开新会话的桌面 agent tab（desktop switch 完成）。
    await waitFor(() => {
      expect(openTab).toHaveBeenCalledWith(expect.objectContaining({
        type: 'agent',
        data: expect.objectContaining({ filePath: 'web:chat-new', channel: 'web' }),
      }))
    })
  })
})
