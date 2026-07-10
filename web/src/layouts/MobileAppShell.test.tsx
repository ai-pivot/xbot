import { fireEvent, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { MobileAppShell } from './MobileAppShell'

const mocks = vi.hoisted(() => ({
  createSession: vi.fn(),
  sessionStore: {
    activeSession: { channel: 'web', chatID: 'chat-1' },
    activeSessionId: 'chat-1',
    sessions: [{
      channel: 'web',
      chatID: 'chat-1',
      label: 'Mobile Chat',
      lastActive: '2026-07-09T00:00:00Z',
      preview: '',
      status: 'idle',
      isCurrent: true,
    }],
    createSession: vi.fn(),
  },
}))

vi.mock('@/workspace/panels/AgentPanel', () => ({
  AgentPanel: () => <div>agent-panel</div>,
}))

vi.mock('@/components/session/SessionSidebar', () => ({
  SessionSidebar: () => <div>session-sidebar</div>,
}))

vi.mock('@/components/sidebar/FileExplorer', () => ({
  FileExplorer: () => <div>files-panel</div>,
}))

vi.mock('@/components/sidebar/FileSearch', () => ({
  FileSearch: () => <div>search-panel</div>,
}))

vi.mock('@/components/sidebar/SessionInfo', () => ({
  SessionInfo: () => <div>info-panel</div>,
}))

vi.mock('@/components/sidebar/TasksPanel', () => ({
  TasksPanel: () => <div>tasks-panel</div>,
}))

vi.mock('@/components/settings/SettingsDialog', () => ({
  SettingsDialog: () => null,
}))

vi.mock('@/hooks/useSessionStore', () => ({
  useSessionStore: () => mocks.sessionStore,
}))

vi.mock('@/hooks/useTabManager', () => ({
  useTabManager: () => ({
    tabs: [],
    activeTabId: null,
    openTab: vi.fn(),
    closeTab: vi.fn(),
    setActiveTab: vi.fn(),
    splitRight: vi.fn(),
    resetWorkGroup: vi.fn(),
    bindApi: vi.fn(),
  }),
}))

vi.mock('@/hooks/useTheme', () => ({
  useTheme: () => ({ theme: 'dark', setTheme: vi.fn() }),
}))

vi.mock('@/providers/WSProvider', () => ({
  useWSConnection: () => ({ connected: true, send: vi.fn(), rpc: vi.fn(), onMessage: vi.fn(() => vi.fn()), onSession: vi.fn(() => vi.fn()), onProgress: vi.fn(() => vi.fn()) }),
}))

vi.mock('@/providers/CwdProvider', () => ({
  useCwd: () => ({ cwd: '/repo', loading: false }),
}))

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => ({ user: null, loading: false, login: vi.fn(), register: vi.fn(), logout: vi.fn(), refresh: vi.fn() }),
}))

describe('MobileAppShell', () => {
  beforeEach(() => {
    mocks.sessionStore.createSession.mockReset()
    mocks.sessionStore.createSession.mockResolvedValue('new-chat')
  })

  it('renders mobile chrome and toggles detail/back state', () => {
    renderWithProviders(<MobileAppShell />)

    expect(screen.getByText('Mobile Chat')).toBeInTheDocument()
    expect(screen.getByText('agent-panel')).toBeInTheDocument()

    fireEvent.click(screen.getByText('详细'))
    expect(screen.getByText('info-panel')).toBeInTheDocument()
    expect(screen.getByText('返回')).toBeInTheDocument()

    fireEvent.click(screen.getByText('返回'))
    expect(screen.getByText('agent-panel')).toBeInTheDocument()
  })

  it('opens the session drawer from the top bar', () => {
    renderWithProviders(<MobileAppShell />)

    fireEvent.click(screen.getByLabelText('Sessions'))
    expect(screen.getByText('session-sidebar')).toBeInTheDocument()
  })

  it('creates a session from the top bar action', () => {
    renderWithProviders(<MobileAppShell />)

    fireEvent.click(screen.getByLabelText('New Session'))
    expect(mocks.sessionStore.createSession).toHaveBeenCalled()
  })
})
