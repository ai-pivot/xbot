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
vi.mock('@/components/settings/SettingsDialog', () => ({ SettingsDialog: () => null }))
// 布局测试不关心插件视图面板内容，但要记录挂载的 container 名——布局 v5 断言
// status_bar_right 容器已移除（被引擎路 TopRail 替代），info_bar 仍在（InfoBar 内部）。
const panelContainers = vi.hoisted(() => ({ list: [] as string[] }))
vi.mock('@/plugins/manager/PluginPanelContainer', () => ({
  PluginPanelContainer: ({ container }: { container: string }) => {
    panelContainers.list.push(container)
    return null
  },
}))
// 布局 v4：面板系统聚焦外——builtin 面板不注册（dock 空），PanelDock/
// FloatingLayer 用真实实现渲染空壳，测试聚焦 main flex-col 布局回归。
vi.mock('@/components/panel/builtinPanels', () => ({ registerBuiltinPanels: vi.fn() }))

import { renderWithProviders } from '@/test-utils'
import { AppShell } from './AppShell'
import { PluginWidgetsContext } from '@/plugins/PluginWidgetProvider'

describe('AppShell workspace layout (info bar must not squeeze the dockview)', () => {
  beforeEach(() => {
    localStorage.clear()
    panelContainers.list.length = 0
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

    // Order: children[0] = header, children[1] = dockview host,
    // children[2] = bottom rail row (InfoBar + separator; BottomRailBadges
    // wired later — status-bar style at the BOTTOM, VSCode-like).
    expect(main!.children.length).toBeGreaterThanOrEqual(1)
    const dockview = main!.children[0]
    const railRow = document.querySelector('.flex.h-10.min-w-0.shrink-0.items-center.border-t') as HTMLElement
    // railRow 已上面赋值
    // Top header bar (☰ + 连接点 + 会话名 / TopRail / 环 + ⚙).
    // header 已删——功能统一到底栏
    expect(dockview.className).toContain('flex-1')
    // Dockview host fills the REMAINING space (flex-1 min-h-0), not
    // h-full w-full — h-full would overflow since the rail row consumed height.
    expect(dockview.className).toContain('flex-1')
    expect(dockview.className).toContain('min-h-0')
    expect(dockview.className).toContain('w-full')
    expect(dockview.className).not.toContain('h-full')
    // Bottom rail row: flex line, shrink-0 (fixed height, never squeezed).
    expect(railRow.className).toContain('flex')
    expect(railRow.className).toContain('shrink-0')
    // InfoBar stays INSIDE the rail row with its fixed-height strip (the
    // always-rendered gotcha is untouched): height = 1.5rem + bottom
    // safe-area inset, top border (sits below the workspace).
    const infoBarEl = railRow.querySelector('[class*="border-t"]') as HTMLElement
    expect(infoBarEl.style.height).toBe('calc(1.5rem + var(--safe-area-bottom))')
    expect(infoBarEl.style.paddingBottom).toBe('var(--safe-area-bottom)')
    expect(infoBarEl.className).toContain('border-t')
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
    // The info bar is ALWAYS rendered as a fixed-height strip (inside the
    // bottom rail row), even with no plugin content — it never pops in/out.
    const railRow = document.querySelector('.flex.h-10.min-w-0.shrink-0.items-center.border-t') as HTMLElement as HTMLElement
    const infoBarEl = railRow.querySelector('[class*="border-t"]') as HTMLElement
    expect(infoBarEl.style.height).toBe('calc(1.5rem + var(--safe-area-bottom))')
  })

  it('layout v5 header: no model pill / think pill, no status_bar_right container', () => {
    renderWithProviders(
      <PluginWidgetsContext.Provider value={{ zones: {}, components: [], revision: 0 }}>
        <AppShell />
      </PluginWidgetsContext.Provider>,
    )

    // 模型 pill（Popover 下拉）与 think pill 已整体移除（布局 v5：将来由
    // 居中插件实现，本期不留占位）。ctxUsage 上下文环保留。
    expect(screen.queryByTitle('切换模型')).not.toBeInTheDocument()
    expect(screen.queryByText(/think/)).not.toBeInTheDocument()
    expect(screen.queryByRole('img', { name: '上下文用量' })).not.toBeInTheDocument()

    // status_bar_right 插件容器已移除（被 TopRail 替代）；InfoBar 内部的
    // info_bar 容器不受影响。
    expect(panelContainers.list).not.toContain('status_bar_right')
    expect(panelContainers.list).toContain('info_bar')
  })
})
