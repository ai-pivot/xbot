import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'

const mocks = vi.hoisted(() => {
  const order: string[] = []
  const chat = {
    messages: [],
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
  const progress = { progressSnapshot: { todos: [], tokenUsage: null }, liveMessage: null, isStreaming: false }
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
  MessageList: (props: { onRewind?: (content: string, message: unknown) => void; busy?: boolean }) => (
    <div>
      <div data-testid="message-list-busy">{String(props.busy ?? false)}</div>
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
