import { afterEach, describe, expect, it, vi } from 'vitest'
import { postAPI, postRawAPI } from './api'
import { openSessionInBrowserTab } from '@/components/session/session-shortcuts'
import { appPath, appResourceUrl } from './appUrl'
import { renderHook } from '@testing-library/react'
import { useSWUpdate } from '@/components/SWUpdateButton'
import { registerSW } from '@/components/PWAUpdatePrompt'
import { TerminalWS } from './terminalWS'
import { PluginFileService } from '@/plugin-runtime/files'

afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('feature preview base path', () => {
  it('keeps JSON and raw requests on the preview backend', async () => {
    vi.stubEnv('BASE_URL', '/xbot/session-shortcuts/')
    const fetchMock = vi.fn(async (_url: string) => new Response(JSON.stringify({ ok: true, data: {} })))
    vi.stubGlobal('fetch', fetchMock)
    await postAPI('/api/settings')
    await postRawAPI('/api/fs/read', {})
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      '/xbot/session-shortcuts/api/settings', '/xbot/session-shortcuts/api/fs/read',
    ])
  })

  it('opens the same feature version in a browser tab', () => {
    vi.stubEnv('BASE_URL', '/xbot/session-shortcuts/')
    const open = vi.spyOn(window, 'open').mockImplementation(() => null)
    openSessionInBrowserTab({ channel: 'cli', chatID: '/work/project', label: '', lastActive: '', preview: '', status: 'idle', isCurrent: false })
    expect(open).toHaveBeenCalledWith(
      `${window.location.origin}/xbot/session-shortcuts/?session=cli%3A%2Fwork%2Fproject`,
      '_blank', 'noopener',
    )
  })

  it('never registers or updates a root service worker from a preview', () => {
    vi.stubEnv('BASE_URL', '/xbot/session-shortcuts/')
    const register = vi.fn(async () => ({ update: vi.fn(), addEventListener: vi.fn() }))
    const getRegistrations = vi.fn(async () => [])
    vi.stubGlobal('navigator', { serviceWorker: { register, getRegistrations } })
    registerSW()
    const hook = renderHook(() => useSWUpdate())
    window.dispatchEvent(new Event('load'))
    expect(register).not.toHaveBeenCalled()
    expect(getRegistrations).not.toHaveBeenCalled()
    hook.unmount()
  })

  it('rebases internal media and plugins without changing external or already-prefixed URLs', () => {
    vi.stubEnv('BASE_URL', '/xbot/session-shortcuts/')
    expect(appPath('/login')).toBe('/xbot/session-shortcuts/login')
    for (const path of ['/api/files/download?key=a%2Fb', '/plugins/xbot.genui/web/index.js', '/s/token']) {
      expect(appResourceUrl(path)).toBe(`/xbot/session-shortcuts${path}`)
      expect(appResourceUrl(appResourceUrl(path))).toBe(appResourceUrl(path))
    }
    for (const url of ['https://example.com/api/files/a', '//example.com/a', 'data:image/png;base64,A', '/other/page']) {
      expect(appResourceUrl(url)).toBe(url)
    }
  })

  it('preserves default root deployment URLs', () => {
    vi.stubEnv('BASE_URL', '/')
    expect(appResourceUrl('/api/settings')).toBe('/api/settings')
    expect(appPath('/')).toBe('/')
  })

  it('uses the preview path for terminal WebSocket upgrades', () => {
    vi.stubEnv('BASE_URL', '/xbot/session-shortcuts/')
    const urls: string[] = []
    vi.stubGlobal('WebSocket', class {
      static OPEN = 1
      constructor(url: string) { urls.push(url) }
      close() {}
    })
    const connection = new TerminalWS('terminal/a', {
      onStdout: vi.fn(), onStderr: vi.fn(), onExit: vi.fn(), onError: vi.fn(), onOpen: vi.fn(), onClose: vi.fn(),
    })
    expect(urls).toEqual([`ws://${window.location.host}/xbot/session-shortcuts/ws/terminal?tid=terminal%2Fa`])
    connection.dispose()
  })

  it('uses preview plugin-file endpoints while preserving stored canonical metadata', async () => {
    vi.stubEnv('BASE_URL', '/xbot/session-shortcuts/')
    const file = { filename: 'a.png', url: '/api/plugin-files/xbot.ambience/a.png' }
    const fetchMock = vi.fn(async (_url: string) => new Response(JSON.stringify({ ok: true, data: [file] })))
    vi.stubGlobal('fetch', fetchMock)
    const files = new PluginFileService('xbot.ambience')
    expect(await files.list()).toEqual([file])
    expect(fetchMock).toHaveBeenCalledWith('/xbot/session-shortcuts/api/plugin-files/xbot.ambience', {})
    expect(files.getUrl('a.png')).toBe('/xbot/session-shortcuts/api/plugin-files/xbot.ambience/a.png')
  })
})
