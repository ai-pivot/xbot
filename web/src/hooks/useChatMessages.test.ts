import { act, renderHook, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { useChatMessages } from './useChatMessages'
import type { WSConnection } from '@/types/ws'
import type { WSMessage } from '@/types/shared'
import {
  bumpProgressGeneration,
  clearWebCaches,
  sessionCacheKey,
} from '@/lib/webCache'

function makeWS(responses: unknown[]): WSConnection {
  vi.stubGlobal('fetch', vi.fn(async () => {
    const next = responses.shift() ?? { messages: [] }
    const body = await Promise.resolve(next)
    return new Response(JSON.stringify({ ok: true, data: body, error: null }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }))
  return {
    rpc: vi.fn(async () => responses.shift() ?? { messages: [] }),
    send: vi.fn(async () => undefined),
    setLastSeq: vi.fn(),
    onMessage: vi.fn(() => vi.fn()),
    onConnectionChange: vi.fn(() => vi.fn()),
    connected: false,
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

  it('isolates message caches for matching chat IDs on different channels', async () => {
    const ws = makeWS([
      { messages: [{ role: 'user', content: 'from web', timestamp: '2026-07-08T00:00:00Z' }] },
      { messages: [{ role: 'user', content: 'from cli', timestamp: '2026-07-08T00:00:01Z' }] },
    ])
    const { result, rerender } = renderHook(
      ({ channel }) => useChatMessages({ chatID: 'shared', channel, ws }),
      { initialProps: { channel: 'web' } },
    )
    await waitFor(() => expect(result.current.messages.map((message) => message.content)).toEqual(['from web']))

    rerender({ channel: 'cli' })
    await waitFor(() => expect(result.current.messages.map((message) => message.content)).toEqual(['from cli']))
  })

  it('ignores an old-channel listener after switching the same raw chat ID', async () => {
    const handlers: Array<(message: WSMessage) => void> = []
    const ws = makeWS([
      { messages: [{ role: 'user', content: 'from web', timestamp: '2026-07-08T00:00:00Z' }] },
      { messages: [{ role: 'user', content: 'from cli', timestamp: '2026-07-08T00:00:01Z' }] },
    ])
    vi.mocked(ws.onMessage).mockImplementation((handler) => {
      handlers.push(handler)
      return vi.fn()
    })
    const { result, rerender } = renderHook(
      ({ channel }) => useChatMessages({ chatID: 'shared-listener', channel, ws }),
      { initialProps: { channel: 'web' } },
    )
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
  })

  it('keeps concurrent history cursors scoped to their response chats', async () => {
    const histories = {
      'cursor-a': deferred<{ messages: never[]; chat_id: string; last_seq: number }>(),
      'cursor-b': deferred<{ messages: never[]; chat_id: string; last_seq: number }>(),
    }
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const body = init?.body ? JSON.parse(String(init.body)) : {}
      const chat_id = body.chat_id as keyof typeof histories
      const data = await histories[chat_id].promise
      return new Response(JSON.stringify({ ok: true, data, error: null }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }))
    const ws = {
      rpc: vi.fn(),
      send: vi.fn(async () => undefined),
      setLastSeq: vi.fn(),
      onMessage: vi.fn(() => vi.fn()),
    } as unknown as WSConnection

    const first = renderHook(() => useChatMessages({ chatID: 'cursor-a', channel: 'web', ws }))
    const second = renderHook(() => useChatMessages({ chatID: 'cursor-b', channel: 'web', ws }))

    await act(async () => {
      histories['cursor-b'].resolve({ messages: [], chat_id: 'cursor-b', last_seq: 22 })
      histories['cursor-a'].resolve({ messages: [], chat_id: 'cursor-a', last_seq: 11 })
      await Promise.all([histories['cursor-a'].promise, histories['cursor-b'].promise])
    })
    await waitFor(() => expect(ws.setLastSeq).toHaveBeenCalledTimes(2))

    expect(ws.setLastSeq).toHaveBeenCalledWith('cursor-a', 11, 'web')
    expect(ws.setLastSeq).toHaveBeenCalledWith('cursor-b', 22, 'web')
    first.unmount()
    second.unmount()
  })

  it('does not duplicate a replayed user echo included above the history cursor', async () => {
    const replayTimestamp = '2026-08-06T07:06:40Z'
    const history = deferred<{
      messages: { role: string; content: string; timestamp: string }[]
      chat_id: string
      last_seq: number
    }>()
    let messageHandler: ((message: WSMessage) => void) | null = null
    vi.stubGlobal('fetch', vi.fn(async () => {
      const data = await history.promise
      return new Response(JSON.stringify({ ok: true, data, error: null }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }))
    const ws = {
      rpc: vi.fn(),
      send: vi.fn(async () => undefined),
      setLastSeq: vi.fn(),
      onMessage: vi.fn((handler) => {
        messageHandler = handler
        return vi.fn()
      }),
    } as unknown as WSConnection
    const { result } = renderHook(() => (
      useChatMessages({ chatID: 'replay-chat', channel: 'web', ws })
    ))
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1))

    act(() => {
      messageHandler?.({
        type: 'user_echo',
        chat_id: 'replay-chat',
        content: 'message with attachment',
        ts: Date.parse(replayTimestamp) / 1000,
        seq: 7,
      })
    })
    expect(result.current.messages.map((message) => message.content)).toEqual([
      'message with attachment',
    ])

    await act(async () => {
      history.resolve({
        messages: [{
          role: 'user',
          content: 'message with attachment',
          timestamp: replayTimestamp,
        }],
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
    expect(ws.setLastSeq).not.toHaveBeenCalled()
  })

  it('keeps a covered replay echo when history does not contain that occurrence', async () => {
    const history = deferred<{
      messages: never[]
      chat_id: string
      last_seq: number
    }>()
    let messageHandler: ((message: WSMessage) => void) | null = null
    vi.stubGlobal('fetch', vi.fn(async () => {
      const data = await history.promise
      return new Response(JSON.stringify({ ok: true, data, error: null }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }))
    const ws = {
      rpc: vi.fn(),
      send: vi.fn(async () => undefined),
      setLastSeq: vi.fn(),
      onMessage: vi.fn((handler) => {
        messageHandler = handler
        return vi.fn()
      }),
    } as unknown as WSConnection
    const { result } = renderHook(() => (
      useChatMessages({ chatID: 'missing-echo-chat', channel: 'web', ws })
    ))
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1))

    act(() => {
      messageHandler?.({
        type: 'user_echo',
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
        last_seq: 6,
      })
      await history.promise
    })

    await waitFor(() => expect(result.current.messages).toHaveLength(1))
    expect(result.current.messages[0]).toMatchObject({
      content: 'not persisted yet',
      persisted: true,
      eventSeq: 7,
    })
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
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({
      ok: true,
      data: { messages: [] },
      error: null,
    }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })))
    const { result } = renderHook(() => (
      useChatMessages({ chatID: 'echo-order-chat', channel: 'web', ws })
    ))
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
      chat_id: 'echo-order-chat',
      content: 'second + attachment',
      original_content: 'second',
      ts: 1_786_000_002,
      seq: 2,
    }
    const firstEcho: WSMessage = {
      type: 'user_echo',
      id: sent[0].id,
      chat_id: 'echo-order-chat',
      content: 'first + attachment',
      original_content: 'first',
      ts: 1_786_000_001,
      seq: 1,
    }
    act(() => {
      // Real backend pushes echoes in send order; repeated echoes (SSE replay)
      // are deduped by requestID.
      messageHandler?.(firstEcho)
      messageHandler?.(secondEcho)
      messageHandler?.(firstEcho)
    })

    expect(result.current.messages.map((message) => message.content)).toEqual([
      'first + attachment',
      'second + attachment',
    ])
    expect(result.current.messages.map((message) => message.requestID)).toEqual([
      sent[0].id,
      sent[1].id,
    ])
  })

  it('accepts qualified inject_user events for CLI sessions', async () => {
    let messageHandler: ((message: WSMessage) => void) | null = null
    const ws = makeWS([{ messages: [] }])
    vi.mocked(ws.onMessage).mockImplementation((handler) => {
      messageHandler = handler
      return vi.fn()
    })
    const { result } = renderHook(() => (
      useChatMessages({ chatID: '/repo', channel: 'cli', ws })
    ))
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
  })

  it('never returns the previous session messages during a target transition', async () => {
    const historyB = deferred<{ messages: never[]; chat_id: string }>()
    const ws = makeWS([
      { messages: [{ role: 'user', content: 'history A', timestamp: '2026-07-08T00:00:00Z' }] },
      historyB.promise,
    ])
    const { result, rerender } = renderHook(
      ({ chatID }) => useChatMessages({ chatID, channel: 'web', ws }),
      { initialProps: { chatID: 'session-a' } },
    )
    await waitFor(() => expect(result.current.messages.map((message) => message.content)).toEqual(['history A']))

    rerender({ chatID: 'session-b' })

    expect(result.current.messages).toEqual([])
    historyB.resolve({ messages: [], chat_id: 'session-b' })
  })

  it('does not publish delayed active progress after a newer live progress event', async () => {
    const history = deferred<{
      messages: never[]
      chat_id: string
      active_progress: { phase: string; stream_content: string }
    }>()
    vi.stubGlobal('fetch', vi.fn(async () => {
      const data = await history.promise
      return new Response(JSON.stringify({ ok: true, data, error: null }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }))
    const ws = makeWS([])
    const { result } = renderHook(() => useChatMessages({ chatID: 'progress-chat', channel: 'web', ws }))
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1))

    bumpProgressGeneration(sessionCacheKey('web', 'progress-chat'))
    await act(async () => {
      history.resolve({
        messages: [],
        chat_id: 'progress-chat',
        active_progress: { phase: 'thinking', stream_content: 'stale progress' },
      })
      await history.promise
    })
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.initialProgress).toBeNull()
    expect(ws.setLastSeq).not.toHaveBeenCalled()
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

  it('accepts an empty history after an explicit destructive clear', async () => {
    const ws = makeWS([
      { messages: [{ role: 'user', content: 'first', timestamp: '2026-07-08T00:00:00Z' }] },
      { messages: [] },
    ])

    const { result } = renderHook(() =>
      useChatMessages({ chatID: 'rewind-first', channel: 'web', ws }),
    )

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
          { role: 'user', content: 'prefix', timestamp: '2026-07-08T00:00:00Z' },
          { role: 'user', content: 'rewind target', timestamp: '2026-07-08T00:00:01Z' },
          { role: 'assistant', content: 'later reply', timestamp: '2026-07-08T00:00:02Z' },
        ],
      },
      { messages: [{ role: 'user', content: 'prefix', timestamp: '2026-07-08T00:00:00Z' }] },
    ])

    const { result } = renderHook(() =>
      useChatMessages({ chatID: 'rewind-prefix', channel: 'web', ws }),
    )

    await waitFor(() => expect(result.current.messages).toHaveLength(3))

    await act(async () => {
      result.current.clearMessages()
      await result.current.reload()
    })

    expect(result.current.messages.map((m) => m.content)).toEqual(['prefix'])
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

  it('cancel: appendAssistant survives reload — assistant message does not vanish', async () => {
    const ws = makeWS([
      // First fetch: user message only (no assistant yet)
      { messages: [{ role: 'user', content: 'hello', timestamp: '2026-07-24T00:00:00Z', seq: 1 }], chat_id: 'cancel-chat', last_seq: 1 },
    ])
    const { result } = renderHook(() => useChatMessages({ chatID: 'cancel-chat', channel: 'web', ws }))
    await waitFor(() => expect(result.current.messages.map(m => m.role)).toEqual(['user']))

    // Simulate cancel: appendAssistant commits the assistant message
    act(() => {
      result.current.appendAssistant('partial reply', [], 2)
    })
    // messages = [user, assistant]
    expect(result.current.messages.map(m => m.role)).toEqual(['user', 'assistant'])
    expect(result.current.messages[1].content).toBe('partial reply')
    expect(result.current.messages[1].persisted).toBe(false)
    expect(result.current.messages[1].eventSeq).toBe(2)

    // Now a reload comes back — server has user + [interrupted] assistant persisted
    vi.stubGlobal('fetch', vi.fn(async () => {
      return new Response(JSON.stringify({
        ok: true,
        data: {
          messages: [
            { role: 'user', content: 'hello', timestamp: '2026-07-24T00:00:00Z', seq: 1 },
            { role: 'assistant', content: '', timestamp: '2026-07-24T00:00:01Z', seq: 2,
              iterations: [{ iteration: 1, thinking: 'partial reply', tools: [{ name: 'user_cancelled', status: 'done' }] }] },
          ],
          chat_id: 'cancel-chat', last_seq: 2,
        },
        error: null,
      }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))

    await act(async () => {
      void result.current.reload()
    })

    // The assistant message must survive — not vanish
    const assistantMsg = result.current.messages.find(m => m.role === 'assistant')
    expect(assistantMsg).toBeDefined()
    // content may be '' from server, but the message must exist
    // (the live row with 'partial reply' is kept via >= watermark)
  })

  it('cancel: user message does NOT vanish after reload (persisted echo + DB row)', async () => {
    // USER BUG: send a user message then cancel — the user message disappears
    // until refresh. user_echo rows are persisted:true, so reconcile's first
    // check (persisted !== false) drops them and the DB snapshot must carry
    // them. This test pins the invariant: when the DB snapshot contains the
    // row, the reload must render it (no vanish).
    const ws = makeWS([{ messages: [{ role: 'user', content: 'hello', timestamp: '2026-07-24T00:00:00Z', seq: 1, turn_id: 1 }], chat_id: 'cancel-user-chat', last_seq: 1 }])
    const { result } = renderHook(() => useChatMessages({ chatID: 'cancel-user-chat', channel: 'web', ws }))
    await waitFor(() => expect(result.current.messages.map(m => m.role)).toEqual(['user']))
    const handler = vi.mocked(ws.onMessage).mock.calls[0][0] as (m: WSMessage) => void

    // User sends 'world' — backend echoes it deterministically (persisted:true)
    act(() => handler({ type: 'user_echo', content: 'world', turn_id: 2, ts: 1000, id: 'r2' }))
    expect(result.current.messages.map(m => m.content)).toContain('world')

    // User cancels — destructive mutation marks next reload for reconcile
    act(() => { result.current.markDestructiveMutation() })

    // Reload returns a RACING snapshot that does NOT contain the user row yet
    // (cancel landed between eager-save and the snapshot; the DB write is
    // still in flight). This is the exact "user msg vanishes until refresh"
    // bug: the persisted user_echo row (eventSeq=undefined) must survive the
    // reload, otherwise the user message disappears and only a refresh (after
    // the write lands) brings it back.
    vi.stubGlobal('fetch', vi.fn(async () => {
      return new Response(JSON.stringify({
        ok: true,
        data: {
          messages: [
            { role: 'user', content: 'hello', timestamp: '2026-07-24T00:00:00Z', seq: 1, turn_id: 1 },
            { role: 'assistant', content: '', timestamp: '2026-07-24T00:00:02Z', seq: 3, turn_id: 2,
              iterations: [{ iteration: 1, thinking: 'partial reply', tools: [{ name: 'user_cancelled', status: 'done' }] }] },
          ],
          chat_id: 'cancel-user-chat', last_seq: 3,
        },
        error: null,
      }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))

    await act(async () => { void result.current.reload() })

    // The user message must survive (persisted user_echo row is deterministic
    // data — never dropped when the racing snapshot lacks it)
    const contents = result.current.messages.map(m => m.content)
    expect(contents).toContain('world')
  })

  it('cancel: appendAssistant with turnID does NOT duplicate after reload', async () => {
    // Bug: after cancel, appendAssistant creates seq-N (turnID=3, persisted=false).
    // markDestructiveMutation → next reload uses reconcileHistoryWithLiveRows.
    // DB returns hist-N (turnID=3, content="" from [interrupted]).
    // Old code kept BOTH because content/eventSeq didn't match.
    // Fix: dedup by turnID:role — the DB message has the same turnID.
    const ws = makeWS([
      { messages: [{ role: 'user', content: 'hello', timestamp: '2026-07-24T00:00:00Z', seq: 1 }], chat_id: 'dup-chat', last_seq: 1 },
    ])
    const { result } = renderHook(() => useChatMessages({ chatID: 'dup-chat', channel: 'web', ws }))
    await waitFor(() => expect(result.current.messages.map(m => m.role)).toEqual(['user']))

    // Simulate cancel: appendAssistant with turnID=3
    act(() => {
      result.current.appendAssistant('partial reply', [], 2, 3)
    })
    result.current.markDestructiveMutation()

    // Reload returns the [interrupted] message with same turnID=3
    vi.stubGlobal('fetch', vi.fn(async () => {
      return new Response(JSON.stringify({
        ok: true,
        data: {
          messages: [
            { role: 'user', content: 'hello', timestamp: '2026-07-24T00:00:00Z', seq: 1 },
            { role: 'assistant', content: '', timestamp: '2026-07-24T00:00:01Z', seq: 2, turn_id: 3,
              iterations: [{ iteration: 1, thinking: 'partial reply', tools: [{ name: 'user_cancelled', status: 'done' }] }] },
          ],
          chat_id: 'dup-chat', last_seq: 2,
        },
        error: null,
      }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))

    await act(async () => {
      void result.current.reload()
    })

    // MUST have exactly ONE assistant message (no duplication)
    const assistantMsgs = result.current.messages.filter(m => m.role === 'assistant')
    expect(assistantMsgs).toHaveLength(1)
    // The DB version (with iterations) should win
    expect(assistantMsgs[0].persisted).toBe(true)
    expect(assistantMsgs[0].iterations).toHaveLength(1)
  })

  it('reloads messages from DB when replay_gap is dispatched (real data loss)', async () => {
    let messageHandler: ((message: WSMessage) => void) | null = null
    let call = 0
    vi.stubGlobal('fetch', vi.fn(async () => {
      call++
      const data = call === 1
        ? { messages: [{ role: 'user', content: 'initial', timestamp: '2026-07-08T00:00:00Z' }] }
        : { messages: [
            { role: 'user', content: 'initial', timestamp: '2026-07-08T00:00:00Z' },
            { role: 'assistant', content: 'reply after gap', timestamp: '2026-07-08T00:00:02Z' },
          ] }
      return new Response(JSON.stringify({ ok: true, data, error: null }), {
        status: 200, headers: { 'Content-Type': 'application/json' },
      })
    }))
    const ws = {
      rpc: vi.fn(),
      send: vi.fn(async () => undefined),
      setLastSeq: vi.fn(),
      onMessage: vi.fn((handler: (m: WSMessage) => void) => { messageHandler = handler; return vi.fn() }),
    } as unknown as WSConnection

    const { result } = renderHook(() =>
      useChatMessages({ chatID: 'chat-gap', channel: 'web', ws }),
    )
    await waitFor(() => expect(result.current.messages.map((m) => m.content)).toEqual(['initial']))

    // Simulate replay_gap dispatch (TurnID changed during SSE gap → real data loss)
    act(() => messageHandler?.({ type: 'replay_gap', chat_id: 'web:chat-gap' }))

    await waitFor(() => expect(result.current.messages.map((m) => m.content)).toEqual([
      'initial', 'reply after gap',
    ]))
  })

  it('inserts a committed assistant after ITS OWN turn user even when the next turn user is already persisted', async () => {
    // BUG: appendAssistant(insertBeforeLastUser=true) only looked for an
    // UNPERSISTED user. When the next turn's user was persisted first (REST
    // response arrived), it fell through to the END of the list, rendering
    // turn N's iteration history AFTER turn N+1's user/live content — the
    // "严重迭代混乱" layout (asst-42 appears below user-43).
    const ws = makeWS([{ messages: [] }])
    const { result } = renderHook(() => useChatMessages({ chatID: 'turn-order', channel: 'web', ws }))
    await waitFor(() => expect(result.current.messages).toEqual([]))
    const handler = vi.mocked(ws.onMessage).mock.calls[0][0] as (m: WSMessage) => void

    // turn 42 + turn 43 users arrive via deterministic backend user_echo (turn_id included).
    act(() => handler({ type: 'user_echo', content: 'u42', turn_id: 42, ts: 1000, id: 'r42' }))
    act(() => handler({ type: 'user_echo', content: 'u43', turn_id: 43, ts: 1001, id: 'r43' }))

    // turn 42's assistant committed late (turn_started(43) / text fallback)
    act(() => result.current.appendAssistant('A42', [], undefined, 42, true))

    const contents = result.current.messages.map((m) => m.content)
    expect(contents.indexOf('A42')).toBeGreaterThan(contents.indexOf('u42'))
    expect(contents.indexOf('A42')).toBeLessThan(contents.indexOf('u43'))
  })

  it('inserts committed assistant AFTER the AskUser answer user (same turnID, last user)', async () => {
    // AskUser answer case: the answer is persisted as a user message with the
    // SAME turnID as the iterations that follow, and it is the LAST user in
    // the list. insertBeforeLastUser must locate THIS TURN's user (turnID
    // match) and insert AFTER it — otherwise the new iterations render ABOVE
    // the answer (broken order).
    const ws = makeWS([{ messages: [] }])
    const { result } = renderHook(() => useChatMessages({ chatID: 'askuser-order', channel: 'web', ws }))
    await waitFor(() => expect(result.current.messages).toEqual([]))
    const handler = vi.mocked(ws.onMessage).mock.calls[0][0] as (m: WSMessage) => void

    // original user + AskUser answer user — both arrive via backend user_echo
    // (deterministic turn_id), same turn 2.
    act(() => handler({ type: 'user_echo', content: 'u2', turn_id: 2, ts: 1000, id: 'r2a' }))
    act(() => handler({ type: 'user_echo', content: 'answer', turn_id: 2, ts: 1001, id: 'r2b' }))

    // post-answer iteration (turn 2) commits via insertBeforeLastUser
    act(() => result.current.appendAssistant('A2', [], undefined, 2, true))

    const contents = result.current.messages.map((m) => m.content)
    expect(contents.indexOf('A2')).toBeGreaterThan(contents.indexOf('answer'))
    expect(contents.indexOf('A2')).toBeGreaterThan(contents.indexOf('u2'))
  })

  it('injectUserMessage deduplicates notification by turnID (SSE reconnect replay)', async () => {
    // BUG: turn_started events are buffered by the web hub's ring buffer as
    // stateful messages and replayed on SSE reconnect. Without dedup, each
    // replay calls injectUserMessage again, creating duplicate notification
    // user messages. After refresh, reconcileHistoryWithLiveRows drops the
    // live rows (eventSeq=-1 < watermark), so the duplicate "disappears".
    // Fix: injectUserMessage checks if a notification with the same turnID
    // already exists before creating a new one.
    const ws = makeWS([{ messages: [] }])
    const { result } = renderHook(() => useChatMessages({ chatID: 'notif-dedup', channel: 'web', ws }))
    await waitFor(() => expect(result.current.messages).toEqual([]))

    // First turn_started (notification) — creates the notification user message.
    act(() => result.current.injectUserMessage('bg task completed', 55, true))
    expect(result.current.messages).toHaveLength(1)
    expect(result.current.messages[0].content).toBe('bg task completed')
    expect(result.current.messages[0].turnID).toBe(55)
    expect(result.current.messages[0].isNotification).toBe(true)

    // SSE reconnect replays the same turn_started — must NOT create a duplicate.
    act(() => result.current.injectUserMessage('bg task completed', 55, true))
    expect(result.current.messages).toHaveLength(1)

    // A different turnID (new notification) — should create a new message.
    act(() => result.current.injectUserMessage('cron job done', 56, true))
    expect(result.current.messages).toHaveLength(2)
  })

  it('reconcileHistoryWithLiveRows keeps a notification user message (eventSeq=-1) when history lacks it', async () => {
    // Scenario 1 (weak network): a bg notification turn starts, the notification
    // user message is injected (eventSeq=-1 marker), then a racing reload
    // returns history WITHOUT the row yet (eager-save in flight). The old
    // watermark rule (eventSeq=-1 < last_seq) dropped the notification until
    // refresh. Fix: eventSeq=-1 notifications are kept unless history already
    // covers the same turnID:role.
    const ws = makeWS([
      {
        messages: [{
          id: 90, role: 'assistant', content: 'older reply', turn_id: 54,
          timestamp: '2026-08-03T00:00:00Z',
          iterations: [],
        }],
        last_seq: 200, oldest_id: 90, has_more: false,
      },
    ])
    const { result } = renderHook(() => useChatMessages({ chatID: 'notif-reconcile', channel: 'web', ws }))
    await waitFor(() => expect(result.current.messages.length).toBeGreaterThan(0))
    // Inject a notification whose DB row does not exist yet (racing reload).
    act(() => result.current.injectUserMessage('bg task done', 55, true))
    expect(result.current.messages.some((m) => m.isNotification && m.turnID === 55)).toBe(true)
    // Reload: history (last_seq=200) lacks the notification row; the eventSeq=-1
    // marker must NOT be dropped by the watermark rule.
    await act(async () => { await result.current.reload() })
    const notif = result.current.messages.find((m) => m.isNotification)
    expect(notif).toBeDefined()
    expect(notif?.turnID).toBe(55)
  })

  it('loadMore deduplicates by turnID:role across batch boundaries', async () => {
    // BUG: ConvertMessagesToHistory processes each batch independently.
    // When the batch boundary cuts mid-turn, batch 1 has the turn's END (final
    // assistant reply with Detail/iterations, e.g. ID=106) and batch 2 has the
    // turn's BEGINNING (user + assistant with tool_calls, e.g. ID=99). Both
    // produce an assistant HistoryMessage with the same turnID:role but different
    // DB IDs. The id-only dedup in loadMore can't catch them → the same turn's
    // iterations render twice after scrolling up.
    //
    // Fix: loadMore also dedups by turnID:role (same pattern as
    // reconcileHistoryWithLiveRows). When the new batch's message has the same
    // turnID:role as an existing message, drop the new one (the existing message
    // from the more recent batch has the complete final reply + iterations).
    const ws = makeWS([
      // Initial load: one assistant with turnID=5 (final reply, has iterations)
      {
        messages: [{
          id: 106, role: 'assistant', content: 'final reply', turn_id: 5,
          timestamp: '2026-08-03T00:00:06Z',
          iterations: [{ iteration: 1, content: 'final reply', tools: [] }],
        }],
        last_seq: 200, oldest_id: 106, has_more: true,
      },
      // loadMore: user(5) + assistant(5, tool_summary from flushPending, same turnID)
      {
        messages: [
          { id: 98, role: 'user', content: 'hello', turn_id: 5, timestamp: '2026-08-03T00:00:01Z' },
          { id: 99, role: 'assistant', content: '', turn_id: 5, timestamp: '2026-08-03T00:00:02Z',
            iterations: [{ iteration: 1, tools: [{ name: 'Shell', status: 'done' }] }] },
        ],
        last_seq: 99, oldest_id: 98, has_more: false,
      },
    ])

    const { result } = renderHook(() => useChatMessages({ chatID: 'loadmore-dedup', channel: 'web', ws }))
    await waitFor(() => expect(result.current.messages.map((m) => m.content)).toEqual(['final reply']))

    // loadMore: batch 2 arrives with user(5) + assistant(5, tool_summary).
    // The assistant(5) must be DEDUPED — batch 1 already has assistant(5)
    // with the complete final reply + iterations.
    let loaded = false
    await act(async () => { loaded = await result.current.loadMore() })
    expect(loaded).toBe(true)

    const msgs = result.current.messages
    // Should have: user(5) + assistant(5, final reply). NOT user(5) + assistant(5, tool_summary) + assistant(5, final reply).
    const assistantTurn5 = msgs.filter((m) => m.role === 'assistant' && m.turnID === 5)
    expect(assistantTurn5).toHaveLength(1)
    expect(assistantTurn5[0].content).toBe('final reply')
    // user(5) should be present (it's new — not in batch 1).
    expect(msgs.some((m) => m.role === 'user' && m.turnID === 5 && m.content === 'hello')).toBe(true)
  })

  it('cancelled-turn assistant committed via commitLiveProgressAndReset lands AFTER its turn user, even when next user is not yet in the list', async () => {
    // BUG: when turn_started(2) fires and commitLiveProgressAndReset commits
    // the cancelled turn 1's assistant, appendAssistant(insertBeforeLastUser=true)
    // scans backwards for the LAST user message. If user2's optimistic row
    // hasn't been added to the messages array yet (race: turn_started arrives
    // before sendMessage's setMessages is applied), the scan finds user1 at
    // index 0 and inserts BEFORE it: [assistant1, user1]. Then user2 is added:
    // [assistant1, user1, user2] — the assistant appears BEFORE user1.
    //
    // Fix: when insertBeforeLastUser=true AND turnID > 0, first scan for the
    // assistant's OWN turn user (role=user && turnID matches) and insert AFTER
    // it. This correctly positions the assistant even when the next turn's
    // user is not yet in the list.
    const ws = makeWS([{ messages: [] }])
    const { result } = renderHook(() => useChatMessages({ chatID: 'cancel-race', channel: 'web', ws }))
    await waitFor(() => expect(result.current.messages).toEqual([]))

    // turn 1: user1 sent with turnID=1 (use injectUserMessage to set turnID directly)
    act(() => result.current.injectUserMessage('u1', 1, false))
    expect(result.current.messages).toHaveLength(1)
    expect(result.current.messages[0].turnID).toBe(1)

    // Simulate the race: commitLiveProgressAndReset fires BEFORE user2 is
    // added to the messages array. The committed assistant has turnID=1
    // (the cancelled turn's ID).
    act(() => result.current.appendAssistant('A1', [], undefined, 1, true))

    // The assistant must land AFTER user1 (its own turn user), not before it.
    const contents = result.current.messages.map((m) => m.content)
    expect(contents).toEqual(['u1', 'A1'])

    // Now user2 is added (next turn's user)
    act(() => result.current.injectUserMessage('u2', 2, false))
    expect(result.current.messages.map((m) => m.content)).toEqual(['u1', 'A1', 'u2'])
  })

  it('sendMessage updates optimistic message with REST response turn_id (prevents assistant displacement)', async () => {
    // BUG: sendMessage's .then() callback didn't update the optimistic message
    // with the REST response data (turn_id, message_id). The optimistic message
    // stayed turnID=0, persisted=false until user_echo arrived. When
    // commitLiveProgressAndReset fired (from turn_started of the NEXT turn),
    // appendAssistant(insertBeforeLastUser=true, turnID=N) couldn't find a
    // user with turnID=N (it was 0), fell back to inserting BEFORE the last
    // user — which was user(N) itself — resulting in [assistant(N), user(N)]
    // instead of [user(N), assistant(N)].
    //
    // Fix: .then() updates the optimistic message with turn_id, dbID,
    // persisted=true, sending=false from the REST response.
    const ws = makeWS([{ messages: [] }])
    // REST response includes turn_id and message_id
    vi.mocked(ws.send).mockResolvedValue({ turn_id: 42, queued: false, message_id: 100, timestamp: 1_786_000_000 })
    const { result } = renderHook(() => useChatMessages({ chatID: 'rest-bind', channel: 'web', ws }))
    await waitFor(() => expect(result.current.messages).toEqual([]))

    // Send message — optimistic user is added (no sending spinner)
    act(() => result.current.sendMessage('hello'))
    expect(result.current.messages).toHaveLength(1)
    expect(result.current.messages[0].content).toBe('hello')

    // REST response resolves — optimistic message is updated
    await act(async () => { await Promise.resolve() })
    expect(result.current.messages[0].turnID).toBe(42)
    expect(result.current.messages[0].persisted).toBe(true)
    expect(result.current.messages[0].dbID).toBe(100)

    // Now commitLiveProgressAndReset (from turn_started of next turn) fires
    // with turnID=42. appendAssistant scans for user with turnID=42 → found
    // → inserts AFTER it (not before).
    act(() => result.current.appendAssistant('reply', [], undefined, 42, true))
    expect(result.current.messages.map((m) => m.content)).toEqual(['hello', 'reply'])
  })

  it('full linear consistency: user1 → assistant1(cancelled) → user2 → assistant2', async () => {
    // End-to-end test covering the cancelled-turn rendering bug.
    // After this fix, the order must ALWAYS be [u1, A1, u2, A2] — never
    // [A1, u1, u2, A2] or [u1, u2, A1, A2].
    const ws = makeWS([{ messages: [] }])
    vi.mocked(ws.send).mockResolvedValue({ turn_id: 1, queued: false, message_id: 1, timestamp: 1 })
    const { result } = renderHook(() => useChatMessages({ chatID: 'e2e-linear', channel: 'web', ws }))
    await waitFor(() => expect(result.current.messages).toEqual([]))

    // Turn 1: user1 sent (REST response binds turnID=1)
    act(() => result.current.sendMessage('u1'))
    await act(async () => { await Promise.resolve() })
    expect(result.current.messages[0].turnID).toBe(1)

    // Turn 1 cancelled: commitLiveProgressAndReset fires with turnID=1
    // (simulating turn_started(2) committing turn 1's frozen content)
    act(() => result.current.appendAssistant('A1', [], undefined, 1, true))
    expect(result.current.messages.map((m) => m.content)).toEqual(['u1', 'A1'])

    // Turn 2: user2 sent (REST response binds turnID=2)
    vi.mocked(ws.send).mockResolvedValue({ turn_id: 2, queued: false, message_id: 2, timestamp: 2 })
    act(() => result.current.sendMessage('u2'))
    await act(async () => { await Promise.resolve() })
    expect(result.current.messages.map((m) => m.content)).toEqual(['u1', 'A1', 'u2'])
    expect(result.current.messages[2].turnID).toBe(2)

    // Turn 2 completes: text event commits assistant2 with turnID=2
    act(() => result.current.appendAssistant('A2', [], undefined, 2, false))
    expect(result.current.messages.map((m) => m.content)).toEqual(['u1', 'A1', 'u2', 'A2'])

    // Verify turnIDs are correct
    expect(result.current.messages.map((m) => m.turnID)).toEqual([1, 1, 2, 2])
  })

  it('user_echo deduplicates against REST-response-bound optimistic message', async () => {
    // Race: REST response arrives first (binds turnID, sets persisted=true),
    // then user_echo arrives. The echo must NOT create a duplicate.
    const ws = makeWS([{ messages: [] }])
    vi.mocked(ws.send).mockResolvedValue({ turn_id: 42, queued: false, message_id: 100, timestamp: 1 })
    const { result } = renderHook(() => useChatMessages({ chatID: 'echo-dedup', channel: 'web', ws }))
    await waitFor(() => expect(result.current.messages).toEqual([]))

    // sendMessage creates optimistic, REST resolves with turnID=42
    act(() => result.current.sendMessage('hello'))
    await act(async () => { await Promise.resolve() })
    expect(result.current.messages).toHaveLength(1)
    expect(result.current.messages[0].turnID).toBe(42)
    expect(result.current.messages[0].persisted).toBe(true)

    // Simulate user_echo arriving via SSE (ws.onMessage callback)
    const onMessageCb = vi.mocked(ws.onMessage).mock.calls[0]?.[0] as (msg: WSMessage) => () => void
    expect(onMessageCb).toBeDefined()
    act(() => {
      const off = onMessageCb({
        type: 'user_echo',
        id: result.current.messages[0].requestID,
        content: 'hello',
        turn_id: 42,
        seq: 1,
        ts: 1,
      } as WSMessage)
      off?.()
    })

    // Must NOT create a duplicate — the echo should be deduped
    expect(result.current.messages).toHaveLength(1)
    expect(result.current.messages[0].content).toBe('hello')
    expect(result.current.messages[0].turnID).toBe(42)
  })
})
