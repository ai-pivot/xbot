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

/** 长队列（12 条）—— 用来验证「展开 = 全量渲染 + 容器内部滚动（有界高度）」。 */
const longQueueItems = Array.from({ length: 12 }, (_, i) => ({
  msg_id: `q${i + 1}`,
  turn_id: 100 + i,
  content: `/goal 继续迭代 ${i + 1}`,
  preview: `/goal 继续迭代 ${i + 1} —— 一条足够长的预览文本用于截断与滚动验证`,
  source: 'user',
  enqueued_at: i + 1,
}))

let seqCounter = 0

/** Inject an SSE frame into the app's mocked EventSource listeners. */
async function emitSSE(page: Page, type: string, data: Record<string, unknown>) {
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

async function setupMock(page: Page, calls: ReorderCall[], items = queueItems) {
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
  await page.route('**/api/queue/list', (r) => r.fulfill({ json: { ok: true, data: { chat_id: 'chat-1', channel: 'web', items } } }))
  await page.route('**/api/queue/reorder', async (r) => {
    const body = r.request().postDataJSON() as ReorderCall
    calls.push(body)
    // Server echoes the AUTHORITATIVE snapshot for the requested order.
    const byID = new Map(items.map((i) => [i.msg_id, i]))
    const ordered = body.msg_ids.map((id) => byID.get(id)).filter(Boolean)
    await r.fulfill({ json: { ok: true, data: { chat_id: 'chat-1', channel: 'web', reordered: true, items: ordered } } })
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

test.describe('staging tray — single expand + structural alignment', () => {
  test.beforeEach(() => {
    seqCounter = 0
  })

  /** 让会话进入 busy（busy 才渲染队首 Next 徽章）。 */
  async function goBusy(page: Page) {
    await emitSSE(page, 'session', {
      type: 'session',
      session: { action: 'busy', chat_id: 'chat-1', channel: 'web' },
    })
    await page.waitForTimeout(200)
  }

  test('Next marker is structurally aligned with the preview text (|Δleft| ≤ 1px)', async ({ page }) => {
    await setupMock(page, [])
    await login(page)
    await goBusy(page)

    const tray = page.getByTestId('staging-tray')
    await expect(tray).toBeVisible()
    await page.getByTestId('staging-toggle').click()

    const headCard = page.locator('[data-queue-id="m1"]')
    const mark = headCard.getByTestId('staging-next-mark')
    const preview = headCard.getByTestId('staging-card-preview')
    await expect(mark).toBeVisible()
    await expect(preview).toBeVisible()

    const markBox = await mark.boundingBox()
    const previewBox = await preview.boundingBox()
    if (!markBox || !previewBox) throw new Error('missing bounding box for the alignment check')

    // 机器判据（取代肉眼）：标记与预览文本左边界一致（同一布局链推导，零缩进魔数）。
    expect(Math.abs(markBox.x - previewBox.x)).toBeLessThanOrEqual(1)
    // 同一条 flex 行 → 垂直中心也一致（内容行没有换行 / 没有第二行）。
    const markMid = markBox.y + markBox.height / 2
    const previewMid = previewBox.y + previewBox.height / 2
    expect(Math.abs(markMid - previewMid)).toBeLessThanOrEqual(1)

    // 结构：标记在队首卡片的内容行里（不是卡片下方的兄弟节点），且是 preview 容器的首个元素。
    expect(
      await mark.evaluate((el) => el.closest('[data-queue-id]')?.getAttribute('data-queue-id')),
    ).toBe('m1')
    expect(await mark.evaluate((el) => el.parentElement?.getAttribute('data-testid'))).toBe('staging-card-preview')
    await expect(headCard.getByTestId('staging-card-row')).toHaveCount(1)
    // 队首左侧 accent 条（2px，结构性「这是队首」标记，不是缩进魔数）。
    expect(await headCard.evaluate((el) => getComputedStyle(el).borderLeftWidth)).toBe('2px')
    // 内容行未换行：行高保持单行量级（配合上面的「垂直中心一致」判据）。
    const rowBox = await headCard.getByTestId('staging-card-row').boundingBox()
    if (!rowBox) throw new Error('missing bounding box for the content row')
    expect(rowBox.height).toBeLessThanOrEqual(44)
    // 旧的「卡片下方兄弟行 + pl-8 缩进」节点已删除。
    await expect(page.getByTestId('staging-next-hint')).toHaveCount(0)
  })

  test('header has exactly two controls (toggle + clear) and no Collapse copy', async ({ page }) => {
    await setupMock(page, [])
    await login(page)
    await expect(page.getByTestId('staging-tray')).toBeVisible()

    const header = page.getByTestId('staging-header')
    await expect(header.locator('button')).toHaveCount(2)
    await page.getByTestId('staging-toggle').click()
    await expect(header.locator('button')).toHaveCount(2)
    // toggle 里恰好一个 chevron（收起/展开的唯一状态图标）。
    await expect(page.getByTestId('staging-toggle').locator('svg.lucide-chevron-down, svg.lucide-chevron-right')).toHaveCount(1)
    // 不得出现任何「收起 / Collapse / 显示全部 / 收起列表」文案。
    const text = (await page.getByTestId('staging-tray').textContent()) ?? ''
    for (const copy of ['Collapse', 'Show all', 'Show fewer', '收起', '显示全部']) {
      expect(text).not.toContain(copy)
    }
  })

  test('expanding renders ALL 12 items inside a bounded, internally scrolling list', async ({ page }) => {
    await setupMock(page, [], longQueueItems)
    await login(page)

    await expect(page.getByTestId('staging-tray')).toBeVisible()
    await page.getByTestId('staging-toggle').click()

    // 唯一展开 = 全量渲染（没有 MAX_VISIBLE 截断、没有第二层「显示全部」）。
    await expect(page.locator('[data-queue-id]')).toHaveCount(12)

    const list = page.getByTestId('staging-list')
    await expect(list).toBeVisible()
    const geo = await list.evaluate((el) => ({
      overflowY: getComputedStyle(el).overflowY,
      clientHeight: el.clientHeight,
      scrollHeight: el.scrollHeight,
      viewportHeight: window.innerHeight,
    }))
    // 有界 + 内部滚动：容器高度 ≤ min(50vh, 22rem=352px)，内容溢出走内部滚动条。
    expect(geo.overflowY).toBe('auto')
    expect(geo.clientHeight).toBeLessThanOrEqual(Math.min(geo.viewportHeight * 0.5, 352) + 1)
    expect(geo.scrollHeight).toBeGreaterThan(geo.clientHeight)

    // 面板整体不顶满视口（列表有界 ⇒ 托盘不会把输入框挤下去）。
    const trayBox = await page.getByTestId('staging-tray').boundingBox()
    if (!trayBox) throw new Error('missing bounding box for the tray')
    expect(trayBox.height).toBeLessThan(geo.viewportHeight * 0.7)
  })
})

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
