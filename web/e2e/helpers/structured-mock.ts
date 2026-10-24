/**
 * 结构化消息页（`/structured`）的全 mock 脚手架。
 *
 * 2026-10-22：本分支删掉了旧的 `AskUserPanel`（会话面板里的弹层 + `/api/ask_user/respond`），
 * ask_user 改由**结构化消息** + `AskUserCard` 渲染，用户界面也从 `/`（Agent 面板）
 * 搬到 `/structured`（`web/src/structured/StructuredWorkspace.tsx`）。
 *
 * 所以原先落在旧面板上的 E2E 契约，需要按新路径重写：
 *   - 问题/选项可见 ⇒ 断言 `data-testid="ask-user-card"` 内的内容；
 *   - 多端失效（别人答了/取消了）⇒ 服务端广播的 `structured_message`（answer 行），
 *     由 `useStructuredMessages.deriveTargetStates` 投影回目标卡片。
 *
 * 这里只做 route mock + 注入 MockEventSource，**不触真实后端**；真后端那条链路由
 * `e2e/structured-cards.spec.ts` 覆盖 —— 两者互补：那边测端到端，这边测前端契约，
 * 且能精确控制事件顺序/重放（多端一致性、幂等）。
 */
import { expect, type Page } from '@playwright/test'

export const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'
export const CHAT_ID = 'chat-1'

/** 一行结构化消息（`protocol/structured.go` 的形状，测试只填关心的字段）。 */
export interface StructuredRow {
  message_id: number
  kind: 'ask_user' | 'answer' | 'report' | 'decision' | 'text' | 'event'
  correlation_id?: string
  reply_to?: string
  direction?: 'in' | 'out'
  answered?: boolean
  cancelled?: boolean
  text?: string
  payload?: Record<string, unknown>
  sender?: { type: string; id: string; name?: string }
  created_at?: string
}

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

/** 每页独立的 SSE 游标（跨页共享会让第二个页面的首个事件被序号校验丢弃）。 */
const seqByPage = new WeakMap<Page, number>()

/** 派发一个命名 SSE 事件（服务端用 `event: <type>` 发送，必须走 addEventListener）。 */
export async function emitSSE(page: Page, type: string, data: Record<string, unknown>): Promise<void> {
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

/** 推一行结构化消息（等价于服务端 `structured_message` 广播 / 另一端的写入）。 */
export async function emitStructured(page: Page, row: StructuredRow): Promise<void> {
  await emitSSE(page, 'structured_message', { type: 'structured_message', structured: normalize(row) })
}

function normalize(row: StructuredRow): StructuredRow {
  return {
    direction: 'in',
    answered: false,
    cancelled: false,
    text: '',
    created_at: new Date().toISOString(),
    sender: { type: 'agent', id: 'web-1' },
    ...row,
  }
}

/** 等页面上的结构化消息 SSE 订阅就绪（否则事件发出去也没人收）。 */
export async function waitStructuredSubscribed(page: Page): Promise<void> {
  await page.waitForFunction(
    () => {
      const w = window as unknown as SSEMockState
      return !!w.__sseListeners?.['structured_message']
    },
    { timeout: 10_000 },
  )
}

/**
 * 打开一个全 mock 的 `/structured` 会话页。
 *
 * `rows` 是首屏下发的结构化消息（REST `/api/messages`），未回答的 ask_user 同时
 * 出现在 `/api/inbox` 里（收件箱徽标）。
 */
export async function bootStructured(browser: import('@playwright/test').Browser, rows: StructuredRow[]): Promise<Page> {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const page = await ctx.newPage()
  await page.addInitScript(() => {
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
    ;(window as unknown as SSEMockState).__sseListeners = listeners
    class MockEventSource {
      readyState = 1
      onopen: ((ev: Event) => void) | null = null
      onerror: ((ev: Event) => void) | null = null
      constructor(public url: string) {
        setTimeout(() => this.onopen?.(new Event('open')), 0)
      }
      addEventListener(type: string, handler: (ev: MessageEvent) => void) {
        if (!listeners[type]) listeners[type] = new Set()
        listeners[type].add(handler)
      }
      removeEventListener(type: string, handler: (ev: MessageEvent) => void) {
        listeners[type]?.delete(handler)
      }
      close() {
        for (const key of Object.keys(listeners)) listeners[key].clear()
      }
    }
    ;(window as unknown as { EventSource: typeof MockEventSource }).EventSource = MockEventSource
  })

  const items = rows.map(normalize)
  const unanswered = items.filter((m) => m.kind === 'ask_user' && !m.answered && !m.cancelled)
  const now = new Date().toISOString()
  const lastID = items.reduce((mx, m) => Math.max(mx, m.message_id), 0)

  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: {
          sessions: [{ chat_id: CHAT_ID, channel: 'web', label: 'Test', last_active: now }],
          chats: [{ chat_id: CHAT_ID, channel: 'web', label: 'Test', last_active: now, isCurrent: true }],
          orphan_subagents: [],
        },
      },
    }),
  )
  await page.route('**/api/history', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: { messages: [], chat_id: CHAT_ID, channel: 'web', last_seq: 0, active_progress: null, has_more: false },
      },
    }),
  )
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/messages**', (r) =>
    r.fulfill({ json: { ok: true, data: { items, has_more: false, next_cursor: lastID } } }),
  )
  await page.route('**/api/inbox**', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: { items: unanswered, has_more: false, next_cursor: 0, unanswered: unanswered.length },
      },
    }),
  )
  await page.route('**/api/answer', (r) =>
    r.fulfill({ json: { ok: true, data: { message_id: 9001, duplicate: false, message: null } } }),
  )
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))

  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.goto(`${BASE}/structured?channel=web&chat_id=${CHAT_ID}`)
  await expect(page.getByTestId('structured-workspace')).toBeVisible({ timeout: 15_000 })
  await waitStructuredSubscribed(page)
  await page.waitForTimeout(150)
  return page
}

/** 一条 ask_user 结构化消息（可选覆盖 payload/状态）。 */
export function askRow(overrides: Partial<StructuredRow> = {}): StructuredRow {
  const question = (overrides.payload?.questions as Array<{ question: string }> | undefined)?.[0]?.question
  return {
    message_id: 42,
    kind: 'ask_user',
    correlation_id: 'req-1',
    text: question ?? 'Do you want to proceed?',
    payload: {
      questions: [
        {
          id: 'q1',
          question: 'Do you want to proceed?',
          options: [
            { id: 'yes', label: 'yes' },
            { id: 'no', label: 'no' },
          ],
        },
      ],
      allow_cancel: true,
    },
    ...overrides,
  }
}

/** 一条 answer 行（另一端的回答/取消），`deriveTargetStates` 靠 reply_to 投影回目标。 */
export function answerRow(targetID: number, opts: { cancelled?: boolean } = {}): StructuredRow {
  return {
    message_id: targetID + 1,
    kind: 'answer',
    reply_to: String(targetID),
    correlation_id: 'req-1',
    cancelled: opts.cancelled ?? false,
    text: opts.cancelled ? '已取消' : 'q1:yes',
  }
}
