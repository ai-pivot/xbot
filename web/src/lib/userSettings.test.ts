import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// Mock postAPI before importing the module under test.
const postAPIMock = vi.fn()
vi.mock('@/lib/api', () => ({
  postAPI: (...args: unknown[]) => postAPIMock(...args),
}))

import {
  syncSettingToServer,
  syncAndMigrateSettings,
  SETTINGS_SYNCED_EVENT,
  __SETTING_MAP,
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
    expect(__SETTING_MAP['xbot-collapse-level']).toBe('web:ui:collapse-level')
    expect(__SETTING_MAP['xbot-merge-tools']).toBe('web:ui:merge-tools')
    expect(__SETTING_MAP['xbot:leftSidebarWidth']).toBe('web:ui:left-sidebar-width')
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
    postAPIMock.mockRejectedValue(new Error('unauthorized'))

    await syncAndMigrateSettings()

    expect(postAPIMock).toHaveBeenCalledTimes(1)
    // No sync event dispatched.
    const events = dispatchSpy.mock.calls.map((c: unknown[]) => c[0] as Event)
    const syncEvent = events.find((e: Event) => e instanceof CustomEvent && e.type === SETTINGS_SYNCED_EVENT)
    expect(syncEvent).toBeUndefined()
  })
})
