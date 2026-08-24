/**
 * AppShell layout regression tests.
 *
 * Bug: the plugin widget info bar (git plugin, `whitespace-nowrap` content)
 * squeezed the dockview workspace past the screen edge. Root cause: `<main>`
 * was a flex container with the DEFAULT row direction, so InfoBar (shrink-0,
 * content-sized width) and DockviewContainer (`h-full w-full` = 100% width)
 * sat side by side — combined width exceeded the container.
 *
 * Fix: main is now `flex flex-col` (InfoBar spans the full row width above the
 * workspace) and the dockview host uses `min-h-0 w-full flex-1` to fill the
 * remaining vertical space instead of `h-full w-full`.
 */
import { screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'

vi.mock('@/hooks/useIsMobile', () => ({ useIsMobile: () => false }))
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
vi.mock('@/hooks/useSessionStore', () => ({
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
// carries the flex-1 fix under test).
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
// Heavy chrome mocked away; keep InfoBar + DockviewContainer real (they own the
// layout under test).
vi.mock('@/layouts/ActivityBar', () => ({ ActivityBar: () => <div data-testid="activity-bar" /> }))
vi.mock('@/components/session/SessionSidebar', () => ({ SessionSidebar: () => <div data-testid="session-sidebar" /> }))
vi.mock('@/components/sidebar/RightSidebar', () => ({ RightSidebar: () => <div data-testid="right-sidebar" /> }))
vi.mock('@/components/sidebar/RightActivityBar', () => ({ RightActivityBar: () => <div data-testid="right-activity-bar" /> }))
vi.mock('@/components/settings/SettingsDialog', () => ({ SettingsDialog: () => null }))
// 布局测试不关心插件视图面板（PluginPanelContainer 需要 PluginRuntimeProvider）；
// mock 为 null，保持测试聚焦 flex-col 布局回归。
vi.mock('@/plugins/manager/PluginPanelContainer', () => ({ PluginPanelContainer: () => null }))

import { renderWithProviders } from '@/test-utils'
import { AppShell } from './AppShell'
import { PluginWidgetsContext } from '@/plugins/PluginWidgetProvider'

describe('AppShell workspace layout (info bar must not squeeze the dockview)', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('stacks the info bar above the dockview (flex column), never side by side', () => {
    renderWithProviders(
      <PluginWidgetsContext.Provider
        value={{
          // 用非 git widget 内容验证布局——git span 现在由 fancy GitStatusPanel
          // 渲染（InfoBar 的 WidgetZone 用 excludePrefixes 排除了 git:）。
          zones: { infoBar: [{ text: 'status: ready', style: 'info' }] },
          components: [],
          revision: 1,
        }}
      >
        <AppShell />
      </PluginWidgetsContext.Provider>,
    )

    // InfoBar content is rendered (plugin widget present).
    expect(screen.getByText('status: ready')).toBeInTheDocument()

    const main = document.querySelector('main')
    expect(main).not.toBeNull()

    // Fix 1: main is a flex COLUMN — InfoBar spans the full row width and the
    // dockview sits above it. Without flex-col the nowrap InfoBar and the
    // w-full dockview would share a row and overflow the screen.
    expect(main!.className).toContain('flex-col')

    // Order: children[0] = dockview host, children[1] = InfoBar (status-bar
    // style at the BOTTOM, VSCode-like).
    expect(main!.children.length).toBeGreaterThanOrEqual(2)
    const dockview = main!.children[0]
    const infoBar = main!.children[1]
    // Dockview host fills the REMAINING space (flex-1 min-h-0), not
    // h-full w-full — h-full would overflow since the InfoBar consumed height.
    expect(dockview.className).toContain('flex-1')
    expect(dockview.className).toContain('min-h-0')
    expect(dockview.className).toContain('w-full')
    expect(dockview.className).not.toContain('h-full')
    // InfoBar is a slim fixed-height status bar BELOW the workspace. Its
    // height (inline style) is 1.5rem + the bottom safe-area inset — a plain
    // h-6 on desktop (inset 0), and it paints through the iOS home-indicator
    // strip in standalone PWA mode.
    const infoBarEl = infoBar as HTMLElement
    expect(infoBarEl.style.height).toBe('calc(1.5rem + var(--safe-area-bottom))')
    expect(infoBarEl.style.paddingBottom).toBe('var(--safe-area-bottom)')
    // It uses a TOP border (sits below the workspace), not a bottom one.
    expect(infoBar.className).toContain('border-t')
  })

  it('ALWAYS renders the info bar (empty zone shows a stable empty strip — no sudden pop-in)', () => {
    renderWithProviders(
      <PluginWidgetsContext.Provider value={{ zones: {}, components: [], revision: 0 }}>
        <AppShell />
      </PluginWidgetsContext.Provider>,
    )

    const main = document.querySelector('main')
    expect(main).not.toBeNull()
    expect(main!.className).toContain('flex-col')
    // The info bar is ALWAYS rendered as a fixed-height strip, even with no
    // plugin content — so it never suddenly pops in/out.
    const infoBar = main!.children[1]
    expect((infoBar as HTMLElement).style.height).toBe('calc(1.5rem + var(--safe-area-bottom))')
  })
})
