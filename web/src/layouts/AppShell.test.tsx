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
import { render, screen } from '@testing-library/react'
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
          zones: { infoBar: [{ text: 'git:feat/iteration-content-v55 Δ8', style: 'warning' }] },
          components: [],
          revision: 1,
        }}
      >
        <AppShell />
      </PluginWidgetsContext.Provider>,
    )

    // InfoBar content is rendered (plugin widget present).
    expect(screen.getByText('git:feat/iteration-content-v55 Δ8')).toBeInTheDocument()

    const main = document.querySelector('main')
    expect(main).not.toBeNull()

    // Fix 1: main is a flex COLUMN — InfoBar spans the full row width and the
    // dockview sits below it. Without flex-col the nowrap InfoBar and the
    // w-full dockview would share a row and overflow the screen.
    expect(main!.className).toContain('flex-col')

    // Order: children[0] = InfoBar, children[1] = dockview host.
    expect(main!.children.length).toBeGreaterThanOrEqual(2)
    const infoBar = main!.children[0]
    const dockview = main!.children[1]
    // InfoBar is a slim fixed-height banner.
    expect(infoBar.className).toContain('h-6')
    // Fix 2: dockview host fills the REMAINING space (flex-1 min-h-0), not
    // h-full w-full — h-full would overflow since the InfoBar consumed height.
    expect(dockview.className).toContain('flex-1')
    expect(dockview.className).toContain('min-h-0')
    expect(dockview.className).toContain('w-full')
    expect(dockview.className).not.toContain('h-full')
  })

  it('renders no info bar when the zone is empty', () => {
    renderWithProviders(
      <PluginWidgetsContext.Provider value={{ zones: {}, components: [], revision: 0 }}>
        <AppShell />
      </PluginWidgetsContext.Provider>,
    )

    const main = document.querySelector('main')
    expect(main).not.toBeNull()
    expect(main!.className).toContain('flex-col')
    // No info bar banner when there is no plugin content.
    expect(main!.children[0].className).not.toContain('h-6')
  })
})
