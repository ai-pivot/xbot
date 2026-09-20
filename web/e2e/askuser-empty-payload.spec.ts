import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * 回归守护（2026-09-20 CI 9 个 spec 全红的根因）：
 * **`get_pending_ask_user` 的响应不含任何问题时，不得被当成 prompt。**
 *
 * 现场：`goal-todo-optimistic` / `todo-edit-goal` / `todo-mobile` 都用通用
 * `/api/rpc` 通配路由 mock 回 `{ok:true, data:{ok:true}}`。AskUser 的 DB
 * 水合 effect（AgentPanel）拿到这个真值后，`parseAskUserPrompt` 把它合成为
 * `{requestId: Date.now(), questions: []}` ⇒ `AskUserPanel` 以
 * `questions[0] === undefined` 渲染 ⇒ 读 `.allowOther` 抛异常 ⇒ **崩溃边界把整块
 * 面板替换成崩溃覆盖层**（`#crash-overlay`）⇒ goal banner / todo 面板全部不在 DOM
 * ⇒ 那些 spec 的定位器永远找不到元素（CI 上 9 个失败，且是 60s 超时型）。
 *
 * 契约（本文件即判别实验）：
 *   ① 没有 pending（或载荷没有问题）⇒ **不渲染 AskUser 面板、应用不崩、面板其余部分照常**；
 *   ② 真·提问（事件带 questions）⇒ 面板照常渲染（修复不得把功能一起掐掉）。
 *
 * 判别力：把 `parseAskUserPrompt` 的「questions 为空 ⇒ null」删掉，或把 AgentPanel
 * 水合分支的 null 守卫删掉 ⇒ 用例 ① 必红（`#crash-overlay` 出现 + goal banner 消失）。
 */

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

const GOAL = '回归守护：无 pending 时不得渲染 AskUser 面板'

/** seq 是每客户端 SSE 游标 —— 必须按 page 独立计数（见 askuser-resolved.spec.ts）。 */
const seqByPage = new WeakMap<Page, number>()

async function emitSSE(page: Page, type: string, data: Record<string, unknown>) {
  const seq = (seqByPage.get(page) ?? 0) + 1
  seqByPage.set(page, seq)
  await page.evaluate(
    ({ type, data, seq }) => {
      const w = window as unknown as SSEMockState
      const handlers = w.__sseListeners?.[type]
      if (!handlers) return
      const ev = new MessageEvent(type, { data: JSON.stringify({ ...data, seq }) })
      handlers.forEach((h) => h(ev))
    },
    { type, data, seq },
  )
}

/** 登录 + 用**通用** `/api/rpc` mock（正是 CI 里触发崩溃的那个形状）。 */
async function boot(page: Page) {
  await page.addInitScript(() => {
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
    const w = window as unknown as SSEMockState
    w.__sseListeners = listeners
    class MockEventSource {
      readyState = 1
      onopen: ((ev: Event) => void) | null = null
      onerror: ((ev: Event) => void) | null = null
      constructor(public url: string) {
        setTimeout(() => this.onopen?.(new Event('open')), 0)
      }
      addEventListener(t: string, h: (ev: MessageEvent) => void) {
        if (!listeners[t]) listeners[t] = new Set()
        listeners[t].add(h)
      }
      removeEventListener() {}
      close() {}
    }
    ;(window as unknown as { EventSource: typeof MockEventSource }).EventSource = MockEventSource
  })

  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: {
          sessions: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() }],
          chats: [],
          orphan_subagents: [],
        },
      },
    }),
  )
  await page.route('**/api/history', (r) =>
    r.fulfill({ json: { ok: true, data: { messages: [], chat_id: 'chat-1', last_seq: 0, active_progress: null } } }),
  )
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  // ⛔ 通用 mock：任何 RPC（含 get_pending_ask_user）都回 {ok:true, data:{ok:true}}。
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: { ok: true } } }))

  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForTimeout(2000)
}

/** 播种会话级状态（goal / todos）—— 与失败 spec 同一路径（SSE progress_structured）。 */
async function seedSessionState(page: Page) {
  await emitSSE(page, 'progress_structured', {
    type: 'progress_structured',
    progress: { chat_id: 'web:chat-1', phase: 'tool_exec', iteration: 1, turn_id: 7, goal: { objective: GOAL, status: 'active' } },
  })
  await page.waitForTimeout(600)
}

test.describe('AskUser 载荷契约：没有问题 ⇒ 不渲染面板、应用不崩', () => {
  test('通用 /api/rpc mock（{ok:true}）不得伪造空 AskUser 面板', async ({ browser }) => {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
    await boot(page)
    await seedSessionState(page)

    // ① 崩溃覆盖层必须不存在（旧实现：AskUserPanel 读 questions[0].allowOther 抛异常
    //    ⇒ CrashBoundary/全局覆盖层抹掉整块面板）。
    await expect(page.locator('#crash-overlay')).toHaveCount(0)

    // ② 不得渲染 AskUser 面板（"无 pending" 的唯一正当表现）。
    await expect(page.getByTestId('ask-user-panel')).toHaveCount(0)

    // ③ 面板其余部分照常：goal banner 仍在（这正是失败 spec 的定位器）。
    await expect(page.getByTestId('goal-text')).toBeVisible()
    await expect(page.getByTestId('goal-text')).toHaveText(GOAL)
  })

  test('真·提问（事件带 questions）仍照常渲染面板 —— 修复不得掐掉功能', async ({ browser }) => {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
    await boot(page)

    await emitSSE(page, 'ask_user', {
      type: 'ask_user',
      channel: 'web',
      chat_id: 'chat-1',
      progress: { request_id: 'req-live-1', questions: [{ question: 'Proceed?', options: ['yes', 'no'] }] },
    })

    await expect(page.getByTestId('ask-user-panel')).toBeVisible()
    await expect(page.locator('#crash-overlay')).toHaveCount(0)
  })
})
