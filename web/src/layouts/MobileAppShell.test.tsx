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

vi.mock('@/hooks/useTerminal', () => ({
  useTerminal: () => ({
    terminals: [],
    activeTerminalId: null,
    createTerminal: vi.fn(),
    killTerminal: vi.fn(),
    write: vi.fn(),
    setActiveTerminal: vi.fn(),
  }),
}))

vi.mock('@/hooks/useTheme', () => ({
  useTheme: () => ({ theme: 'dark', accentColor: '#3388BB', setAccentColor: vi.fn(), mdTheme: 'vscode-dark', setMdTheme: vi.fn() }),
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

// 插件运行时面板：测试环境无 PluginRuntimeProvider，mock 为空列表
// （等同"无插件"），避免 usePluginRuntime 抛错。
vi.mock('@/plugin-runtime/usePluginViewPanels', () => ({
  usePluginViewPanels: () => [],
}))

// 布局系统：测试环境直接渲染 MobileAppShell（不经 App.tsx），需手动注册
// 内置布局项，否则底部导航/顶栏为空（会话/工具按钮不渲染）。
// 固定产品默认 locale（zh-CN）：底部/顶栏按钮经 labelKey 走 i18n，测试断言用中文。
import { registerBuiltinLayoutItems } from '@/plugin-runtime/layoutRegistry'
import { changeLocale } from '@/i18n'

describe('MobileAppShell', () => {
  beforeEach(() => {
    mocks.sessionStore.createSession.mockReset()
    mocks.sessionStore.createSession.mockResolvedValue('new-chat')
    registerBuiltinLayoutItems()
    changeLocale('zh-CN')
  })

  it('renders mobile chrome and toggles detail/back state', () => {
    renderWithProviders(<MobileAppShell />)

    expect(screen.getByText('Mobile Chat')).toBeInTheDocument()
    expect(screen.getByText('agent-panel')).toBeInTheDocument()

    // Switch to the detail/panel view via the bottom nav "工具" button
    fireEvent.click(screen.getByText('工具'))
    // Panel buttons use i18n labels (locale fixed to zh-CN in beforeEach)
    fireEvent.click(screen.getByLabelText('信息'))
    expect(screen.getByText('info-panel')).toBeInTheDocument()
    // Agent 面板必须保持挂载（display:none 隐藏）——卸载会销毁
    // MessageStore/ProgressStore，切回时依赖网络恢复，迭代可能少
    expect(screen.getByText('agent-panel')).toBeInTheDocument()

    // Return to the agent view via the bottom nav "会话" button
    fireEvent.click(screen.getByText('会话'))
    expect(screen.getByText('agent-panel')).toBeInTheDocument()
  })

  it('opens the session drawer from the top bar', () => {
    renderWithProviders(<MobileAppShell />)

    fireEvent.click(screen.getByLabelText('会话'))
    expect(screen.getByText('session-sidebar')).toBeInTheDocument()
  })

  it('creates a session from the top bar action', () => {
    renderWithProviders(<MobileAppShell />)

    fireEvent.click(screen.getByLabelText('新建会话'))
    expect(mocks.sessionStore.createSession).toHaveBeenCalled()
  })
})
