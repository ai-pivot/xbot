import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
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
  return { chat, context, order, progress, rewindHistory: vi.fn(), fetchHistory: vi.fn(), lastChatID: null as string | null }
})

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock('@/hooks/useAskUser', () => ({ useAskUser: () => ({ prompt: null, respond: vi.fn(), cancel: vi.fn() }) }))
vi.mock('@/hooks/useChatMessages', () => ({
  // 捕获 chatID —— 会话归属不变量（一个会话至多被一个 agent 面板渲染）的断言点。
  useChatMessages: (opts: { chatID?: string | null }) => {
    mocks.lastChatID = opts?.chatID ?? null
    return mocks.chat
  },
}))
vi.mock('@/chat/useAgentChatState', () => ({
  // M4：新状态机 hook 的测试替身 —— messages/liveProgress 直通 mocks
  //（与旧 useProgressStream mock 同语义：busy 测试改 progressSnapshot，
  // live 可见性测试改 chat.messages 的 isPartial 行）。
  useAgentChatState: () => ({
    messages: mocks.chat.messages,
    liveProgress: mocks.progress.progressSnapshot,
    busyFallback: false,
    tokenPrompt: null,
    reset: vi.fn(),
    sendUser: vi.fn(),
    ackUser: vi.fn(),
    failUser: vi.fn(),
  }),
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
  // LLM-config change bus: AgentPanel subscribes to re-resolve the session's
  // model/limits when the settings dialog mutates subscriptions/models.
  subscribeLLMConfigChanged: () => () => {},
}))
vi.mock('@/components/agent/api', () => ({
  rewindHistory: (...args: unknown[]) => mocks.rewindHistory(...args),
  fetchHistory: (...args: unknown[]) => mocks.fetchHistory(...args),
  setGoal: vi.fn().mockResolvedValue(undefined),
  clearGoal: vi.fn().mockResolvedValue(undefined),
  getGoal: vi.fn().mockResolvedValue(null),
}))
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
  }) => {
    // 方案 A：live 行在 messages 里（isPartial）——模拟真实 MessageList 的
    // liveId 检测（liveMessage prop 恒 null）
    const hasLive = (props.messages ?? []).some((m) => (m as { isPartial?: boolean }).isPartial)
    return (
    <div>
      <div data-testid="message-list-busy">{String(props.busy ?? false)}</div>
      <div data-testid="message-list-live">{hasLive ? 'live-visible' : 'live-hidden'}</div>
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
      <button
        type="button"
        onClick={() => props.onRewind?.('edited echo', {
          id: 'echo-1',
          role: 'user',
          content: 'echo message',
          timestamp: '2026-07-08T00:00:02Z',
          turnID: 7,
          persisted: true,
        })}
      >
        rewind-echo
      </button>
    </div>
    )
  },
}))
vi.mock('@/workspace/types', () => ({ useDockviewContext: () => mocks.context }))
vi.mock('@/providers/i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

import { AgentPanel } from './AgentPanel'

describe('AgentPanel rewind', () => {
  beforeEach(() => {
    mocks.order.length = 0
    mocks.rewindHistory.mockReset()
    mocks.rewindHistory.mockResolvedValue({})
    mocks.fetchHistory.mockReset()
    mocks.fetchHistory.mockResolvedValue({ messages: [] })
    mocks.chat.clearMessages.mockClear()
    mocks.chat.reload.mockClear()
    mocks.chat.sendMessage.mockClear()
  })

  it('clears and reloads before resending the edited message', async () => {
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)

    fireEvent.click(screen.getByRole('button', { name: 'rewind' }))

    await waitFor(() => expect(mocks.chat.sendMessage).toHaveBeenCalledWith('edited message', undefined, expect.any(String), undefined))
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

  it('resolves missing dbID via fetchHistory when message has no dbID (echo row)', async () => {
    mocks.fetchHistory.mockResolvedValue({
      messages: [{ id: 99, role: 'user', content: 'echo message', turn_id: 7, timestamp: '2026-07-08T00:00:02Z' }],
    })
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)

    fireEvent.click(screen.getByRole('button', { name: 'rewind-echo' }))

    await waitFor(() => expect(mocks.fetchHistory).toHaveBeenCalled())
    await waitFor(() => expect(mocks.rewindHistory).toHaveBeenCalledWith(
      { channel: 'web', chatID: 'chat-1' },
      99,
    ))
    expect(mocks.order).toEqual(['clear', 'reload', 'send'])
  })

  it('shows rewindUnavailable toast when fetchHistory returns no matching message', async () => {
    mocks.fetchHistory.mockResolvedValue({ messages: [] })
    const { toast } = await import('sonner')
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)

    fireEvent.click(screen.getByRole('button', { name: 'rewind-echo' }))

    await waitFor(() => expect(mocks.fetchHistory).toHaveBeenCalled())
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith('agent.rewindUnavailable'))
    expect(mocks.rewindHistory).not.toHaveBeenCalled()
    expect(mocks.chat.clearMessages).not.toHaveBeenCalled()
  })

  it('shows rewindFailed toast when fetchHistory throws a network error', async () => {
    mocks.fetchHistory.mockRejectedValueOnce(new Error('network error'))
    const { toast } = await import('sonner')
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)

    fireEvent.click(screen.getByRole('button', { name: 'rewind-echo' }))

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith('network error'))
    expect(mocks.rewindHistory).not.toHaveBeenCalled()
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

  it('suppresses busy while waiting_input — even with running=true and a live streaming snapshot', () => {
    // F3: waiting_input (AskUser pending) ⇔ NOT busy. Worst case here: the
    // local prompt was already dropped but the session status is still
    // waiting_input while the backend row / streaming snapshot says busy — the
    // input must not show the generating/stop state (the turn is PAUSED).
    const store = mocks.context.sessionStore as unknown as {
      sessions: Array<{ chatID: string; channel: string; running: boolean; status: string }>
    }
    store.sessions = [{ chatID: 'chat-1', channel: 'web', running: true, status: 'waiting_input' }]
    mocks.progress.progressSnapshot = { todos: [], tokenUsage: null, streaming: true, phase: 'thinking' }
    const first = render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)
    expect(screen.getByTestId('message-list-busy').textContent).toBe('false')
    first.unmount()

    // Control (mutation discrimination): the SAME running+streaming state
    // without waiting_input IS busy — proving the suppression is the status gate.
    store.sessions = [{ chatID: 'chat-1', channel: 'web', running: true, status: 'running' }]
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)
    expect(screen.getByTestId('message-list-busy').textContent).toBe('true')
    store.sessions = []
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
    // live store stays authoritative during reload：Step 3 后 live 行由
    // store.toRows() 保留在 messages 里（mergeHistory replace 不清进行中
    // turn 的 live），与 loading 无关。
    mocks.chat.messages = [{ id: 'turn-live', role: 'assistant', content: '', isPartial: true }]
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
    mocks.chat.messages = [{ id: 'turn-live', role: 'assistant', content: '', isPartial: true }]
    mocks.chat.loading = true
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)
    expect(screen.getByTestId('message-list-live').textContent).toBe('live-visible')
    expect(screen.getByTestId('message-list-live-progress').textContent).toBe('progress-visible')
  })

  it('shows live when not loading (normal streaming)', () => {
    mocks.chat.messages = [
      { id: 'u1', role: 'user', content: 'hi' },
      { id: 'turn-live', role: 'assistant', content: '', isPartial: true },
    ]
    mocks.chat.loading = false
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)
    expect(screen.getByTestId('message-list-live').textContent).toBe('live-visible')
  })
})

describe('AgentPanel re-subscribe reconcile（P0：通知行在不可见期间丢失后必须自愈）', () => {
  it('不可见 → 可见（重新订阅）时必须做一次历史对账', async () => {
    // 复现（2026-09-16 用户报告）：「切回缓存 tab，user 消息消失，刷新才恢复」，
    // 消失的一定是**通知变成的 user 行**（🔔 Notification）。
    // 机制：通知行的唯一载体是 turn_started(trigger='notification')（chat/reduce.ts
    // 的 notifContent 分支）；面板不可见时 SSE 主动断开（useActiveSSESubscription
    // 的 active=isVisible）⇒ 该事件丢失；而 reconcile 只在**可检测到 seq gap** 时触发
    // （resync_required → replay_gap → reloadChat），断连+游标推进不产生 gap ⇒ 该行
    // 永久缺失（只有手刷全量加载）。
    // 契约（本用例钉死）：重新可见（= 重新订阅）时**必须**触发一次历史对账。
    const cbs: Array<(e: { isVisible: boolean }) => void> = []
    const api = {
      isVisible: true,
      onDidVisibilityChange: (fn: (e: { isVisible: boolean }) => void) => {
        cbs.push(fn)
        // 必须返回 { dispose } —— AgentPanel.tsx:81 的清理调的是 disp.dispose()
        // （原先的 `() => {}` 会在清理时抛 TypeError，导致本用例假红）。
        return { dispose: () => {} }
      },
    }
    render(<AgentPanel params={{} as never} api={api as never} containerApi={{} as never} />)
    await waitFor(() => expect(cbs.length).toBeGreaterThan(0))
    mocks.chat.reload.mockClear()

    // 隐藏期间不该对账（避免无谓刷新）。
    act(() => cbs[0]({ isVisible: false }))
    expect(mocks.chat.reload).not.toHaveBeenCalled()

    // 重新可见 ⇒ 对账一次（补回断连期间丢失、且不在重放窗口里的行）。
    act(() => cbs[0]({ isVisible: true }))
    await waitFor(() => expect(mocks.chat.reload).toHaveBeenCalledTimes(1))
  })
})

/**
 * 会话归属不变量：**一个会话至多被一个 agent 面板渲染**。
 *
 * 根因（2026-09-16「切会话后同一 user 行重复渲染」，e2e + DOM 铁证）：seed 在
 * "还没有任何已知会话"时建的无 sessionId 占位 tab 用
 * `params.sessionId ?? activeSession` 解析会话（跟着 activeSession 走）；侧栏点击
 * 会话时既 `openTab(session tab)` 又 `activateSession` ⇒ 两个面板同时挂载同一会话
 *（agent tab 是 renderer='always'，常驻 DOM）⇒ 整个消息列表渲染两份（同一
 * user/assistant 行出现两次、`data-message-id` 相同）、`/api/history` 拉两次、
 * SSE 双订阅。
 *
 * 契约：占位 tab 仅在**独占** main agent 面板时才跟随 activeSession（引导态）；
 * 已有 session tab 拥有该会话时，占位 tab 不得镜像它。
 */
describe('AgentPanel 会话归属（一个会话至多被一个 agent 面板渲染）', () => {
  const placeholderParams = { type: 'agent', tabId: 't1', title: 'Agent', closable: true }

  it('已有 session tab 拥有 activeSession 时，占位 tab 不得镜像该会话', () => {
    const peer = {
      id: 'peer-panel',
      params: { type: 'agent', tabId: 't2', title: 'S1', sessionId: 'chat-1', closable: true },
    }
    const containerApi = {
      panels: [peer],
      onDidAddPanel: () => ({ dispose: () => {} }),
      onDidRemovePanel: () => ({ dispose: () => {} }),
    }
    render(
      <AgentPanel
        params={placeholderParams as never}
        api={{ id: 'self-panel' } as never}
        containerApi={containerApi as never}
      />,
    )
    expect(mocks.lastChatID).toBeNull()
  })

  it('占位 tab 独占 main agent 面板时仍跟随 activeSession（引导态保持不变）', () => {
    const containerApi = {
      panels: [{ id: 'files-panel', params: { type: 'panel', tabId: 'p1', panelId: 'files', closable: true } }],
      onDidAddPanel: () => ({ dispose: () => {} }),
      onDidRemovePanel: () => ({ dispose: () => {} }),
    }
    render(
      <AgentPanel
        params={placeholderParams as never}
        api={{ id: 'self-panel' } as never}
        containerApi={containerApi as never}
      />,
    )
    expect(mocks.lastChatID).toBe('chat-1')
  })
})
