import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  activatePreviewConnection, backendUrl, backendWebSocketUrl, clearPreviewConnection,
  DEFAULT_PREVIEW_BACKEND, getPreviewToken, normalizeBackendUrl, readPreviewBackend,
  savePreviewBackend, testPreviewConnection, verifyPreviewToken,
} from './previewConnection'

beforeEach(() => {
  const values = new Map<string, string>()
  vi.stubGlobal('localStorage', { getItem: (key: string) => values.get(key) ?? null, setItem: (key: string, value: string) => { values.set(key, value) }, clear: () => values.clear(), get length() { return values.size } })
})

afterEach(() => { clearPreviewConnection(); localStorage.clear(); vi.useRealTimers(); vi.unstubAllGlobals(); vi.restoreAllMocks() })
const config = () => new Response(JSON.stringify({ ok: true, data: { invite_only: false }, error: null }))

describe('preview backend configuration', () => {
  it('defaults to editable localhost and preserves backend subpaths', () => {
    expect(readPreviewBackend()).toBe(DEFAULT_PREVIEW_BACKEND)
    expect(normalizeBackendUrl(' https://HOST/xbot/// ')).toBe('https://host/xbot')
    expect(backendUrl('/api/settings', 'https://host/xbot')).toBe('https://host/xbot/api/settings')
  })
  it.each(['ftp://host', '//host', 'https://a:b@host', 'https://@host', 'https://host?', 'https://host#', 'https://host/?a=1', 'https://host/#a', 'http://host\\evil', 'http://ho st'])('rejects %s', value => {
    expect(() => normalizeBackendUrl(value)).toThrow()
  })
  it('stores only a normalized backend address and tolerates inaccessible storage', () => {
    expect(savePreviewBackend('https://host/backend/')).toBe(true)
    expect(readPreviewBackend()).toBe('https://host/backend')
    vi.spyOn(localStorage, 'setItem').mockImplementation(() => { throw new Error('blocked') })
    expect(savePreviewBackend('https://other')).toBe(false)
    vi.spyOn(localStorage, 'getItem').mockImplementation(() => { throw new Error('blocked') })
    expect(readPreviewBackend()).toBe(DEFAULT_PREVIEW_BACKEND)
  })
  it('keeps token and selected base in memory and chooses backend WebSocket scheme', () => {
    vi.stubGlobal('__PREVIEW_ENABLED__', true)
    activatePreviewConnection('http://backend:8082/xbot', 'secret')
    expect(getPreviewToken()).toBe('secret')
    expect(backendWebSocketUrl('/ws')).toBe('ws://backend:8082/xbot/ws')
    expect(localStorage.length).toBe(0)
    clearPreviewConnection()
    expect(getPreviewToken()).toBe('')
  })
  it('never treats network success as authorization, sends no cookies/model calls', async () => {
    const fetchMock = vi.fn().mockResolvedValue(config())
    vi.stubGlobal('fetch', fetchMock)
    await testPreviewConnection('http://localhost:8082')
    expect(fetchMock).toHaveBeenCalledWith('http://localhost:8082/api/auth/config', expect.objectContaining({ method: 'POST', credentials: 'omit', redirect: 'error' }))
    expect(getPreviewToken()).toBe('')
  })
  it('verifies the token without activating or persisting it', async () => {
    const expiry = new Date(Date.now() + 60_000).toISOString()
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ ok: true, data: { user_id: 'u', origin: window.location.origin, expires_at: expiry }, error: null })))
    vi.stubGlobal('fetch', fetchMock)
    expect(await verifyPreviewToken('https://backend/base', 'secret')).toBe(expiry)
    expect(fetchMock).toHaveBeenCalledWith('https://backend/base/api/preview/session', expect.objectContaining({ credentials: 'omit', headers: { 'Content-Type': 'application/json', Authorization: 'Bearer secret' } }))
    expect(getPreviewToken()).toBe('')
    expect(localStorage.length).toBe(0)
  })
  it('rejects HTML and HTTP failures instead of claiming health', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('<html>SPA</html>')))
    await expect(testPreviewConnection('https://host')).rejects.toMatchObject({ reason: 'response' })
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('', { status: 403 })))
    await expect(testPreviewConnection('https://host')).rejects.toMatchObject({ reason: 'http', status: 403 })
  })
  it('times out and aborts even a fetch that never settles', async () => {
    vi.useFakeTimers()
    const fetchMock = vi.fn().mockReturnValue(new Promise(() => {}))
    vi.stubGlobal('fetch', fetchMock)
    const result = testPreviewConnection('https://host', undefined, 50)
    const assertion = expect(result).rejects.toMatchObject({ reason: 'timeout' })
    await vi.advanceTimersByTimeAsync(50)
    await assertion
    expect(fetchMock.mock.calls[0][1].signal.aborted).toBe(true)
  })
  it('cancels promptly without reporting timeout', async () => {
    vi.stubGlobal('fetch', vi.fn().mockReturnValue(new Promise(() => {})))
    const controller = new AbortController()
    const result = testPreviewConnection('https://host', controller.signal)
    controller.abort()
    await expect(result).rejects.toMatchObject({ name: 'AbortError' })
  })
})
