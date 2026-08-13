import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  bumpProgressGeneration,
  clearWebCaches,
  getProgressGeneration,
  lastSeqCache,
  loadSessionTreeCache,
  progressSnapshotCache,
  saveSessionTreeCache,
  sessionCacheKey,
  SESSION_TREE_CACHE_KEY,
} from './webCache'
import type { SessionInfo } from '@/types/shared'

const session: SessionInfo = {
  chatID: 'chat-1',
  channel: 'web',
  label: 'Chat',
  lastActive: '2026-07-13T00:00:00Z',
  preview: '',
  status: 'idle',
  isCurrent: true,
}

beforeEach(() => {
  const store = new Map<string, string>()
  vi.stubGlobal('localStorage', {
    getItem: (key: string) => store.get(key) ?? null,
    setItem: (key: string, value: string) => store.set(key, value),
    removeItem: (key: string) => store.delete(key),
    clear: () => store.clear(),
  })
  lastSeqCache.clear()
  progressSnapshotCache.clear()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('web caches', () => {
  it('persists a versioned session tree', () => {
    saveSessionTreeCache([session], [])
    expect(loadSessionTreeCache()).toEqual({ version: 1, sessions: [session], subAgents: [] })
  })

  it('uses the channel as part of every session cache identity', () => {
    expect(sessionCacheKey('web', 'shared')).not.toBe(sessionCacheKey('cli', 'shared'))
  })

  it('clears local and in-memory cache layers together', () => {
    localStorage.setItem(SESSION_TREE_CACHE_KEY, '{}')
    const cacheKey = sessionCacheKey('web', 'chat-1')
    lastSeqCache.set(cacheKey, 4)
    progressSnapshotCache.set(cacheKey, { phase: 'tool' })
    bumpProgressGeneration(cacheKey)

    clearWebCaches()

    expect(localStorage.getItem(SESSION_TREE_CACHE_KEY)).toBeNull()
    expect(lastSeqCache.size).toBe(0)
    expect(progressSnapshotCache.size).toBe(0)
    expect(getProgressGeneration(cacheKey)).toBe(0)
  })

  it('getProgressGeneration starts at 0 and bumps monotonically', () => {
    const cacheKey = sessionCacheKey('web', 'chat-2')
    expect(getProgressGeneration(cacheKey)).toBe(0)
    bumpProgressGeneration(cacheKey)
    expect(getProgressGeneration(cacheKey)).toBe(1)
    bumpProgressGeneration(cacheKey)
    expect(getProgressGeneration(cacheKey)).toBe(2)
  })
})
