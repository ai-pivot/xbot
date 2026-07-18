import { act, renderHook } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { useActiveSSESubscription } from './useActiveSSESubscription'
import type { WSConnection } from '@/types/ws'

function makeWS(): WSConnection {
  return {
    subscribe: vi.fn(),
    disconnect: vi.fn(),
  } as unknown as WSConnection
}

describe('useActiveSSESubscription', () => {
  it('hands ownership from a CLI main panel to a SubAgent and back once', () => {
    const ws = makeWS()
    const main = renderHook(
      ({ active }) => useActiveSSESubscription({
        ws,
        chatID: '/repo:Agent-main',
        channel: 'cli',
        active,
      }),
      { initialProps: { active: true } },
    )
    const subAgent = renderHook(
      ({ active }) => useActiveSSESubscription({
        ws,
        chatID: 'cli:/repo:Agent-main/review:1',
        channel: 'agent',
        active,
      }),
      { initialProps: { active: false } },
    )

    expect(ws.subscribe).toHaveBeenCalledTimes(1)
    expect(ws.subscribe).toHaveBeenLastCalledWith('/repo:Agent-main', 'cli')

    act(() => {
      main.rerender({ active: false })
      subAgent.rerender({ active: true })
    })
    expect(ws.subscribe).toHaveBeenCalledTimes(2)
    expect(ws.subscribe).toHaveBeenLastCalledWith('cli:/repo:Agent-main/review:1', 'agent')

    act(() => {
      subAgent.rerender({ active: false })
      main.rerender({ active: true })
    })
    expect(ws.subscribe).toHaveBeenCalledTimes(3)
    expect(ws.subscribe).toHaveBeenLastCalledWith('/repo:Agent-main', 'cli')
  })

  it('disconnects when the focused Agent panel has no session', () => {
    const ws = makeWS()

    renderHook(() => useActiveSSESubscription({ ws, chatID: null, channel: 'web', active: true }))

    expect(ws.disconnect).toHaveBeenCalledTimes(1)
    expect(ws.subscribe).not.toHaveBeenCalled()
  })
})
