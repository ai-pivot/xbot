/**
 * useAskUser — the panel must be driven by the SERVER-synced store, never by a
 * client-side authority (F1/F4):
 *   - `ask_user_resolved` (answered/cancelled in another channel or tab) ⇒ the
 *     cached prompt is dropped and the panel disappears ("多 channel 同步").
 *   - `session(busy)` (frozen contract: busy ⇒ 不存在 AskUser) ⇒ same.
 * Rendered through SessionStoreProvider so the assertions exercise the real
 * store wiring (event handler → askUserPrompts → useAskUser.prompt).
 */
import { createElement, useEffect } from 'react'
import { act, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'

import { SessionStoreProvider, useSessionStore, parseAskUserPrompt } from './useSessionStore'
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
    const target = endpoint
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
  // 模块级 storeRef 的赋值必须发生在 render 之外（react-hooks/globals：
  // render 期间改外部变量是不纯的副作用）。用 effect 同步即可。
  useEffect(() => {
    storeRef = store
  }, [store])
  return (
    <div>
      <div data-testid="panel">{prompt ? 'panel-visible' : 'panel-hidden'}</div>
      <div data-testid="prompt-count">{String(store.askUserPrompts.size)}</div>
      <div data-testid="session-count">{String(store.sessions.length)}</div>
    </div>
  )
}

/** Latest store handle from the rendered probe (hydration tests drive it). */
let storeRef: ReturnType<typeof useSessionStore> | null = null

/** Render the probe against the real store wiring (shared by every describe). */
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

/** Parse a payload that MUST yield a prompt (test assertions below depend on it).
 * Keeps the nullable contract explicit instead of scattering `!` at call sites. */
function mustParse(payload: unknown, fallbackRequestID?: string) {
  const prompt = parseAskUserPrompt(payload, fallbackRequestID)
  if (!prompt) throw new Error('expected payload to yield a prompt')
  return prompt
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

  // 2026-09-20 P0 事故回归：提问发布时该会话【没有任何 SSE 订阅者】（用户正在看别的
  // 会话 —— 服务端日志：该路由最后一次订阅 05:19:37、下一次 05:29:40），所以实时
  // ask_user 事件永远不会到达这个会话的面板。此时唯一能让面板出现的路径是 DB 权威水合
  // （AgentPanel 的 get_pending_ask_user effect → hydrateAskUserPrompt）。缺这条路径的
  // 症状就是事故现场：只有 AskUser 工具 pill + 永远"思考中"，用户只能按 Stop 逃出去。
  it('实时事件丢失（无 SSE 订阅者）+ DB 权威水合 ⇒ 面板必须渲染', async () => {
    await renderProbe()
    expect(screen.getByTestId('panel').textContent).toBe('panel-hidden')
    expect(screen.getByTestId('prompt-count').textContent).toBe('0')

    // No ask_user event ever arrives (session had no subscription when the ask
    // was published). The panel state comes from the server's persisted
    // ask_question record instead.
    act(() => {
      storeRef?.hydrateAskUserPrompt(
        'web',
        'web-chat-1',
        mustParse({
          request_id: 'req-hydrated',
          questions: [{ question: '「7ms」按哪个口径判定？', options: ['A', 'B'], allow_other: true }],
        }),
      )
    })

    expect(screen.getByTestId('panel').textContent).toBe('panel-visible')
    expect(screen.getByTestId('prompt-count').textContent).toBe('1')
  })

  it('水合幂等；同 key 的旧 request 被服务端权威替换', async () => {
    await renderProbe()

    act(() => {
      storeRef?.hydrateAskUserPrompt('web', 'web-chat-1', mustParse({ request_id: 'req-1', questions: [{ question: 'Q1' }] }))
    })
    const first = storeRef?.askUserPrompts.get('web:web-chat-1')
    expect(first?.requestId).toBe('req-1')

    // Same request id ⇒ identical content ⇒ the Map instance is preserved
    // (zero re-render for a repeated hydration — the panel is already correct).
    act(() => {
      storeRef?.hydrateAskUserPrompt('web', 'web-chat-1', mustParse({ request_id: 'req-1', questions: [{ question: 'Q1' }] }))
    })
    expect(storeRef?.askUserPrompts.get('web:web-chat-1')).toBe(first)

    // A NEWER server-side pending question (different request id) replaces the
    // stale prompt — the DB is the authority for "which question is pending".
    act(() => {
      storeRef?.hydrateAskUserPrompt('web', 'web-chat-1', mustParse({ request_id: 'req-2', questions: [{ question: 'Q2' }] }))
    })
    expect(storeRef?.askUserPrompts.get('web:web-chat-1')?.requestId).toBe('req-2')
  })

  it('水合不得为缺失身份建 key（空 channel/chatID 直接忽略）', async () => {
    await renderProbe()

    act(() => {
      storeRef?.hydrateAskUserPrompt('', 'web-chat-1', mustParse({ request_id: 'req-x', questions: [{ question: 'Q' }] }))
      storeRef?.hydrateAskUserPrompt('web', '', mustParse({ request_id: 'req-y', questions: [{ question: 'Q' }] }))
    })

    expect(screen.getByTestId('prompt-count').textContent).toBe('0')
    expect(screen.getByTestId('panel').textContent).toBe('panel-hidden')
  })
})

/**
 * 载荷契约：**没有问题的问题不是 prompt**（2026-09-20 CI 回归的根因）。
 *
 * 任何"成功但不含可用问题"的响应（典型：通用 `/api/rpc` 通配路由 mock 回的
 * `{ok:true, data:{ok:true}}`）过去会被合成为 `{requestId: Date.now(), questions: []}`
 * ⇒ AskUserPanel 以 `questions[0] === undefined` 渲染 ⇒ 读 `.allowOther` 抛异常 ⇒
 * 崩溃边界把**整块面板**换掉（CI 的 9 个 goal/todo spec 全红）。真实提问必然 ≥1 题，
 * 所以"无问题"只能解释为"没有 prompt"。
 */
describe('AskUser 载荷契约：没有问题 ⇒ 不是 prompt', () => {
  it('通用 RPC / 空载荷 / 空问题列表都不产生 prompt', () => {
    expect(parseAskUserPrompt({ ok: true })).toBeNull()
    expect(parseAskUserPrompt({})).toBeNull()
    expect(parseAskUserPrompt(null)).toBeNull()
    expect(parseAskUserPrompt(undefined)).toBeNull()
    expect(parseAskUserPrompt({ request_id: 'req-x', questions: [] })).toBeNull()
    expect(parseAskUserPrompt({ request_id: 'req-x', questions: 'nope' })).toBeNull()
    // 有问题数组但每个都被过滤掉（既无文本也无 options）同样不是 prompt。
    expect(parseAskUserPrompt({ request_id: 'req-x', questions: [{}] })).toBeNull()
  })

  it('实时事件缺 questions ⇒ 不建 prompt（绝不伪造空面板、也不进 waiting_input）', async () => {
    await renderProbe()

    act(() => {
      messageHandler?.({
        type: 'ask_user',
        channel: 'web',
        chat_id: 'web-chat-1',
        progress: { request_id: 'r-empty' },
      } as WSMessage)
    })

    expect(screen.getByTestId('prompt-count').textContent).toBe('0')
    expect(screen.getByTestId('panel').textContent).toBe('panel-hidden')
  })

  it('≥1 个有效问题仍是 prompt（契约未被削弱）', () => {
    const withText = parseAskUserPrompt({ request_id: 'r1', questions: [{ question: 'Q' }] })
    expect(withText?.requestId).toBe('r1')
    expect(withText?.questions).toHaveLength(1)

    // 只有 options（LLM 有时不发 question 文本）的问题同样保留，且 fallback id 生效。
    const optionsOnly = parseAskUserPrompt(
      { questions: [{ options: ['A', 'B'], allow_other: true }] },
      'fallback-id',
    )
    expect(optionsOnly?.requestId).toBe('fallback-id')
    expect(optionsOnly?.questions[0].allowOther).toBe(true)
    expect(optionsOnly?.questions[0].options).toEqual(['A', 'B'])
  })
})
