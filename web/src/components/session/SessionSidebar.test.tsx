import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import '@testing-library/jest-dom'
import { describe, expect, it, vi } from 'vitest'

import { renderWithProviders } from '@/test-utils'
import { SessionSidebar } from './SessionSidebar'
import type { SessionStore } from '@/hooks/useSessionStore'
import type { SessionInfo } from '@/types/shared'
import type { TabManager } from '@/hooks/useTabManager'

const switchSession = vi.fn()
const activateSession = vi.fn()
const openTab = vi.fn()
const createSession = vi.fn().mockResolvedValue('web:chat-new')

function session(overrides: Partial<SessionInfo> & { chatID: string; channel: string; label: string }): SessionInfo {
  return {
    chatID: overrides.chatID,
    channel: overrides.channel,
    label: overrides.label,
    lastActive: overrides.lastActive ?? '2026-07-08T00:00:00Z',
    preview: overrides.preview ?? '',
    status: overrides.status ?? (overrides.type === 'agent' ? 'running' : 'idle'),
    isCurrent: overrides.isCurrent ?? false,
    type: overrides.type,
    role: overrides.role,
    instance: overrides.instance,
    parentChatID: overrides.parentChatID,
    parentChannel: overrides.parentChannel,
    agentChatID: overrides.agentChatID,
    running: overrides.running ?? overrides.type === 'agent',
    children: overrides.children,
  }
}

const review = session({
  chatID: 'cli:/repo:Agent-main/review:1',
  channel: 'agent',
  label: 'default',
  type: 'agent',
  role: undefined,
  instance: undefined,
  parentChannel: 'web',
  parentChatID: 'stale-parent',
  agentChatID: 'cli:/repo:Agent-main/review:1',
})
const parent = session({
  chatID: '/repo:Agent-main',
  channel: 'cli',
  label: 'Agent-main',
  type: 'main',
  children: [review],
})

vi.mock('@/hooks/useSessionStore', () => ({
  useSessionStore: (): SessionStore => ({
    sessions: [parent],
    groups: [{ key: 'today', sessions: [parent] }],
    sortedSessions: [parent],
    activeSessionId: null,
    activeSession: null,
    starredIds: [],
    category: 'time',
    unreadIds: [],
    activeChannel: null,
    loading: false,
    error: null,
    subAgents: [review],
    askUserPrompts: new Map(),
    setCategory: vi.fn(),
    setActiveChannel: vi.fn(),
    markRead: vi.fn(),
    refresh: vi.fn(),
    toggleStar: vi.fn(),
    createSession,
    forkSession: vi.fn(),
    switchSession,
    activateSession,
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

// PathPicker 会打后端 RPC 拉目录 —— 用例只验证「创建 → 完成切换」的接线。
vi.mock('@/components/session/PathPicker', () => ({
  PathPicker: ({ value, onChange }: { value?: string; onChange: (v: string) => void }) => (
    <input aria-label="work-path" value={value ?? ''} onChange={(e) => onChange(e.target.value)} />
  ),
}))

const tabManager = {
  tabs: [],
  activeTabId: null,
  openTab,
  closeTab: vi.fn(),
  setActiveTab: vi.fn(),
  splitRight: vi.fn(),
  resetWorkGroup: vi.fn(),
  bindApi: vi.fn(),
  getLayoutJSON: vi.fn(() => null),
  applyLayoutJSON: vi.fn(),
  getWorkLayoutJSON: vi.fn(() => null),
  groupTabsOf: vi.fn(() => []),
  closeTabsInGroup: vi.fn(),
} satisfies TabManager

describe('SessionSidebar', () => {
  it('opens agent tab + activates session for main sessions, opens Agent tabs for SubAgents', () => {
    renderWithProviders(<SessionSidebar tabManager={tabManager} />)

    fireEvent.click(screen.getByText('Agent-main'))
    // Desktop: main session click opens/focuses an agent tab + activates session
    // (does NOT call switchSession — that's mobile-only via onSubAgentSelect path).
    expect(activateSession).toHaveBeenCalledWith('/repo:Agent-main', 'cli')
    expect(openTab).toHaveBeenCalledWith(expect.objectContaining({
      type: 'agent',
      data: expect.objectContaining({
        filePath: '/repo:Agent-main',
        channel: 'cli',
      }),
    }))
    expect(switchSession).not.toHaveBeenCalled()

    fireEvent.click(screen.getByText('review/1'))
    expect(openTab).toHaveBeenCalledWith(expect.objectContaining({
      type: 'agent',
      title: 'review/1',
      data: expect.objectContaining({
        agentChatID: 'cli:/repo:Agent-main/review:1',
        parentChannel: 'cli',
        parentChatID: '/repo:Agent-main',
        subAgentRole: 'review',
        subAgentInstance: '1',
      }),
    }))
  })

  it('uses parsed SubAgent identity for tab titles when backend label is default', () => {
    openTab.mockClear()
    renderWithProviders(<SessionSidebar tabManager={tabManager} />)

    fireEvent.click(screen.getByText('review/1'))

    expect(openTab).toHaveBeenCalledWith(expect.objectContaining({
      title: 'review/1',
    }))
  })

  // REPRO（2026-09-17）：点侧栏「新建会话」→ 确认后侧栏新会话高亮了，但会话窗口
  // 没有切过去，必须再点一下新会话。根因：createSession 只 switchSession（侧栏
  // 高亮），没有完成"切换"的最后一步 —— mobile 抽屉要关闭、desktop 要打开新会话
  // 的 agent tab（主区身份在 tab 的 sessionId 上）。
  describe('新建会话完成切换（REPRO: 创建后窗口没切）', () => {
    /** 打开对话框 → 填名 → 确认（会话侧栏有两个入口同名按钮，取工具栏主按钮）。 */
    async function createViaDialog() {
      const toolbar = await screen.findByTestId('session-list-toolbar')
      fireEvent.click(within(toolbar).getAllByRole('button', { name: /New Session|新建会话/ })[0])
      const nameInput = await screen.findByLabelText(/Session name|会话名称/)
      fireEvent.change(nameInput, { target: { value: '新会话' } })
      fireEvent.click(await screen.findByRole('button', { name: /^Confirm$|^确认$/ }))
      await waitFor(() => {
        expect(createSession).toHaveBeenCalledWith('新会话', expect.anything())
      })
    }

    it('mobile（onSubAgentSelect 存在）：关闭抽屉（AgentPanel 跟随 activeSession），不开 tab', async () => {
      createSession.mockClear()
      createSession.mockResolvedValue('web:chat-new')
      const openTabMock = tabManager.openTab
      openTabMock.mockClear()
      const onSessionSelected = vi.fn()
      renderWithProviders(
        <SessionSidebar tabManager={tabManager} onSubAgentSelect={vi.fn()} onSessionSelected={onSessionSelected} />,
      )

      await createViaDialog()
      // 手机端没有 dockview —— 完成切换 = 关闭抽屉（AgentPanel 跟随 activeSession），
      // 不应开 tab（openTab 会进 pending 队列静默丢失）。
      await waitFor(() => {
        expect(onSessionSelected).toHaveBeenCalled()
      })
      expect(openTabMock).not.toHaveBeenCalled()
    })

    it('desktop（无 onSubAgentSelect）：打开新会话的 agent tab', async () => {
      createSession.mockClear()
      createSession.mockResolvedValue('web:chat-new')
      const openTabMock = vi.fn()
      tabManager.openTab = openTabMock
      try {
        renderWithProviders(<SessionSidebar tabManager={tabManager} />)

        await createViaDialog()
        // desktop 的主区身份在 tab 的 sessionId 上 —— 必须打开新会话的 tab，
        // 否则「侧栏高亮了、窗口没切」，要再点一次新会话才切过去。
        await waitFor(() => {
          expect(openTabMock).toHaveBeenCalledWith(expect.objectContaining({
            type: 'agent',
            data: expect.objectContaining({ filePath: 'web:chat-new', channel: 'web' }),
          }))
        })
      } finally {
        tabManager.openTab = openTab
      }
    })
  })
})
