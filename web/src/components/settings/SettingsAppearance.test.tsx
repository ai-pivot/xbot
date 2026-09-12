import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'

// syncSettingToServer 走网络 —— 测试只断言 localStorage + UI 状态。
vi.mock('@/lib/userSettings', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/userSettings')>()
  return { ...actual, syncSettingToServer: vi.fn() }
})

// useTheme 必须在 ThemeProvider 内使用 —— 本测试只关心 UI 模式区块。
vi.mock('@/hooks/useTheme', () => ({
  useTheme: () => ({
    accentColor: '#3388BB',
    setAccentColor: vi.fn(),
    mdTheme: 'vscode-dark',
    setMdTheme: vi.fn(),
  }),
}))

import { renderWithProviders } from '@/test-utils'
import { UI_MODE_STORAGE_KEY } from '@/hooks/useUIMode'
import { SettingsAppearance } from './SettingsAppearance'

describe('SettingsAppearance — UI 模式切换', () => {
  beforeEach(() => {
    const store = new Map<string, string>()
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => store.get(key) ?? null,
      setItem: (key: string, value: string) => store.set(key, value),
      removeItem: (key: string) => store.delete(key),
    })
    vi.stubGlobal('matchMedia', (query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    }))
  })

  it('渲染三个模式选项，默认 auto 选中', () => {
    renderWithProviders(<SettingsAppearance />)
    expect(screen.getByTestId('ui-mode-auto')).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByTestId('ui-mode-desktop')).toHaveAttribute('aria-pressed', 'false')
    expect(screen.getByTestId('ui-mode-mobile')).toHaveAttribute('aria-pressed', 'false')
  })

  it('读回已存偏好（mobile）', () => {
    localStorage.setItem(UI_MODE_STORAGE_KEY, 'mobile')
    renderWithProviders(<SettingsAppearance />)
    expect(screen.getByTestId('ui-mode-mobile')).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByTestId('ui-mode-auto')).toHaveAttribute('aria-pressed', 'false')
  })

  it('点击切换写 localStorage 并更新选中态', () => {
    renderWithProviders(<SettingsAppearance />)
    fireEvent.click(screen.getByTestId('ui-mode-desktop'))
    expect(localStorage.getItem(UI_MODE_STORAGE_KEY)).toBe('desktop')
    expect(screen.getByTestId('ui-mode-desktop')).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByTestId('ui-mode-auto')).toHaveAttribute('aria-pressed', 'false')
  })

  it('「当前生效」行渲染插值后的模式名（不得显示原始 {mode} 模板）', () => {
    renderWithProviders(<SettingsAppearance />)
    const line = screen.getByText(/当前生效：|Currently active:/)
    expect(line.textContent).not.toContain('{mode}')
    expect(line.textContent).toMatch(/桌面|Desktop/)
  })

  it('切换模式后「当前生效」行同步更新', () => {
    renderWithProviders(<SettingsAppearance />)
    fireEvent.click(screen.getByTestId('ui-mode-mobile'))
    const line = screen.getByText(/当前生效：|Currently active:/)
    expect(line.textContent).not.toContain('{mode}')
    expect(line.textContent).toMatch(/移动端|Mobile/)
  })
})
