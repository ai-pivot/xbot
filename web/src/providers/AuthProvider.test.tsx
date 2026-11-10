import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useContext, type ReactNode } from 'react'

import { postAPI } from '@/lib/api'
import {
  lastSeqCache,
  progressSnapshotCache,
  sessionCacheKey,
  SESSION_TREE_CACHE_KEY,
} from '@/lib/webCache'
import { AuthContext, AuthProvider } from './AuthProvider'

vi.mock('@/lib/api', () => ({
  postAPI: vi.fn(),
}))

const postAPIMock = vi.mocked(postAPI)

function wrapper({ children }: { children: ReactNode }) {
  return <AuthProvider>{children}</AuthProvider>
}

beforeEach(() => {
  const store = new Map<string, string>()
  vi.stubGlobal('localStorage', {
    getItem: vi.fn((key: string) => store.get(key) ?? null),
    setItem: vi.fn((key: string, value: string) => store.set(key, value)),
    removeItem: vi.fn((key: string) => store.delete(key)),
    clear: vi.fn(() => store.clear()),
  })
  lastSeqCache.clear()
  progressSnapshotCache.clear()
  postAPIMock.mockReset()
  postAPIMock.mockImplementation(async (endpoint: string) => {
    if (endpoint === '/api/auth/config') return { invite_only: false }
    return {}
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('AuthProvider cache isolation', () => {
  it('clears session caches before exposing the logged-out state', async () => {
    const { result } = renderHook(() => useContext(AuthContext), { wrapper })
    await waitFor(() => expect(result.current?.loading).toBe(false))

    localStorage.setItem(SESSION_TREE_CACHE_KEY, '{"version":1,"sessions":[],"subAgents":[]}')
    const cacheKey = sessionCacheKey('web', 'chat-a')
    lastSeqCache.set(cacheKey, 7)
    progressSnapshotCache.set(cacheKey, { phase: 'tool' })

    await act(async () => {
      await result.current?.logout()
    })

    expect(localStorage.getItem(SESSION_TREE_CACHE_KEY)).toBeNull()
    expect(lastSeqCache.size).toBe(0)
    expect(progressSnapshotCache.size).toBe(0)
    expect(result.current?.user).toBeNull()
  })
})

describe('preview authentication', () => {
  afterEach(() => vi.unstubAllGlobals())
  it('hydrates the verified identity without probing normal account endpoints', async () => {
    vi.stubGlobal('__PREVIEW_ENABLED__', true)
    postAPIMock.mockResolvedValue({ user_id: 42, username: 'preview-user' })
    const { result } = renderHook(() => useContext(AuthContext), { wrapper })
    await waitFor(() => expect(result.current?.loading).toBe(false))
    expect(result.current?.user).toEqual({ username: 'preview-user' })
    expect(postAPIMock.mock.calls.map(([path]) => path)).toEqual(['/api/preview/session'])
    await act(async () => {
      expect(await result.current?.login('user', 'password')).toBe(false)
      expect(await result.current?.register('user', 'password')).toBe(false)
    })
    expect(postAPIMock).toHaveBeenCalledTimes(1)
  })
  it('falls back to the numeric user id when no username is provided', async () => {
    vi.stubGlobal('__PREVIEW_ENABLED__', true)
    postAPIMock.mockResolvedValue({ user_id: 42 })
    const { result } = renderHook(() => useContext(AuthContext), { wrapper })
    await waitFor(() => expect(result.current?.loading).toBe(false))
    expect(result.current?.user).toEqual({ username: '42' })
  })
  it('fails closed when preview identity verification fails', async () => {
    vi.stubGlobal('__PREVIEW_ENABLED__', true)
    postAPIMock.mockRejectedValue(new Error('expired'))
    const { result } = renderHook(() => useContext(AuthContext), { wrapper })
    await waitFor(() => expect(result.current?.loading).toBe(false))
    expect(result.current?.user).toBeNull()
  })
})


it('revokes only the preview authorization and disconnects locally on logout', async () => {
  vi.stubGlobal('__PREVIEW_ENABLED__', true)
  const { activatePreviewConnection, getPreviewToken } = await import('@/lib/previewConnection')
  activatePreviewConnection('https://backend.example', 'token')
  const reload = vi.fn()
  vi.stubGlobal('window', new Proxy(window, {
    get(target, key) { return key === 'location' ? { reload } : Reflect.get(target, key) },
  }))
  postAPIMock.mockResolvedValue({ user_id: 42, username: 'preview-user' })
  const { result } = renderHook(() => useContext(AuthContext), { wrapper })
  await waitFor(() => expect(result.current?.loading).toBe(false))
  await act(async () => { await result.current?.logout() })
  expect(postAPIMock.mock.calls.map(([path]) => path)).toEqual(['/api/preview/session', '/api/preview/revoke'])
  expect(getPreviewToken()).toBe('')
  expect(result.current?.user).toBeNull()
  expect(reload).toHaveBeenCalledOnce()
})
