/**
 * useAskUser — the panel must be driven by the SERVER-synced store, never by a
 * client-side authority (F1/F4):
 *   - `ask_user_resolved` (answered/cancelled in another channel or tab) ⇒ the
 *     cached prompt is dropped and the panel disappears ("多 channel 同步").
 *   - `session(busy)` (frozen contract: busy ⇒ 不存在 AskUser) ⇒ same.
 * Rendered through SessionStoreProvider so the assertions exercise the real
 * store wiring (event handler → askUserPrompts → useAskUser.prompt).
 */
import { createElement } from 'react'
import { act, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'

import { SessionStoreProvider, useSessionStore } from './useSessionStore'
import { useAskUser } from './useAskUser'
import type { WSMessage } from '@/types/shared'

let messageHandler: ((event: WSMessage) => void) | null = null
let sessionHandler:
  | ((event: { channel?: string; chat_id?: string; action?: string }) => void)
  | null = null

vi.mock('@/hooks/useWSConnection', () => ({
  useWSConnection: () => ({
    connected: true,
    subscribe: vi.fn(),
    disconnect: vi.fn(),
    rpc: vi.fn(),
    send: vi.fn(async () => ({ ok: true, data: {} })),
    onSession: vi.fn((handler) => {
      sessionHandler = handler
      return vi.fn()
    }),
    onMessage: vi.fn((handler) => {
      messageHandler = handler
      return vi.fn()
    }),
    chatID: null,
    channel: null,
  }),
}))

vi.mock('@/lib/api', () => ({
  postAPI: async (endpoint: string, body: Record<string, unknown> = {}) => {
    let target = endpoint
    if (endpoint === '/api/session-tree') {
      let response = await fetch('/api/chats', { method: 'POST', body: JSON.stringify(body) })
      if (!response.ok) response = await fetch('/api/session-tree', { method: 'POST', body: JSON.stringify(body) })
      const raw = await response.json()
      const data = raw.data ?? raw
      return {
        sessions: data.sessions ?? data.chats ?? [],
        orphan_subagents: data.orphan_subagents ?? [],
        has_more: data.has_more ?? false,
        next_offset: data.next_offset ?? 0,
      }
    }
    const response = await fetch(target, { method: 'POST', body: JSON.stringify(body) })
    if (!response.ok) throw new Error(`request failed: ${response.status}`)
    const raw = await response.json()
    return raw.data ?? raw
  },
}))

function Probe({ chatID, channel }: { chatID: string; channel: string }) {
  const { prompt } = useAskUser({ chatID, channel })
  const store = useSessionStore()
  return (
    <div>
      <div data-testid="panel">{prompt ? 'panel-visible' : 'panel-hidden'}</div>
      <div data-testid="prompt-count">{String(store.askUserPrompts.size)}</div>
      <div data-testid="session-count">{String(store.sessions.length)}</div>
    </div>
  )
}

function askUserEvent() {
  return {
    type: 'ask_user',
    channel: 'web',
    chat_id: 'web-chat-1',
    progress: {
      request_id: 'r-panel',
      questions: [{ question: 'Proceed?', options: ['yes', 'no'] }],
    },
  } as WSMessage
}

describe('useAskUser panel mirrors the server state', () => {
  beforeEach(() => {
    messageHandler = null
    sessionHandler = null
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/chats') {
        return {
          ok: true,
          json: async () => ({
            ok: true,
            sessions: [{
              chat_id: 'web-chat-1', channel: 'web', label: 'My Chat',
              last_active: '2026-07-08T00:00:00Z', is_current: true,
            }],
          }),
        } as Response
      }
      if (url === '/api/session-tree') {
        return { ok: true, json: async () => ({ ok: true, sessions: [] }) } as Response
      }
      if (url === '/api/subagents') {
        return { ok: true, json: async () => ({ ok: true, subagents: [] }) } as Response
      }
      throw new Error(`unexpected fetch: ${url}`)
    }))
  })

  async function renderProbe() {
    render(
      createElement(
        SessionStoreProvider,
        null,
        createElement(Probe, { chatID: 'web-chat-1', channel: 'web' }),
      ),
    )
    await waitFor(() => expect(screen.getByTestId('session-count').textContent).toBe('1'))
  }

  it('ask_user_resolved (answered in another channel) ⇒ prompt dropped, panel hidden', async () => {
    await renderProbe()

    act(() => {
      messageHandler?.(askUserEvent())
    })
    expect(screen.getByTestId('panel').textContent).toBe('panel-visible')
    expect(screen.getByTestId('prompt-count').textContent).toBe('1')

    // The prompt was answered/cancelled elsewhere — the server broadcasts the
    // invalidation to every client of the session.
    act(() => {
      messageHandler?.({
        type: 'ask_user_resolved',
        channel: 'web',
        chat_id: 'web-chat-1',
        reason: 'answered',
      } as WSMessage)
    })
    expect(screen.getByTestId('panel').textContent).toBe('panel-hidden')
    expect(screen.getByTestId('prompt-count').textContent).toBe('0')
  })

  it('session(busy) ⇒ stale prompt dropped, panel hidden (busy ⇒ 不存在 AskUser)', async () => {
    await renderProbe()

    act(() => {
      messageHandler?.(askUserEvent())
    })
    expect(screen.getByTestId('panel').textContent).toBe('panel-visible')

    act(() => {
      sessionHandler?.({ action: 'busy', channel: 'web', chat_id: 'web-chat-1' })
    })
    expect(screen.getByTestId('panel').textContent).toBe('panel-hidden')
    expect(screen.getByTestId('prompt-count').textContent).toBe('0')
  })
})
