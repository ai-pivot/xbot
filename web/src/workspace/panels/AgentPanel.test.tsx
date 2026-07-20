import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'

const mocks = vi.hoisted(() => {
  const order: string[] = []
  const progressOptions: Array<{ onHistoryRewound?: (historyID?: number, eventSeq?: number) => void }> = []
  const chatOptions: Array<{ onSendError?: (content: string, error: unknown) => void }> = []
  const askUserPanelProps: Array<{
    disabled?: boolean
    onRespond: (answers: Record<string, string>) => void
    onCancel: () => void
  }> = []
  const askUser = {
    prompt: null as { requestId: string; questions: Array<{ question: string }> } | null,
    respond: vi.fn(),
    cancel: vi.fn(),
  }
  const sessionHandlers: Array<(event: {
    action?: string
    channel?: string
    chat_id?: string
    session_key?: string
    role?: string
    instance?: string
    parent_id?: string
  }) => void> = []
  const chat = {
    messages: [],
    loading: false,
    error: null,
    processing: false,
    resolvedChatID: 'chat-1',
    initialProgress: null,
    clearMessages: vi.fn(() => order.push('clear')),
    reload: vi.fn(async () => {
      order.push('reload')
      return true
    }),
    sendMessage: vi.fn(() => {
      order.push('send')
    }),
    cancel: vi.fn(),
    upload: vi.fn(),
    appendAssistant: vi.fn(),
  }
  const context = {
    ws: {
      onSession: vi.fn((handler: (typeof sessionHandlers)[number]) => {
        sessionHandlers.push(handler)
        return vi.fn()
      }),
    },
    sessionStore: {
      activeSession: { channel: 'web', chatID: 'chat-1' },
      clearAskUserPrompt: vi.fn(),
    },
    rightSidebar: { openPanel: vi.fn() },
  }
  return {
    chat,
    chatOptions,
    askUser,
    askUserPanelProps,
    context,
    order,
    progressOptions,
    rewindHistory: vi.fn(),
    sessionHandlers,
    useChatMessages: vi.fn((options: { onSendError?: (content: string, error: unknown) => void }) => {
      chatOptions.push(options)
      return chat
    }),
  }
})

vi.mock('sonner', () => ({
  toast: Object.assign(vi.fn(), {
    success: vi.fn(),
    error: vi.fn(),
    warning: vi.fn(),
  }),
}))
vi.mock('@/hooks/useAskUser', () => ({
  useAskUser: () => mocks.askUser,
}))
vi.mock('@/hooks/useChatMessages', () => ({
  useChatMessages: (options: { onSendError?: (content: string, error: unknown) => void }) => mocks.useChatMessages(options),
}))
vi.mock('@/hooks/useCollapseLevel', () => ({
  useCollapseLevel: () => ({ level: 'all' }),
  useMergeTools: () => ({ mergeTools: false }),
}))
vi.mock('@/hooks/useProgressStream', () => ({
  useProgressStream: (options: { onHistoryRewound?: (historyID?: number, eventSeq?: number) => void }) => {
    mocks.progressOptions.push(options)
    return {
      progressSnapshot: { todos: [], tokenUsage: null },
      liveMessage: null,
      isStreaming: false,
      resetProgress: vi.fn(),
    }
  },
}))
vi.mock('@/hooks/useTodos', () => ({ useTodos: () => ({ total: 0 }) }))
vi.mock('@/hooks/useActiveSSESubscription', () => ({
  useActiveSSESubscription: vi.fn(),
}))
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
vi.mock('@/components/agent/api', () => ({
  rewindHistory: (...args: unknown[]) => mocks.rewindHistory(...args),
}))
vi.mock('@/components/agent/AskUserPanel', () => ({
  AskUserPanel: (props: {
    disabled?: boolean
    onRespond: (answers: Record<string, string>) => void
    onCancel: () => void
  }) => {
    mocks.askUserPanelProps.push(props)
    return (
      <>
        <button type="button" aria-label="ask-respond" disabled={props.disabled} onClick={() => props.onRespond({ 0: 'yes' })}>
          respond
        </button>
        <button type="button" aria-label="ask-cancel" disabled={props.disabled} onClick={props.onCancel}>
          cancel
        </button>
      </>
    )
  },
}))
vi.mock('@/components/agent/ContextRing', () => ({ ContextRing: () => null }))
vi.mock('@/components/agent/MessageInput', () => ({
  MessageInput: (props: { onSend: (content: string) => void; draft?: string; busy?: boolean; disabled?: boolean }) => (
    <>
      <button type="button" aria-label="composer-send" onClick={() => props.onSend('ordinary continuation')}>
        send
      </button>
      {props.draft ? <span>{`draft:${props.draft}`}</span> : null}
      <span data-testid="composer-busy">{String(props.busy)}</span>
      <span data-testid="composer-disabled">{String(props.disabled)}</span>
    </>
  ),
}))
vi.mock('@/components/agent/ModelSelector', () => ({
  ModelSelector: () => null,
}))
vi.mock('@/components/agent/MessageList', () => ({
  MessageList: (props: {
    editingMessageId?: string | null
    footer?: import('react').ReactNode
    onStartEdit?: (id: string) => void
    onRewind?: (content: string, message: unknown) => void
  }) => (
    <>
      <button
        type="button"
        onClick={() => {
          props.onStartEdit?.('target')
          props.onRewind?.('edited message', {
            id: 'target',
            historyID: 42,
            recordType: 'message',
            role: 'user',
            content: 'original message',
            timestamp: '2026-07-08T00:00:01Z',
            persisted: true,
          })
        }}
      >
        rewind
      </button>
      {props.editingMessageId ? <span>{`editing:${props.editingMessageId}`}</span> : null}
      {props.footer}
    </>
  ),
}))
vi.mock('@/workspace/types', () => ({
  useDockviewContext: () => mocks.context,
}))
import { AgentPanel } from './AgentPanel'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: Error) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, reject, resolve }
}

describe('AgentPanel rewind', () => {
  beforeEach(() => {
    mocks.order.length = 0
    mocks.progressOptions.length = 0
    mocks.chatOptions.length = 0
    mocks.askUserPanelProps.length = 0
    mocks.sessionHandlers.length = 0
    mocks.rewindHistory.mockReset()
    mocks.rewindHistory.mockResolvedValue({
      history_rewound: true,
      files_rewound: true,
    })
    mocks.chat.clearMessages.mockClear()
    mocks.chat.reload.mockClear()
    mocks.chat.reload.mockImplementation(async () => {
      mocks.order.push('reload')
      return true
    })
    mocks.chat.sendMessage.mockClear()
    mocks.askUser.prompt = null
    mocks.askUser.respond.mockClear()
    mocks.askUser.cancel.mockClear()
    mocks.chat.processing = false
    mocks.useChatMessages.mockClear()
    mocks.context.ws.onSession.mockClear()
    mocks.context.sessionStore.activeSession = { channel: 'web', chatID: 'chat-1' }
    mocks.context.sessionStore.clearAskUserPrompt.mockClear()
  })

  it('clears and reloads before resending the edited message', async () => {
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)

    fireEvent.click(screen.getByRole('button', { name: 'rewind' }))

    await waitFor(() => expect(mocks.chat.sendMessage).toHaveBeenCalledWith('edited message', undefined))
    expect(mocks.rewindHistory).toHaveBeenCalledWith({ channel: 'web', chatID: 'chat-1' }, 42)
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
    expect(await screen.findByText('draft:edited message')).toBeInTheDocument()
  })

  it('honors a rewind barrier while the local REST response is still pending', async () => {
    const pending = deferred<never>()
    mocks.rewindHistory.mockReturnValueOnce(pending.promise)
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)

    fireEvent.click(screen.getByRole('button', { name: 'rewind' }))
    await waitFor(() => expect(mocks.rewindHistory).toHaveBeenCalled())
    expect(screen.getByTestId('composer-disabled')).toHaveTextContent('true')
    fireEvent.click(screen.getByRole('button', { name: 'composer-send' }))
    expect(mocks.chat.sendMessage).not.toHaveBeenCalled()
    const onHistoryRewound = mocks.progressOptions.at(-1)?.onHistoryRewound
    expect(onHistoryRewound).toBeTypeOf('function')

    act(() => onHistoryRewound?.(42))
    await waitFor(() => expect(mocks.chat.reload).toHaveBeenCalledTimes(1))
    expect(mocks.order).toEqual(['clear', 'reload'])

    await act(async () => {
      pending.reject(new Error('response lost'))
      await pending.promise.catch(() => undefined)
    })
    expect(mocks.chat.sendMessage).not.toHaveBeenCalled()
    expect(await screen.findByText('draft:edited message')).toBeInTheDocument()
    expect(screen.getByTestId('composer-disabled')).toHaveTextContent('false')
  })

  it('keeps the local rewind barrier locked when REST fails during the reset reload', async () => {
    const pendingResponse = deferred<never>()
    const pendingReload = deferred<boolean>()
    mocks.rewindHistory.mockReturnValueOnce(pendingResponse.promise)
    mocks.chat.reload.mockImplementationOnce(() => {
      mocks.order.push('reload')
      return pendingReload.promise
    })
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)

    fireEvent.click(screen.getByRole('button', { name: 'rewind' }))
    await waitFor(() => expect(mocks.rewindHistory).toHaveBeenCalled())
    const onHistoryRewound = mocks.progressOptions.at(-1)?.onHistoryRewound
    act(() => onHistoryRewound?.(42, 10))
    await waitFor(() => expect(mocks.chat.reload).toHaveBeenCalledTimes(1))

    await act(async () => {
      pendingResponse.reject(new Error('response lost'))
      await pendingResponse.promise.catch(() => undefined)
      await Promise.resolve()
    })
    expect(screen.getByTestId('composer-disabled')).toHaveTextContent('true')

    await act(async () => {
      pendingReload.resolve(true)
      await pendingReload.promise
    })
    await waitFor(() => expect(screen.getByTestId('composer-disabled')).toHaveTextContent('false'))
    expect(mocks.chat.sendMessage).not.toHaveBeenCalled()
  })

  it('treats a different rewind target during a local request as external', async () => {
    const pendingResponse = deferred<never>()
    const pendingReload = deferred<boolean>()
    mocks.rewindHistory.mockReturnValueOnce(pendingResponse.promise)
    mocks.chat.reload.mockImplementationOnce(() => pendingReload.promise)
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)

    fireEvent.click(screen.getByRole('button', { name: 'rewind' }))
    await waitFor(() => expect(mocks.rewindHistory).toHaveBeenCalled())
    const onHistoryRewound = mocks.progressOptions.at(-1)?.onHistoryRewound
    act(() => onHistoryRewound?.(43, 11))
    await waitFor(() => expect(mocks.chat.reload).toHaveBeenCalledTimes(1))

    await act(async () => {
      pendingResponse.reject(new Error('response lost'))
      await pendingResponse.promise.catch(() => undefined)
      await Promise.resolve()
    })
    expect(screen.getByTestId('composer-disabled')).toHaveTextContent('true')

    await act(async () => {
      pendingReload.resolve(true)
      await pendingReload.promise
    })
    await waitFor(() => expect(screen.getByTestId('composer-disabled')).toHaveTextContent('false'))
  })

  it('reuses a pending post-REST reload when the matching rewind barrier arrives', async () => {
    const pendingReload = deferred<boolean>()
    mocks.chat.reload.mockImplementationOnce(() => {
      mocks.order.push('reload')
      return pendingReload.promise
    })
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)

    fireEvent.click(screen.getByRole('button', { name: 'rewind' }))
    await waitFor(() => expect(mocks.chat.reload).toHaveBeenCalledTimes(1))
    const onHistoryRewound = mocks.progressOptions.at(-1)?.onHistoryRewound
    expect(onHistoryRewound).toBeTypeOf('function')

    act(() => onHistoryRewound?.(42))
    expect(mocks.chat.clearMessages).toHaveBeenCalledTimes(1)
    expect(mocks.chat.reload).toHaveBeenCalledTimes(1)
    expect(mocks.chat.sendMessage).not.toHaveBeenCalled()

    await act(async () => {
      pendingReload.resolve(true)
      await pendingReload.promise
    })
    await waitFor(() => expect(mocks.chat.sendMessage).toHaveBeenCalledWith('edited message', undefined))
    expect(mocks.order).toEqual(['clear', 'reload', 'send'])
  })

  it('acknowledges a late matching rewind event without reloading after resend', async () => {
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)

    fireEvent.click(screen.getByRole('button', { name: 'rewind' }))
    await waitFor(() => expect(mocks.chat.sendMessage).toHaveBeenCalledWith('edited message', undefined))
    expect(mocks.chat.reload).toHaveBeenCalledTimes(1)
    const onHistoryRewound = mocks.progressOptions.at(-1)?.onHistoryRewound

    act(() => onHistoryRewound?.(42, 10))

    expect(mocks.chat.clearMessages).toHaveBeenCalledTimes(1)
    expect(mocks.chat.reload).toHaveBeenCalledTimes(1)
    expect(mocks.chat.sendMessage).toHaveBeenCalledTimes(1)
    expect(screen.getByTestId('composer-disabled')).toHaveTextContent('false')
  })

  it('keeps external rewind controls locked until its history reload settles', async () => {
    const pendingReload = deferred<boolean>()
    mocks.chat.reload.mockImplementationOnce(() => {
      mocks.order.push('reload')
      return pendingReload.promise
    })
    mocks.askUser.prompt = { requestId: 'ask-1', questions: [{ question: 'Continue?' }] }
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)
    const onHistoryRewound = mocks.progressOptions.at(-1)?.onHistoryRewound

    act(() => onHistoryRewound?.(42, 10))

    expect(screen.getByTestId('composer-disabled')).toHaveTextContent('true')
    expect(screen.getByRole('button', { name: 'ask-respond' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'ask-cancel' })).toBeDisabled()
    await act(async () => {
      pendingReload.resolve(true)
      await pendingReload.promise
    })
    await waitFor(() => expect(screen.getByTestId('composer-disabled')).toHaveTextContent('false'))
  })

  it('rechecks the rewind barrier inside AskUser response callbacks', async () => {
    const pending = deferred<never>()
    mocks.rewindHistory.mockReturnValueOnce(pending.promise)
    mocks.askUser.prompt = { requestId: 'ask-1', questions: [{ question: 'Continue?' }] }
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)

    fireEvent.click(screen.getByRole('button', { name: 'rewind' }))
    await waitFor(() => expect(mocks.rewindHistory).toHaveBeenCalled())
    const askUserProps = mocks.askUserPanelProps.at(-1)
    act(() => {
      askUserProps?.onRespond({ 0: 'yes' })
      askUserProps?.onCancel()
    })

    expect(mocks.askUser.respond).not.toHaveBeenCalled()
    expect(mocks.askUser.cancel).not.toHaveBeenCalled()
    await act(async () => {
      pending.reject(new Error('response lost'))
      await pending.promise.catch(() => undefined)
    })
  })

  it('starts a fresh reload when the same history ID is explicitly rewound again', async () => {
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)
    const rewind = screen.getByRole('button', { name: 'rewind' })

    fireEvent.click(rewind)
    await waitFor(() => expect(mocks.chat.sendMessage).toHaveBeenCalledTimes(1))

    fireEvent.click(rewind)
    await waitFor(() => expect(mocks.chat.sendMessage).toHaveBeenCalledTimes(2))

    expect(mocks.rewindHistory).toHaveBeenCalledTimes(2)
    expect(mocks.chat.clearMessages).toHaveBeenCalledTimes(2)
    expect(mocks.chat.reload).toHaveBeenCalledTimes(2)
    expect(mocks.order).toEqual(['clear', 'reload', 'send', 'clear', 'reload', 'send'])
  })

  it('starts a fresh reload for a later external rewind to the same history ID', async () => {
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)
    const onHistoryRewound = mocks.progressOptions.at(-1)?.onHistoryRewound

    act(() => onHistoryRewound?.(42, 10))
    await waitFor(() => expect(mocks.chat.reload).toHaveBeenCalledTimes(1))
    act(() => onHistoryRewound?.(42, 11))
    await waitFor(() => expect(mocks.chat.reload).toHaveBeenCalledTimes(2))
  })

  it('accepts a lower rewind sequence after the server sequence restarts', async () => {
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)
    const onHistoryRewound = mocks.progressOptions.at(-1)?.onHistoryRewound

    act(() => onHistoryRewound?.(42, 11))
    await waitFor(() => expect(mocks.chat.reload).toHaveBeenCalledTimes(1))
    act(() => onHistoryRewound?.(42, 1))
    await waitFor(() => expect(mocks.chat.reload).toHaveBeenCalledTimes(2))
  })

  it('supersedes a pending reload when a different rewind target arrives', async () => {
    const firstReload = deferred<boolean>()
    const secondReload = deferred<boolean>()
    mocks.chat.reload.mockImplementationOnce(() => {
      mocks.order.push('reload-42')
      return firstReload.promise
    })
    mocks.chat.reload.mockImplementationOnce(() => {
      mocks.order.push('reload-43')
      return secondReload.promise
    })
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)
    const onHistoryRewound = mocks.progressOptions.at(-1)?.onHistoryRewound

    act(() => onHistoryRewound?.(42, 10))
    await waitFor(() => expect(mocks.chat.reload).toHaveBeenCalledTimes(1))
    act(() => onHistoryRewound?.(43, 11))
    await waitFor(() => expect(mocks.chat.reload).toHaveBeenCalledTimes(2))

    await act(async () => {
      firstReload.resolve(true)
      await firstReload.promise
    })
    expect(screen.getByTestId('composer-disabled')).toHaveTextContent('true')
    await act(async () => {
      secondReload.resolve(true)
      await secondReload.promise
    })
    await waitFor(() => expect(screen.getByTestId('composer-disabled')).toHaveTextContent('false'))
    expect(mocks.chat.clearMessages).toHaveBeenCalledTimes(2)
  })

  it('does not reload or resend when the server reports no history change', async () => {
    mocks.rewindHistory.mockResolvedValueOnce({ history_rewound: false })
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)

    fireEvent.click(screen.getByRole('button', { name: 'rewind' }))

    await waitFor(() => expect(mocks.rewindHistory).toHaveBeenCalled())
    expect(mocks.chat.clearMessages).not.toHaveBeenCalled()
    expect(mocks.chat.reload).not.toHaveBeenCalled()
    expect(mocks.chat.sendMessage).not.toHaveBeenCalled()
  })

  it('does not resend when the post-rewind history reload fails', async () => {
    mocks.chat.reload.mockImplementationOnce(async () => {
      mocks.order.push('reload')
      return false
    })
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)

    fireEvent.click(screen.getByRole('button', { name: 'rewind' }))

    await waitFor(() => expect(mocks.chat.reload).toHaveBeenCalled())
    expect(mocks.order).toEqual(['clear', 'reload'])
    expect(mocks.chat.sendMessage).not.toHaveBeenCalled()
    expect(await screen.findByText('draft:edited message')).toBeInTheDocument()
  })

  it('still resends when only the file checkpoint rollback fails', async () => {
    mocks.rewindHistory.mockResolvedValueOnce({
      history_rewound: true,
      files_rewound: false,
      checkpoint_error: 'checkpoint unavailable',
    })
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)

    fireEvent.click(screen.getByRole('button', { name: 'rewind' }))

    await waitFor(() => expect(mocks.chat.sendMessage).toHaveBeenCalledWith('edited message', undefined))
    expect(mocks.order).toEqual(['clear', 'reload', 'send'])
  })

  it('restores the main-session draft when automatic resend fails', async () => {
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)
    fireEvent.click(screen.getByRole('button', { name: 'rewind' }))
    await waitFor(() => expect(mocks.chat.sendMessage).toHaveBeenCalledWith('edited message', undefined))

    act(() => {
      mocks.chatOptions.at(-1)?.onSendError?.('edited message', new Error('send failed'))
    })

    expect(await screen.findByText('draft:edited message')).toBeInTheDocument()
  })

  it('restores the SubAgent draft when automatic resend fails', async () => {
    render(<AgentPanel params={{ agentChatID: 'web:parent-chat/review:1' } as never} api={{} as never} containerApi={{} as never} />)
    fireEvent.click(screen.getByRole('button', { name: 'rewind' }))
    await waitFor(() => expect(mocks.chat.sendMessage).toHaveBeenCalledWith('edited message', undefined))

    act(() => {
      mocks.chatOptions.at(-1)?.onSendError?.('edited message', new Error('send failed'))
    })

    expect(await screen.findByText('draft:edited message')).toBeInTheDocument()
  })

  it('treats an authoritative processing-only history snapshot as busy', () => {
    mocks.chat.processing = true
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)

    expect(screen.getByTestId('composer-busy')).toHaveTextContent('true')
  })

  it('does not clear or resend into a session selected while rewind is pending', async () => {
    const pending = deferred<{ history_rewound: boolean; files_rewound: boolean }>()
    mocks.rewindHistory.mockReturnValueOnce(pending.promise)
    const panel = render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)
    fireEvent.click(screen.getByRole('button', { name: 'rewind' }))
    await waitFor(() => expect(mocks.rewindHistory).toHaveBeenCalled())

    mocks.context.sessionStore.activeSession = { channel: 'web', chatID: 'chat-2' }
    panel.rerender(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)
    await act(async () => {
      pending.resolve({ history_rewound: true, files_rewound: true })
      await pending.promise
    })

    expect(mocks.chat.clearMessages).not.toHaveBeenCalled()
    expect(mocks.chat.reload).not.toHaveBeenCalled()
    expect(mocks.chat.sendMessage).not.toHaveBeenCalled()
    expect(screen.queryByText('draft:edited message')).not.toBeInTheDocument()

    mocks.context.sessionStore.activeSession = { channel: 'web', chatID: 'chat-1' }
    panel.rerender(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)
    expect(await screen.findByText('draft:edited message')).toBeInTheDocument()
  })

  it('does not mutate or resend after the panel unmounts during rewind', async () => {
    const pending = deferred<{ history_rewound: boolean; files_rewound: boolean }>()
    mocks.rewindHistory.mockReturnValueOnce(pending.promise)
    const panel = render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)
    fireEvent.click(screen.getByRole('button', { name: 'rewind' }))
    await waitFor(() => expect(mocks.rewindHistory).toHaveBeenCalled())

    panel.unmount()
    await act(async () => {
      pending.resolve({ history_rewound: true, files_rewound: true })
      await pending.promise
    })

    expect(mocks.chat.clearMessages).not.toHaveBeenCalled()
    expect(mocks.chat.reload).not.toHaveBeenCalled()
    expect(mocks.chat.sendMessage).not.toHaveBeenCalled()
  })

  it('loads and rewinds a live SubAgent through its canonical Agent session', async () => {
    render(
      <AgentPanel
        params={
          {
            subAgentRole: 'review',
            subAgentInstance: '1',
            parentChannel: 'web',
            parentChatID: 'parent-chat',
          } as never
        }
        api={{} as never}
        containerApi={{} as never}
      />,
    )

    expect(mocks.useChatMessages).toHaveBeenCalledWith(
      expect.objectContaining({
        channel: 'agent',
        chatID: 'web:parent-chat/review:1',
      }),
    )
    fireEvent.click(screen.getByRole('button', { name: 'rewind' }))

    await waitFor(() => expect(mocks.chat.sendMessage).toHaveBeenCalledWith('edited message', undefined))
    expect(mocks.rewindHistory).toHaveBeenCalledWith({ channel: 'agent', chatID: 'web:parent-chat/review:1' }, 42)
    expect(mocks.chat.clearMessages).toHaveBeenCalledTimes(1)
  })

  it('shows a composer for SubAgents and delegates ordinary sends to their canonical chat hook', () => {
    render(<AgentPanel params={{ agentChatID: 'web:parent-chat/review:1' } as never} api={{} as never} containerApi={{} as never} />)

    fireEvent.click(screen.getByRole('button', { name: 'composer-send' }))

    expect(mocks.chat.sendMessage).toHaveBeenCalledWith('ordinary continuation', undefined)
  })

  it('moves a SubAgent edit into the composer when history reload fails', async () => {
    mocks.chat.reload.mockResolvedValueOnce(false)
    render(<AgentPanel params={{ agentChatID: 'web:parent-chat/review:1' } as never} api={{} as never} containerApi={{} as never} />)

    fireEvent.click(screen.getByRole('button', { name: 'rewind' }))

    await waitFor(() => expect(mocks.chat.reload).toHaveBeenCalled())
    expect(mocks.chat.clearMessages).toHaveBeenCalledTimes(1)
    expect(mocks.chat.sendMessage).not.toHaveBeenCalled()
    expect(await screen.findByText('draft:edited message')).toBeInTheDocument()
    expect(screen.queryByText('editing:target')).not.toBeInTheDocument()
  })

  it('moves a SubAgent edit into the composer when rewind REST fails', async () => {
    mocks.rewindHistory.mockRejectedValueOnce(new Error('rewind failed'))
    render(<AgentPanel params={{ agentChatID: 'web:parent-chat/review:1' } as never} api={{} as never} containerApi={{} as never} />)

    fireEvent.click(screen.getByRole('button', { name: 'rewind' }))

    expect(await screen.findByText('draft:edited message')).toBeInTheDocument()
    expect(screen.queryByText('editing:target')).not.toBeInTheDocument()
    expect(mocks.chat.sendMessage).not.toHaveBeenCalled()
  })

  it('reloads a SubAgent only for its exact lifecycle session key', async () => {
    render(
      <AgentPanel
        params={
          {
            subAgentRole: 'review',
            subAgentInstance: '1',
            parentChannel: 'web',
            parentChatID: 'shared-parent',
          } as never
        }
        api={{} as never}
        containerApi={{} as never}
      />,
    )
    const handler = mocks.sessionHandlers.at(-1)
    expect(handler).toBeTypeOf('function')

    act(() => {
      handler?.({
        action: 'subagent_started',
        channel: 'cli',
        chat_id: 'shared-parent',
        session_key: 'cli:shared-parent/review:1',
        role: 'review',
        instance: '1',
        parent_id: 'shared-parent',
      })
    })
    expect(mocks.chat.reload).not.toHaveBeenCalled()

    act(() => {
      handler?.({
        action: 'subagent_stopped',
        channel: 'web',
        chat_id: 'shared-parent',
        session_key: 'web:shared-parent/review:1',
        role: 'review',
        instance: '1',
        parent_id: 'shared-parent',
      })
    })
    expect(mocks.chat.reload).toHaveBeenCalledTimes(1)
  })
})
