/**
 * App 分流判据 —— 宿主嵌入态（`xbot_embed=1`）**两条路都要走嵌入**。
 *
 * 为什么值得测（真机实证 2026-10-15）：原生壳「能力面板」点插件时发的深链是
 * `?xbot_view=<viewId>&xbot_view_title=…&xbot_embed=1`（**没有** `xbot_panel`）。
 * 此前 App 的判定是 `parseEmbedQuery ? parsePanelQuery : null` ⇒ 插件视图落到 `<AppShell />`
 * （**整个 SPA**）⇒ 原生 chrome（`‹ 插件 ⟳`）之下又出现 web 自己的 `← 插件 ＋ 🔧 ⚙️` 头
 * ＋ 底部 `本机 ●` 栏 = 用户看到的"套娃"，且"为一个视图渲了整个 app"
 * （用户原话「去掉多余的 ui」「不要做多余的渲染」）。
 *
 * 判据（有判别力）：
 *  ① `?xbot_panel=tasks&xbot_embed=1` ⇒ 渲 EmbedPanelShell（kind=panel）；
 *  ② `?xbot_view=x.y.panel&xbot_view_title=…&xbot_embed=1` ⇒ 渲 EmbedPanelShell（kind=view）⭐；
 *  ③ 无 `xbot_embed=1` ⇒ 渲 AppShell（浏览器直接访问必须仍是完整应用）；
 *  ④ `?xbot_embed=1` 但两条都不可用 ⇒ 渲 AppShell（fail-safe：不渲空白壳）。
 * mutation 自证：把 App 的判定改回「只认 xbot_panel」⇒ ② 必红（其余三条仍绿）。
 */
import { render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

// ⛔ `vi.mock` 工厂会被提升 ⇒ 工厂内**不得**引用顶层变量
//   （vitest 硬错：Cannot access 'x' before initialization）。
vi.mock('@/layouts/AppShell', () => ({ AppShell: () => <div data-testid="app-shell" /> }))
vi.mock('@/layouts/EmbedPanelShell', () => ({
  EmbedPanelShell: ({
    target,
  }: {
    target: { kind: string; panelId?: string; viewId?: string }
  }) => <div data-testid="embed-shell">{`${target.kind}:${target.panelId ?? target.viewId}`}</div>,
}))

// 重型 provider / 页面：本判据只关心「分流到哪个外壳」，其余一律透传或空实现。
vi.mock('@/providers/WSProvider', () => ({
  WSProvider: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
}))
vi.mock('@/providers/CwdProvider', () => ({
  CwdProvider: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
}))
vi.mock('@/hooks/useSessionStore', () => ({
  SessionStoreProvider: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
}))
vi.mock('@/plugins/PluginWidgetProvider', () => ({
  PluginWidgetProvider: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
}))
vi.mock('@/plugin-runtime/usePluginRuntimeHost', () => ({
  PluginRuntimeRoot: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
}))
vi.mock('@/plugin-runtime/layoutRegistry', () => ({ registerBuiltinLayoutItems: () => {} }))
vi.mock('@/components/auth/AuthGuard', () => ({
  AuthGuard: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
}))
vi.mock('@/components/ui/sonner', () => ({ Toaster: () => null }))
vi.mock('@/components/ui/tooltip', () => ({
  TooltipProvider: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
}))
vi.mock('@/components/agent/Lightbox', () => ({ ImageLightboxHost: () => null }))
vi.mock('@/pages/LoginPage', () => ({ LoginPage: () => null }))
vi.mock('@/pages/RegisterPage', () => ({ RegisterPage: () => null }))
vi.mock('@/pages/SharePage', () => ({ SharePage: () => null }))

import App from '@/App'

function withSearch(search: string) {
  window.history.replaceState({}, '', `/${search}`)
}

afterEach(() => {
  withSearch('')
})

describe('App 宿主嵌入分流（xbot_panel 与 xbot_view 两条路）', () => {
  it('① 内置面板深链 ⇒ EmbedPanelShell(kind=panel)', () => {
    withSearch('?xbot_panel=tasks&xbot_embed=1')
    render(<App />)
    expect(screen.getByTestId('embed-shell').textContent).toBe('panel:tasks')
    expect(screen.queryByTestId('app-shell')).toBeNull()
  })

  it('② ⭐ 插件视图深链（无 xbot_panel）⇒ EmbedPanelShell(kind=view)，⛔ 不得落 AppShell', () => {
    withSearch('?xbot_view=xbot.plugin-manager.panel&xbot_view_title=%E6%8F%92%E4%BB%B6&xbot_embed=1')
    render(<App />)
    expect(screen.getByTestId('embed-shell').textContent).toBe('view:xbot.plugin-manager.panel')
    expect(screen.queryByTestId('app-shell')).toBeNull()
  })

  it('③ 非嵌入态（浏览器直接访问）⇒ AppShell（完整应用）', () => {
    withSearch('')
    render(<App />)
    expect(screen.getByTestId('app-shell')).toBeTruthy()
    expect(screen.queryByTestId('embed-shell')).toBeNull()
  })

  it('④ 嵌入态但无可用目标 ⇒ AppShell（fail-safe：不给宿主渲空白壳）', () => {
    withSearch('?xbot_embed=1&xbot_panel=nope')
    render(<App />)
    expect(screen.getByTestId('app-shell')).toBeTruthy()
    expect(screen.queryByTestId('embed-shell')).toBeNull()
  })
})
