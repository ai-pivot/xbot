import { act, renderHook, waitFor } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import { useSessionStoreImpl } from './useSessionStore'
const mocks = vi.hoisted(() => ({ post: vi.fn(), warning: vi.fn(), notify: vi.fn() }))
vi.mock('sonner', () => ({ toast: { warning: mocks.warning, error: vi.fn() } }))
vi.mock('@/hooks/useSessionGroups', () => ({ notifySessionGroupsChanged: mocks.notify }))
vi.mock('@/lib/api', () => ({ postAPI: mocks.post }))
vi.mock('@/hooks/useWSConnection', () => ({ useWSConnection: () => ({ connected: true, rpc: vi.fn(), subscribe: vi.fn(), onSession: vi.fn(() => vi.fn()), onMessage: vi.fn(() => vi.fn()) }) }))
beforeEach(() => {
  vi.clearAllMocks()
  mocks.post.mockImplementation(async (path: string) => {
    if (path === '/api/session-tree') return { sessions: [], has_more: false, next_offset: 0 }
    if (path === '/api/chats/fork') return { chat_id: 'fork-new', group_inheritance_error: 'temporary DB failure' }
    return {}
  })
})
it('keeps a successfully created fork when group inheritance fails, warns and refreshes membership readers', async () => {
  const { result } = renderHook(() => useSessionStoreImpl())
  await waitFor(() => expect(result.current.loading).toBe(false))
  let id: string | null = null
  await act(async () => { id = await result.current.forkSession('source', 'cli', 'fork label') })
  expect(id).toBe('fork-new')
  expect(mocks.post).toHaveBeenCalledWith('/api/chats/fork', { source_channel: 'cli', source_chat_id: 'source', label: 'fork label' })
  expect(mocks.warning).toHaveBeenCalledTimes(1)
  expect(mocks.notify).toHaveBeenCalledTimes(1)
})
