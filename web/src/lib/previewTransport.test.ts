import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { backendFetch, postAPI, postRawAPI } from './api'
import { appPath, appResourceUrl, backendStreamUrl } from './appUrl'
import { activatePreviewConnection, clearPreviewConnection } from './previewConnection'
import { uploadFile } from '@/components/agent/api'
import { SSEConnectionImpl } from '@/providers/sseConnection'
import { TerminalWS } from './terminalWS'
import { PluginFileService } from '@/plugin-runtime/files'

beforeEach(() => {
  vi.stubGlobal('__PREVIEW_ENABLED__', true)
  vi.stubEnv('BASE_URL', '/frontend/')
  activatePreviewConnection('https://backend.example/xbot', 'secret/a+b')
})
afterEach(() => {
  clearPreviewConnection()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})
describe('external preview transport', () => {
  it('separates navigation, public modules and canonical resources without URL secrets', () => {
    expect(appPath('/login')).toBe('/frontend/login')
    expect(appResourceUrl('/s/public')).toBe('/frontend/s/public')
    expect(appResourceUrl('/plugins/test/web/index.js')).toBe('https://backend.example/xbot/plugins/test/web/index.js')
    expect(appResourceUrl('/api/files/a')).toBe('https://backend.example/xbot/api/files/a')
    expect(appResourceUrl('https://external.example/api/a')).toBe('https://external.example/api/a')
  })
  it('authorizes JSON/raw/multipart calls without cookies or redirects', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ ok: true, data: {} })))
    vi.stubGlobal('fetch', fetchMock)
    await postAPI('/api/settings')
    await postRawAPI('/api/fs/read', {})
    const form = new FormData()
    await backendFetch('/api/files/upload', { method: 'POST', body: form })
    for (const args of fetchMock.mock.calls as unknown as [string, RequestInit][]) {
      expect(args[0]).toMatch(/^https:\/\/backend.example\/xbot\/api\//)
      expect(new Headers(args[1].headers).get('Authorization')).toBe('Bearer secret/a+b')
      expect(args[1]).toMatchObject({ credentials: 'omit', redirect: 'error', mode: 'cors' })
    }
    const options = (fetchMock.mock.calls as unknown as [string, RequestInit][])[2][1]
    expect(options.body).toBe(form)
    expect(new Headers(options.headers).has('Content-Type')).toBe(false)
  })
  it('rejects external authorization destinations and disconnected calls', () => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    expect(() => backendFetch('https://evil.example/api/a')).toThrow('invalid-backend-api-url')
    expect(() => backendFetch('/api/../private')).toThrow('invalid-backend-api-url')
    expect(() => backendFetch('/api/%2e%2e/private')).toThrow('invalid-backend-api-url')
    expect(() => backendStreamUrl('/plugins/test.js')).toThrow('invalid-stream-path')
    clearPreviewConnection()
    expect(() => backendFetch('/api/settings')).toThrow('preview-not-connected')
    expect(fetchMock).not.toHaveBeenCalled()
  })
  it('limits query authorization to stream URLs and derives WS protocol from backend', () => {
    expect(backendStreamUrl('/api/sse?chat_id=a')).toBe('https://backend.example/xbot/api/sse?chat_id=a&preview_token=secret%2Fa%2Bb')
    const urls: string[] = []
    vi.stubGlobal('WebSocket', class {
      static OPEN = 1
      constructor(url: string) { urls.push(url) }
      close() {}
    })
    const conn = new TerminalWS('a/b', {
      onStdout: vi.fn(), onStderr: vi.fn(), onExit: vi.fn(), onError: vi.fn(), onOpen: vi.fn(), onClose: vi.fn(),
    })
    expect(urls).toEqual(['wss://backend.example/xbot/ws/terminal?tid=a%2Fb&preview_token=secret%2Fa%2Bb'])
    conn.dispose()
  })
  it('opens native SSE with backend stream authorization', () => {
    const urls: string[] = []
    vi.stubGlobal('EventSource', class {
      constructor(url: string) { urls.push(url) }
      addEventListener() {}
      close() {}
    })
    const conn = new SSEConnectionImpl()
    conn.subscribe('chat/a', 'web')
    const url = new URL(urls[0])
    expect(url.origin + url.pathname).toBe('https://backend.example/xbot/api/sse')
    expect(url.searchParams.get('preview_token')).toBe('secret/a+b')
    conn.dispose()
  })
  it('authorizes XHR uploads and preserves form data', async () => {
    const headers: Record<string, string> = {}
    let destination = ''
    let body: unknown
    vi.stubGlobal('XMLHttpRequest', class {
      upload = {}
      status = 200
      response = { ok: true, data: { upload_key: 'uploads/a' } }
      onload?: () => void
      open(_method: string, url: string) { destination = url }
      setRequestHeader(key: string, value: string) { headers[key] = value }
      send(value: unknown) { body = value; this.onload?.() }
    })
    await expect(uploadFile(new File(['test'], 'a.txt'))).resolves.toMatchObject({ upload_key: 'uploads/a' })
    expect(destination).toBe('https://backend.example/xbot/api/files/upload')
    expect(headers.Authorization).toBe('Bearer secret/a+b')
    expect(body).toBeInstanceOf(FormData)
  })
  it('routes plugin file operations while preserving canonical returned metadata', async () => {
    const file = { filename: 'a.png', url: '/api/plugin-files/test/a.png' }
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ ok: true, data: [file] })))
    vi.stubGlobal('fetch', fetchMock)
    const files = new PluginFileService('test')
    expect(await files.list()).toEqual([file])
    await files.delete('a.png')
    expect(files.getUrl('a.png')).toBe('https://backend.example/xbot/api/plugin-files/test/a.png')
    expect(fetchMock).toHaveBeenLastCalledWith('https://backend.example/xbot/api/plugin-files/test/a.png', expect.objectContaining({ method: 'DELETE' }))
  })
})
