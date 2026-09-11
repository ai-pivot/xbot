import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E test for Staging Tray drag-to-reorder.
 *
 * The tray renders the pending queue (admitted, not yet dequeued messages).
 * The user drags a card onto another one to change the execution order; the
 * frontend posts the new id order to /api/queue/reorder and hydrates the tray
 * from the authoritative snapshot the server returns.
 *
 * The drag is pointer-based (works on mouse + touch + pen), so this test drives
 * real pointer events through the mouse and asserts on the reorder request.
 */

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

interface ReorderCall {
  msg_ids: string[]
}

const queueItems = [
  { msg_id: 'm1', turn_id: 11, content: 'first message', preview: 'first message', source: 'user', enqueued_at: 1 },
  { msg_id: 'm2', turn_id: 12, content: 'second message', preview: 'second message', source: 'user', enqueued_at: 2 },
  { msg_id: 'm3', turn_id: 13, content: 'third message', preview: 'third message', source: 'user', enqueued_at: 3 },
]

async function setupMock(page: Page, calls: ReorderCall[]) {
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: {
          sessions: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() }],
          chats: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() }],
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
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
  await page.route('**/api/queue/list', (r) => r.fulfill({ json: { ok: true, data: { chat_id: 'chat-1', channel: 'web', items: queueItems } } }))
  await page.route('**/api/queue/reorder', async (r) => {
    const body = r.request().postDataJSON() as ReorderCall
    calls.push(body)
    // Server echoes the AUTHORITATIVE snapshot for the requested order.
    const byID = new Map(queueItems.map((i) => [i.msg_id, i]))
    const items = body.msg_ids.map((id) => byID.get(id)).filter(Boolean)
    await r.fulfill({ json: { ok: true, data: { chat_id: 'chat-1', channel: 'web', reordered: true, items } } })
  })
}

async function login(page: Page) {
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
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForTimeout(1500)
}

/** Drag `srcID`'s handle onto `targetID`, landing on its top or bottom half. */
async function dragCard(page: Page, srcID: string, targetID: string, edge: 'before' | 'after') {
  const handle = page.locator(`[data-queue-id="${srcID}"] [data-testid="staging-drag-handle"]`)
  await handle.hover()
  const target = page.locator(`[data-queue-id="${targetID}"]`)
  const hb = await handle.boundingBox()
  const tb = await target.boundingBox()
  if (!hb || !tb) throw new Error('missing bounding box for drag')
  await page.mouse.move(hb.x + hb.width / 2, hb.y + hb.height / 2)
  await page.mouse.down()
  await page.mouse.move(tb.x + tb.width / 2, edge === 'before' ? tb.y + 4 : tb.y + tb.height - 4, { steps: 10 })
  await page.mouse.up()
}

test.describe('staging tray drag-to-reorder', () => {
  test('dragging a card below another commits the new order', async ({ page }) => {
    const calls: ReorderCall[] = []
    await setupMock(page, calls)
    await login(page)

    const tray = page.getByTestId('staging-tray')
    await expect(tray).toBeVisible()
    await page.getByTestId('staging-toggle').click()

    // Cards render in queue order.
    await expect(page.locator('[data-queue-id]')).toHaveCount(3)
    await expect(page.locator('[data-queue-id]').first()).toHaveAttribute('data-queue-id', 'm1')

    await dragCard(page, 'm1', 'm3', 'after')

    await expect.poll(() => calls.length).toBe(1)
    expect(calls[0].msg_ids).toEqual(['m2', 'm3', 'm1'])

    // The tray re-renders from the authoritative snapshot: m1 is now last.
    await expect(page.locator('[data-queue-id]').last()).toHaveAttribute('data-queue-id', 'm1')
  })

  test('a drag that lands back in place sends no reorder request', async ({ page }) => {
    const calls: ReorderCall[] = []
    await setupMock(page, calls)
    await login(page)

    await expect(page.getByTestId('staging-tray')).toBeVisible()
    await page.getByTestId('staging-toggle').click()
    await expect(page.locator('[data-queue-id]')).toHaveCount(3)

    // Drop m1 on its own lower half → no-op.
    await dragCard(page, 'm1', 'm1', 'after')
    await page.waitForTimeout(300)
    expect(calls).toHaveLength(0)

    // Order unchanged.
    await expect(page.locator('[data-queue-id]').first()).toHaveAttribute('data-queue-id', 'm1')
  })
})
