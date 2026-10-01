import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'
const CHAT = 'chat-1'

/**
 * 用户报告（2026-09-28）：「askuser 在被用户取消后，前端还是渲染为 busy」。
 *
 * 根因在**后端**（见 agent/interceptCancel 注释）：WaitingUser 暂停期间
 * chatProcessLoop 已把 ss.busy 置 false，而取消路径把 session(idle) 的发射门控在
 * `ss.busy.Load()` 上 ⇒ 恒假 ⇒ 真实取消**从不发 idle** ⇒ 会话状态永不回到 idle、
 * 状态机的 activeTurn 永不清（busyFallback）⇒ 输入框卡 cancel。
 *
 * ⚠️ 2026-10-22 迁移：旧的 `AskUserPanel` 与其 `/api/ask_user/respond` 已被删除
 * （514ec7b3），ask_user 的**交互面**搬到 `/structured` 的 `AskUserCard`
 * （多端同步、回答/取消链路由 e2e/askuser-resolved.spec.ts 与
 * e2e/structured-cards.spec.ts 覆盖）。本 spec 保留的是**会话状态机**那条契约，
 * 它从没变过，也不该因为搬家而失去守卫：
 *
 *   「ask_user_resolved(cancelled) 只让 prompt 失效，不得顺手清掉 busy；
 *     会话真正回到 idle 必须由 session(idle) 决定 ⇒ 输入框的取消按钮消失」
 *
 * 判别点：把本 spec 自己 emit 的 `session(idle)` 删掉（模拟"后端不发 idle"）⇒ 必红
 * （输入框仍是 cancel/stop）。这正是用户现场的形状。
 *
 * SSE 由 addInitScript 注入的 MockEventSource 驱动，api/* 全部 route mock ⇒ 不触真实后端。
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

async function newClient(browser: import('@playwright/test').Browser): Promise<Page> {
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
    // 不再有 pending 提问（旧水合路径随 AskUserPanel 一起删除）：一律 null。
    return r.fulfill({ json: { ok: true, data: null } })
  })
  await page.route('**/api/messages**', (r) =>
    r.fulfill({ json: { ok: true, data: { items: [], has_more: false, next_cursor: 0 } } }),
  )
  await page.route('**/api/inbox**', (r) =>
    r.fulfill({ json: { ok: true, data: { items: [], has_more: false, next_cursor: 0, unanswered: 0 } } }),
  )

  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await expect(page.locator('[data-message-list-content]')).toContainText('帮我选个方向', { timeout: 30000 })
  await page.waitForTimeout(300)
  return page
}

test('AskUser 取消后：会话回到 idle，输入框不再是 cancel 按钮（用户 2026-09-28）', async ({ browser }) => {
  seqCounter = 10
  const page = await newClient(browser)

  // 输入框里已经有字：按 2026-10-12 的 P0 修复，`busy && 有内容` 时必须另有一颗
  // 只走 onCancel 的停止按钮（`composer-cancel`）—— 否则"有一个字时点停止"会被
  // 当成排队发送，cancel 永远发不出去。用它作判据比"主按钮换图标"稳定得多。
  const composer = page.locator('.tiptap[contenteditable="true"]')
  await composer.click()
  await composer.fill('等着回答时顺手打的字')

  // 前提：会话在跑 ⇒ 输入框旁边是 cancel/stop（否则"回到 idle"是平凡通过）。
  const cancelBtn = page.getByTestId('composer-cancel')
  await expect(cancelBtn, '前提：running 会话的输入框必须是 cancel/stop').toBeVisible({ timeout: 10000 })

  // 后端取消该 pending prompt 的第一条输出：resolved(cancelled)（prompt 级失效）。
  await emitSSE(page, 'ask_user_resolved', {
    type: 'ask_user_resolved',
    channel: 'web',
    chat_id: CHAT,
    request_id: 'ask-cancel-1',
    reason: 'cancelled',
  })

  // ★ prompt 失效 ≠ 会话空闲：此刻 idle 还没到，输入框必须仍是 cancel。
  //   这条前提同时证明"会话已回到 idle"不是平凡通过。
  await expect(
    cancelBtn,
    'prompt 失效不得顺手清 busy（idle 未到时输入框仍是 cancel）',
  ).toBeVisible({ timeout: 10000 })

  // 后端随后发出权威 idle ⇒ 会话回到 idle。
  await emitSSE(page, 'session', { chat_id: CHAT, session: { action: 'idle', chat_id: CHAT } })

  // 用户可见契约：输入框不再是 cancel/stop（本修复的判据）。
  await expect(
    cancelBtn,
    'AskUser 取消后输入框不得仍渲染为 busy/cancel',
  ).toHaveCount(0, { timeout: 10000 })

  await page.context().close()
})
