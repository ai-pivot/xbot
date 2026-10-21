/**
 * web 前端缓存：**已全部停用**（用户 2026-10-21「彻底去掉 session 缓存」）。
 *
 * 保留的唯一例外是 `lastSeqCache` —— 它是 SSE 协议游标（重连时跳过已处理事件），
 * 不是界面状态缓存。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  bumpProgressGeneration,
  clearWebCaches,
  getLastIteration,
  getLastSeq,
  getProgressGeneration,
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

let store: Map<string, string>

beforeEach(() => {
  store = new Map<string, string>()
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

describe('session cache identity', () => {
  it('channel 参与 key：不同 channel 的同名 chat 不互相污染', () => {
    expect(sessionCacheKey('web', 'chat-1')).not.toBe(sessionCacheKey('cli', 'chat-1'))
  })

  it('clearWebCaches 清空所有内存层', () => {
    setLastSeq(sessionCacheKey('web', 'chat-1'), 9)
    setLastIteration(sessionCacheKey('web', 'chat-1'), 7)
    clearWebCaches()
    expect(getLastSeq(sessionCacheKey('web', 'chat-1'))).toBe(0)
    expect(getLastIteration(sessionCacheKey('web', 'chat-1'))).toBe(0)
  })
})

describe('SSE 协议游标（唯一保留的缓存）', () => {
  it('lastSeq 可写可读：重连靠它跳过已处理事件', () => {
    const key = sessionCacheKey('web', 'chat-1')
    expect(getLastSeq(key)).toBe(0)
    setLastSeq(key, 42)
    expect(getLastSeq(key)).toBe(42)
  })
})

describe('其余 session 缓存全部 no-op', () => {
  it('progressSnapshotCache 不存任何东西', () => {
    const key = sessionCacheKey('web', 'chat-1')
    progressSnapshotCache.set(key, { phase: 'tool' } as never)
    expect(progressSnapshotCache.get(key)).toBeUndefined()
    expect(progressSnapshotCache.has(key)).toBe(false)
  })

  it('lastIterationCache 不存任何东西', () => {
    const key = sessionCacheKey('web', 'chat-1')
    setLastIteration(key, 5)
    expect(getLastIteration(key)).toBe(0)
  })

  it('progressGeneration 不递增缓存（bump 后仍读 0）', () => {
    const key = sessionCacheKey('web', 'chat-1')
    expect(getProgressGeneration(key)).toBe(0)
    bumpProgressGeneration(key)
    expect(getProgressGeneration(key)).toBe(0)
  })

  it('会话树不从 localStorage 恢复，且清掉历史残留', () => {
    store.set(SESSION_TREE_CACHE_KEY, JSON.stringify({ version: 1, sessions: [session], subAgents: [] }))
    expect(loadSessionTreeCache()).toBeNull()
    expect(store.has(SESSION_TREE_CACHE_KEY)).toBe(false)
  })

  it('会话树不再写入 localStorage', () => {
    saveSessionTreeCache([session], [])
    expect(store.has(SESSION_TREE_CACHE_KEY)).toBe(false)
  })
})
