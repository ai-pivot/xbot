import { fireEvent, screen } from '@testing-library/react'
import '@testing-library/jest-dom'
import { describe, expect, it, vi } from 'vitest'

import { renderWithProviders } from '@/test-utils'
import { SessionSidebar } from './SessionSidebar'
import type { SessionStore } from '@/hooks/useSessionStore'
import type { SessionInfo } from '@/types/shared'
import type { TabManager } from '@/hooks/useTabManager'

const switchSession = vi.fn()
const openTab = vi.fn()

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
    createSession: vi.fn(),
    switchSession,
    renameSession: vi.fn(),
    deleteSession: vi.fn(),
    clearAskUserPrompt: vi.fn(),
    reorderSessions: vi.fn(),
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

const tabManager = {
  tabs: [],
  activeTabId: null,
  openTab,
  closeTab: vi.fn(),
  setActiveTab: vi.fn(),
  splitRight: vi.fn(),
  resetWorkGroup: vi.fn(),
  bindApi: vi.fn(),
} satisfies TabManager

describe('SessionSidebar', () => {
  it('switches main sessions and only opens Agent tabs for SubAgents', () => {
    renderWithProviders(<SessionSidebar tabManager={tabManager} />)

    fireEvent.click(screen.getByText('Agent-main'))
    expect(switchSession).toHaveBeenCalledWith('/repo:Agent-main', 'cli')
    expect(openTab).not.toHaveBeenCalled()

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
})
