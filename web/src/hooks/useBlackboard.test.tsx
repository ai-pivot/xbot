/**
 * useBlackboard — the shared blackboard hook.
 *
 * The contract under test is the one that keeps the panel honest:
 *   - the board is READ from the server (the SSE event is only a refetch signal),
 *   - a burst of events collapses into one refetch,
 *   - a stale in-flight response never wins (session/board switched),
 *   - mutations go through the API and then re-read the board.
 */
import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { useBlackboard } from './useBlackboard'
import type { BlackboardEntry } from '@/types/shared'

const api = vi.hoisted(() => ({
  fetchBlackboardEntries: vi.fn(),
  fetchBlackboardBoards: vi.fn(),
  fetchBlackboardEntry: vi.fn(),
  postBlackboardEntry: vi.fn(),
  closeBlackboardEntry: vi.fn(),
  releaseBlackboardEntry: vi.fn(),
  deleteBlackboardEntry: vi.fn(),
}))

vi.mock('@/components/agent/api', () => api)

function entry(overrides: Partial<BlackboardEntry> = {}): BlackboardEntry {
  return {
    board: 'web:chat-1',
    key: 'api-impl',
    kind: 'task',
    title: '实现 /v2 API',
    status: 'open',
    closed: false,
    revision: 3,
    blocked: false,
    ready: true,
    created_at: 1,
    updated_at: 1,
    ...overrides,
  }
}

const session = { channel: 'web', chatID: 'chat-1' }

afterEach(() => {
  vi.clearAllMocks()
})

describe('useBlackboard', () => {
  it('reads the board of the session and exposes the derived counts', async () => {
    api.fetchBlackboardEntries.mockResolvedValue({
      board: 'web:chat-1',
      entries: [entry(), entry({ key: 'api-design', claimed_by: 'main/explore', claim_expires_at: Date.now() + 60_000, ready: false })],
    })
    api.fetchBlackboardBoards.mockResolvedValue([{ board: 'web:chat-1', total: 2, open: 2, claimed: 1, blocked: 0, closed: 0, updated_at: 1 }])

    const { result } = renderHook(() => useBlackboard(session))

    await waitFor(() => expect(result.current.entries).toHaveLength(2))
    expect(result.current.board).toBe('web:chat-1')
    expect(api.fetchBlackboardEntries).toHaveBeenCalledWith(session, expect.objectContaining({ board: '' }))
    expect(result.current.boards).toHaveLength(1)
  })

  it('refetches when a blackboard_update event arrives, coalescing a burst into one call', async () => {
    api.fetchBlackboardEntries.mockResolvedValue({ board: 'web:chat-1', entries: [entry()] })
    api.fetchBlackboardBoards.mockResolvedValue([])

    const { result } = renderHook(() => useBlackboard(session))
    await waitFor(() => expect(result.current.entries).toHaveLength(1))
    const callsAfterLoad = api.fetchBlackboardEntries.mock.calls.length

    // A burst of changes (a plan being written) must not cost one round-trip each.
    act(() => {
      for (let i = 0; i < 5; i++) {
        window.dispatchEvent(new CustomEvent('blackboard-update', { detail: { board: 'web:chat-1', key: `k${i}`, op: 'post', revision: 1 } }))
      }
    })

    await waitFor(() => expect(api.fetchBlackboardEntries.mock.calls.length).toBeGreaterThan(callsAfterLoad))
    // Debounced: the burst produced exactly one extra read.
    expect(api.fetchBlackboardEntries.mock.calls.length).toBe(callsAfterLoad + 1)
  })

  it('drops a stale response after the session switches (never renders another board)', async () => {
    let resolveFirst: ((v: { board: string; entries: BlackboardEntry[] }) => void) | null = null
    api.fetchBlackboardEntries.mockImplementationOnce(
      () => new Promise((resolve) => { resolveFirst = resolve }),
    )
    api.fetchBlackboardEntries.mockResolvedValue({ board: 'web:chat-2', entries: [entry({ board: 'web:chat-2', key: 'from-chat-2' })] })
    api.fetchBlackboardBoards.mockResolvedValue([])

    const { result, rerender } = renderHook(({ s }) => useBlackboard(s), {
      initialProps: { s: { channel: 'web', chatID: 'chat-1' } },
    })
    // Switch sessions before the first read resolves.
    rerender({ s: { channel: 'web', chatID: 'chat-2' } })

    await waitFor(() => expect(result.current.entries.map((e) => e.key)).toEqual(['from-chat-2']))

    // The late first response must be ignored.
    await act(async () => {
      resolveFirst?.({ board: 'web:chat-1', entries: [entry({ key: 'stale-from-chat-1' })] })
    })
    expect(result.current.entries.map((e) => e.key)).toEqual(['from-chat-2'])
    expect(result.current.board).toBe('web:chat-2')
  })

  it('writes through the API and re-reads the board (single authority)', async () => {
    api.fetchBlackboardEntries.mockResolvedValue({ board: 'web:chat-1', entries: [] })
    api.fetchBlackboardBoards.mockResolvedValue([])
    api.postBlackboardEntry.mockResolvedValue(entry())
    api.closeBlackboardEntry.mockResolvedValue(entry({ closed: true }))
    api.releaseBlackboardEntry.mockResolvedValue(undefined)
    api.deleteBlackboardEntry.mockResolvedValue(undefined)

    const { result } = renderHook(() => useBlackboard(session))
    await waitFor(() => expect(api.fetchBlackboardEntries).toHaveBeenCalled())

    await act(async () => {
      await result.current.post({ key: 'api-impl', kind: 'task', title: '实现' })
    })
    expect(api.postBlackboardEntry).toHaveBeenCalledWith(session, expect.objectContaining({ key: 'api-impl', board: '' }))

    await act(async () => {
      await result.current.setClosed('api-impl', true)
      await result.current.release('api-impl')
      await result.current.remove('api-impl')
    })
    expect(api.closeBlackboardEntry).toHaveBeenCalledWith('web:chat-1', 'api-impl', true)
    expect(api.releaseBlackboardEntry).toHaveBeenCalledWith('web:chat-1', 'api-impl')
    expect(api.deleteBlackboardEntry).toHaveBeenCalledWith('web:chat-1', 'api-impl')
  })

  it('loads an entry body on demand (lists carry no bodies)', async () => {
    api.fetchBlackboardEntries.mockResolvedValue({ board: 'web:chat-1', entries: [entry()] })
    api.fetchBlackboardBoards.mockResolvedValue([])
    api.fetchBlackboardEntry.mockResolvedValue(entry({ body: '详细设计…' }))

    const { result } = renderHook(() => useBlackboard(session))
    await waitFor(() => expect(result.current.entries).toHaveLength(1))

    await act(async () => {
      await result.current.loadBody('api-impl')
    })
    expect(api.fetchBlackboardEntry).toHaveBeenCalledWith('web:chat-1', 'api-impl')
    expect(result.current.bodies['api-impl']).toBe('详细设计…')
  })
})
