/**
 * AppShell layout regression tests（布局 v6 全卡片化终态）。
 *
 * 桌面 AppShell = Dockview workspace（flex-1 全卡片）+ PanelLauncher（底部
 * chip 栏）。全局 header 与底部 rail row（InfoBar + BottomRailBadges）均已删
 * ——连接状态/会话名在主卡片（AgentPanel）header，检查更新 + 设置在
 * PanelLauncher 右侧组，InfoBar 只在 MobileAppShell 渲染。
 *
 * 回归守护（本文件历史 bug 语义）：
 * - main 是 flex COLUMN；dockview host 用 `flex-1 min-h-0 w-full` 填充剩余
 *   空间（不能 h-full —— rail row 占高时溢出）
 * - infoBar 插件 widget 不在桌面渲染（rail row 已删）
 */
import { screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'

vi.mock('@/hooks/useIsMobile', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  useIsMobile: () => false,
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
    groupTabsOf: () => [],
    bindApi: vi.fn(),
  }),
}))
vi.mock('@/hooks/useSessionStore', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  useSessionStore: () => ({
    activeSession: null,
    activeChannel: null,
    sessions: [],
    subAgents: [],
    createSession: vi.fn(),
    switchSession: vi.fn(),
    deleteSession: vi.fn(),
    renameSession: vi.fn(),
    refresh: vi.fn(),
  }),
}))
vi.mock('@/hooks/useLayoutPersistence', () => ({ useLayoutPersistence: () => {} }))
// DockviewContainer deps (DockviewContainer itself stays real — its host div
// carries the flex-1 fill under test).
vi.mock('@/hooks/useTheme', () => ({
  useTheme: () => ({ theme: 'dark', accentColor: '#3388BB', setAccentColor: vi.fn(), mdTheme: 'vscode-dark', setMdTheme: vi.fn() }),
}))
// Partial mocks：只覆盖 hook，保留真实 Context 导出（DockviewContainer 的
// withDockviewProviders 需要 import WSContext/CwdContext/AuthContext）。
vi.mock('@/providers/WSProvider', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  useWSConnection: () => ({ connected: true, send: vi.fn(), rpc: vi.fn(), onMessage: vi.fn(() => vi.fn()), onSession: vi.fn(() => vi.fn()), onProgress: vi.fn(() => vi.fn()) }),
}))
vi.mock('@/providers/CwdProvider', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  useCwd: () => ({ cwd: '/repo', loading: false }),
}))
vi.mock('@/hooks/useAuth', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  useAuth: () => ({ user: null, loading: false, login: vi.fn(), register: vi.fn(), logout: vi.fn(), refresh: vi.fn() }),
}))
vi.mock('@/components/settings/SettingsDialog', () => ({ SettingsDialog: () => null }))

import { renderWithProviders } from '@/test-utils'
import { AppShell } from './AppShell'
import { PluginWidgetsContext } from '@/plugins/PluginWidgetProvider'

describe('AppShell v6 全卡片化布局（无全局 header、无底部 rail row）', () => {
  it('main = dockview + PanelLauncher：dockview flex-1 填充，无 rail row，infoBar widget 不渲染', () => {
    renderWithProviders(
      <PluginWidgetsContext.Provider
        value={{
          // 提供 infoBar widget 验证它不在桌面渲染（rail row 已删，
          // InfoBar 只在 MobileAppShell）。
          zones: { infoBar: [{ text: 'status: ready', style: 'info' }] },
          components: [],
          revision: 1,
        }}
      >
        <AppShell />
      </PluginWidgetsContext.Provider>,
    )

    // infoBar 插件 widget 不在桌面渲染（底部 rail row 已删）。
    expect(screen.queryByText('status: ready')).not.toBeInTheDocument()

    const main = document.querySelector('main')
    expect(main).not.toBeNull()

    // main is a flex COLUMN — dockview 与 PanelLauncher 上下排列。
    expect(main!.className).toContain('flex-col')

    // children = [dockview host, PanelLauncher]（全局 header 与底部 rail row
    // 均已删——主卡片 header（AgentPanel）与 chip 栏承接全部入口）。
    expect(main!.children.length).toBe(2)
    const dockview = main!.children[0]
    // Dockview host fills the space (flex-1 min-h-0), not h-full w-full —
    // h-full would overflow since the launcher consumed height.
    expect(dockview.className).toContain('flex-1')
    expect(dockview.className).toContain('min-h-0')
    expect(dockview.className).toContain('w-full')
    expect(dockview.className).not.toContain('h-full')
  })

  it('layout v6: 全局 header 已删（连接状态/会话名在主卡片 header，设置按钮在 chip 栏）', () => {
    renderWithProviders(
      <PluginWidgetsContext.Provider value={{ zones: {}, components: [], revision: 0 }}>
        <AppShell />
      </PluginWidgetsContext.Provider>,
    )

    // 模型 pill / think pill 已移除；全局 header（含 ctxUsage 上下文环 svg）
    // 已删——上下文环由主卡片 AgentPanel 输入区（ContextRing）承载。
    expect(screen.queryByTitle('切换模型')).not.toBeInTheDocument()
    expect(screen.queryByText(/think/)).not.toBeInTheDocument()
    expect(screen.queryByRole('img', { name: '上下文用量' })).not.toBeInTheDocument()
    // 连接状态点也不再全局渲染（AgentPanel 卡片 header 内，面板被 mock）
    expect(screen.queryByText('已连接')).not.toBeInTheDocument()
    expect(screen.queryByText('连接中…')).not.toBeInTheDocument()
  })
})
