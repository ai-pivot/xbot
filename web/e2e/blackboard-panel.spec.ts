/**
 * 共享黑板面板（真实浏览器 + 真实 SSE 消费链路）。
 *
 * 契约（用户可见）：
 *  ① 面板显示当前会话的黑板：条目、状态（可认领/认领中/阻塞/已关闭）、
 *     持有者与**租约倒计时**、依赖；
 *  ② 别人改动该板时，**面板自动刷新**（服务端 seq=0 全客户端广播 →
 *     前端 window 事件 → 去抖重取）—— 这条最容易被静默破坏：
 *     新事件若漏进 SSE_EVENT_TYPES 白名单，EventSource 根本不注册 listener，
 *     面板永远看不到同伴的改动，而单测（直接派发 window 事件）发现不了。
 *
 * 因此本 spec 断言的是"真实链路"：mock EventSource → 后端事件名 →
 * 前端派发 → 面板重取。
 */
import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'
const CHAT = 'chat-1'
const BOARD = `web:${CHAT}`

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

/** Emit a server-side SSE event into the mocked transport. */
async function emitSSE(page: Page, type: string, data: Record<string, unknown>): Promise<void> {
  await page.evaluate(
    ({ type, data }) => {
      const w = window as unknown as SSEMockState
      const handlers = w.__sseListeners?.[type]
      if (!handlers) return
      const ev = new MessageEvent(type, { data: JSON.stringify(data) })
      handlers.forEach((h) => h(ev))
    },
    { type, data },
  )
}

function entry(overrides: Record<string, unknown> = {}) {
  return {
    board: BOARD,
    key: 'api-impl',
    kind: 'task',
    title: '实现 /v2 API',
    status: 'open',
    closed: false,
    revision: 3,
    blocked: false,
    ready: true,
    claim_expires_at: 0,
    created_at: Date.now(),
    updated_at: Date.now(),
    ...overrides,
  }
}

/** Login + mock every REST route, then open the Blackboard panel. */
async function openBlackboard(page: Page, listResponses: unknown[][]): Promise<void> {
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

  const now = new Date().toISOString()
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: {
          sessions: [{ chat_id: CHAT, channel: 'web', label: 'Test', last_active: now }],
          chats: [{ chat_id: CHAT, channel: 'web', label: 'Test', last_active: now }],
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
          messages: [
            { role: 'user', content: 'Message 1', seq: 1, timestamp: now },
            { role: 'assistant', content: 'Message 2', seq: 2, timestamp: now },
          ],
          chat_id: CHAT,
          last_seq: 2,
          active_progress: null,
        },
      },
    }),
  )
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))

  // Blackboard RPCs: each call to blackboard_list pops the next queued response,
  // so the test can make the board change between the initial load and the
  // broadcast-triggered refetch.
  let listCall = 0
  await page.route('**/api/rpc', (r) => {
    const body = r.request().postDataJSON() as { method?: string }
    switch (body.method) {
      case 'blackboard_list': {
        const entries = listResponses[Math.min(listCall, listResponses.length - 1)]
        listCall++
        return r.fulfill({ json: { ok: true, data: { board: BOARD, entries } } })
      }
      case 'blackboard_boards':
        return r.fulfill({ json: { ok: true, data: { boards: [] } } })
      case 'blackboard_get':
        return r.fulfill({ json: { ok: true, data: { entry: entry({ body: '设计草案：先定接口' }) } } })
      default:
        return r.fulfill({ json: { ok: true, data: null } })
    }
  })

  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForFunction(() => document.body.textContent?.includes('Message 2'), { timeout: 10_000 })

  // Non-pinned core panels live on the ActivityBar (left rail): clicking the
  // item expands the sidebar and focuses that panel.
  const item = page.locator('[data-activity-item="core.blackboard"]')
  await expect(item).toBeVisible({ timeout: 10_000 })
  await item.click()
}

test.describe('shared blackboard panel', () => {
  test('renders entries with their state, holder and lease countdown', async ({ page }) => {
    await openBlackboard(page, [
      [
        entry({ key: 'api-design', title: '设计 /v2 API' }),
        entry({
          key: 'api-impl',
          title: '实现 /v2 API',
          ready: false,
          claimed_by: 'main/explore',
          claim_expires_at: Date.now() + 5 * 60_000,
        }),
        entry({ key: 'api-doc', title: '写文档', ready: false, blocked: true, blocked_by: ['api-design'] }),
        entry({ key: 'api-old', title: '旧接口', closed: true, ready: false }),
      ],
    ])

    const rows = page.getByTestId('blackboard-entry')
    await expect(rows).toHaveCount(4)
    await expect(rows.nth(0)).toHaveAttribute('data-state', 'ready')
    await expect(rows.nth(1)).toHaveAttribute('data-state', 'claimed')
    await expect(rows.nth(2)).toHaveAttribute('data-state', 'blocked')
    await expect(rows.nth(3)).toHaveAttribute('data-state', 'closed')

    // Who holds it, and for how long (the lease countdown is computed locally).
    await expect(page.getByText(/main\/explore/)).toBeVisible()
    await expect(page.getByText(/\dm\d\ds/)).toBeVisible()
    // Blocked entries name the dependency that gates them.
    await expect(page.getByText('api-design', { exact: true })).toBeVisible()
  })

  test('refreshes when another session changes the board (SSE → refetch)', async ({ page }) => {
    await openBlackboard(page, [
      [entry({ key: 'before' })],
      [entry({ key: 'before' }), entry({ key: 'posted-by-peer', title: '同伴新加的活' })],
    ])

    await expect(page.getByTestId('blackboard-entry')).toHaveCount(1)

    // The server fanned the change out to every web client (seq=0 broadcast).
    await emitSSE(page, 'blackboard_update', {
      blackboard: { board: BOARD, key: 'posted-by-peer', op: 'post', revision: 1 },
    })

    await expect(page.getByTestId('blackboard-entry')).toHaveCount(2)
    await expect(page.getByText('同伴新加的活')).toBeVisible()
  })

  test('loads an entry body on demand (lists never carry bodies)', async ({ page }) => {
    await openBlackboard(page, [[entry({ key: 'api-impl' })]])

    await expect(page.getByTestId('blackboard-entry')).toHaveCount(1)
    await expect(page.getByText('设计草案：先定接口')).toHaveCount(0)

    await page.getByRole('button', { name: /展开|Expand/ }).first().click()
    await expect(page.getByText('设计草案：先定接口')).toBeVisible()
  })
})
