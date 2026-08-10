import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  bumpProgressGeneration,
  clearWebCaches,
  getCachedMessages,
  getProgressGeneration,
  lastSeqCache,
  loadSessionTreeCache,
  messagesCache,
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
  messagesCache.clear()
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
    messagesCache.set(cacheKey, { messages: [], progressGen: 0 })
    lastSeqCache.set(cacheKey, 4)
    progressSnapshotCache.set(cacheKey, { phase: 'tool' })
    bumpProgressGeneration(cacheKey)

    clearWebCaches()

    expect(localStorage.getItem(SESSION_TREE_CACHE_KEY)).toBeNull()
    expect(messagesCache.size).toBe(0)
    expect(lastSeqCache.size).toBe(0)
    expect(progressSnapshotCache.size).toBe(0)
    expect(getProgressGeneration(cacheKey)).toBe(0)
  })

  it('getCachedMessages returns null for a stale entry (progress generation changed)', () => {
    const cacheKey = sessionCacheKey('web', 'chat-1')
    messagesCache.set(cacheKey, { messages: [{ id: 'a', role: 'assistant', content: 'old', iterations: [], timestamp: '', isPartial: false, turnID: 1 }], progressGen: 0 })
    expect(getCachedMessages(cacheKey)).not.toBeNull()
    bumpProgressGeneration(cacheKey)
    expect(getCachedMessages(cacheKey)).toBeNull()
    expect(messagesCache.has(cacheKey)).toBe(false)
  })

  it('getCachedMessages returns the entry when the generation matches', () => {
    const cacheKey = sessionCacheKey('web', 'chat-2')
    const entry = { messages: [{ id: 'a', role: 'assistant' as const, content: 'x', iterations: [], timestamp: '', isPartial: false, turnID: 2 }], progressGen: 3 }
    bumpProgressGeneration(cacheKey)
    bumpProgressGeneration(cacheKey)
    bumpProgressGeneration(cacheKey)
    messagesCache.set(cacheKey, entry)
    expect(getCachedMessages(cacheKey)).toEqual(entry.messages)
  })
})
