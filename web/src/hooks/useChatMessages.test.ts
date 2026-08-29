import { act, renderHook, waitFor } from '@testing-library/react'
import React from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { useChatMessages } from './useChatMessages'
import type { WSConnection } from '@/types/ws'
import type { WSMessage, ChatMessage } from '@/types/shared'
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

  it('attaches SubAgent history iterations to the assistant message (via fetchHistory)', async () => {
    const ws = makeWS([
      {
        messages: [
          { role: 'user', content: 'check this' },
          { role: 'assistant', content: 'done', iterations: [
            { iteration: 1, content: 'thinking', completed_tools: [{ name: 'Read', status: 'done', summary: 'ok' }] },
          ] },
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

  it('loads nested SubAgent history by full key via the same fetchHistory path as main sessions', async () => {
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
    // SubAgent 走 fetchHistory（/api/history），不再走内存 get_agent_session_dump RPC。
    expect(ws.rpc).not.toHaveBeenCalled()
  })

  it('shows SubAgent history iterations even when there is no assistant text yet (via fetchHistory)', async () => {
    const ws = makeWS([
      {
        messages: [
          { role: 'assistant', content: '', iterations: [
            { iteration: 1, completed_tools: [{ name: 'Shell', status: 'running', summary: 'running' }] },
          ] },
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

  // F#4：以下 useProgressStream 时代的死接口测试（appendAssistant /
  // injectUserMessage / markDestructiveMutation）已删除 —— 接口生产零引用
  //（useAgentChatState TDSM 状态机接管 commit/notification 注入路径），等价
  // 行为由 reduce.test.ts / useAgentChatState.test.tsx / messageStore.test.ts
  // 覆盖（commit 排序由 MessageStore turnID 槽位结构保证、notification dedup
  // 由 reduce user_echo 幂等规则覆盖）。

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

  it('reload() returns the fresh rows including dbID for rewind resolution', async () => {
    // BUG: user messages rendered from user_echo SSE carry persisted=true but
    // NO dbID (the DB id is assigned at persistence, AFTER the echo is sent at
    // queue-admission time). Rewind needs that id. reload() is the only source
    // of dbID (parseHistoryMessages → dbID: m.id), so it must return the fresh
    // rows instead of void — rewindTo resolves a missing dbID from them.
    const ws = makeWS([
      // Initial load: history rows already carry dbID.
      { messages: [{ id: 41, role: 'user', content: 'earlier', turn_id: 6, timestamp: '2026-08-03T00:00:01Z' }] },
      // reload() response: the persisted user row with its DB id.
      { messages: [{ id: 42, role: 'user', content: 'hello', turn_id: 7, timestamp: '2026-08-03T00:00:02Z' }] },
    ])

    const { result } = renderHook(() => useChatMessages({ chatID: 'rewind-reload', channel: 'web', ws }))
    await waitFor(() => expect(result.current.messages.map((m) => m.content)).toEqual(['earlier']))

    const holder: { rows: ChatMessage[] | null } = { rows: null }
    await act(async () => {
      holder.rows = await result.current.reload()
    })

    expect(holder.rows).not.toBeNull()
    expect(holder.rows?.map((m) => m.content)).toEqual(['hello'])
    expect(holder.rows?.[0].dbID).toBe(42)
    expect(result.current.messages[0].dbID).toBe(42)
  })

  it('sendMessage updates optimistic message with REST response turn_id (prevents assistant displacement)', async () => {
    // BUG: sendMessage's .then() callback didn't update the optimistic message
    // with the REST response data (turn_id, message_id). The optimistic message
    // stayed turnID=0, persisted=false until user_echo arrived — breaking
    // turn binding (turn_started(N) couldn't bind the pending user).
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

  it('StrictMode 下 user_echo 只产生一行（F#5：副作用移出 setMessages updater）', async () => {
    // React StrictMode 双调用 setState updater —— 旧实现把 messageMutationGenRef
    // 自增、store 写入、syncMessages（嵌套 setMessages）放在 updater 内，双执行
    // 导致 mutation 计数双递增（SSE 重连 last_event_id 水位回退的根因）。副作用
    // 移到 listener 回调体后，StrictMode 下 echo 行为不变：单行、store 幂等。
    let messageHandler: ((message: WSMessage) => void) | null = null
    const ws = makeWS([])
    vi.mocked(ws.onMessage).mockImplementation((handler) => {
      messageHandler = handler
      return vi.fn()
    })
    const { result } = renderHook(
      () => useChatMessages({ chatID: 'strict-chat', channel: 'web', ws }),
      { wrapper: React.StrictMode },
    )
    await waitFor(() => expect(result.current.loading).toBe(false))

    act(() => {
      messageHandler?.({
        type: 'user_echo',
        chat_id: 'strict-chat',
        content: 'strict message',
        ts: 1723600000,
        seq: 1,
      })
    })

    expect(result.current.messages.map((m) => m.content)).toEqual(['strict message'])
  })

  it('loadMore 无 id 无 seq 的历史行不因合成 id 跨批冲突被判重（分页截断）', async () => {
    // Loop2 F4：E2E mock / 旧 fixture 的消息可能无 id 无 seq ——
    // parseHistoryMessages 的 id fallback `hist-${i}`（i 是批内 index）每批
    // 从 0 重来 → loadMore 第二批的 `hist-0`/`hist-1` 与第一批冲突 →
    // noExactDups=0 → hasMore=false 分页截断（更老的消息永远加载不出来，
    // 尽管第二批是新数据）。修复：loadMore 传批判别符（beforeId cursor），
    // id fallback 变 `hist-{beforeId}-{i}`（跨批唯一）。
    const ws = makeWS([
      // 初始批：3 行（无 id 无 seq）
      {
        messages: [
          { role: 'user', content: 'turn5 q', turn_id: 5, timestamp: '2026-08-03T00:00:05Z' },
          { role: 'assistant', content: 'turn4 reply', turn_id: 4, timestamp: '2026-08-03T00:00:04Z' },
          { role: 'user', content: 'turn4 q', turn_id: 4, timestamp: '2026-08-03T00:00:03Z' },
        ],
        last_seq: 200, oldest_id: 300, has_more: true,
      },
      // loadMore 批：2 行（无 id 无 seq —— id fallback hist-0/hist-1）
      {
        messages: [
          { role: 'user', content: 'turn2 q', turn_id: 2, timestamp: '2026-08-03T00:00:02Z' },
          { role: 'assistant', content: 'turn1 reply', turn_id: 1, timestamp: '2026-08-03T00:00:01Z' },
        ],
        last_seq: 100, oldest_id: 100, has_more: false,
      },
    ])

    const { result } = renderHook(() => useChatMessages({ chatID: 'loadmore-synthid', channel: 'web', ws }))
    await waitFor(() => expect(result.current.messages).toHaveLength(3))

    let loaded = false
    await act(async () => { loaded = await result.current.loadMore() })
    // 修复前：第二批合成 id hist-0/hist-1 与第一批冲突 → 全被判 dup →
    // noExactDups=0 → hasMore=false，loaded=false（更老消息截断）。
    expect(loaded).toBe(true)
    // 第二批的老消息可见（分页未截断）
    const contents = result.current.messages.map((m) => m.content)
    expect(contents).toContain('turn2 q')
    expect(contents).toContain('turn1 reply')
    expect(result.current.hasMore).toBe(false) // 第三批 has_more=false（真实结束）
  })

  it('loadMore 无 id 无 seq 多批：合成 id 批间唯一（hist-{beforeId}-{i}）', async () => {
    // 三批连续 loadMore（cursor 移动）—— 每批判别符不同，合成 id 不冲突。
    const ws = makeWS([
      {
        messages: [
          { role: 'user', content: 'newest', turn_id: 9, timestamp: '2026-08-03T00:00:09Z' },
        ],
        last_seq: 900, oldest_id: 90, has_more: true,
      },
      {
        messages: [
          { role: 'user', content: 'older1', turn_id: 8, timestamp: '2026-08-03T00:00:08Z' },
          { role: 'user', content: 'older2', turn_id: 7, timestamp: '2026-08-03T00:00:07Z' },
        ],
        last_seq: 800, oldest_id: 70, has_more: true,
      },
      {
        messages: [
          { role: 'user', content: 'oldest', turn_id: 6, timestamp: '2026-08-03T00:00:06Z' },
        ],
        last_seq: 700, oldest_id: 60, has_more: false,
      },
    ])

    const { result } = renderHook(() => useChatMessages({ chatID: 'loadmore-multi-batch', channel: 'web', ws }))
    await waitFor(() => expect(result.current.messages.map((m) => m.content)).toEqual(['newest']))

    let l1 = false
    await act(async () => { l1 = await result.current.loadMore() })
    expect(l1).toBe(true)
    let l2 = false
    await act(async () => { l2 = await result.current.loadMore() })
    expect(l2).toBe(true)

    // 三批全部加载（合成 id 批间唯一，无跨批误判重）；渲染按 turnID 升序：
    // 6(oldest) → 7(older2) → 8(older1) → 9(newest)。
    const contents = result.current.messages.map((m) => m.content)
    expect(contents).toEqual(['oldest', 'older2', 'older1', 'newest'])
    expect(result.current.hasMore).toBe(false)
  })
})
