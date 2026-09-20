import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'

const mocks = vi.hoisted(() => {
  const order: string[] = []
  const chat = {
    messages: [] as Array<{ id: string; role: string; content: string; isPartial?: boolean; turnID?: number }>,
    loading: false,
    historyReady: true,
    markHistoryStale: vi.fn(() => order.push('markHistoryStale')),
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
    ws: { connected: true, onSession: vi.fn(() => vi.fn()) },
    sessionStore: {
      activeSession: { channel: 'web', chatID: 'chat-1' },
      sessions: [],
      // AgentPanel 的 AskUser DB 权威水合 effect 调它（会话加载 / tab 重新可见）。
      hydrateAskUserPrompt: vi.fn(),
    },
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
  return {
    chat,
    context,
    order,
    progress,
    rewindHistory: vi.fn(),
    fetchHistory: vi.fn(),
    // get_pending_ask_user 水合（AgentPanel 的 DB 权威 AskUser 水合 effect）。
    // 这里是**全量模块 mock**：生产代码 import 的每个符号都必须导出，否则 effect
    // 在被动挂载期间访问该绑定即抛
    // `No "getPendingAskUser" export is defined on the "@/components/agent/api" mock`
    // （vitest 的 mock 命名空间对未知导出直接抛错，不是返回 undefined）。
    getPendingAskUser: vi.fn(),
    lastChatID: null as string | null,
  }
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
  getPendingAskUser: (...args: unknown[]) => mocks.getPendingAskUser(...args),
}))
vi.mock('@/components/agent/AskUserPanel', () => ({ AskUserPanel: () => null }))
vi.mock('@/components/agent/ContextRing', () => ({ ContextRing: () => null }))
vi.mock('@/components/agent/MessageInput', () => ({
  MessageInput: () => <div data-testid="agent-composer" />,
}))
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

// `get_pending_ask_user` 水合（AgentPanel 的 DB 权威 AskUser 水合 effect）在**每个用例
// 渲染时都会各发一次**（chatID/messageChannel/isVisible 就绪即触发）。默认给"当前无
// pending 提问"⇒ 水合是 no-op，既有用例（rewind/busy/live/归属/断线）的断言不被副作用
// 污染；需要验证水合的用例在自身 it 里覆盖实现。
beforeEach(() => {
  mocks.getPendingAskUser.mockReset()
  mocks.getPendingAskUser.mockResolvedValue(null)
  mocks.context.sessionStore.hydrateAskUserPrompt.mockClear()
})

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

describe('\u4e0d\u53ef\u89c1\u9762\u677f\u4e0d\u5f97\u5728 DOM \u91cc\u4fdd\u7559 chrome\uff082026-09-19 P0\uff1a\u5207\u56de\u300c\u5df2\u6253\u5f00\u8fc7\u300d\u7684 tab \u65f6\u8f93\u5165\u6846\u6d6e\u5728\u6d88\u606f\u533a\u4e0a\u65b9\u4e00\u95ea\uff09', () => {
  it('\u9762\u677f\u4e0d\u53ef\u89c1\u65f6\u4e0d\u5f97\u6e32\u67d3\u8f93\u5165\u6846\uff08\u9648\u65e7 DOM \u662f\u6fc0\u6d3b\u90a3\u4e00\u5e27\u88ab\u753b\u51fa\u6765\u7684\u552f\u4e00\u6765\u6e90\uff09', async () => {
    // \u7528\u6237\u590d\u73b0\u6761\u4ef6\uff08\u51b3\u5b9a\u6027\uff09\uff1a\u53ea\u6709\u7535\u8111\u7aef\u3001\u4e14\u76ee\u6807 tab **\u4e4b\u524d\u5df2\u7ecf\u6253\u5f00\u8fc7**\u65f6\u51fa\u73b0\uff1b
    // \u5148\u628a\u8be5 tab \u4ece tab \u680f x \u6389\u518d\u5207\u5c31\u6ca1\u6709\u3002\u21d2 \u5df2\u6253\u5f00 = \u9762\u677f\u65e9\u5df2\u6302\u8f7d\uff08renderer='always'
    // \u5e38\u9a7b DOM\uff09\u3002dockview \u5bf9\u975e\u6fc0\u6d3b\u9762\u677f\u7f6e visibility:hidden\uff0c\u6fc0\u6d3b\u90a3\u4e00\u5e27\u5728\u5b83\u81ea\u5df1\u7684 rAF \u91cc
    // \u6e05\u6389 hidden\uff0c\u800c React \u7684 isVisible \u66f4\u65b0\uff08+ markHistoryStale \u21d2 loading \u5c4f\uff09\u843d\u5728\u66f4\u665a\u7684\u63d0\u4ea4
    // \u21d2 \u6d4f\u89c8\u5668\u5148\u753b**\u9648\u65e7 DOM**\u3002\u65e7\u4ee3\u7801\u53ea\u628a MessageList \u6309 isVisible \u9690\u85cf\uff0c
    // \u6258\u76d8/\u8f93\u5165\u6846\u4ecd\u5728 DOM \u21d2 \u753b\u51fa\u300c\u7a7a\u6d88\u606f\u533a + \u6258\u76d8 + \u8f93\u5165\u6846\u300d\uff08\u8f93\u5165\u6846\u81ea\u7136\u9ad8\u5ea6\u3001
    // \u8d34\u5728\u9762\u677f\u9876\u90e8\uff09= \u7528\u6237\u622a\u56fe\u90a3\u6392\u6d6e\u7740\u7684\u63a7\u4ef6\u3002
    // \u5951\u7ea6\uff1a\u4e0d\u53ef\u89c1 \u21d2 chrome\uff08\u6258\u76d8/\u8f93\u5165\u6846\uff09\u4e5f\u5fc5\u987b\u4e0d\u6e32\u67d3\uff08\u4e0e MessageList \u540c\u4e00\u6761\u89c4\u5219\uff09\u3002
    const cbs: Array<(e: { isVisible: boolean }) => void> = []
    const api = {
      isVisible: true,
      onDidVisibilityChange: (fn: (e: { isVisible: boolean }) => void) => {
        cbs.push(fn)
        return { dispose: () => {} }
      },
    }
    const { container } = render(
      <AgentPanel params={{} as never} api={api as never} containerApi={{} as never} />,
    )
    await waitFor(() => expect(cbs.length).toBeGreaterThan(0))
    // 可见时确实渲染（自证 mock 生效、断言有判别力）
    expect(container.querySelector('[data-testid="agent-composer"]')).not.toBeNull()

    act(() => cbs[0]({ isVisible: false }))

    // 不可见 ⇒ 输入框必须从 DOM 移除（陈旧 DOM 是激活那一帧被画出来的唯一来源）
    expect(container.querySelector('[data-testid="agent-composer"]')).toBeNull()
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

describe('断线（重连中）不再显示黄色 Reconnecting 条，改走 loading splash', () => {
  // 2026-09-17 用户要求：「把黄色的 reconnecting… 去掉，以后这个期间直接显示 loading 的
  // splash screen」。
  // ⛔ 但**只对"曾经连上过再掉线"的真·重连生效** —— 从未连上（初次加载 / 无 SSE 的 mock
  // 场景）绝不能遮罩，否则会把已渲染的历史一起藏起来（CI E2E 实测：一刀切会让 8 个
  // 非 SSE 的 spec 找不到内容）。
  it('从未连上过（connected=false 首帧）⇒ 不遮罩，照常渲染消息列表', () => {
    mocks.context.ws.connected = false
    try {
      render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)
      expect(screen.queryByTestId('session-loading-screen')).toBeNull()
    } finally {
      mocks.context.ws.connected = true
    }
  })

  it('连上过再掉线（真·重连）⇒ 渲染 session-loading-screen，且没有任何 Reconnecting 文案', () => {
    mocks.context.ws.connected = true
    const { rerender } = render(
      <AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />,
    )
    expect(screen.queryByTestId('session-loading-screen')).toBeNull()
    mocks.context.ws.connected = false // 掉线（重连中）
    rerender(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)
    expect(screen.getByTestId('session-loading-screen')).toBeInTheDocument()
    // 黄条已删除（三语文案都不应出现）
    expect(screen.queryByText(/Reconnecting|重新连接中|再接続中/)).toBeNull()
    mocks.context.ws.connected = true
  })
})

/**
 * AskUser 面板的 DB 权威水合（`get_pending_ask_user`）。
 *
 * 面板过去唯一的载体是实时 `ask_user` 事件：会话在提问时刻没有 SSE 订阅（用户正在看
 * 别的会话 / 事件被 replay ring 淘汰 / 信封 key 推导失败）⇒ 没有任何路径重新推导
 * pending 状态 ⇒ 面板永不渲染、turn 永远"思考中"（2026-09-20 事故）。
 * 契约（本组用例钉死）：会话加载 / tab 重新可见时经 `get_pending_ask_user` 水合状态机
 * —— 该 RPC 走服务端持久化的 ask_question/ask_answer 记录，是 DB 单一权威。
 *
 * 判别力：删掉 AgentPanel 的那次水合调用 ⇒ 本例的 `hydrateAskUserPrompt` 断言必红。
 */
describe('AgentPanel AskUser 水合（get_pending_ask_user，DB 权威）', () => {
  it('会话可见时用 DB 的 pending 记录水合（漏掉实时 ask_user 事件也能自愈）', async () => {
    mocks.getPendingAskUser.mockResolvedValue({
      request_id: 'req-7',
      questions: [{ question: 'proceed?', options: ['yes', 'no'], multi_select: true, allow_other: true }],
    })
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)

    await waitFor(() =>
      expect(mocks.getPendingAskUser).toHaveBeenCalledWith({ channel: 'web', chatID: 'chat-1' }),
    )
    await waitFor(() =>
      expect(mocks.context.sessionStore.hydrateAskUserPrompt).toHaveBeenCalledWith('web', 'chat-1', {
        // 与实时 ask_user 事件共用同一个 parseAskUserPrompt（snake_case → camelCase）。
        requestId: 'req-7',
        questions: [{ question: 'proceed?', options: ['yes', 'no'], multiSelect: true, allowOther: true }],
      }),
    )
  })

  it('DB 无 pending 记录时不水合（绝不伪造面板）', async () => {
    mocks.getPendingAskUser.mockResolvedValue(null)
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)

    await waitFor(() => expect(mocks.getPendingAskUser).toHaveBeenCalled())
    expect(mocks.context.sessionStore.hydrateAskUserPrompt).not.toHaveBeenCalled()
  })

  it('载荷不含任何问题（通用 RPC mock 的 {ok:true}）时不水合 —— 空 prompt 会崩掉整块面板', async () => {
    // 判别力：这不是"额外的防御"——它是 2026-09-20 CI 9 个 spec 全红的根因。
    // 通用 `/api/rpc` 通配路由 mock 对任何方法都回 {ok:true, data:{ok:true}}，
    // 过去它被合成为 questions: [] 的 prompt ⇒ AskUserPanel 读 questions[0].allowOther
    // 抛异常 ⇒ 崩溃边界替换整块面板（goal banner / todo 面板全消失）。
    mocks.getPendingAskUser.mockResolvedValue({ ok: true })
    render(<AgentPanel params={{} as never} api={{} as never} containerApi={{} as never} />)

    await waitFor(() => expect(mocks.getPendingAskUser).toHaveBeenCalled())
    expect(mocks.context.sessionStore.hydrateAskUserPrompt).not.toHaveBeenCalled()
  })
})
