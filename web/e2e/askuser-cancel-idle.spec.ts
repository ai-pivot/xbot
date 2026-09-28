import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'
const CHAT = 'chat-1'
const QUESTION = '请选择方向'

/**
 * 用户报告（2026-09-28）：「askuser 在被用户取消后，前端还是渲染为 busy」。
 *
 * 根因在**后端**（见 agent/interceptCancel 注释 + Go 回归
 * `TestAskUserCancelDuringWaitingUserPauseEmitsSessionIdle`）：WaitingUser 暂停
 * 期间 chatProcessLoop 已把 ss.busy 置 false，而取消路径把 session(idle) 的发射
 * 门控在 `ss.busy.Load()` 上 ⇒ 恒假 ⇒ 真实取消**从不发 idle** ⇒ 会话状态永不回到
 * idle、状态机的 activeTurn 永不清（busyFallback）⇒ 输入框卡 cancel。
 *
 * 本 E2E 走**面板 Cancel 按钮的真实链路**并钉用户可见契约：
 * 「AskUser 取消（后端发出 ask_user_resolved(cancelled) + session(idle)）⇒
 *   会话回到 idle、输入框不再渲染 cancel/stop」。
 *
 * ⚠️ 提问经 **DB 权威水合**（`get_pending_ask_user`）到达（实时 ask_user 事件缺失
 * 的形态）——此时会话仍处于 running（面板由水合渲染，状态不会是 waiting_input），
 * 取消后若后端不发 idle，输入框就会卡在 busy（本用例的判别点）。
 *
 * 判别力（变异自证）：把下面那条 `session(idle)` 拿掉（= 修复前后端输出）⇒
 * 本用例必红（输入框仍是 cancel）。CI 真机验证见 PR。
 *
 * SSE 由 addInitScript 注入的 MockEventSource 驱动（沿用 busy-invariant.spec.ts /
 * askuser-resolved.spec.ts 的既有做法），api/* 全部 route mock ⇒ 不触真实后端。
 */

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

let seqCounter = 0

async function emitSSE(page: Page, type: string, data: Record<string, unknown>): Promise<void> {
  await page.evaluate(
    ({ type, data, seq }) => {
      const w = window as unknown as SSEMockState
      const handlers = w.__sseListeners?.[type]
      if (!handlers) return
      const ev = new MessageEvent(type, { data: JSON.stringify({ ...data, seq }) })
      handlers.forEach((h) => h(ev))
    },
    { type, data, seq: ++seqCounter },
  )
}

// 会话在跑：active_progress 快照必须存在，否则 sseConnection 的恢复会把会话
// 判成 idle（agent-idle）——那就不是"busy 会话"了。
const ACTIVE_PROGRESS = {
  phase: 'tool_exec',
  iteration: 2,
  seq: 10,
  turn_id: 1,
  chat_id: `web:${CHAT}`,
  active_tools: [{ name: 'AskUser', status: 'running', iteration: 2, label: '请选择方向', summary: '' }],
  completed_tools: [],
  iteration_history: [{ iteration: 1, content: '正在分析…', completed_tools: [] }],
  todos: [],
}

const HISTORY_MESSAGES = [
  { id: 1, role: 'user', content: '帮我选个方向', timestamp: '2026-09-28T04:00:00Z', turn_id: 1 },
  {
    id: 2,
    role: 'assistant',
    content: '',
    timestamp: '2026-09-28T04:01:00Z',
    turn_id: 1,
    iterations: [{ iteration: 1, content: '正在分析…', reasoning: '', tools: [], tool_count: 0 }],
  },
]

/** 可变的服务端 pending 状态：取消后必须变 null（否则水合会把 prompt 加回来）。 */
interface PendingState {
  pending: Record<string, unknown> | null
}

async function newClient(browser: import('@playwright/test').Browser, state: PendingState): Promise<Page> {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const page = await ctx.newPage()
  await page.addInitScript(() => {
    try { localStorage.setItem('xbot-locale', 'zh-CN') } catch { /* ignore */ }
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
    const w = window as unknown as SSEMockState
    w.__sseListeners = listeners
    class MockEventSource {
      readyState = 1
      onopen: ((ev: Event) => void) | null = null
      onerror: ((ev: Event) => void) | null = null
      constructor(public url: string) { setTimeout(() => this.onopen?.(new Event('open')), 0) }
      addEventListener(type: string, handler: (ev: MessageEvent) => void) {
        if (!listeners[type]) listeners[type] = new Set()
        listeners[type].add(handler)
      }
      removeEventListener(type: string, handler: (ev: MessageEvent) => void) { listeners[type]?.delete(handler) }
      close() { for (const key of Object.keys(listeners)) listeners[key].clear() }
    }
    ;(window as unknown as { EventSource: typeof MockEventSource }).EventSource = MockEventSource
  })

  const now = new Date().toISOString()
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: {
          sessions: [{ chat_id: CHAT, channel: 'web', label: 'Test', last_active: now, running: true }],
          chats: [{ chat_id: CHAT, channel: 'web', label: 'Test', last_active: now, isCurrent: true, running: true }],
          orphan_subagents: [],
        },
      },
    }),
  )
  await page.route('**/api/history', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: {
          messages: HISTORY_MESSAGES,
          chat_id: CHAT,
          channel: 'web',
          last_seq: 10,
          active_progress: ACTIVE_PROGRESS,
          has_more: false,
          oldest_id: 1,
        },
      },
    }),
  )
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => {
    const body = r.request().postDataJSON() as { method?: string } | null
    if (body?.method === 'get_active_progress') return r.fulfill({ json: { ok: true, data: ACTIVE_PROGRESS } })
    // DB 权威水合：pending 提问 ⇒ 面板渲染（不经实时 ask_user 事件）。
    if (body?.method === 'get_pending_ask_user') return r.fulfill({ json: { ok: true, data: state.pending } })
    return r.fulfill({ json: { ok: true, data: null } })
  })
  await page.route('**/api/ask_user/respond', (r) => r.fulfill({ json: { ok: true, data: {} } }))

  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await expect(page.locator('[data-message-list-content]')).toContainText('帮我选个方向', { timeout: 30000 })
  await page.waitForFunction(() => {
    const w = window as unknown as SSEMockState
    return !!w.__sseListeners?.['ask_user']
  }, { timeout: 10000 })
  await page.waitForTimeout(200)
  return page
}

test('AskUser 取消后：会话回到 idle，输入框不再是 cancel 按钮（用户 2026-09-28）', async ({ browser }) => {
  seqCounter = 10
  // 服务端权威 pending 状态：面板由 DB 水合渲染；取消后服务端不再返回它。
  const state: PendingState = {
    pending: { request_id: 'ask-cancel-1', questions: [{ question: QUESTION, options: ['A', 'B'] }] },
  }
  const page = await newClient(browser, state)

  // 面板经 DB 水合出现（实时 ask_user 事件缺失的形态）。
  const panel = page.getByTestId('ask-user-panel')
  await expect(panel).toBeVisible({ timeout: 10000 })
  await expect(page.getByText(QUESTION)).toBeVisible()

  // 用户点面板的 Cancel ⇒ POST /api/ask_user/respond {cancelled:true}。
  // 服务端随后不再把该 prompt 视为 pending（水合不得把它加回来）。
  state.pending = null
  await panel.getByRole('button', { name: /取消|Cancel/i }).first().click()

  // 后端取消该 pending prompt 的输出：resolved(cancelled) + session(idle)。
  await emitSSE(page, 'ask_user_resolved', {
    type: 'ask_user_resolved',
    channel: 'web',
    chat_id: CHAT,
    request_id: 'ask-cancel-1',
    reason: 'cancelled',
  })
  await emitSSE(page, 'session', { chat_id: CHAT, session: { action: 'idle', chat_id: CHAT } })

  // 用户可见契约：面板消失 + 输入框不再是 cancel/stop（会话回到 idle）。
  await expect(panel).toHaveCount(0, { timeout: 10000 })
  await expect(
    page.getByRole('button', { name: /停止|Stop|Cancel|取消/i }),
    'AskUser 取消后输入框不得仍渲染为 busy/cancel',
  ).toHaveCount(0, { timeout: 10000 })

  await page.context().close()
})
