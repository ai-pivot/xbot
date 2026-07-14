import { renderHook } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { useActiveSSESubscription } from './useActiveSSESubscription'
import type { WSConnection } from '@/types/ws'

function makeWS(): WSConnection {
  return {
    subscribe: vi.fn(),
    disconnect: vi.fn(),
    addSubscription: vi.fn().mockReturnValue('sub-1'),
    removeSubscription: vi.fn(),
  } as unknown as WSConnection
}

describe('useActiveSSESubscription', () => {
  it('creates a persistent subscription on mount and removes it on unmount', () => {
    const ws = makeWS()
    const { unmount } = renderHook(() =>
      useActiveSSESubscription({
        ws,
        chatID: '/repo:Agent-main',
        channel: 'web',
      }),
    )

    expect(ws.addSubscription).toHaveBeenCalledTimes(1)
    expect(ws.addSubscription).toHaveBeenCalledWith('/repo:Agent-main', 'web')
    expect(ws.removeSubscription).not.toHaveBeenCalled()

    unmount()

    expect(ws.removeSubscription).toHaveBeenCalledTimes(1)
    expect(ws.removeSubscription).toHaveBeenCalledWith('sub-1')
  })

  it('does not subscribe when chatID is null', () => {
    const ws = makeWS()
    renderHook(() =>
      useActiveSSESubscription({ ws, chatID: null, channel: 'web' }),
    )

    expect(ws.addSubscription).not.toHaveBeenCalled()
  })

  it('removes old subscription and creates new one when chatID changes', () => {
    const ws = makeWS()
    const { rerender } = renderHook(
      ({ chatID }) => useActiveSSESubscription({ ws, chatID, channel: 'web' }),
      { initialProps: { chatID: 'chat-1' } },
    )

    expect(ws.addSubscription).toHaveBeenCalledWith('chat-1', 'web')

    rerender({ chatID: 'chat-2' })

    expect(ws.removeSubscription).toHaveBeenCalledWith('sub-1')
    expect(ws.addSubscription).toHaveBeenLastCalledWith('chat-2', 'web')
  })

  it('does not resubscribe when only the ws wrapper identity changes', () => {
    const ws = makeWS()
    const replacement = { ...ws } as WSConnection
    const { rerender, unmount } = renderHook(
      ({ currentWS }: { currentWS: WSConnection }) =>
        useActiveSSESubscription({
          ws: currentWS,
          chatID: 'chat-1',
          channel: 'web',
        }),
      { initialProps: { currentWS: ws } },
    )

    rerender({ currentWS: replacement })

    expect(ws.addSubscription).toHaveBeenCalledTimes(1)
    expect(ws.removeSubscription).not.toHaveBeenCalled()

    unmount()
    expect(ws.removeSubscription).toHaveBeenCalledTimes(1)
    expect(ws.removeSubscription).toHaveBeenCalledWith('sub-1')
  })

  it('multiple panels can coexist — subscriptions are independent', () => {
    const ws = makeWS()
    const main = renderHook(() =>
      useActiveSSESubscription({
        ws,
        chatID: '/repo:Agent-main',
        channel: 'web',
      }),
    )
    const subAgent = renderHook(() =>
      useActiveSSESubscription({
        ws,
        chatID: 'web:/repo:Agent-main/review:1',
        channel: 'agent',
      }),
    )

    // Both panels have active subscriptions simultaneously
    expect(ws.addSubscription).toHaveBeenCalledTimes(2)
    expect(ws.addSubscription).toHaveBeenNthCalledWith(1, '/repo:Agent-main', 'web')
    expect(ws.addSubscription).toHaveBeenNthCalledWith(2, 'web:/repo:Agent-main/review:1', 'agent')

    // Neither is removed when both are alive
    expect(ws.removeSubscription).not.toHaveBeenCalled()

    // Closing one panel removes only its subscription
    main.unmount()
    expect(ws.removeSubscription).toHaveBeenCalledTimes(1)

    // The other panel's subscription is still alive
    subAgent.unmount()
    expect(ws.removeSubscription).toHaveBeenCalledTimes(2)
  })
})
