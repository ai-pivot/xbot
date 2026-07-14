import { act, renderHook, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { useChatMessages } from './useChatMessages'
import type { WSConnection } from '@/types/ws'

function makeWS(responses: unknown[]): WSConnection {
  vi.stubGlobal('fetch', vi.fn(async () => {
    const next = responses.shift() ?? { messages: [] }
    const body = await Promise.resolve(next)
    return new Response(JSON.stringify(body), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }))
  return {
    rpc: vi.fn(async () => responses.shift() ?? { messages: [] }),
    send: vi.fn(),
    setLastSeq: vi.fn(),
    onMessage: vi.fn(() => vi.fn()),
  } as unknown as WSConnection
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((r) => {
    resolve = r
  })
  return { promise, resolve }
}

describe('useChatMessages', () => {
  it('keeps cached rows visible during same-session background reloads', async () => {
    const ws = makeWS([
      { messages: [{ role: 'user', content: 'hello', timestamp: '2026-07-08T00:00:00Z' }] },
      { messages: [{ role: 'user', content: 'hello again', timestamp: '2026-07-08T00:00:01Z' }] },
    ])

    const { result } = renderHook(() =>
      useChatMessages({
        chatID: 'chat-1',
        channel: 'web',
        ws,
      }),
    )

    await waitFor(() => expect(result.current.messages.map((m) => m.content)).toEqual(['hello']))
    expect(result.current.loading).toBe(false)

    await act(async () => {
      const pending = result.current.reload()
      expect(result.current.messages.map((m) => m.content)).toEqual(['hello'])
      expect(result.current.loading).toBe(false)
      await pending
    })

    expect(result.current.messages.map((m) => m.content)).toEqual(['hello again'])
    expect(result.current.loading).toBe(false)
  })

  it('reuses cached rows across hook remounts without a loading flash', async () => {
    const pendingSecond = deferred<{ messages: { role: string; content: string; timestamp: string }[] }>()
    const ws = makeWS([
      { messages: [{ role: 'user', content: 'cached', timestamp: '2026-07-08T00:00:00Z' }] },
      pendingSecond.promise,
    ])

    const first = renderHook(() =>
      useChatMessages({
        chatID: 'chat-remount',
        channel: 'web',
        ws,
      }),
    )

    await waitFor(() => expect(first.result.current.messages.map((m) => m.content)).toEqual(['cached']))
    first.unmount()

    const second = renderHook(() =>
      useChatMessages({
        chatID: 'chat-remount',
        channel: 'web',
        ws,
      }),
    )

    expect(second.result.current.messages.map((m) => m.content)).toEqual(['cached'])
    expect(second.result.current.loading).toBe(false)

    await act(async () => {
      pendingSecond.resolve({
        messages: [{ role: 'user', content: 'fresh', timestamp: '2026-07-08T00:00:01Z' }],
      })
    })

    await waitFor(() => expect(second.result.current.messages.map((m) => m.content)).toEqual(['fresh']))
  })

  it('does not let stale unmounted reloads overwrite the shared cache', async () => {
    const stale = deferred<{ messages: { role: string; content: string; timestamp: string }[] }>()
    const fresh = deferred<{ messages: { role: string; content: string; timestamp: string }[] }>()
    const ws = makeWS([stale.promise, fresh.promise, { messages: [{ role: 'user', content: 'fresh', timestamp: '2026-07-08T00:00:02Z' }] }])

    const first = renderHook(() =>
      useChatMessages({
        chatID: 'chat-stale-cache',
        channel: 'web',
        ws,
      }),
    )
    first.unmount()

    const second = renderHook(() =>
      useChatMessages({
        chatID: 'chat-stale-cache',
        channel: 'web',
        ws,
      }),
    )

    await act(async () => {
      fresh.resolve({ messages: [{ role: 'user', content: 'fresh', timestamp: '2026-07-08T00:00:01Z' }] })
      await fresh.promise
    })
    await waitFor(() => expect(second.result.current.messages.map((m) => m.content)).toEqual(['fresh']))

    await act(async () => {
      stale.resolve({ messages: [{ role: 'user', content: 'stale', timestamp: '2026-07-08T00:00:00Z' }] })
      await stale.promise
    })
    second.unmount()

    const third = renderHook(() =>
      useChatMessages({
        chatID: 'chat-stale-cache',
        channel: 'web',
        ws,
      }),
    )

    expect(third.result.current.messages.map((m) => m.content)).toEqual(['fresh'])
  })

  it('does not flash loading during same-session background reloads after an empty history loaded', async () => {
    const pendingSecond = deferred<{ messages: { role: string; content: string; timestamp: string }[] }>()
    const ws = makeWS([
      { messages: [] },
      pendingSecond.promise,
    ])

    const { result } = renderHook(() =>
      useChatMessages({
        chatID: 'chat-empty',
        channel: 'web',
        ws,
      }),
    )

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.messages).toEqual([])

    await act(async () => {
      const pending = result.current.reload()
      expect(result.current.messages).toEqual([])
      expect(result.current.loading).toBe(false)
      pendingSecond.resolve({ messages: [] })
      await pending
    })

    expect(result.current.loading).toBe(false)
  })

  it('does not replace visible same-session rows with an empty background refresh', async () => {
    const ws = makeWS([
      { messages: [{ role: 'user', content: 'keep me', timestamp: '2026-07-08T00:00:00Z' }] },
      { messages: [] },
    ])

    const { result } = renderHook(() =>
      useChatMessages({
        chatID: 'chat-nonempty',
        channel: 'web',
        ws,
      }),
    )

    await waitFor(() => expect(result.current.messages.map((m) => m.content)).toEqual(['keep me']))

    await act(async () => {
      await result.current.reload()
    })

    expect(result.current.messages.map((m) => m.content)).toEqual(['keep me'])
    expect(result.current.loading).toBe(false)
  })

  it('does not show the previous session while a newly selected session loads', async () => {
    const pendingSecond = deferred<{ messages: { role: string; content: string; timestamp: string }[] }>()
    const ws = makeWS([
      { messages: [{ role: 'user', content: 'from A', timestamp: '2026-07-08T00:00:00Z' }] },
      pendingSecond.promise,
    ])

    const { result, rerender } = renderHook(
      ({ chatID }) =>
        useChatMessages({
          chatID,
          channel: 'web',
          ws,
        }),
      { initialProps: { chatID: 'a' } },
    )

    await waitFor(() => expect(result.current.messages.map((m) => m.content)).toEqual(['from A']))

    rerender({ chatID: 'b' })

    await waitFor(() => expect(result.current.loading).toBe(true))
    expect(result.current.messages).toEqual([])

    await act(async () => {
      pendingSecond.resolve({
        messages: [{ role: 'user', content: 'from B', timestamp: '2026-07-08T00:00:01Z' }],
      })
    })

    expect(result.current.messages.map((m) => m.content)).toEqual(['from B'])
    expect(result.current.loading).toBe(false)
  })

  it('sends /new to the agent without showing an optimistic slash-command row', async () => {
    const ws = makeWS([
      { messages: [{ role: 'user', content: 'old', timestamp: '2026-07-08T00:00:00Z' }] },
    ])

    const { result } = renderHook(() =>
      useChatMessages({
        chatID: 'chat-1',
        channel: 'web',
        ws,
      }),
    )

    await waitFor(() => expect(result.current.messages.map((m) => m.content)).toEqual(['old']))

    act(() => {
      result.current.sendMessage('/new')
    })

    expect(result.current.messages.map((m) => m.content)).toEqual(['old'])
    expect(ws.send).toHaveBeenCalledWith(expect.objectContaining({
      type: 'message',
      channel: 'web',
      chat_id: 'chat-1',
      content: '/new',
    }))
  })

  it('does not subscribe to live user_echo events when live events are disabled', async () => {
    const ws = makeWS([{ messages: [] }])

    renderHook(() =>
      useChatMessages({
        chatID: 'chat-1',
        channel: 'web',
        ws,
        liveEventsEnabled: false,
      }),
    )

    await waitFor(() => expect(fetch).toHaveBeenCalled())
    expect(ws.onMessage).not.toHaveBeenCalled()
  })

  it('does not refetch history when only the ws wrapper identity changes', async () => {
    const ws = makeWS([
      { messages: [], last_seq: 11 },
      { messages: [], last_seq: 12 },
    ])
    const replacement = { ...ws } as WSConnection
    const { rerender } = renderHook(
      ({ currentWS }: { currentWS: WSConnection }) =>
        useChatMessages({
          chatID: 'chat-stable-ws-wrapper',
          channel: 'web',
          ws: currentWS,
          liveEventsEnabled: false,
        }),
      { initialProps: { currentWS: ws } },
    )

    await waitFor(() => expect(ws.setLastSeq).toHaveBeenCalledWith(
      'chat-stable-ws-wrapper',
      11,
      'web',
    ))

    rerender({ currentWS: replacement })
    await act(async () => {
      await Promise.resolve()
    })

    expect(fetch).toHaveBeenCalledTimes(1)
    expect(ws.setLastSeq).toHaveBeenCalledTimes(1)
  })

  it('attaches SubAgent dump iterations to the assistant message', async () => {
    const ws = makeWS([
      {
        messages: [
          { role: 'user', content: 'check this' },
          { role: 'assistant', content: 'done' },
        ],
        iterations: [
          {
            iteration: 1,
            thinking: 'thinking',
            completed_tools: [{ name: 'Read', status: 'done', summary: 'ok' }],
          },
        ],
      },
    ])

    const { result } = renderHook(() =>
      useChatMessages({
        chatID: 'cli:/repo:Agent-main/review:1',
        channel: 'agent',
        ws,
        agentChatID: 'cli:/repo:Agent-main/review:1',
      }),
    )

    await waitFor(() => expect(result.current.messages.map((m) => m.content)).toEqual(['check this', 'done']))
    expect(result.current.messages[1].iterations).toHaveLength(1)
    expect(result.current.messages[1].iterations[0].tools[0].name).toBe('Read')
  })

  it('loads nested SubAgent dumps by full key without truncating the parent chain', async () => {
    const ws = makeWS([
      {
        messages: [
          { role: 'assistant', content: 'nested done' },
        ],
      },
    ])

    const fullKey = 'agent:cli:/repo:Agent-main/review:1/fix:2'
    const { result } = renderHook(() =>
      useChatMessages({
        chatID: fullKey,
        channel: 'agent',
        ws,
        agentChatID: fullKey,
      }),
    )

    await waitFor(() => expect(result.current.messages.map((m) => m.content)).toEqual(['nested done']))
    expect(ws.rpc).toHaveBeenCalledWith('get_agent_session_dump_by_full_key', {
      full_key: fullKey,
    })
  })

  it('shows SubAgent dump iterations even when there is no assistant text yet', async () => {
    const ws = makeWS([
      {
        messages: [],
        iterations: [
          {
            iteration: 1,
            completed_tools: [{ name: 'Shell', status: 'running', summary: 'running' }],
          },
        ],
      },
    ])

    const { result } = renderHook(() =>
      useChatMessages({
        chatID: 'cli:/repo:Agent-main/review:1',
        channel: 'agent',
        ws,
        agentChatID: 'cli:/repo:Agent-main/review:1',
      }),
    )

    await waitFor(() => expect(result.current.messages).toHaveLength(1))
    expect(result.current.messages[0].role).toBe('assistant')
    expect(result.current.messages[0].iterations[0].tools[0].name).toBe('Shell')
  })
})
