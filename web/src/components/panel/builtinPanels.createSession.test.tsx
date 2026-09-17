import { describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { CoreSessionsPanel } from './builtinPanels'
import type { SessionStore } from '@/hooks/useSessionStore'
import type { SessionInfo } from '@/types/shared'

// REPRO: 用户报告「点击侧栏『新建会话』，确认后侧栏新会话高亮了，但会话窗口
// 没有实际切换过去，必须再点一下新会话才切过去」。
//
// 根因：CoreSessionsPanel 的 NewSessionDialog `onCreate` 直接透传
// store.createSession —— createSession 只做 switchSession（store activeSession
// + 后端 /switch），侧栏高亮因此更新；但 desktop 的 AgentPanel 身份来自它自己
// tab 的 params.sessionId（session-per-tab 架构），没有人为新会话打开/聚焦
// agent tab，正在看的旧 tab 依旧携带旧 sessionId，于是主区仍渲染旧会话。
// 会话列表点击（handleSelect）与 fork（onFork）都补了这一步 openTab，
// 只有「新建会话」漏了 —— 所以再点一下新会话就能切过去。
//
// 修复前：openTab 未被调用（红灯）；修复后：openTab 带新 chatID 被调用（绿灯）。

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

const createSession = vi.fn().mockResolvedValue('web:chat-new')

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
    createSession,
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

// PathPicker 会打后端 RPC 拉目录 —— 本用例只关心「创建 → 切换」的接线，
// 用普通输入框替身（NewSessionDialog 只读 value/onChange）。
vi.mock('@/components/session/PathPicker', () => ({
  PathPicker: ({ value, onChange }: { value?: string; onChange: (v: string) => void }) => (
    <input aria-label="work-path" value={value ?? ''} onChange={(e) => onChange(e.target.value)} />
  ),
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

describe('CoreSessionsPanel 新建会话（REPRO: 创建后桌面 Agent tab 未切换）', () => {
  it('侧栏「新建会话」确认后：createSession 成功 → 打开新会话的桌面 agent tab', async () => {
    createSession.mockClear()
    createSession.mockResolvedValue('web:chat-new')
    const openTab = tabManagerMock.openTab
    openTab.mockClear()
    renderWithProviders(<CoreSessionsPanel ctx={ctx} />)

    // 「新建会话」按钮 → 打开 NewSessionDialog。
    const newBtn = await screen.findByRole('button', { name: /新建会话|New Session/ })
    fireEvent.click(newBtn)

    // 填名字 + 确认（创建会话）。i18n 默认 en —— 查询写多语言正则。
    const nameInput = await screen.findByLabelText(/Session name|会话名称|セッション名/)
    fireEvent.change(nameInput, { target: { value: '新会话' } })
    fireEvent.click(await screen.findByRole('button', { name: /^Confirm$|^确认$|^確認$/ }))

    await waitFor(() => {
      expect(createSession).toHaveBeenCalledWith('新会话', expect.anything())
    })
    // 关键断言：创建成功后必须为新会话打开/聚焦 agent tab（desktop 切换完成）。
    // 修复前：只 switchSession，主区 tab 仍绑旧 sessionId → 用户看到"侧栏高亮了
    // 但窗口没切"，必须再点一次新会话。
    await waitFor(() => {
      expect(openTab).toHaveBeenCalledWith(
        expect.objectContaining({
          type: 'agent',
          data: expect.objectContaining({ filePath: 'web:chat-new', channel: 'web' }),
        }),
      )
    })
  })
})
