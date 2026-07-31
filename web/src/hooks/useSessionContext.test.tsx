import { act, renderHook, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { WSConnection } from '@/types/ws'
import { useSessionContext } from './useSessionContext'

const connection = vi.hoisted(() => ({ current: undefined as unknown }))

vi.mock('@/hooks/useWSConnection', () => ({
  useWSConnection: () => connection.current,
}))

function makeConnection(rpc: WSConnection['rpc'], connected = true): WSConnection {
  return { connected, rpc } as WSConnection
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => {
    resolve = done
  })
  return { promise, resolve }
}

const snapshot = {
  available: true,
  prompt_tokens: 123_456,
  completion_tokens: 789,
  max_context_tokens: 200_000,
  usage_percent: 61.728,
  model: 'model-a',
  subscription_id: 'sub-a',
  subscription_name: 'Account A',
}

describe('useSessionContext', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
  })

  it('loads the complete authoritative snapshot with one RPC', async () => {
    const rpc = vi.fn().mockResolvedValue(snapshot)
    connection.current = makeConnection(rpc as WSConnection['rpc'])

    const { result } = renderHook(() => useSessionContext('web', 'chat-a'))

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(rpc).toHaveBeenCalledTimes(1)
    expect(rpc).toHaveBeenCalledWith('get_context_usage', {
      channel: 'web',
      chat_id: 'chat-a',
    })
    expect(result.current).toMatchObject({
      available: true,
      promptTokens: 123_456,
      completionTokens: 789,
      maxContext: 200_000,
      usagePercent: 61.728,
      model: 'model-a',
      subscriptionID: 'sub-a',
      subscriptionName: 'Account A',
    })
  })

  it('keeps the last exact snapshot visible while refreshing', async () => {
    const next = deferred<typeof snapshot>()
    const rpc = vi.fn()
      .mockResolvedValueOnce(snapshot)
      .mockReturnValueOnce(next.promise)
    connection.current = makeConnection(rpc)

    const { result } = renderHook(() => useSessionContext('web', 'chat-a'))
    await waitFor(() => expect(result.current.promptTokens).toBe(123_456))

    let refresh!: Promise<void>
    act(() => {
      refresh = result.current.refresh()
    })
    expect(result.current.loading).toBe(true)
    expect(result.current.promptTokens).toBe(123_456)

    next.resolve({ ...snapshot, prompt_tokens: 140_000, usage_percent: 70 })
    await act(async () => refresh)
    expect(result.current.promptTokens).toBe(140_000)
    expect(result.current.usagePercent).toBe(70)
  })

  it('enters the unknown state when the backend invalidates usage after a model switch', async () => {
    const rpc = vi.fn()
      .mockResolvedValueOnce(snapshot)
      .mockResolvedValueOnce({
        ...snapshot,
        available: false,
        prompt_tokens: 0,
        completion_tokens: 0,
        usage_percent: null,
        model: 'model-b',
      })
    connection.current = makeConnection(rpc)

    const { result } = renderHook(() => useSessionContext('web', 'chat-a'))
    await waitFor(() => expect(result.current.available).toBe(true))
    await act(async () => result.current.refresh())

    expect(result.current).toMatchObject({
      available: false,
      promptTokens: 0,
      completionTokens: 0,
      maxContext: 200_000,
      usagePercent: null,
      model: 'model-b',
    })
  })

  it('ignores an older response after a fast session switch', async () => {
    const oldRequest = deferred<typeof snapshot>()
    const newRequest = deferred<typeof snapshot>()
    const rpc = vi.fn((_method: string, params: unknown) => {
      const chatID = (params as { chat_id: string }).chat_id
      return chatID === 'chat-a' ? oldRequest.promise : newRequest.promise
    })
    connection.current = makeConnection(rpc as WSConnection['rpc'])

    const { result, rerender } = renderHook(
      ({ chatID }) => useSessionContext('web', chatID),
      { initialProps: { chatID: 'chat-a' } },
    )
    rerender({ chatID: 'chat-b' })

    newRequest.resolve({
      ...snapshot,
      prompt_tokens: 50_000,
      usage_percent: 25,
      model: 'model-b',
      subscription_id: 'sub-b',
    })
    await waitFor(() => expect(result.current.model).toBe('model-b'))

    oldRequest.resolve(snapshot)
    await act(async () => oldRequest.promise)
    expect(result.current).toMatchObject({
      promptTokens: 50_000,
      model: 'model-b',
      subscriptionID: 'sub-b',
    })
  })
})
