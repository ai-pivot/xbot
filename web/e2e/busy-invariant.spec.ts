/**
 * P0 不变量（用户 2026-09-19 反复点名，此前多次未修好）：
 *
 *   **「只要输入框是 cancel 按钮，就一定不能上面渲染的内容是 idle 内容」**
 *
 * 现场（用户截图）：手机 busy 会话里，输入框是橙色停止按钮（= cancel/busy），
 * 但消息列表渲染的是**已收尾**的内容（无任何进行中信号）—— compose 与列表两侧
 * 权威分叉。破坏点有两处（本 spec 用真实浏览器 + 真实 SSE 消费链路覆盖）：
 *
 *  (A) 渲染层：`frozen` 行同样 `isPartial=true`，被 MessageList 当作 live 行
 *      （`liveId`）⇒ ① 它拿到 `liveProgressFromState` 的**空**快照（frozen ⇒
 *      activeTurn===null）⇒ 自身不渲染进行中信号；② busy 占位符的
 *      `liveId === null` 条件因此不成立 ⇒ 也被抑制。
 *  (B) 状态层：一条**迟到/误传/重放**的 coarse `session(idle)`（不带 turn 身份，
 *      可能来自 SSE 重连的 last_event_id 回放窗口）冻结运行中的 turn 并清
 *      activeTurn ⇒ 与 composer 的 `currentSession.running`（服务端 reconcile
 *      权威）分叉。修复：running 是 turn live-ness 的权威（`session_running`）。
 *
 * 断言（用户可见契约）：
 *   ① 停止（cancel）按钮可见（busy 前提成立）；
 *   ② 列表里必须能看到进行中信号（`.sweep-text` 思考中/live 指示器，或 busy 占位符）；
 *   ③ 流式内容继续更新（live 进度没有停在冻结那一刻）。
 */
import { test, expect, type Browser, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'
const CHAT = 'chat-1'

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

/** 进行中迭代（3）的一帧流式内容（流式帧 = progress_structured + phase ''）。 */
async function emitStream(page: Page, streamContent: string): Promise<void> {
  await emitSSE(page, 'progress_structured', {
    chat_id: CHAT,
    progress: {
      chat_id: `web:${CHAT}`,
      turn_id: 1,
      iteration: 3,
      phase: '',
      stream_content: streamContent,
    },
  })
}

const ACTIVE_PROGRESS = {
  phase: 'tool_exec',
  iteration: 3,
  seq: 10,
  turn_id: 1,
  chat_id: `web:${CHAT}`,
  active_tools: [{ name: 'Read', status: 'running', iteration: 3, label: 'src/lib.rs', summary: '' }],
  completed_tools: [],
  iteration_history: [
    { iteration: 1, content: 'iter1', completed_tools: [] },
    { iteration: 2, content: 'iter2', completed_tools: [] },
  ],
  todos: [],
}

const HISTORY_MESSAGES = [
  { id: 1, role: 'user', content: '继续接线', timestamp: '2026-09-19T04:00:00Z', turn_id: 1 },
  {
    id: 2,
    role: 'assistant',
    content: '',
    timestamp: '2026-09-19T04:01:00Z',
    turn_id: 1,
    iterations: [
      { iteration: 1, content: 'iter1', reasoning: '', tools: [], tool_count: 0 },
      { iteration: 2, content: 'iter2', reasoning: '', tools: [], tool_count: 0 },
    ],
  },
]

async function newContext(browser: Browser) {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true })
  await ctx.addInitScript(() => {
    try { localStorage.setItem('xbot-locale', 'zh-CN') } catch { /* ignore */ }
  })
  return ctx
}

async function setupMock(page: Page): Promise<void> {
  await page.addInitScript(() => {
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
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) => r.fulfill({
    json: { ok: true, data: {
      sessions: [{ chat_id: CHAT, channel: 'web', label: 'mtp-step-opt', last_active: new Date().toISOString(), running: true }],
      chats: [{ chat_id: CHAT, channel: 'web', label: 'mtp-step-opt', last_active: new Date().toISOString(), isCurrent: true, running: true }],
      orphan_subagents: [],
    } },
  }))
  await page.route('**/api/history', (r) => r.fulfill({
    json: { ok: true, data: {
      messages: HISTORY_MESSAGES,
      chat_id: CHAT,
      channel: 'web',
      last_seq: 10,
      active_progress: ACTIVE_PROGRESS,
      has_more: false,
      oldest_id: 1,
    } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => {
    const body = r.request().postDataJSON() as { method?: string } | null
    if (body?.method === 'get_active_progress') return r.fulfill({ json: { ok: true, data: ACTIVE_PROGRESS } })
    return r.fulfill({ json: { ok: true, data: null } })
  })
}

test('不变量：输入框是 cancel（busy）⇒ 列表必须能看到进行中信号，且流式继续更新', async ({ browser }) => {
  seqCounter = 10
  const ctx = await newContext(browser)
  const page = await ctx.newPage()
  await setupMock(page)
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForSelector('[data-testid="tool-pill"]', { timeout: 30000 })

  // 服务端状态：会话在跑 + turn 1 在流式（live 行 + live 进度）。
  await emitSSE(page, 'session', { chat_id: CHAT, session: { action: 'busy', chat_id: CHAT } })
  await emitSSE(page, 'progress_structured', {
    chat_id: CHAT,
    progress: {
      chat_id: `web:${CHAT}`, phase: 'turn_started', turn_id: 1, seq: ++seqCounter,
      turn_start: { trigger: 'user', content: '继续接线', request_id: null },
    },
  })
  await emitStream(page, '进行中 A')

  const stopButton = page.getByRole('button', { name: /停止|Stop|Cancel|取消/i }).first()
  const list = page.locator('[data-message-list-content]')
  await expect(stopButton, '前提：busy ⇒ 输入框是停止（cancel）按钮').toBeVisible({ timeout: 10000 })
  await expect(list, '不变量①：busy 时列表必须有进行中信号').toContainText('进行中 A')

  // ★ 迟到/误传的 coarse idle（SSE 重连回放窗口 / restoreActiveProgress 竞态）。
  await emitSSE(page, 'session', { chat_id: CHAT, session: { action: 'idle', chat_id: CHAT } })
  // 服务端的真实状态仍然是"在跑"（心跳/后续 busy 事件）—— 前提保持 busy。
  await emitSSE(page, 'session', { chat_id: CHAT, session: { action: 'busy', chat_id: CHAT } })

  // 不变量：cancel 仍在 ⇒ 列表必须仍能看到进行中信号（不得呈现为 idle 内容）。
  await expect(stopButton, '权威 idle 之后 busy 依然成立（服务端在跑）').toBeVisible({ timeout: 10000 })
  const inProgress = page.locator('[data-message-list-content] .sweep-text')
  await expect(inProgress, '不变量②：busy ⇒ 必须有进行中信号（live 指示器或 busy 占位符）').not.toHaveCount(0)

  // 流式继续更新（live 进度没有停在冻结那一刻）。
  await emitStream(page, '进行中 A B')
  await expect(list, '不变量③：流式必须继续更新').toContainText('进行中 A B')
  await emitStream(page, '进行中 A B C')
  await expect(list).toContainText('进行中 A B C')
  await ctx.close()
})
