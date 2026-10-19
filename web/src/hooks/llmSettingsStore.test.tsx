import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { WSConnection } from '@/types/ws'
import { llmSettingsStore } from './llmSettingsStore'
import { useLLMSettings } from './useLLMSettings'
const connection = vi.hoisted(() => ({ current: undefined as unknown }))
vi.mock('@/hooks/useWSConnection', () => ({ useWSConnection: () => connection.current }))
function server() {
  let mode = ''
  const handlers = new Set<(connected: boolean) => void>()
  const conn = {
    connected: false,
    onConnectionChange: vi.fn((fn: (connected: boolean) => void) => {
      handlers.add(fn); return () => handlers.delete(fn)
    }),
    rpc: vi.fn(async (method: string, params?: Record<string, unknown>) => {
      if (method === 'list_subscriptions' || method === 'list_all_model_entries') return []
      if (method === 'get_user_thinking_mode') return mode
      if (method === 'set_user_thinking_mode') { mode = params?.mode as string; return null }
      if (method === 'get_llm_concurrency') return 4
      if (method === 'get_settings') return {}
      return null
    }),
  } as unknown as WSConnection
  return { conn, handlers, count: () => vi.mocked(conn.rpc).mock.calls.filter(c => c[0] === 'list_subscriptions').length }
}
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(r => { resolve = r })
  return { promise, resolve }
}
afterEach(() => vi.restoreAllMocks())
describe('shared authenticated LLM settings', () => {
  it('seven panels mount/SSE/switches share one batch and release their listener', async () => {
    const s = server(); connection.current = s.conn
    const hooks = renderHook(() => [useLLMSettings(), useLLMSettings(), useLLMSettings(), useLLMSettings(), useLLMSettings(), useLLMSettings(), useLLMSettings()])
    await waitFor(() => expect(hooks.result.current.every(h => !h.loading)).toBe(true))
    act(() => { s.handlers.forEach(fn => fn(true)); s.handlers.forEach(fn => fn(false)); s.handlers.forEach(fn => fn(true)) })
    expect(s.count()).toBe(1)
    expect(s.handlers.size).toBe(1)
    hooks.unmount(); expect(s.handlers.size).toBe(0)
  })
  it('expired reconnects coalesce; a fresh provider never sees the previous account', async () => {
    const clock = vi.spyOn(Date, 'now').mockReturnValue(1000)
    const s = server(), store = llmSettingsStore(s.conn)
    const unsubscribe = store.subscribe(() => {})
    await store.load(); clock.mockReturnValue(32_000)
    s.handlers.forEach(fn => fn(true)); await store.load()
    expect(s.count()).toBe(2)
    const other = server(); await llmSettingsStore(other.conn).load()
    expect(other.count()).toBe(1)
    unsubscribe()
  })
  it('a failed batch remains retryable instead of caching an error', async () => {
    const s = server(); vi.mocked(s.conn.rpc).mockRejectedValueOnce(new Error('offline'))
    const store = llmSettingsStore(s.conn)
    await expect(store.load()).rejects.toThrow('offline')
    expect(store.getSnapshot().error).toBe('offline')
    await store.load()
    expect(store.getSnapshot().error).toBeNull(); expect(s.count()).toBe(2)
  })
  it('mutation reloads cannot be overwritten by an older read finishing later', async () => {
    const s = server(), old = deferred<unknown>()
    vi.mocked(s.conn.rpc).mockImplementationOnce(() => old.promise as never)
    const store = llmSettingsStore(s.conn), initial = store.load()
    await store.load(true)
    old.resolve([{ id: 'stale' }]); await initial
    expect(store.getSnapshot().data.subscriptions).toEqual([])
  })
  it('a reload updates every observer without each observer issuing another batch', async () => {
    const s = server(); connection.current = s.conn
    const hooks = renderHook(() => ({ a: useLLMSettings(), b: useLLMSettings() }))
    await waitFor(() => expect(hooks.result.current.a.loading).toBe(false))
    await act(async () => { await hooks.result.current.b.setThinkingMode('high') })
    expect(hooks.result.current.a.data.thinkingMode).toBe('high')
    expect(hooks.result.current.b.data.thinkingMode).toBe('high')
    expect(s.count()).toBe(2)
  })
})
