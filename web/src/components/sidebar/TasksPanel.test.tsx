import { act, fireEvent, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { TasksPanel } from './TasksPanel'
import type { TabManager } from '@/hooks/useTabManager'

const mocks = vi.hoisted(() => ({
  useTasks: vi.fn((): {
    cronTasks: unknown[]
    bgTasks: unknown[]
    loading: boolean
    killBgTask: ReturnType<typeof vi.fn>
  } => ({
    cronTasks: [],
    bgTasks: [],
    loading: false,
    killBgTask: vi.fn(),
  })),
  sessionStore: {
    activeSession: { channel: 'cli', chatID: '/repo:Agent-main' },
    refresh: vi.fn(),
    sessions: [{
      chatID: '/repo:Agent-main',
      channel: 'cli',
      label: 'Agent-main',
      lastActive: '2026-07-08T00:00:00Z',
      preview: '',
      status: 'idle',
      isCurrent: true,
      children: [{
        chatID: 'cli:/repo:Agent-main/review:1',
        channel: 'agent',
        label: 'default',
        lastActive: '2026-07-08T00:00:01Z',
        preview: 'checking files',
        status: 'running',
        running: true,
        isCurrent: false,
        type: 'agent',
        role: 'review',
        instance: '1',
        parentChannel: 'cli',
        parentChatID: '/repo:Agent-main',
        agentChatID: 'cli:/repo:Agent-main/review:1',
        children: [{
          chatID: 'cli:/repo:Agent-main/review:1/fix:1',
          channel: 'agent',
          label: 'default',
          lastActive: '2026-07-08T00:00:02Z',
          preview: '',
          status: 'running',
          running: true,
          isCurrent: false,
          type: 'agent',
          role: 'fix',
          instance: '1',
          parentChannel: 'agent',
          parentChatID: 'cli:/repo:Agent-main/review:1',
          agentChatID: 'cli:/repo:Agent-main/review:1/fix:1',
        }],
      }],
    }],
  },
}))

vi.mock('@/hooks/useWSConnection', () => ({
  useWSConnection: () => ({ connected: true, rpc: vi.fn() }),
}))

vi.mock('@/hooks/useTasks', () => ({
  useTasks: mocks.useTasks,
}))

vi.mock('@/hooks/useSessionStore', () => ({
  useSessionStore: () => mocks.sessionStore,
}))

describe('TasksPanel', () => {
  beforeEach(() => {
    mocks.useTasks.mockClear()
    mocks.sessionStore.refresh.mockClear()
    mocks.sessionStore.sessions[0].children![0].running = true
    mocks.sessionStore.sessions[0].children![0].status = 'running'
    mocks.sessionStore.sessions[0].children![0].children![0].running = true
    mocks.sessionStore.sessions[0].children![0].children![0].status = 'running'
    mocks.useTasks.mockReturnValue({
      cronTasks: [],
      bgTasks: [],
      loading: false,
      killBgTask: vi.fn(),
    })
  })

  it('opens SubAgent rows in an Agent tab', () => {
    const openTab = vi.fn()
    const tabManager = { activeTabId: 'main', tabs: [{ id: 'main', type: 'agent' }], openTab } as unknown as TabManager

    renderWithProviders(<TasksPanel tabManager={tabManager} />)

    fireEvent.click(screen.getByText('review/1'))

    expect(openTab).toHaveBeenCalledWith(expect.objectContaining({
      type: 'agent',
      title: 'review/1',
      data: expect.objectContaining({
        subAgentRole: 'review',
        subAgentInstance: '1',
        parentChannel: 'cli',
        parentChatID: '/repo:Agent-main',
        agentChatID: 'cli:/repo:Agent-main/review:1',
      }),
    }))
    expect(screen.getByText('checking files')).toBeInTheDocument()
  })

  it('refreshes the session tree while SubAgents are running', () => {
    vi.useFakeTimers()
    mocks.sessionStore.sessions[0].children![0].running = true

    renderWithProviders(<TasksPanel />)

    act(() => {
      vi.advanceTimersByTime(2_000)
    })
    expect(mocks.sessionStore.refresh).toHaveBeenCalled()
    vi.useRealTimers()
  })

  it('uses the focused SubAgent full key for task RPCs while displaying its children', () => {
    const tabManager = {
      activeTabId: 'agent-tab',
      tabs: [{
        id: 'agent-tab',
        type: 'agent',
        title: 'review/1',
        closable: true,
        data: {
          subAgentRole: 'review',
          subAgentInstance: '1',
          parentChannel: 'cli',
          parentChatID: '/repo:Agent-main',
          agentChatID: 'cli:/repo:Agent-main/review:1',
        },
      }],
      openTab: vi.fn(),
    } as unknown as TabManager

    renderWithProviders(<TasksPanel tabManager={tabManager} />)

    expect(mocks.useTasks).toHaveBeenCalledWith(expect.anything(), {
      channel: 'agent',
      chatID: 'cli:/repo:Agent-main/review:1',
    })
    expect(screen.getByText('fix/1')).toBeInTheDocument()
    expect(screen.queryByText('review/1')).not.toBeInTheDocument()
  })

  it('opens background task output in a Background tab', () => {
    const openTab = vi.fn()
    const tabManager = { activeTabId: 'main', tabs: [{ id: 'main', type: 'agent' }], openTab } as unknown as TabManager
    mocks.useTasks.mockReturnValue({
      cronTasks: [],
      bgTasks: [{
        id: 'task-1',
        command: 'npm test',
        status: 'running',
        startedAt: '2026-07-08T00:00:00Z',
        exitCode: 0,
        output: 'line 1\nline 2',
      }],
      loading: false,
      killBgTask: vi.fn(),
    })

    renderWithProviders(<TasksPanel tabManager={tabManager} />)
    fireEvent.click(screen.getByText('npm test'))

    expect(openTab).toHaveBeenCalledWith(expect.objectContaining({
      type: 'background',
      title: 'npm test',
      data: expect.objectContaining({
        taskID: 'task-1',
        command: 'npm test',
        taskChannel: 'cli',
        taskChatID: '/repo:Agent-main',
      }),
    }))
  })
})
