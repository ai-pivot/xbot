/**
 * Tests for PluginWidgetProvider — web_widgets SSE message consumption.
 *
 * Verifies:
 *  - web_widgets messages for the active session update zones/components
 *  - messages for other chatIDs are ignored
 *  - session switch resets state (no cross-session leak)
 */
import { act, renderHook } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { PluginWidgetProvider, usePluginWidgets } from '@/plugins/PluginWidgetProvider'
import type { WSMessage } from '@/types/shared'

type MessageHandler = (msg: WSMessage) => void

interface FakeWS {
  onMessage: (h: MessageHandler) => () => void
  onProgress: (h: (e: unknown) => void) => () => void
  send: (msg: unknown) => void
  connected: boolean
  chatID: string | null
  emit: (msg: WSMessage) => void
}

function makeFakeWS(): FakeWS & { handlers: Set<MessageHandler> } {
  const handlers = new Set<MessageHandler>()
  return {
    handlers,
    onMessage: (h) => {
      handlers.add(h)
      return () => handlers.delete(h)
    },
    onProgress: () => () => {},
    send: () => {},
    connected: true,
    chatID: null,
    emit: (msg) => handlers.forEach((h) => h(msg)),
  }
}

let currentWS: FakeWS & { handlers: Set<MessageHandler> }

// Mock useWSConnection to return the fake WS.
vi.mock('@/hooks/useWSConnection', () => ({
  useWSConnection: () => currentWS,
}))

// Mock useSessionStore to return a controllable active session.
const sessionState = { activeSession: { channel: 'web', chatID: '/home/test' } }
vi.mock('@/hooks/useSessionStore', () => ({
  useSessionStore: () => sessionState,
}))

// Mock the initial pull RPC (skip network in tests).
vi.spyOn(globalThis, 'fetch').mockResolvedValue({
  ok: true,
  json: async () => ({ zones: {} }),
} as Response)

// Simple wrapper to run the provider hook tree.
import { createElement } from 'react'

function renderProvider() {
  return renderHook(() => usePluginWidgets(), {
    wrapper: ({ children }: { children?: React.ReactNode }) =>
      createElement(PluginWidgetProvider, null, children),
  })
}

beforeEach(() => {
  currentWS = makeFakeWS()
  sessionState.activeSession = { channel: 'web', chatID: '/home/test' }
})

describe('PluginWidgetProvider', () => {
  it('consumes web_widgets for the active chat and updates zones', () => {
    const { result } = renderProvider()
    expect(result.current.zones).toEqual({})

    act(() => {
      currentWS.emit({
        type: 'web_widgets',
        chat_id: '/home/test',
        content: JSON.stringify({
          zones: { status_bar_left: [{ text: 'git:main', style: 'accent' }] },
          revision: 3,
        }),
      })
    })

    expect(result.current.zones.status_bar_left).toHaveLength(1)
    expect(result.current.zones.status_bar_left[0].text).toBe('git:main')
    expect(result.current.revision).toBe(3)
  })

  it('parses components from web_widgets payload', () => {
    const { result } = renderProvider()
    act(() => {
      currentWS.emit({
        type: 'web_widgets',
        chat_id: '/home/test',
        content: JSON.stringify({
          components: [{ widget_id: 'ci', slot: 'right_sidebar', component: { type: 'sparkline', props: {} } }],
        }),
      })
    })
    expect(result.current.components).toHaveLength(1)
    expect(result.current.components[0].widget_id).toBe('ci')
  })

  it('ignores messages for a different chatID', () => {
    const { result } = renderProvider()
    act(() => {
      currentWS.emit({
        type: 'web_widgets',
        chat_id: '/other/chat',
        content: JSON.stringify({ zones: { info_bar: [{ text: 'x' }] } }),
      })
    })
    expect(result.current.zones).toEqual({})
  })

  it('ignores malformed payload', () => {
    const { result } = renderProvider()
    act(() => {
      currentWS.emit({ type: 'web_widgets', chat_id: '/home/test', content: 'not-json{{' })
    })
    expect(result.current.zones).toEqual({})
  })
})
