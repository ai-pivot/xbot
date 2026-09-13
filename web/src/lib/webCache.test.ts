import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  bumpProgressGeneration,
  clearWebCaches,
  getLastIteration,
  getLastSeq,
  getProgressGeneration,
  hasLastSeq,
  lastIterationCache,
  lastSeqCache,
  loadSessionTreeCache,
  progressSnapshotCache,
  saveSessionTreeCache,
  sessionCacheKey,
  SESSION_TREE_CACHE_KEY,
  setLastIteration,
  setLastSeq,
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
  lastIterationCache.clear()
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

// ── Session-scoped cache bounds (LRU) + snapshot trimming ────────────────────
// The session-keyed Maps here (lastSeqCache / lastIterationCache /
// progressSnapshotCache / progressGenerationCache) previously kept one entry per
// visited session FOREVER. Bound them by recency and trim the reconnect snapshot
// so "many sessions × heavy iterations" cannot multiply. Contract pinned with
// literal bounds on purpose (the tests should not just mirror the constant).
const MAX_CACHED_SESSIONS = 8
const MAX_CACHED_ITERATION_HISTORY = 4

describe('session cache bounds (LRU)', () => {
  beforeEach(() => {
    clearWebCaches()
    lastIterationCache.clear()
  })

  it('① keeps at most MAX_CACHED_SESSIONS entries and evicts the least recently used', () => {
    const keys = Array.from({ length: MAX_CACHED_SESSIONS + 1 }, (_, i) => sessionCacheKey('web', `s${i}`))
    keys.forEach((key, i) => setLastSeq(key, i + 1))

    expect(lastSeqCache.size).toBe(MAX_CACHED_SESSIONS)
    // Oldest (first inserted, never re-used) is the eviction victim.
    expect(lastSeqCache.has(keys[0])).toBe(false)
    expect(getLastSeq(keys[0])).toBe(0)
    // Newest survives.
    expect(lastSeqCache.has(keys[MAX_CACHED_SESSIONS])).toBe(true)
    expect(getLastSeq(keys[MAX_CACHED_SESSIONS])).toBe(MAX_CACHED_SESSIONS + 1)
  })

  it('② an actively-used session keeps its recovery state under eviction pressure', () => {
    const current = sessionCacheKey('web', 'current')
    setLastSeq(current, 42)
    setLastIteration(current, 7)
    progressSnapshotCache.set(current, { phase: 'tool', iteration: 7, turn_id: 3, iteration_history: [{ iteration: 7 }] })

    // 20 background sessions are visited while the active session keeps
    // receiving events → each access refreshes its recency (as the real SSE
    // event loop does).
    for (let i = 0; i < 20; i++) {
      setLastSeq(sessionCacheKey('web', `bg${i}`), i + 1)
      expect(getLastSeq(current)).toBe(42)
    }

    // Bound holds, but the active session's recovery data is intact.
    expect(lastSeqCache.size).toBeLessThanOrEqual(MAX_CACHED_SESSIONS)
    expect(hasLastSeq(current)).toBe(true)
    expect(getLastSeq(current)).toBe(42)
    expect(getLastIteration(current)).toBe(7)
    expect(progressSnapshotCache.get(current)).toMatchObject({ iteration: 7, turn_id: 3 })
  })

  it('③ trims a heavy snapshot to the recovery-critical fields and bounded history', () => {
    const key = sessionCacheKey('web', 'heavy')
    const history = Array.from({ length: 50 }, (_, i) => ({
      iteration: i + 1,
      content: 'c'.repeat(1000),
      reasoning: 'r'.repeat(1000),
      tools: [{ name: 'Read', status: 'done' }],
    }))
    progressSnapshotCache.set(key, {
      phase: 'tool',
      iteration: 50,
      turn_id: 3,
      content: 'big'.repeat(1000),
      reasoning: 'big'.repeat(1000),
      iteration_history: history,
    })

    const cached = progressSnapshotCache.get(key)!
    expect(cached.phase).toBe('tool')
    expect(cached.iteration).toBe(50)
    expect(cached.turn_id).toBe(3)
    // History bounded to the last N entries, each reduced to its identity.
    expect(cached.iteration_history).toEqual([
      { iteration: 47 },
      { iteration: 48 },
      { iteration: 49 },
      { iteration: 50 },
    ])
    expect(cached.iteration_history).toHaveLength(MAX_CACHED_ITERATION_HISTORY)
    // Heavy per-iteration / streaming payload is NOT cached.
    expect(cached.content).toBeUndefined()
    expect(cached.reasoning).toBeUndefined()
  })

  it('④ re-accessing a session returns the cached value (LRU — not wiped on overflow)', () => {
    const warm = sessionCacheKey('web', 'warm')
    setLastSeq(warm, 5)
    for (let i = 0; i < 20; i++) {
      setLastSeq(sessionCacheKey('web', `c${i}`), i + 1)
      // Second (and Nth) access must still HIT — a cache that cleared itself
      // whenever it overflowed would return 0 here.
      expect(getLastSeq(warm)).toBe(5)
    }
  })

  it('⑤ bounds the iteration/generation caches on the same recency rule', () => {
    const keys = Array.from({ length: MAX_CACHED_SESSIONS + 1 }, (_, i) => sessionCacheKey('web', `n${i}`))
    keys.forEach((key, i) => {
      setLastIteration(key, i + 1)
      bumpProgressGeneration(key)
    })

    // Oldest evicted → its generation is gone (falls back to 0), latest kept.
    expect(getProgressGeneration(keys[0])).toBe(0)
    expect(getProgressGeneration(keys[MAX_CACHED_SESSIONS])).toBe(1)
  })
})
