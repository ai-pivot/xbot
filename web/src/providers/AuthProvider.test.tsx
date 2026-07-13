import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useContext, type ReactNode } from 'react'

import { postAPI } from '@/lib/api'
import {
  lastSeqCache,
  messagesCache,
  progressSnapshotCache,
  sessionCacheKey,
  SESSION_TREE_CACHE_KEY,
} from '@/lib/webCache'
import { AuthContext, AuthProvider } from './AuthProvider'

vi.mock('@/lib/api', () => ({
  postAPI: vi.fn(),
}))

const postAPIMock = vi.mocked(postAPI)

// Mock global fetch for direct fetch calls (e.g. logout uses fetch directly)
const fetchMock = vi.fn()
vi.stubGlobal('fetch', fetchMock)

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
  messagesCache.clear()
  lastSeqCache.clear()
  progressSnapshotCache.clear()
  postAPIMock.mockReset()
  fetchMock.mockReset()
  postAPIMock.mockImplementation(async (endpoint: string) => {
    if (endpoint === '/api/auth/config') return { invite_only: false }
    return {}
  })
  fetchMock.mockResolvedValue({ ok: true, json: async () => ({}) })
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
    messagesCache.set(cacheKey, [])
    lastSeqCache.set(cacheKey, 7)
    progressSnapshotCache.set(cacheKey, { phase: 'tool' })

    await act(async () => {
      await result.current?.logout()
    })

    expect(localStorage.getItem(SESSION_TREE_CACHE_KEY)).toBeNull()
    expect(messagesCache.size).toBe(0)
    expect(lastSeqCache.size).toBe(0)
    expect(progressSnapshotCache.size).toBe(0)
    expect(result.current?.user).toBeNull()
  })
})
