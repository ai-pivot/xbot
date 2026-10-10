import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { SESSION_SHORTCUTS } from '@/components/session/session-shortcuts'
import { SETTINGS_SYNCED_EVENT, syncSettingToServer } from '@/lib/userSettings'
import { useSessionShortcuts } from './useSessionShortcuts'

vi.mock('@/lib/userSettings', async importOriginal => ({
  ...await importOriginal<typeof import('@/lib/userSettings')>(),
  syncSettingToServer: vi.fn(),
}))

const legacyKey = 'xbot-session-shortcuts'
const windowsKey = 'xbot-session-shortcuts-windows'
const windowsDefaults = {
  newSession: 'ctrl+alt+n', openInBrowserTab: 'ctrl+shift+2', star: 'ctrl+alt+s',
  rename: 'f2', fork: 'ctrl+alt+f', export: 'ctrl+alt+e', delete: 'ctrl+alt+backspace',
}

beforeEach(() => {
  localStorage.removeItem(legacyKey)
  localStorage.removeItem(windowsKey)
})
afterEach(() => {
  localStorage.removeItem(legacyKey)
  localStorage.removeItem(windowsKey)
  vi.restoreAllMocks()
})

describe('platform-isolated session shortcut settings', () => {
  it('starts Windows with independent defaults without rewriting Mac preferences', () => {
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue('Win32')
    const mac = JSON.stringify({ newSession: 'ctrl+meta+n', star: 'meta+s', delete: 'meta+backspace' })
    localStorage.setItem(legacyKey, mac)
    const { result } = renderHook(() => useSessionShortcuts())
    expect(result.current.bindings).toEqual(windowsDefaults)
    expect(localStorage.getItem(legacyKey)).toBe(mac)
    expect(localStorage.getItem(windowsKey)).toBeNull()
    expect(syncSettingToServer).not.toHaveBeenCalled()
  })

  it('saves and resets Windows overrides without touching the original key', () => {
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue('Win32')
    const mac = JSON.stringify({ star: 'meta+s' })
    localStorage.setItem(legacyKey, mac)
    const { result } = renderHook(() => useSessionShortcuts())
    act(() => result.current.setBinding('star', 'f8'))
    expect(result.current.bindings.star).toBe('f8')
    expect(localStorage.getItem(windowsKey)).toBe(JSON.stringify({ star: 'f8' }))
    expect(syncSettingToServer).toHaveBeenLastCalledWith(windowsKey, JSON.stringify({ star: 'f8' }))
    act(() => result.current.setBinding('star', 'ctrl+alt+s'))
    expect(localStorage.getItem(windowsKey)).toBe('{}')
    act(() => result.current.setBinding('delete', null))
    expect(result.current.bindings.delete).toBeNull()
    act(() => result.current.resetAll())
    expect(result.current.bindings).toEqual(windowsDefaults)
    expect(localStorage.getItem(legacyKey)).toBe(mac)
  })

  it('preserves saved Windows modifiers, old numeric overrides, and disabled actions without migration', () => {
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue('Win32')
    const overrides = { newSession: 'ctrl+meta+n', star: 'ctrl+shift+3', fork: 'meta+alt+f', delete: null }
    const raw = JSON.stringify(overrides)
    localStorage.setItem(windowsKey, raw)
    const { result } = renderHook(() => useSessionShortcuts())
    expect(result.current.bindings).toEqual({ ...windowsDefaults, ...overrides })
    expect(localStorage.getItem(windowsKey)).toBe(raw)
  })

  it('updates Windows bindings after server hydration and cross-tab changes', () => {
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue('Win32')
    const { result } = renderHook(() => useSessionShortcuts())
    act(() => {
      localStorage.setItem(windowsKey, JSON.stringify({ rename: 'f8' }))
      window.dispatchEvent(new CustomEvent(SETTINGS_SYNCED_EVENT))
    })
    expect(result.current.bindings.rename).toBe('f8')
    act(() => {
      localStorage.setItem(windowsKey, JSON.stringify({ rename: null }))
      window.dispatchEvent(new StorageEvent('storage', { key: windowsKey }))
    })
    expect(result.current.bindings.rename).toBeNull()
    act(() => {
      localStorage.setItem(legacyKey, JSON.stringify({ rename: 'f9' }))
      window.dispatchEvent(new StorageEvent('storage', { key: legacyKey }))
    })
    expect(result.current.bindings.rename).toBeNull()
  })

  it.each(['MacIntel', 'Linux x86_64'])('keeps existing configuration and writes unchanged on %s', platform => {
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue(platform)
    const windows = JSON.stringify({ star: 'f9' })
    localStorage.setItem(windowsKey, windows)
    localStorage.setItem(legacyKey, JSON.stringify({ openInTab: 'ctrl+meta+n', star: 'meta+s' }))
    const { result } = renderHook(() => useSessionShortcuts())
    expect(result.current.bindings).toEqual({ ...SESSION_SHORTCUTS, newSession: 'ctrl+meta+n', star: 'meta+s' })
    act(() => result.current.setBinding('rename', 'f8'))
    expect(JSON.parse(localStorage.getItem(legacyKey)!)).toEqual({ newSession: 'ctrl+meta+n', star: 'meta+s', rename: 'f8' })
    expect(syncSettingToServer).toHaveBeenLastCalledWith(legacyKey, expect.any(String))
    act(() => result.current.resetAll())
    expect(result.current.bindings).toEqual(SESSION_SHORTCUTS)
    expect(localStorage.getItem(windowsKey)).toBe(windows)
  })
})
