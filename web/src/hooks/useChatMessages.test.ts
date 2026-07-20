import { act, renderHook, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { useChatMessages } from './useChatMessages'
import type { WSConnection } from '@/types/ws'
import type { WSMessage } from '@/types/shared'
import { bumpProgressGeneration, clearWebCaches, messagesCache, sessionCacheKey } from '@/lib/webCache'

function makeWS(responses: unknown[]): WSConnection {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => {
      const next = responses.shift() ?? { messages: [] }
      const body = await Promise.resolve(next)
      return new Response(JSON.stringify({ ok: true, data: body, error: null }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }),
  )
  return {
    rpc: vi.fn(async () => responses.shift() ?? { messages: [] }),
    send: vi.fn(async () => undefined),
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
  beforeEach(() => {
    clearWebCaches()
  })
  it('keeps cached rows visible during same-session background reloads', async () => {
    const ws = makeWS([
      {
        messages: [{ role: 'user', content: 'hello', timestamp: '2026-07-08T00:00:00Z' }],
      },
      {
        messages: [
          {
            role: 'user',
            content: 'hello again',
            timestamp: '2026-07-08T00:00:01Z',
          },
        ],
      },
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

  it('isolates message caches for matching chat IDs on different channels', async () => {
    const ws = makeWS([
      {
        messages: [
          {
            role: 'user',
            content: 'from web',
            timestamp: '2026-07-08T00:00:00Z',
          },
        ],
      },
      {
        messages: [
          {
            role: 'user',
            content: 'from cli',
            timestamp: '2026-07-08T00:00:01Z',
          },
        ],
      },
    ])
    const { result, rerender } = renderHook(({ channel }) => useChatMessages({ chatID: 'shared', channel, ws }), {
      initialProps: { channel: 'web' },
    })
    await waitFor(() => expect(result.current.messages.map((message) => message.content)).toEqual(['from web']))

    rerender({ channel: 'cli' })
    await waitFor(() => expect(result.current.messages.map((message) => message.content)).toEqual(['from cli']))

    expect(messagesCache.get(sessionCacheKey('web', 'shared'))?.map((message) => message.content)).toEqual(['from web'])
    expect(messagesCache.get(sessionCacheKey('cli', 'shared'))?.map((message) => message.content)).toEqual(['from cli'])
  })

  it('ignores an old-channel listener after switching the same raw chat ID', async () => {
    const handlers: Array<(message: WSMessage) => void> = []
    const ws = makeWS([
      {
        messages: [
          {
            role: 'user',
            content: 'from web',
            timestamp: '2026-07-08T00:00:00Z',
          },
        ],
      },
      {
        messages: [
          {
            role: 'user',
            content: 'from cli',
            timestamp: '2026-07-08T00:00:01Z',
          },
        ],
      },
    ])
    vi.mocked(ws.onMessage).mockImplementation((handler) => {
      handlers.push(handler)
      return vi.fn()
    })
    const { result, rerender } = renderHook(({ channel }) => useChatMessages({ chatID: 'shared-listener', channel, ws }), { initialProps: { channel: 'web' } })
    await waitFor(() => expect(result.current.messages.map((message) => message.content)).toEqual(['from web']))
    const staleWebHandler = handlers[0]

    rerender({ channel: 'cli' })
    await waitFor(() => expect(result.current.messages.map((message) => message.content)).toEqual(['from cli']))
    act(() => {
      staleWebHandler({
        type: 'inject_user',
        chat_id: 'web:shared-listener',
        content: 'stale web event',
      })
    })

    expect(result.current.messages.map((message) => message.content)).toEqual(['from cli'])
    expect(messagesCache.get(sessionCacheKey('web', 'shared-listener'))?.map((message) => message.content)).toEqual(['from web'])
    expect(messagesCache.get(sessionCacheKey('cli', 'shared-listener'))?.map((message) => message.content)).toEqual(['from cli'])
  })

  it('isolates user echoes for the same raw chat ID across channels', async () => {
    const handlers = new Set<(message: WSMessage) => void>()
    const ws = makeWS([{ messages: [] }, { messages: [] }])
    vi.mocked(ws.onMessage).mockImplementation((handler) => {
      handlers.add(handler)
      return () => handlers.delete(handler)
    })
    const web = renderHook(() => useChatMessages({ chatID: 'shared-echo', channel: 'web', ws }))
    const cli = renderHook(() => useChatMessages({ chatID: 'shared-echo', channel: 'cli', ws }))
    await waitFor(() => {
      expect(web.result.current.loading).toBe(false)
      expect(cli.result.current.loading).toBe(false)
    })

    act(() => {
      handlers.forEach((handler) =>
        handler({
          type: 'user_echo',
          channel: 'web',
          chat_id: 'shared-echo',
          content: 'web only',
          seq: 1,
        }),
      )
    })

    expect(web.result.current.messages.map((message) => message.content)).toEqual(['web only'])
    expect(cli.result.current.messages).toEqual([])

    act(() => {
      handlers.forEach((handler) =>
        handler({
          type: 'user_echo',
          channel: 'cli',
          chat_id: 'shared-echo',
          content: 'cli only',
          seq: 2,
        }),
      )
    })

    expect(web.result.current.messages.map((message) => message.content)).toEqual(['web only'])
    expect(cli.result.current.messages.map((message) => message.content)).toEqual(['cli only'])
  })

  it('reuses cached rows across hook remounts without a loading flash', async () => {
    const pendingSecond = deferred<{
      messages: { role: string; content: string; timestamp: string }[]
    }>()
    const ws = makeWS([
      {
        messages: [
          {
            role: 'user',
            content: 'cached',
            timestamp: '2026-07-08T00:00:00Z',
          },
        ],
      },
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
    const stale = deferred<{
      messages: { role: string; content: string; timestamp: string }[]
    }>()
    const fresh = deferred<{
      messages: { role: string; content: string; timestamp: string }[]
    }>()
    const ws = makeWS([
      stale.promise,
      fresh.promise,
      {
        messages: [{ role: 'user', content: 'fresh', timestamp: '2026-07-08T00:00:02Z' }],
      },
    ])

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
      fresh.resolve({
        messages: [{ role: 'user', content: 'fresh', timestamp: '2026-07-08T00:00:01Z' }],
      })
      await fresh.promise
    })
    await waitFor(() => expect(second.result.current.messages.map((m) => m.content)).toEqual(['fresh']))

    await act(async () => {
      stale.resolve({
        messages: [{ role: 'user', content: 'stale', timestamp: '2026-07-08T00:00:00Z' }],
      })
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

  it('keeps concurrent history cursors scoped to their response chats', async () => {
    const histories = {
      'cursor-a': deferred<{
        messages: never[]
        chat_id: string
        last_seq: number
      }>(),
      'cursor-b': deferred<{
        messages: never[]
        chat_id: string
        last_seq: number
      }>(),
    }
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
        const request = JSON.parse(String(init?.body)) as {
          chat_id: keyof typeof histories
        }
        const data = await histories[request.chat_id].promise
        return new Response(JSON.stringify({ ok: true, data, error: null }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      }),
    )
    const ws = {
      rpc: vi.fn(),
      send: vi.fn(async () => undefined),
      setLastSeq: vi.fn(),
      onMessage: vi.fn(() => vi.fn()),
    } as unknown as WSConnection

    const first = renderHook(() => useChatMessages({ chatID: 'cursor-a', channel: 'web', ws }))
    const second = renderHook(() => useChatMessages({ chatID: 'cursor-b', channel: 'web', ws }))

    await act(async () => {
      histories['cursor-b'].resolve({
        messages: [],
        chat_id: 'cursor-b',
        last_seq: 22,
      })
      histories['cursor-a'].resolve({
        messages: [],
        chat_id: 'cursor-a',
        last_seq: 11,
      })
      await Promise.all([histories['cursor-a'].promise, histories['cursor-b'].promise])
    })
    await waitFor(() => expect(ws.setLastSeq).toHaveBeenCalledTimes(2))

    expect(ws.setLastSeq).toHaveBeenCalledWith('cursor-a', 11, 'web')
    expect(ws.setLastSeq).toHaveBeenCalledWith('cursor-b', 22, 'web')
    first.unmount()
    second.unmount()
  })

  it('does not let delayed history replace an optimistic message or its SSE echo', async () => {
    const history = deferred<{
      messages: { role: string; content: string; timestamp: string }[]
      chat_id: string
      last_seq: number
    }>()
    let messageHandler: ((message: WSMessage) => void) | null = null
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        const data = await history.promise
        return new Response(JSON.stringify({ ok: true, data, error: null }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      }),
    )
    const ws = {
      rpc: vi.fn(),
      send: vi.fn(async () => undefined),
      setLastSeq: vi.fn(),
      onMessage: vi.fn((handler) => {
        messageHandler = handler
        return vi.fn()
      }),
    } as unknown as WSConnection
    const { result } = renderHook(() => useChatMessages({ chatID: 'slow-chat', channel: 'web', ws }))
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1))

    act(() => {
      result.current.sendMessage('new message')
    })
    expect(result.current.messages.map((message) => message.content)).toEqual(['new message'])
    const sentMessage = vi.mocked(ws.send).mock.calls[0][0]

    act(() => {
      messageHandler?.({
        type: 'user_echo',
        id: sentMessage.id,
        channel: 'web',
        chat_id: 'slow-chat',
        content: 'new message with attachment',
        original_content: 'new message',
        ts: 1_786_000_000,
        seq: 100,
      })
    })
    expect(result.current.messages.map((message) => message.content)).toEqual(['new message with attachment'])

    await act(async () => {
      history.resolve({
        messages: [
          {
            role: 'user',
            content: 'old history',
            timestamp: '2026-07-08T00:00:00Z',
          },
        ],
        chat_id: 'slow-chat',
        last_seq: 99,
      })
      await history.promise
    })
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.messages.map((message) => message.content)).toEqual(['old history', 'new message with attachment'])
    expect(messagesCache.get(sessionCacheKey('web', 'slow-chat'))?.map((message) => message.content)).toEqual(['old history', 'new message with attachment'])
    expect(ws.setLastSeq).not.toHaveBeenCalled()
  })

  it('does not duplicate a replayed user echo included above the history cursor', async () => {
    const replayTimestamp = '2026-08-06T07:06:40Z'
    const history = deferred<{
      messages: { role: string; content: string; timestamp: string }[]
      chat_id: string
      last_seq: number
    }>()
    let messageHandler: ((message: WSMessage) => void) | null = null
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        const data = await history.promise
        return new Response(JSON.stringify({ ok: true, data, error: null }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      }),
    )
    const ws = {
      rpc: vi.fn(),
      send: vi.fn(async () => undefined),
      setLastSeq: vi.fn(),
      onMessage: vi.fn((handler) => {
        messageHandler = handler
        return vi.fn()
      }),
    } as unknown as WSConnection
    const { result } = renderHook(() => useChatMessages({ chatID: 'replay-chat', channel: 'web', ws }))
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1))

    act(() => {
      messageHandler?.({
        type: 'user_echo',
        channel: 'web',
        chat_id: 'replay-chat',
        content: 'message with attachment',
        ts: Date.parse(replayTimestamp) / 1000,
        seq: 7,
      })
    })
    expect(result.current.messages.map((message) => message.content)).toEqual(['message with attachment'])

    await act(async () => {
      history.resolve({
        messages: [
          {
            role: 'user',
            content: 'message with attachment',
            timestamp: replayTimestamp,
          },
        ],
        chat_id: 'replay-chat',
        last_seq: 7,
      })
      await history.promise
    })

    await waitFor(() => expect(result.current.messages).toHaveLength(1))
    expect(result.current.messages[0]).toMatchObject({
      content: 'message with attachment',
      persisted: true,
    })
    expect(messagesCache.get(sessionCacheKey('web', 'replay-chat'))).toHaveLength(1)
    expect(ws.setLastSeq).not.toHaveBeenCalled()
  })

  it('does not duplicate an optimistic message persisted during slow history', async () => {
    const history = deferred<{
      messages: { role: string; content: string; timestamp: string }[]
      chat_id: string
      last_seq: number
    }>()
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        const data = await history.promise
        return new Response(JSON.stringify({ ok: true, data, error: null }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      }),
    )
    const ws = {
      rpc: vi.fn(),
      send: vi.fn(async () => undefined),
      setLastSeq: vi.fn(),
      onMessage: vi.fn(() => vi.fn()),
    } as unknown as WSConnection
    const { result } = renderHook(() =>
      useChatMessages({
        chatID: 'optimistic-history-chat',
        channel: 'web',
        ws,
      }),
    )
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1))

    act(() => result.current.sendMessage('persisted while loading'))
    const optimisticTimestamp = result.current.messages[0].timestamp
    await act(async () => {
      history.resolve({
        messages: [
          {
            role: 'user',
            content: 'persisted while loading',
            timestamp: optimisticTimestamp,
          },
        ],
        chat_id: 'optimistic-history-chat',
        last_seq: 0,
      })
      await history.promise
    })

    await waitFor(() => expect(result.current.messages).toHaveLength(1))
    expect(result.current.messages[0]).toMatchObject({
      content: 'persisted while loading',
      persisted: true,
    })
    expect(messagesCache.get(sessionCacheKey('web', 'optimistic-history-chat'))).toHaveLength(1)
  })

  it('keeps a covered replay echo when history does not contain that occurrence', async () => {
    const history = deferred<{
      messages: never[]
      chat_id: string
      last_seq: number
    }>()
    let messageHandler: ((message: WSMessage) => void) | null = null
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        const data = await history.promise
        return new Response(JSON.stringify({ ok: true, data, error: null }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      }),
    )
    const ws = {
      rpc: vi.fn(),
      send: vi.fn(async () => undefined),
      setLastSeq: vi.fn(),
      onMessage: vi.fn((handler) => {
        messageHandler = handler
        return vi.fn()
      }),
    } as unknown as WSConnection
    const { result } = renderHook(() => useChatMessages({ chatID: 'missing-echo-chat', channel: 'web', ws }))
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1))

    act(() => {
      messageHandler?.({
        type: 'user_echo',
        channel: 'web',
        chat_id: 'missing-echo-chat',
        content: 'not persisted yet',
        ts: Date.parse('2026-08-06T07:06:40Z') / 1000,
        seq: 7,
      })
    })
    await act(async () => {
      history.resolve({
        messages: [],
        chat_id: 'missing-echo-chat',
        last_seq: 7,
      })
      await history.promise
    })

    await waitFor(() => expect(result.current.messages).toHaveLength(1))
    expect(result.current.messages[0]).toMatchObject({
      content: 'not persisted yet',
      persisted: false,
      eventSeq: 7,
    })
    expect(messagesCache.get(sessionCacheKey('web', 'missing-echo-chat'))).toHaveLength(1)
    expect(ws.setLastSeq).not.toHaveBeenCalled()
  })

  it('correlates reversed and repeated attachment echoes by request ID', async () => {
    let messageHandler: ((message: WSMessage) => void) | null = null
    const ws = {
      rpc: vi.fn(),
      send: vi.fn(async () => undefined),
      setLastSeq: vi.fn(),
      onMessage: vi.fn((handler) => {
        messageHandler = handler
        return vi.fn()
      }),
    } as unknown as WSConnection
    vi.stubGlobal(
      'fetch',
      vi.fn(
        async () =>
          new Response(
            JSON.stringify({
              ok: true,
              data: { messages: [] },
              error: null,
            }),
            {
              status: 200,
              headers: { 'Content-Type': 'application/json' },
            },
          ),
      ),
    )
    const { result } = renderHook(() => useChatMessages({ chatID: 'echo-order-chat', channel: 'web', ws }))
    await waitFor(() => expect(result.current.loading).toBe(false))

    act(() => result.current.sendMessage('first'))
    act(() => result.current.sendMessage('second'))
    const sent = vi.mocked(ws.send).mock.calls.map(([message]) => message)
    expect(sent[0].id).toBeTruthy()
    expect(sent[1].id).toBeTruthy()
    expect(sent[0].id).not.toBe(sent[1].id)

    const secondEcho: WSMessage = {
      type: 'user_echo',
      id: sent[1].id,
      channel: 'web',
      chat_id: 'echo-order-chat',
      content: 'second + attachment',
      original_content: 'second',
      ts: 1_786_000_002,
      seq: 2,
    }
    const firstEcho: WSMessage = {
      type: 'user_echo',
      id: sent[0].id,
      channel: 'web',
      chat_id: 'echo-order-chat',
      content: 'first + attachment',
      original_content: 'first',
      ts: 1_786_000_001,
      seq: 1,
    }
    act(() => {
      messageHandler?.(secondEcho)
      messageHandler?.(firstEcho)
      messageHandler?.(firstEcho)
    })

    expect(result.current.messages.map((message) => message.content)).toEqual(['first + attachment', 'second + attachment'])
    expect(result.current.messages.map((message) => message.requestID)).toEqual([sent[0].id, sent[1].id])
  })

  it('accepts qualified inject_user events for CLI sessions', async () => {
    let messageHandler: ((message: WSMessage) => void) | null = null
    const ws = makeWS([{ messages: [] }])
    vi.mocked(ws.onMessage).mockImplementation((handler) => {
      messageHandler = handler
      return vi.fn()
    })
    const { result } = renderHook(() => useChatMessages({ chatID: '/repo', channel: 'cli', ws }))
    await waitFor(() => expect(result.current.loading).toBe(false))

    act(() => {
      messageHandler?.({
        type: 'inject_user',
        chat_id: 'cli:/repo',
        content: 'background task finished',
        seq: 1,
      })
    })

    expect(result.current.messages.map((message) => message.content)).toEqual(['background task finished'])
    expect(messagesCache.get(sessionCacheKey('cli', '/repo'))).toHaveLength(1)
  })

  it('restores initial history when an optimistic send fails during loading', async () => {
    const initialHistory = deferred<{
      messages: { role: string; content: string; timestamp: string }[]
      chat_id: string
      last_seq: number
    }>()
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        const data = await initialHistory.promise
        return new Response(JSON.stringify({ ok: true, data, error: null }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      }),
    )
    let rejectSend!: (reason: Error) => void
    const sendPromise = new Promise<void>((_resolve, reject) => {
      rejectSend = reject
    })
    const ws = {
      rpc: vi.fn(),
      send: vi.fn(() => sendPromise),
      setLastSeq: vi.fn(),
      onMessage: vi.fn(() => vi.fn()),
    } as unknown as WSConnection
    const { result } = renderHook(() => useChatMessages({ chatID: 'failed-send-chat', channel: 'web', ws }))
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1))

    act(() => {
      result.current.sendMessage('temporary message')
    })
    expect(result.current.messages.map((message) => message.content)).toEqual(['temporary message'])

    await act(async () => {
      rejectSend(new Error('network unavailable'))
      await sendPromise.catch(() => undefined)
    })
    expect(result.current.messages).toEqual([])

    await act(async () => {
      initialHistory.resolve({
        messages: [
          {
            role: 'user',
            content: 'persisted history',
            timestamp: '2026-07-08T00:00:01Z',
          },
        ],
        chat_id: 'failed-send-chat',
        last_seq: 77,
      })
      await initialHistory.promise
    })

    await waitFor(() => expect(result.current.messages.map((message) => message.content)).toEqual(['persisted history']))
    expect(messagesCache.get(sessionCacheKey('web', 'failed-send-chat'))?.map((message) => message.content)).toEqual(['persisted history'])
    expect(ws.setLastSeq).not.toHaveBeenCalled()
  })

  it('removes a failed optimistic send only from its original session after switching', async () => {
    let rejectSend!: (reason: Error) => void
    const sendPromise = new Promise<void>((_resolve, reject) => {
      rejectSend = reject
    })
    const ws = makeWS([
      {
        messages: [
          {
            role: 'user',
            content: 'history A',
            timestamp: '2026-07-08T00:00:00Z',
          },
        ],
      },
      {
        messages: [
          {
            role: 'user',
            content: 'history B',
            timestamp: '2026-07-08T00:00:01Z',
          },
        ],
      },
    ])
    vi.mocked(ws.send).mockReturnValue(sendPromise)
    const { result, rerender } = renderHook(({ chatID }) => useChatMessages({ chatID, channel: 'web', ws }), {
      initialProps: { chatID: 'session-a' },
    })
    await waitFor(() => expect(result.current.messages.map((message) => message.content)).toEqual(['history A']))

    act(() => result.current.sendMessage('temporary A'))
    expect(result.current.messages.map((message) => message.content)).toEqual(['history A', 'temporary A'])

    rerender({ chatID: 'session-b' })
    await waitFor(() => expect(result.current.messages.map((message) => message.content)).toEqual(['history B']))
    await act(async () => {
      rejectSend(new Error('network unavailable'))
      await sendPromise.catch(() => undefined)
    })

    expect(result.current.messages.map((message) => message.content)).toEqual(['history B'])
    expect(messagesCache.get(sessionCacheKey('web', 'session-a'))?.map((message) => message.content)).toEqual(['history A'])
    expect(messagesCache.get(sessionCacheKey('web', 'session-b'))?.map((message) => message.content)).toEqual(['history B'])
  })

  it('never returns the previous session messages during a target transition', async () => {
    const historyB = deferred<{ messages: never[]; chat_id: string }>()
    const ws = makeWS([
      {
        messages: [
          {
            role: 'user',
            content: 'history A',
            timestamp: '2026-07-08T00:00:00Z',
          },
        ],
      },
      historyB.promise,
    ])
    const { result, rerender } = renderHook(({ chatID }) => useChatMessages({ chatID, channel: 'web', ws }), {
      initialProps: { chatID: 'session-a' },
    })
    await waitFor(() => expect(result.current.messages.map((message) => message.content)).toEqual(['history A']))

    rerender({ chatID: 'session-b' })

    expect(result.current.messages).toEqual([])
    historyB.resolve({ messages: [], chat_id: 'session-b' })
  })

  it('keeps an optimistic message visible when the initial history request fails', async () => {
    let rejectHistory!: (reason: Error) => void
    const historyPromise = new Promise<never>((_resolve, reject) => {
      rejectHistory = reject
    })
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => historyPromise),
    )
    const ws = {
      rpc: vi.fn(),
      send: vi.fn(async () => undefined),
      setLastSeq: vi.fn(),
      onMessage: vi.fn(() => vi.fn()),
    } as unknown as WSConnection
    const { result } = renderHook(() => useChatMessages({ chatID: 'failed-history-chat', channel: 'web', ws }))
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1))

    act(() => {
      result.current.sendMessage('keep optimistic')
    })
    await act(async () => {
      rejectHistory(new Error('history unavailable'))
      await historyPromise.catch(() => undefined)
    })

    expect(result.current.messages.map((message) => message.content)).toEqual(['keep optimistic'])
    expect(messagesCache.get(sessionCacheKey('web', 'failed-history-chat'))?.map((message) => message.content)).toEqual(['keep optimistic'])
    expect(result.current.error).toBe('history unavailable')
  })

  it('does not publish delayed active progress after a newer live progress event', async () => {
    const history = deferred<{
      messages: never[]
      chat_id: string
      active_progress: { phase: string; stream_content: string }
    }>()
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        const data = await history.promise
        return new Response(JSON.stringify({ ok: true, data, error: null }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      }),
    )
    const ws = makeWS([])
    const { result } = renderHook(() => useChatMessages({ chatID: 'progress-chat', channel: 'web', ws }))
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1))

    bumpProgressGeneration(sessionCacheKey('web', 'progress-chat'))
    await act(async () => {
      history.resolve({
        messages: [],
        chat_id: 'progress-chat',
        active_progress: {
          phase: 'thinking',
          stream_content: 'stale progress',
        },
      })
      await history.promise
    })
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.initialProgress).toBeNull()
    expect(ws.setLastSeq).not.toHaveBeenCalled()
  })

  it('does not flash loading during same-session background reloads after an empty history loaded', async () => {
    const pendingSecond = deferred<{
      messages: { role: string; content: string; timestamp: string }[]
    }>()
    const ws = makeWS([{ messages: [] }, pendingSecond.promise])

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

  it('accepts an authoritative empty same-session history response', async () => {
    const ws = makeWS([
      {
        messages: [
          {
            role: 'user',
            content: 'keep me',
            timestamp: '2026-07-08T00:00:00Z',
          },
        ],
      },
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

    expect(result.current.messages).toEqual([])
    expect(result.current.loading).toBe(false)
  })

  it('accepts an empty history after an explicit destructive clear', async () => {
    const ws = makeWS([
      {
        messages: [{ role: 'user', content: 'first', timestamp: '2026-07-08T00:00:00Z' }],
      },
      { messages: [] },
    ])

    const { result } = renderHook(() => useChatMessages({ chatID: 'rewind-first', channel: 'web', ws }))

    await waitFor(() => expect(result.current.messages.map((m) => m.content)).toEqual(['first']))

    await act(async () => {
      result.current.clearMessages()
      await result.current.reload()
    })

    expect(result.current.messages).toEqual([])
  })

  it('keeps only the prefix returned after an explicit rewind reload', async () => {
    const ws = makeWS([
      {
        messages: [
          {
            role: 'user',
            content: 'prefix',
            timestamp: '2026-07-08T00:00:00Z',
          },
          {
            role: 'user',
            content: 'rewind target',
            timestamp: '2026-07-08T00:00:01Z',
          },
          {
            role: 'assistant',
            content: 'later reply',
            timestamp: '2026-07-08T00:00:02Z',
          },
        ],
      },
      {
        messages: [
          {
            role: 'user',
            content: 'prefix',
            timestamp: '2026-07-08T00:00:00Z',
          },
        ],
      },
    ])

    const { result } = renderHook(() => useChatMessages({ chatID: 'rewind-prefix', channel: 'web', ws }))

    await waitFor(() => expect(result.current.messages).toHaveLength(3))

    await act(async () => {
      result.current.clearMessages()
      await result.current.reload()
    })

    expect(result.current.messages.map((m) => m.content)).toEqual(['prefix'])
  })

  it('does not show the previous session while a newly selected session loads', async () => {
    const pendingSecond = deferred<{
      messages: { role: string; content: string; timestamp: string }[]
    }>()
    const ws = makeWS([
      {
        messages: [
          {
            role: 'user',
            content: 'from A',
            timestamp: '2026-07-08T00:00:00Z',
          },
        ],
      },
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
        messages: [
          {
            role: 'user',
            content: 'from B',
            timestamp: '2026-07-08T00:00:01Z',
          },
        ],
      })
    })

    expect(result.current.messages.map((m) => m.content)).toEqual(['from B'])
    expect(result.current.loading).toBe(false)
  })

  it('sends /new to the agent without showing an optimistic slash-command row', async () => {
    const ws = makeWS([
      {
        messages: [{ role: 'user', content: 'old', timestamp: '2026-07-08T00:00:00Z' }],
      },
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
    expect(ws.send).toHaveBeenCalledWith(
      expect.objectContaining({
        type: 'message',
        channel: 'web',
        chat_id: 'chat-1',
        content: '/new',
      }),
    )
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

  it('reloads authoritative history only for an exact-session sequence gap', async () => {
    const handlers = new Set<(message: WSMessage) => void>()
    const ws = makeWS([
      { messages: [{ role: 'user', content: 'before gap', timestamp: '2026-07-08T00:00:00Z' }] },
      { messages: [{ role: 'user', content: 'after gap', timestamp: '2026-07-08T00:00:01Z' }] },
    ])
    vi.mocked(ws.onMessage).mockImplementation((handler) => {
      handlers.add(handler)
      return () => handlers.delete(handler)
    })
    const { result } = renderHook(() => useChatMessages({ chatID: 'gap-chat', channel: 'web', ws }))
    await waitFor(() => expect(result.current.messages.map((message) => message.content)).toEqual(['before gap']))

    act(() => {
      handlers.forEach((handler) => handler({
        type: 'history_gap', channel: 'cli', chat_id: 'gap-chat',
      }))
    })
    expect(fetch).toHaveBeenCalledTimes(1)

    act(() => {
      handlers.forEach((handler) => handler({
        type: 'history_gap', channel: 'web', chat_id: 'gap-chat',
      }))
    })
    await waitFor(() => expect(result.current.messages.map((message) => message.content)).toEqual(['after gap']))
    expect(fetch).toHaveBeenCalledTimes(2)
  })

  it('reloads authoritative history only for an exact-session resync request', async () => {
    const handlers = new Set<(message: WSMessage) => void>()
    const ws = makeWS([
      { messages: [{ role: 'user', content: 'before resync', timestamp: '2026-07-08T00:00:00Z' }] },
      { messages: [{ role: 'user', content: 'after resync', timestamp: '2026-07-08T00:00:01Z' }] },
    ])
    vi.mocked(ws.onMessage).mockImplementation((handler) => {
      handlers.add(handler)
      return () => handlers.delete(handler)
    })
    const { result } = renderHook(() => useChatMessages({ chatID: 'resync-chat', channel: 'web', ws }))
    await waitFor(() => expect(result.current.messages.map((message) => message.content)).toEqual(['before resync']))

    act(() => {
      handlers.forEach((handler) => handler({
        type: 'resync_required', channel: 'cli', chat_id: 'resync-chat',
      }))
    })
    expect(fetch).toHaveBeenCalledTimes(1)

    act(() => {
      handlers.forEach((handler) => handler({
        type: 'resync_required', channel: 'web', chat_id: 'resync-chat',
      }))
    })
    await waitFor(() => expect(result.current.messages.map((message) => message.content)).toEqual(['after resync']))
    expect(fetch).toHaveBeenCalledTimes(2)
  })

  it('exposes processing and the complete active progress snapshot', async () => {
    const ws = makeWS([{
      messages: [],
      processing: true,
      chat_id: 'processing-chat',
      active_progress: {
        phase: 'thinking',
        reasoning_stream_content: 'reasoning now',
        token_usage: { prompt_tokens: 21, completion_tokens: 3, total_tokens: 24 },
      },
    }])
    const { result } = renderHook(() => useChatMessages({ chatID: 'processing-chat', channel: 'web', ws }))

    await waitFor(() => expect(result.current.processing).toBe(true))
    expect(result.current.initialProgress).toMatchObject({
      phase: 'thinking',
      reasoning_stream_content: 'reasoning now',
      token_usage: { prompt_tokens: 21, completion_tokens: 3, total_tokens: 24 },
    })
  })

  it('does not let a pre-logout history response repopulate the cleared cache', async () => {
    const history = deferred<{ messages: { role: string; content: string; timestamp: string }[] }>()
    const ws = makeWS([])
    vi.stubGlobal('fetch', vi.fn(async () => {
      const data = await history.promise
      return new Response(JSON.stringify({ ok: true, data, error: null }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }))
    const hook = renderHook(() => useChatMessages({ chatID: 'shared-auth-chat', channel: 'web', ws }))
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1))

    clearWebCaches()
    hook.unmount()
    await act(async () => {
      history.resolve({
        messages: [{ role: 'user', content: 'previous user secret', timestamp: '2026-07-08T00:00:00Z' }],
      })
      await history.promise
    })

    expect(messagesCache.has(sessionCacheKey('web', 'shared-auth-chat'))).toBe(false)
  })

  it('keeps equal assistant text from distinct event sequences', async () => {
    const ws = makeWS([{ messages: [] }])
    const { result } = renderHook(() => useChatMessages({ chatID: 'repeat-chat', channel: 'web', ws }))
    await waitFor(() => expect(result.current.loading).toBe(false))

    act(() => {
      result.current.appendAssistant('same answer', [], 10)
      result.current.appendAssistant('same answer', [], 11)
      result.current.appendAssistant('same answer', [], 11)
    })

    expect(result.current.messages).toHaveLength(2)
    expect(result.current.messages.map((message) => message.eventSeq)).toEqual([10, 11])
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

    await waitFor(() => expect(ws.setLastSeq).toHaveBeenCalledWith('chat-stable-ws-wrapper', 11, 'web'))

    rerender({ currentWS: replacement })
    await act(async () => {
      await Promise.resolve()
    })

    expect(fetch).toHaveBeenCalledTimes(1)
    expect(ws.setLastSeq).toHaveBeenCalledTimes(1)
  })

  it('loads Agent history through the canonical REST projection', async () => {
    const ws = makeWS([
      {
        messages: [
          {
            history_id: 1,
            record_type: 'message',
            role: 'user',
            content: 'check this',
          },
          {
            history_id: 2,
            record_type: 'message',
            role: 'assistant',
            content: '',
            reasoning_content: 'thinking',
            tool_calls: [{ id: 'call-1', name: 'Read', arguments: '{"path":"README.md"}' }],
            iterations: [
              {
                iteration: 1,
                reasoning: 'thinking',
                tools: [{ name: 'Read', status: 'done', summary: 'ok' }],
              },
            ],
          },
          {
            history_id: 3,
            record_type: 'message',
            role: 'tool',
            content: 'file contents',
            tool_call_id: 'call-1',
            tool_name: 'Read',
          },
          {
            history_id: 4,
            record_type: 'message',
            role: 'assistant',
            content: '',
            display_only: true,
          },
          {
            history_id: 5,
            record_type: 'compress',
            role: 'system',
            content: '[Compacted context]\nsummary',
            compression: { source_history_ids: [1, 2, 3, 4] },
          },
        ],
        active_progress: {
          iteration: 1,
          completed_tools: [{ name: 'Read', status: 'done', summary: 'ok' }],
        },
      },
    ])

    const fullKey = 'cli:/repo:Agent-main/review:1'

    const { result } = renderHook(() =>
      useChatMessages({
        chatID: fullKey,
        channel: 'agent',
        ws,
      }),
    )

    await waitFor(() => expect(result.current.messages).toHaveLength(5))
    expect(result.current.messages.map((message) => message.historyID)).toEqual([1, 2, 3, 4, 5])
    expect(result.current.messages[1].iterations).toHaveLength(1)
    expect(result.current.messages[1].iterations[0].tools[0].name).toBe('Read')
    expect(result.current.messages[1].toolCalls?.[0].id).toBe('call-1')
    expect(result.current.messages[2]).toMatchObject({
      role: 'tool',
      toolCallID: 'call-1',
      toolName: 'Read',
    })
    expect(result.current.messages[3]).toMatchObject({
      role: 'assistant',
      content: '',
      displayOnly: true,
    })
    expect(result.current.messages[4].compression?.sourceHistoryIDs).toEqual([1, 2, 3, 4])
    expect(ws.rpc).not.toHaveBeenCalled()
    const [, request] = vi.mocked(fetch).mock.calls[0]
    expect(JSON.parse(String(request?.body))).toEqual({
      channel: 'agent',
      chat_id: fullKey,
    })
  })

  it('loads nested Agent history without truncating the full key', async () => {
    const ws = makeWS([
      {
        messages: [
          {
            history_id: 9,
            record_type: 'message',
            role: 'assistant',
            content: 'nested done',
          },
        ],
      },
    ])

    const fullKey = 'agent:cli:/repo:Agent-main/review:1/fix:2'
    const { result } = renderHook(() =>
      useChatMessages({
        chatID: fullKey,
        channel: 'agent',
        ws,
      }),
    )

    await waitFor(() => expect(result.current.messages.map((m) => m.content)).toEqual(['nested done']))
    const [, request] = vi.mocked(fetch).mock.calls[0]
    expect(JSON.parse(String(request?.body))).toEqual({
      channel: 'agent',
      chat_id: fullKey,
    })
  })

  it('sends Agent messages through the canonical interactive continuation RPC', async () => {
    const ws = makeWS([{ messages: [] }])
    const fullKey = 'agent:web:chat-1/review:1/fix:2'
    const { result } = renderHook(() => useChatMessages({ chatID: fullKey, channel: 'agent', ws }))
    await waitFor(() => expect(fetch).toHaveBeenCalled())

    act(() => result.current.sendMessage('continue here'))

    await waitFor(() =>
      expect(ws.rpc).toHaveBeenCalledWith('continue_interactive_session', {
        full_key: fullKey,
        content: 'continue here',
      }),
    )
    expect(ws.send).not.toHaveBeenCalled()
  })

  it('restores the caller draft when an Agent continuation is rejected', async () => {
    const ws = makeWS([{ messages: [] }])
    vi.mocked(ws.rpc).mockRejectedValueOnce(new Error('no active interactive session'))
    const onSendError = vi.fn()
    const fullKey = 'web:chat-1/review:1'
    const { result } = renderHook(() =>
      useChatMessages({ chatID: fullKey, channel: 'agent', ws, onSendError }),
    )
    await waitFor(() => expect(fetch).toHaveBeenCalled())

    act(() => result.current.sendMessage('keep this draft'))

    await waitFor(() =>
      expect(onSendError).toHaveBeenCalledWith(
        'keep this draft',
        expect.objectContaining({ message: 'no active interactive session' }),
      ),
    )
    expect(result.current.messages).toEqual([])
  })

  it('restores the caller draft when a main-session send is rejected', async () => {
    const ws = makeWS([{ messages: [] }])
    vi.mocked(ws.send).mockRejectedValueOnce(new Error('send rejected'))
    const onSendError = vi.fn()
    const { result } = renderHook(() =>
      useChatMessages({ chatID: 'main-chat', channel: 'web', ws, onSendError }),
    )
    await waitFor(() => expect(fetch).toHaveBeenCalled())

    act(() => result.current.sendMessage('keep main draft'))

    await waitFor(() => expect(onSendError).toHaveBeenCalledWith(
      'keep main draft',
      expect.objectContaining({ message: 'send rejected' }),
    ))
    expect(result.current.messages).toEqual([])
  })
})
