import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError } from '@/lib/api'

// Mock postAPI before importing the module under test.
const postAPIMock = vi.fn()
vi.mock('@/lib/api', () => {
  class APIError extends Error {
    readonly status: number
    constructor(message: string, _code: string, status: number) {
      super(message)
      this.name = 'APIError'
      this.status = status
    }
  }
  return {
    postAPI: (...args: unknown[]) => postAPIMock(...args),
    APIError,
  }
})

import {
  syncSettingToServer,
  syncAndMigrateSettings,
  SETTINGS_SYNCED_EVENT,
  __SETTING_MAP,
  __resetServerSynced,
  __markServerSynced,
} from './userSettings'

describe('userSettings', () => {
  let store: Map<string, string>
  let dispatchSpy: ReturnType<typeof vi.spyOn>

  beforeEach(() => {
    store = new Map()
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => store.get(key) ?? null,
      setItem: (key: string, value: string) => store.set(key, value),
      removeItem: (key: string) => store.delete(key),
      clear: () => store.clear(),
    })
    vi.useFakeTimers()
    __resetServerSynced()
    // 默认视为"服务端已同步完成"——只有专门验证 gate 的用例才 reset。
    __markServerSynced()
    postAPIMock.mockReset()
    dispatchSpy = vi.spyOn(window, 'dispatchEvent')
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  // ── syncSettingToServer ──────────────────────────────────────────────────

  it('maps localStorage keys to web:ui:* server keys', () => {
    expect(__SETTING_MAP['xbot-md-theme']).toBe('web:ui:md-theme')
    expect(__SETTING_MAP['xbot-accent']).toBe('web:ui:accent')
    expect(__SETTING_MAP['xbot-locale']).toBe('web:ui:locale')
    expect(__SETTING_MAP['xbot-send-key-mode']).toBe('web:ui:send-key-mode')
    expect(__SETTING_MAP['xbot-session-shortcuts']).toBe('web:ui:session-shortcuts')
    expect(__SETTING_MAP['xbot-session-shortcuts-windows']).toBe('web:ui:session-shortcuts-windows')
    expect(__SETTING_MAP['xbot:leftSidebarWidth']).toBe('web:ui:left-sidebar-width')
    expect(__SETTING_MAP['xbot-starred']).toBe('web:session:starred')
    expect(__SETTING_MAP['xbot:session-category']).toBe('web:session:category')
    expect(__SETTING_MAP['xbot:recent-workdirs:v1']).toBe('web:workspace:recent-workdirs')
  })

  it('debounces and batches multiple writes into one request', async () => {
    postAPIMock.mockResolvedValue({})

    syncSettingToServer('xbot-md-theme', 'dracula')
    syncSettingToServer('xbot-accent', '#ff0000')
    syncSettingToServer('xbot-md-theme', 'nord') // overwrite

    // No request yet — within debounce window.
    expect(postAPIMock).not.toHaveBeenCalled()

    vi.advanceTimersByTime(500)

    expect(postAPIMock).toHaveBeenCalledTimes(1)
    const [endpoint, body] = postAPIMock.mock.calls[0]
    expect(endpoint).toBe('/api/settings')
    expect(body).toEqual({
      settings: {
        'web:ui:md-theme': 'nord',
        'web:ui:accent': '#ff0000',
      },
    })
  })

  it('ignores keys not in the setting map', () => {
    syncSettingToServer('xbot-unknown-key', 'value')
    vi.advanceTimersByTime(500)
    expect(postAPIMock).not.toHaveBeenCalled()
  })

  it('syncs Windows shortcuts separately from the unchanged legacy setting', async () => {
    const mac = JSON.stringify({ star: 'meta+s' })
    const windows = JSON.stringify({ star: 'f8' })
    postAPIMock.mockResolvedValue({ settings: {
      'web:ui:session-shortcuts': mac,
      'web:ui:session-shortcuts-windows': windows,
    } })
    await syncAndMigrateSettings()
    expect(store.get('xbot-session-shortcuts')).toBe(mac)
    expect(store.get('xbot-session-shortcuts-windows')).toBe(windows)
    syncSettingToServer('xbot-session-shortcuts-windows', '{}')
    await vi.advanceTimersByTimeAsync(500)
    expect(postAPIMock).toHaveBeenLastCalledWith('/api/settings', { settings: { 'web:ui:session-shortcuts-windows': '{}' } })
  })

  // ── syncAndMigrateSettings ────────────────────────────────────────────────

  it('writes server settings to localStorage and dispatches sync event', async () => {
    postAPIMock.mockResolvedValue({
      settings: {
        'web:ui:md-theme': 'dracula',
        'web:ui:accent': '#aabbcc',
        'max_context_tokens': '200000', // non-UI key, should be ignored
      },
    })

    await syncAndMigrateSettings()

    expect(store.get('xbot-md-theme')).toBe('dracula')
    expect(store.get('xbot-accent')).toBe('#aabbcc')
    // Non-UI key not written to localStorage.
    expect(store.has('max_context_tokens')).toBe(false)

    // Sync event dispatched.
    const events = dispatchSpy.mock.calls.map((c: unknown[]) => c[0] as Event)
    const syncEvent = events.find((e: Event) => e instanceof CustomEvent && e.type === SETTINGS_SYNCED_EVENT)
    expect(syncEvent).toBeDefined()
  })

  it('migrates localStorage values to server when server has no UI settings', async () => {
    store.set('xbot-md-theme', 'nord')
    store.set('xbot-accent', '#123456')
    store.set('xbot-locale', 'en')

    // First call: server returns no web:ui:* keys.
    postAPIMock.mockResolvedValueOnce({ settings: { max_context_tokens: '200000' } })
    // Second call: the migration write.
    postAPIMock.mockResolvedValueOnce({})

    await syncAndMigrateSettings()

    // Two calls: read + write.
    expect(postAPIMock).toHaveBeenCalledTimes(2)
    const [, writeBody] = postAPIMock.mock.calls[1]
    expect(writeBody).toEqual({
      settings: {
        'web:ui:md-theme': 'nord',
        'web:ui:accent': '#123456',
        'web:ui:locale': 'en',
      },
    })
  })

  it('does not migrate when localStorage is empty', async () => {
    postAPIMock.mockResolvedValueOnce({ settings: {} })

    await syncAndMigrateSettings()

    // Only the read call, no write.
    expect(postAPIMock).toHaveBeenCalledTimes(1)
  })

  it('silently no-ops when not logged in (401)', async () => {
    postAPIMock.mockRejectedValue(new APIError('unauthorized', 'unauthorized', 401))

    await syncAndMigrateSettings()

    expect(postAPIMock).toHaveBeenCalledTimes(1)
    // No sync event dispatched.
    const events = dispatchSpy.mock.calls.map((c: unknown[]) => c[0] as Event)
    const syncEvent = events.find((e: Event) => e instanceof CustomEvent && e.type === SETTINGS_SYNCED_EVENT)
    expect(syncEvent).toBeUndefined()
  })

  /**
   * 2026-11-17 事故：组件默认值（ThemeProvider 的 'vscode-dark'）在
   * syncAndMigrateSettings 之前就调 syncSettingToServer，把服务端真实值
   * （xbot-aurora）覆盖掉了。同步完成前必须整批丢弃。
   */
  it('服务端同步完成前，任何写回都被丢弃（不用默认值覆盖真实设置）', async () => {
    __resetServerSynced()
    postAPIMock.mockClear()
    syncSettingToServer('xbot-md-theme', 'vscode-dark')
    await vi.advanceTimersByTimeAsync(2000)
    expect(postAPIMock).not.toHaveBeenCalled()
  })

  it('服务端同步完成后，用户修改才允许写回', async () => {
    postAPIMock.mockResolvedValue({ settings: { 'web:ui:md-theme': 'xbot-aurora' } })
    await syncAndMigrateSettings()
    postAPIMock.mockClear()

    syncSettingToServer('xbot-md-theme', 'dracula')
    await vi.advanceTimersByTimeAsync(2000)

    expect(postAPIMock).toHaveBeenCalledTimes(1)
    expect(postAPIMock.mock.calls[0][1]).toEqual({ settings: { 'web:ui:md-theme': 'dracula' } })
  })

  it('读取失败（服务端不可达）时不许开闸写回', async () => {
    __resetServerSynced()
    postAPIMock.mockRejectedValue(new Error('network down'))
    await syncAndMigrateSettings()
    postAPIMock.mockClear()

    syncSettingToServer('xbot-md-theme', 'vscode-dark')
    await vi.advanceTimersByTimeAsync(2000)
    expect(postAPIMock).not.toHaveBeenCalled()
  })
})
