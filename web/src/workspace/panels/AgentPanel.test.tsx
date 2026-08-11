import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'

const mocks = vi.hoisted(() => {
  const order: string[] = []
  const chat = {
    messages: [] as Array<{ id: string; role: string; content: string; isPartial?: boolean; turnID?: number }>,
    loading: false,
    error: null,
    resolvedChatID: 'chat-1',
    initialProgress: null,
    clearMessages: vi.fn(() => order.push('clear')),
    reload: vi.fn(async () => { order.push('reload') }),
    sendMessage: vi.fn(() => { order.push('send') }),
    cancel: vi.fn(),
    upload: vi.fn(),
    appendAssistant: vi.fn(),
  }
  const context = {
    ws: { onSession: vi.fn(() => vi.fn()) },
    sessionStore: { activeSession: { channel: 'web', chatID: 'chat-1' }, sessions: [] },
    rightSidebar: { openPanel: vi.fn() },
  }
  const progress: {
    progressSnapshot: { todos: unknown[]; tokenUsage: null; streaming?: boolean; phase?: string }
    liveMessage: unknown
    isStreaming: boolean
  } = {
    progressSnapshot: { todos: [], tokenUsage: null },
    liveMessage: null,
    isStreaming: false,
  }
  return { chat, context, order, progress, rewindHistory: vi.fn() }
})

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock('@/hooks/useAskUser', () => ({ useAskUser: () => ({ prompt: null, respond: vi.fn(), cancel: vi.fn() }) }))
vi.mock('@/hooks/useChatMessages', () => ({ useChatMessages: () => mocks.chat }))
vi.mock('@/hooks/useCollapseLevel', () => ({
  useCollapseLevel: () => ({ level: 'all' }),
  useMergeTools: () => ({ mergeTools: false }),
}))
vi.mock('@/hooks/useProgressStream', () => ({
  useProgressStream: () => mocks.progress,
}))
vi.mock('@/hooks/useTodos', () => ({ useTodos: () => ({ total: 0 }) }))
vi.mock('@/hooks/useActiveSSESubscription', () => ({ useActiveSSESubscription: vi.fn() }))
vi.mock('@/hooks/useSessionContext', () => ({
  useSessionContext: () => ({
    available: true,
    promptTokens: 0,
    maxContext: 200_000,
    usagePercent: 0,
    subscriptionID: '',
    model: '',
    refresh: vi.fn(),
  }),
}))
vi.mock('@/hooks/useLLMSettings', () => ({
  useLLMSettings: () => ({
    data: { subscriptions: [], modelEntries: [], thinkingMode: '' },
    saving: false,
    setThinkingMode: vi.fn(),
  }),
}))
vi.mock('@/components/agent/api', () => ({ rewindHistory: (...args: unknown[]) => mocks.rewindHistory(...args) }))
vi.mock('@/components/agent/AskUserPanel', () => ({ AskUserPanel: () => null }))
vi.mock('@/components/agent/ContextRing', () => ({ ContextRing: () => null }))
vi.mock('@/components/agent/MessageInput', () => ({ MessageInput: () => null }))
vi.mock('@/components/agent/ModelSelector', () => ({ ModelSelector: () => null }))
vi.mock('@/components/agent/MessageList', () => ({
  latestCompactBoundaryIndex: () => -1,
  MessageList: (props: {
    onRewind?: (content: string, message: unknown) => void
    busy?: boolean
    liveMessage?: unknown
    liveProgress?: unknown
    messages?: unknown[]
    loading?: boolean
  }) => (
    <div>
      <div data-testid="message-list-busy">{String(props.busy ?? false)}</div>
      <div data-testid="message-list-live">{props.liveMessage ? 'live-visible' : 'live-hidden'}</div>
      <div data-testid="message-list-live-progress">{props.liveProgress ? 'progress-visible' : 'progress-hidden'}</div>
      <button
        type="button"
        onClick={() => props.onRewind?.('edited message', {
          id: 'db-42',
          role: 'user',
          content: 'original message',
          timestamp: '2026-07-08T00:00:01Z',
          dbID: 42,
          persisted: true,
        })}
      >
        rewind
      </button>
    </div>
  ),
}))
vi.mock('@/workspace/types', () => ({ useDockviewContext: () => mocks.context }))
vi.mock('@/providers/i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

import { AgentPanel } from './AgentPanel'

describe('AgentPanel rewind', () => {
  beforeEach(() => {
    mocks.order.length = 0
    mocks.rewindHistory.mockReset()
    mocks.rewindHistory.mockResolvedValue({})
    mocks.chat.clearMessages.mockClear()
    mocks.chat.reload.mockClear()
    mocks.chat.sendMessage.mockClear()
  })

  it('clears and reloads before resending the edited message', async () => {
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)

    fireEvent.click(screen.getByRole('button', { name: 'rewind' }))

    await waitFor(() => expect(mocks.chat.sendMessage).toHaveBeenCalledWith('edited message', undefined))
    expect(mocks.rewindHistory).toHaveBeenCalledWith(
      { channel: 'web', chatID: 'chat-1' },
      42,
    )
    expect(mocks.order).toEqual(['clear', 'reload', 'send'])
  })

  it('does not clear or resend when the rewind request fails', async () => {
    mocks.rewindHistory.mockRejectedValueOnce(new Error('rewind failed'))
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)

    fireEvent.click(screen.getByRole('button', { name: 'rewind' }))

    await waitFor(() => expect(mocks.rewindHistory).toHaveBeenCalled())
    expect(mocks.chat.clearMessages).not.toHaveBeenCalled()
    expect(mocks.chat.reload).not.toHaveBeenCalled()
    expect(mocks.chat.sendMessage).not.toHaveBeenCalled()
  })
})

describe('AgentPanel busy state', () => {
  beforeEach(() => {
    mocks.progress.progressSnapshot = { todos: [], tokenUsage: null }
    mocks.progress.liveMessage = null
  })

  it('falls back to progressSnapshot.streaming when sessionStore.running is false (refresh mid-turn)', () => {
    // Simulates a page refresh during first-iteration thinking: SSE does not
    // replay session(busy), so sessionStore.running stays false. But the
    // hydrated active_progress (historyProgressToLive) sets streaming=true
    // with phase='thinking' — the "思考中…" placeholder must still render.
    mocks.progress.progressSnapshot = {
      todos: [],
      tokenUsage: null,
      streaming: true,
      phase: 'thinking',
    }
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)
    expect(screen.getByTestId('message-list-busy').textContent).toBe('true')
  })

  it('does NOT treat frozen (cancel) or done snapshots as busy', () => {
    mocks.progress.progressSnapshot = {
      todos: [],
      tokenUsage: null,
      streaming: false,
      phase: 'frozen',
    }
    const { unmount } = render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)
    expect(screen.getByTestId('message-list-busy').textContent).toBe('false')
    unmount()

    mocks.progress.progressSnapshot = {
      todos: [],
      tokenUsage: null,
      streaming: false,
      phase: 'done',
    }
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)
    expect(screen.getByTestId('message-list-busy').textContent).toBe('false')
  })

  it('stays idle when no live progress and session is idle', () => {
    mocks.progress.progressSnapshot = { todos: [], tokenUsage: null }
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)
    expect(screen.getByTestId('message-list-busy').textContent).toBe('false')
  })
})

describe('AgentPanel liveMessage visibility during reload', () => {
  beforeEach(() => {
    mocks.progress.progressSnapshot = { todos: [], tokenUsage: null, streaming: true, phase: 'thinking' }
    mocks.progress.liveMessage = { id: 'turn-live', role: 'assistant', isPartial: true }
  })

  it('KEEPS the live turn visible during a reload that blanks messages (loading=true, messages=[])', () => {
    // User report + [RENDER_LOSS_ROWS] rowsLen:0: a turn with heavy reasoning
    // floods SSE events → ring buffer overflow → resync_required →
    // useChatMessages setLoading(true)+reload() → reload BLANKS messages
    // (setMessages([]) in the no-cache path). The earlier gate
    // `chat.loading ? null : liveMessage` (then refined to
    // `loading && messages.length===0`) hid the ENTIRE live turn for the ~1s
    // reload — rows collapsed from 65 to 0, user could not scroll down. The
    // live store (useProgressStream) is INDEPENDENT of useChatMessages'
    // history loading; gating live on loading is architecturally wrong. The
    // live turn must stay visible whenever the store has liveMessage.
    mocks.chat.messages = [] // reload just blanked history
    mocks.chat.loading = true
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)
    expect(screen.getByTestId('message-list-live').textContent).toBe('live-visible')
    expect(screen.getByTestId('message-list-live-progress').textContent).toBe('progress-visible')
  })

  it('shows live even when messages are still empty during initial load', () => {
    // Even on the very first load the live store is authoritative: if it has a
    // hydrated liveMessage (refresh mid-turn → active_progress), it MUST render
    // immediately. Hiding it on loading caused the turn to vanish whenever a
    // reload coincided with an active turn (the reported bug).
    mocks.chat.messages = []
    mocks.chat.loading = true
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)
    expect(screen.getByTestId('message-list-live').textContent).toBe('live-visible')
    expect(screen.getByTestId('message-list-live-progress').textContent).toBe('progress-visible')
  })

  it('shows live when not loading (normal streaming)', () => {
    mocks.chat.messages = [{ id: 'u1', role: 'user', content: 'hi' }]
    mocks.chat.loading = false
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)
    expect(screen.getByTestId('message-list-live').textContent).toBe('live-visible')
  })
})
