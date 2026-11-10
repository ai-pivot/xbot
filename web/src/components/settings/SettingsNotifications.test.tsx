import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'

import i18n from '@/i18n'
import { renderWithProviders } from '@/test-utils'
import { SettingsNotifications } from './SettingsNotifications'

// 预览构建 / 子路径构建禁用 PWA（__PWA_ENABLED__ = false）：不得注册任何 service worker。
// 开启通知会 register('/sw2.js')，而预览构建根本不产出 sw2.js，且约定运行时零注册。
describe('SettingsNotifications service-worker gating', () => {
  const register = vi.fn(async () => ({}) as ServiceWorkerRegistration)
  const getRegistration = vi.fn(async () => undefined)

  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ ok: true, data: { enable: false, include_preview: false } }))))
    vi.stubGlobal('PushManager', class {})
    vi.stubGlobal('Notification', class { static permission = 'default' })
    Object.defineProperty(navigator, 'serviceWorker', {
      configurable: true,
      value: { register, getRegistration, ready: new Promise(() => {}) },
    })
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    Reflect.deleteProperty(navigator, 'serviceWorker')
    register.mockClear()
    getRegistration.mockClear()
  })

  it('treats PWA-disabled builds as unsupported and never touches service workers', async () => {
    vi.stubGlobal('__PWA_ENABLED__', false)
    renderWithProviders(<SettingsNotifications />)
    expect(await screen.findByText(i18n.t('settings.notifications.unsupported'))).toBeTruthy()
    expect(screen.queryByText(i18n.t('settings.notifications.notEnabled'))).toBeNull()
    expect(getRegistration).not.toHaveBeenCalled()
    expect(register).not.toHaveBeenCalled()
  })

  it('keeps the normal toggle when PWA is enabled', async () => {
    vi.stubGlobal('__PWA_ENABLED__', true)
    renderWithProviders(<SettingsNotifications />)
    expect(await screen.findByText(i18n.t('settings.notifications.notEnabled'))).toBeTruthy()
    await waitFor(() => expect(getRegistration).toHaveBeenCalled())
    expect(register).not.toHaveBeenCalled()
  })
})
