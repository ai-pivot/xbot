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

/**
 * 60 条队列 —— 验证「长队列下拖到容器边缘会自动滚动，从而能把条目拖到
 * 初始可见窗口之外」。实测几何：clientHeight≈352 / scrollHeight≈2760。
 */
const hugeQueueItems = Array.from({ length: 60 }, (_, i) => ({
  msg_id: `q${i + 1}`,
  turn_id: 200 + i,
  content: `/goal 继续迭代 ${i + 1}`,
  preview: `/goal 继续迭代 ${i + 1} —— 一条足够长的预览文本用于截断与滚动验证`,
  source: 'user',
  enqueued_at: i + 1,
}))

/**
 * 6 条短队列 —— 全程可见（无内部滚动），用来断言「松手**之前** DOM 顺序就已经是
 * 预览顺序」（拖动中布局真的动了，而不是只有一条插入线在跳）。
 */
const livePreviewItems = Array.from({ length: 6 }, (_, i) => ({
  msg_id: `m${i + 1}`,
  turn_id: 11 + i,
  content: `message ${i + 1}`,
  preview: `message ${i + 1}`,
  source: 'user',
  enqueued_at: i + 1,
}))

/** 列表容器当前的 scrollTop（自动滚动的观测量）。 */
async function listScrollTop(page: Page): Promise<number> {
  return page.getByTestId('staging-list').evaluate((el) => (el as HTMLElement).scrollTop)
}

/** 当前**完整**落在列表可视窗口内的卡片 msg_id（按 DOM 顺序）。 */
async function visibleCardIDs(page: Page): Promise<string[]> {
  return page.getByTestId('staging-list').evaluate((node) => {
    const list = node as HTMLElement
    const lb = list.getBoundingClientRect()
    return Array.from(list.querySelectorAll('[data-queue-id]'))
      .filter((c) => {
        const r = c.getBoundingClientRect()
        return r.top >= lb.top - 1 && r.bottom <= lb.bottom + 1
      })
      .map((c) => c.getAttribute('data-queue-id') ?? '')
  })
}

/** 在拖拽柄上按下（指针停在卡片处，**尚未进入**边缘热区）。 */
async function pressHandle(page: Page, srcID: string) {
  // 走 beginDrag：按压前有 elementFromPoint 命中自检 + 幽灵确认 + 失败重试，
  // 而不是"按下就祈祷 pointerdown 落在卡片上"。命中落空时后续 pointermove 会被
  // handleDragMove 的 `if (!sessionRef.current) return` 丢掉，自动滚动根本不会
  // 启动 —— CI 上那条 flaky（pointerup in edge hot zone）就是这个形态。
  await beginDrag(page, srcID)
  const lb = await page.getByTestId('staging-list').boundingBox()
  if (!lb) throw new Error('missing bounding box for the staging list')
  return { lb }
}

/** 等到 scrollTop 连续两次采样相同（自动滚动/重排已落定）。 */
async function waitScrollSettled(page: Page, rounds = 25): Promise<number> {
  let prev = await listScrollTop(page)
  for (let i = 0; i < rounds; i++) {
    await page.waitForTimeout(200)
    const cur = await listScrollTop(page)
    if (cur === prev) return cur
    prev = cur
  }
  return prev
}

/** 把指针移到容器的上沿/下沿热区（距内沿 8px ≤ 组件 EDGE_ZONE_PX=24）并保持。 */
async function moveToEdge(page: Page, lb: { x: number; y: number; width: number; height: number }, edge: 'top' | 'bottom') {
  const edgeY = edge === 'bottom' ? lb.y + lb.height - 8 : lb.y + 8
  await page.mouse.move(lb.x + lb.width / 2, edgeY, { steps: 8 })
}

/** 连续采样 scrollTop，直到满足 stop 或超时；用于观察自动滚动的单调性。 */
async function sampleScroll(page: Page, stop: (v: number) => boolean, ms = 6000): Promise<number[]> {
  const samples: number[] = []
  const deadline = Date.now() + ms
  while (Date.now() < deadline) {
    const cur = await listScrollTop(page)
    samples.push(cur)
    if (stop(cur)) break
    await page.waitForTimeout(60)
  }
  return samples
}

/** 列表里 `[data-queue-id]` 的 DOM 顺序（拖拽中 = **预览顺序**）。 */
async function domOrder(page: Page): Promise<string[]> {
  return page.getByTestId('staging-list').evaluate((node) =>
    Array.from(node.querySelectorAll('[data-queue-id]')).map((c) => c.getAttribute('data-queue-id') ?? ''),
  )
}

/** 被拖项在列表 DOM 里的序号（拖拽中 = 预览位置）。 */
async function domIndexOf(page: Page, id: string): Promise<number> {
  return (await domOrder(page)).indexOf(id)
}

/** 列表内容总高（拖动期间必须稳定 —— 变化 = "跳一下"）。 */
async function listScrollHeight(page: Page): Promise<number> {
  return page.getByTestId('staging-list').evaluate((el) => (el as HTMLElement).scrollHeight)
}

/** 连续采样 scrollTop + 被拖项的预览序号（滚动期间预览是否随指针推进）。 */
async function sampleScrollAndIndex(page: Page, id: string, stop: (v: number) => boolean, ms = 6000) {
  const scrolls: number[] = []
  const positions: number[] = []
  const deadline = Date.now() + ms
  while (Date.now() < deadline) {
    scrolls.push(await listScrollTop(page))
    positions.push(await domIndexOf(page, id))
    if (stop(scrolls[scrolls.length - 1])) break
    await page.waitForTimeout(60)
  }
  return { scrolls, positions }
}

/** 按住 `srcID` 的拖拽柄，**分步**把指针移到 (toX,toY)，每一步后采样列表总高。
 *  返回采样序列 —— 用来证明"拖动全程列表总高稳定（≤1px）"。 */
async function pressAndDragInSteps(
  page: Page,
  srcID: string,
  toX: number,
  toY: number,
  steps = 12,
  settle?: { targetID: string; edge: 'before' | 'after'; want: string },
) {
  // 同 pressHandle：按压必须真的命中（含自检与重试），否则整段拖拽是空操作。
  const { x: fromX, y: fromY } = await beginDrag(page, srcID)
  const heights = [await listScrollHeight(page)]
  for (let s = 1; s <= steps; s++) {
    await page.mouse.move(fromX + ((toX - fromX) * s) / steps, fromY + ((toY - fromY) * s) / steps)
    heights.push(await listScrollHeight(page))
  }
  if (settle) {
    // 传入的 toX/toY 是**按下那一刻**的目标几何。拖动期间列表会按预览顺序重排
    // （被拖卡片让位、目标卡片位移），固定落点可能已经指向别的卡片或空白 ——
    // 那就白拖了（CI 上「reverse direction」实测过：顺序完全没变）。
    // 这里用**当前**几何重取目标位置补移动，直到预览顺序真的等于 want。
    for (let attempt = 0; attempt < 3; attempt++) {
      if (await waitForOrder(page, settle.want, 1200)) break
      const tb = await page.locator(`[data-queue-id="${settle.targetID}"]`).boundingBox()
      if (!tb) break
      await page.mouse.move(
        tb.x + tb.width / 2,
        settle.edge === 'before' ? tb.y + 4 : tb.y + tb.height - 4,
        { steps: 4 },
      )
      heights.push(await listScrollHeight(page))
    }
  }
  return { heights }
}

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

/** 把 srcID 插到 targetID 前/后得到的顺序（与组件里的 computeReorder 等价）。 */
function withMoved(ids: string[], srcID: string, targetID: string, edge: 'before' | 'after'): string[] {
  const without = ids.filter((x) => x !== srcID)
  const at = without.indexOf(targetID)
  if (at < 0) return ids
  const insertAt = edge === 'before' ? at : at + 1
  return [...without.slice(0, insertAt), srcID, ...without.slice(insertAt)]
}

/** 轮询直到 DOM 顺序等于 want（松手前必须成立，见 dragCard 注释）。 */
async function waitForOrder(page: Page, want: string, ms: number): Promise<boolean> {
  const deadline = Date.now() + ms
  while (Date.now() < deadline) {
    if ((await domOrder(page)).join(' ') === want) return true
    await page.waitForTimeout(50)
  }
  return false
}

/** 按住 srcID 的拖拽柄，确认拖拽会话真的建立（幽灵出现）。失败会重来几次。
 *
 *  为什么需要自检 + 重来：`mouse.move(x,y)` 用的是**取坐标那一刻**的布局，
 *  而 scrollIntoViewIfNeeded、队列快照重渲染、上一张卡出入场动画都会在取坐标
 *  之后继续动 —— 按压落在别的元素上时 pointerdown 不会进 handleHandleDown，
 *  幽灵永不出现，后续所有 move 都是空操作。2026-11-10 CI 上那条 flaky 就是
 *  这个形态（等 ghost 10s 超时），不是落点算错。 */
async function beginDrag(page: Page, srcID: string): Promise<{ x: number; y: number }> {
  const handle = page.locator(`[data-queue-id="${srcID}"] [data-testid="staging-drag-handle"]`)
  const ghost = page.getByTestId('staging-drag-ghost')
  let lastErr: unknown = null

  for (let attempt = 0; attempt < 3; attempt++) {
    await handle.scrollIntoViewIfNeeded()
    const hb = await handle.boundingBox()
    if (!hb) {
      lastErr = new Error(`no bounding box for the drag handle of ${srcID}`)
      continue
    }
    const x = hb.x + hb.width / 2
    const y = hb.y + hb.height / 2
    await page.mouse.move(x, y)

    // 命中自检：这个坐标必须真的落在这一行卡片上。
    const hit = await page.evaluate(
      ([px, py]) =>
        document
          .elementFromPoint(px, py)
          ?.closest('[data-queue-id]')
          ?.getAttribute('data-queue-id') ?? null,
      [x, y] as [number, number],
    )
    if (hit !== srcID) {
      lastErr = new Error(`press point hit ${hit ?? 'nothing'} instead of ${srcID}`)
      continue
    }

    await page.mouse.down()
    if (await ghost.isVisible({ timeout: 2000 }).catch(() => false)) {
      return { x, y }
    }
    // pointerdown 没建立拖拽会话：松开再试。
    await page.mouse.up()
    lastErr = new Error('pointerdown did not start a drag session')
  }
  throw lastErr ?? new Error('drag did not start')
}

/** Drag `srcID`'s handle onto `targetID`, landing on its top or bottom half.
 *
 *  松手前必须确认预览顺序**已经**等于目标顺序：handleDragEnd 读的是拖拽中累积的
 *  orderRef，落点没生效就松手 ⇒ 提交被判定为 no-op ⇒ 请求根本不发。
 *
 *  expectReorder=false 用于「拖回原位」的 no-op 用例：那时顺序本来就不该变。 */
async function dragCard(
  page: Page,
  srcID: string,
  targetID: string,
  edge: 'before' | 'after',
  expectReorder = true,
) {
  const want = withMoved(await domOrder(page), srcID, targetID, edge).join(' ')

  await beginDrag(page, srcID)

  for (let attempt = 0; attempt < 3; attempt++) {
    // 目标几何每次移动前重取：拖动期间列表按预览顺序重排，旧坐标会指向别的卡。
    const tb = await page.locator(`[data-queue-id="${targetID}"]`).boundingBox()
    if (!tb) throw new Error(`missing bounding box for ${targetID}`)
    await page.mouse.move(tb.x + tb.width / 2, edge === 'before' ? tb.y + 4 : tb.y + tb.height - 4, {
      steps: 8,
    })
    if (!expectReorder) break
    if (await waitForOrder(page, want, 1500)) break
  }

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
    await dragCard(page, 'm1', 'm1', 'after', false)
    await page.waitForTimeout(300)
    expect(calls).toHaveLength(0)

    // Order unchanged.
    await expect(page.locator('[data-queue-id]').first()).toHaveAttribute('data-queue-id', 'm1')
  })
})

/**
 * **松手前就实时重排**（live preview）+ 拖起态（幽灵 / 占位槽）。
 *
 * 缺口（用户报的"显示 bug / 效果很差"）：旧实现拖拽期间布局完全不动，只渲染一条 2px
 * 插入线；指针压到卡片之间的间隙或被拖卡片自己时，落点还会被清空（"线在抖"），用户
 * 无法预判松手后会变成什么样。本组断言：**松手之前** DOM 顺序就已经是预览顺序。
 *
 * 红灯（未修复时）：拖动中 `[data-queue-id]` 顺序恒为初始顺序 —— 第一条断言即失败。
 */
test.describe('staging tray drag-to-reorder — live preview while still holding', () => {
  test('dragging the 1st card below the 4th reorders the DOM before release (ghost + slot + stable height)', async ({ page }) => {
    const calls: ReorderCall[] = []
    await setupMock(page, calls, livePreviewItems)
    await login(page)

    await expect(page.getByTestId('staging-tray')).toBeVisible()
    await page.getByTestId('staging-toggle').click()
    await expect(page.locator('[data-queue-id]')).toHaveCount(6)
    expect(await domOrder(page)).toEqual(['m1', 'm2', 'm3', 'm4', 'm5', 'm6'])

    const cardBox = await page.locator('[data-queue-id="m1"]').boundingBox()
    const targetBox = await page.locator('[data-queue-id="m4"]').boundingBox()
    if (!cardBox || !targetBox) throw new Error('missing bounding box for the drag')
    const cardH = cardBox.height
    // 目标点 = 第 4 张卡的**下半**（按下那一刻的几何；松手前指针不再移动）
    const toX = targetBox.x + targetBox.width / 2
    const toY = targetBox.y + targetBox.height - 4

    const { heights } = await pressAndDragInSteps(page, 'm1', toX, toY, 12, {
      targetID: 'm4',
      edge: 'after',
      want: ['m2', 'm3', 'm4', 'm1', 'm5', 'm6'].join(' '),
    })

    // ① **松手前** DOM 顺序 = 预览顺序：第 1 张已落到第 4 张之后（其它卡片让位）。
    await expect.poll(() => domOrder(page)).toEqual(['m2', 'm3', 'm4', 'm1', 'm5', 'm6'])
    const order = await domOrder(page)
    expect(order.indexOf('m1')).toBeGreaterThan(order.indexOf('m4'))
    expect(order.indexOf('m1')).toBe(3)
    // 还没松手 ⇒ 还没发请求（一次手势一次请求）。
    expect(calls).toHaveLength(0)

    // ② 占位槽：可见、就地、高度 == 原卡片高度（≤1px）、虚线。
    const slot = page.getByTestId('staging-placeholder')
    await expect(slot).toBeVisible()
    await expect(slot).toHaveAttribute('data-queue-id', 'm1')
    const slotBox = await slot.boundingBox()
    if (!slotBox) throw new Error('missing bounding box for the slot')
    expect(Math.abs(slotBox.height - cardH)).toBeLessThanOrEqual(1)
    expect(await slot.evaluate((el) => getComputedStyle(el).borderStyle)).toBe('dashed')

    // ③ 幽灵：存在、fixed、pointer-events:none、与卡片等大、**跟手**（指针在幽灵盒内）。
    const ghost = page.getByTestId('staging-drag-ghost')
    await expect(ghost).toBeVisible()
    expect(await ghost.evaluate((el) => getComputedStyle(el).position)).toBe('fixed')
    expect(await ghost.evaluate((el) => getComputedStyle(el).pointerEvents)).toBe('none')
    const ghostBox = await ghost.boundingBox()
    if (!ghostBox) throw new Error('missing bounding box for the ghost')
    // 幽灵与卡片是**同一个盒子**：布局尺寸相等（offsetHeight 是布局量，不含 scale）。
    const ghostLayoutH = await ghost.evaluate((el) => (el as HTMLElement).offsetHeight)
    expect(Math.abs(ghostLayoutH - cardH)).toBeLessThanOrEqual(1)
    // 视觉上略大（"被拿起"的轻微 scale，≤ +5%）——不是尺寸走样。
    expect(ghostBox.height).toBeGreaterThan(cardH - 1)
    expect(ghostBox.height).toBeLessThan(cardH * 1.05 + 1)
    // 幽灵与占位槽**同一列**（横向不跟手）⇒ 不会和槽错开成两个并排的盒子。
    // 宽度比布局量（offsetWidth 不含 scale）；水平位置比**视觉中心**（scale 绕中心
    // 放大 ⇒ 中心不变，而 scale 后的 left/right 会各自外扩 ~5px，不能直接比）。
    const ghostLayoutW = await ghost.evaluate((el) => (el as HTMLElement).offsetWidth)
    expect(Math.abs(ghostLayoutW - slotBox.width)).toBeLessThanOrEqual(1)
    expect(
      Math.abs(ghostBox.x + ghostBox.width / 2 - (slotBox.x + slotBox.width / 2)),
    ).toBeLessThanOrEqual(1)
    // 幽灵**跟着指针**：盒子中心 = 指针位置（按下时定死的抓手偏移全程不变）。
    expect(Math.abs(ghostBox.y + ghostBox.height / 2 - toY)).toBeLessThanOrEqual(2)
    expect(toX).toBeGreaterThanOrEqual(ghostBox.x - 1)
    expect(toX).toBeLessThanOrEqual(ghostBox.x + ghostBox.width + 1)
    expect(toY).toBeGreaterThanOrEqual(ghostBox.y - 1)
    expect(toY).toBeLessThanOrEqual(ghostBox.y + ghostBox.height + 1)
    // 幽灵在列表之外（portal 到 body）⇒ 不参与列表布局、不被滚动容器裁剪。
    expect(await ghost.evaluate((el) => el.closest('[data-testid="staging-list"]'))).toBeNull()

    // ④ 拖动全程列表总高稳定（≤1px）—— 没有"跳一下"的显示 bug。
    expect(Math.max(...heights) - Math.min(...heights)).toBeLessThanOrEqual(1)

    // ⑤ 松手：只发 1 次请求，请求体 == 松手前预览到的那份顺序（松手前就定好了）。
    const previewedBeforeRelease = await domOrder(page)
    await page.mouse.up()
    await expect.poll(() => calls.length).toBe(1)
    expect(calls[0].msg_ids).toEqual(previewedBeforeRelease)
    expect(calls[0].msg_ids).toEqual(['m2', 'm3', 'm4', 'm1', 'm5', 'm6'])
    // 拖起态收拾干净，顺序保持（提交顺序先留着渲染，服务端快照回来前不闪回旧序）。
    await expect(page.getByTestId('staging-drag-ghost')).toHaveCount(0)
    await expect(page.getByTestId('staging-placeholder')).toHaveCount(0)
    await expect.poll(() => domOrder(page)).toEqual(['m2', 'm3', 'm4', 'm1', 'm5', 'm6'])
  })

  test('dragging the last card above the 1st reorders the DOM before release too (reverse direction)', async ({ page }) => {
    const calls: ReorderCall[] = []
    await setupMock(page, calls, livePreviewItems)
    await login(page)

    await expect(page.getByTestId('staging-tray')).toBeVisible()
    await page.getByTestId('staging-toggle').click()
    await expect(page.locator('[data-queue-id]')).toHaveCount(6)

    const cardBox = await page.locator('[data-queue-id="m6"]').boundingBox()
    const targetBox = await page.locator('[data-queue-id="m1"]').boundingBox()
    if (!cardBox || !targetBox) throw new Error('missing bounding box for the drag')
    const cardH = cardBox.height
    const toX = targetBox.x + targetBox.width / 2
    const toY = targetBox.y + 4 // 第 1 张卡的**上半** → 插到最前

    const { heights } = await pressAndDragInSteps(page, 'm6', toX, toY, 12, {
      targetID: 'm1',
      edge: 'before',
      want: ['m6', 'm1', 'm2', 'm3', 'm4', 'm5'].join(' '),
    })

    // 2026-10-22 CI：拖拽后的 DOM 重排可能慢于 poll 默认 5s（CI runner 慢 +
    // pointer 事件分步派发）。放宽等待，不断言放宽：顺序必须完全一致。
    await expect
      .poll(() => domOrder(page), { timeout: 20000, intervals: [200, 500, 1000] })
      .toEqual(['m6', 'm1', 'm2', 'm3', 'm4', 'm5'])
    const order = await domOrder(page)
    expect(order.indexOf('m6')).toBeLessThan(order.indexOf('m1'))
    expect(order[0]).toBe('m6')
    expect(calls).toHaveLength(0)

    const slotBox = await page.getByTestId('staging-placeholder').boundingBox()
    if (!slotBox) throw new Error('missing bounding box for the slot')
    expect(Math.abs(slotBox.height - cardH)).toBeLessThanOrEqual(1)
    expect(Math.max(...heights) - Math.min(...heights)).toBeLessThanOrEqual(1)

    const previewedBeforeRelease = await domOrder(page)
    await page.mouse.up()
    await expect.poll(() => calls.length).toBe(1)
    expect(calls[0].msg_ids).toEqual(previewedBeforeRelease)
    expect(calls[0].msg_ids).toEqual(['m6', 'm1', 'm2', 'm3', 'm4', 'm5'])
  })
})

/**
 * 长队列（60 条）下的**边缘自动滚动**。
 *
 * 缺口：拖拽走 pointer 事件 + setPointerCapture（不是原生滚动），浏览器不会替我们滚
 * ⇒ 指针停在容器下沿而 scrollTop 恒为 0，条目永远拖不出可见窗口。
 * 本组用真实鼠标事件复现：按住拖拽柄 → 指针停在下/上沿热区并保持 → 观察 scrollTop。
 */
test.describe('staging tray drag-to-reorder — long queue edge auto-scroll', () => {
  test.beforeEach(() => {
    seqCounter = 0
  })

  test('holding at the list bottom edge auto-scrolls and drops the card outside the initial window', async ({ page }) => {
    const calls: ReorderCall[] = []
    await setupMock(page, calls, hugeQueueItems)
    await login(page)
    await expect(page.getByTestId('staging-tray')).toBeVisible()
    await page.getByTestId('staging-toggle').click()

    const list = page.getByTestId('staging-list')
    await expect(list).toBeVisible()
    await expect(page.locator('[data-queue-id]')).toHaveCount(60)

    const geo = await list.evaluate((node) => {
      const l = node as HTMLElement
      return { clientHeight: l.clientHeight, scrollHeight: l.scrollHeight, scrollTop: l.scrollTop }
    })
    expect(geo.scrollHeight).toBeGreaterThan(geo.clientHeight) // 长队列 ⇒ 必然内部滚动
    expect(geo.scrollTop).toBe(0)

    const initiallyVisible = await visibleCardIDs(page)
    expect(initiallyVisible.length).toBeGreaterThan(2)

    // 按下第 1 张卡（指针仍在列表内、未进热区）——此时不应有任何滚动
    const { lb } = await pressHandle(page, 'q1')
    expect(await listScrollTop(page)).toBe(0)
    // 把指针移到容器下沿热区（距内沿 8px ≤ 24px）并保持
    await moveToEdge(page, lb, 'bottom')

    // 指针一动不动 —— rAF 自动滚动必须启动：scrollTop 单调不减，且最终滚过一整屏。
    // 同时观察**预览顺序**：指针没动、但滚动让「指针下方的卡」一路往后 ⇒ 被拖项在
    // 预览里的序号必须随滚动推进（不是停在初始位置 0）。
    const { scrolls: samples, positions } = await sampleScrollAndIndex(page, 'q1', (v) => v > geo.clientHeight)
    // ⚠️ 只断言**数值行为**（滚了多远 / 单调 / 落点），不断言"采样次数"：
    // 采样是固定节奏循环（60ms/次），CI 更慢时会少收几个样本，甚至首个样本就满足
    // stop 条件而立刻 break —— 那是采样节奏的副产物，不是不变量（曾因此在 CI 假红）。
    expect(Math.max(...samples)).toBeGreaterThan(geo.clientHeight)
    for (let i = 1; i < samples.length; i++) expect(samples[i]).toBeGreaterThanOrEqual(samples[i - 1])
    expect(samples[samples.length - 1]).toBeGreaterThan(0) // ← 未修复时这里恒为 0（红灯）
    expect(samples[samples.length - 1]).toBeGreaterThan(geo.clientHeight)
    // ← 未修复时预览顺序恒为 0（这一条是"松手前实时重排"在自动滚动场景的红灯）
    for (let i = 1; i < positions.length; i++) expect(positions[i]).toBeGreaterThanOrEqual(positions[i - 1])
    // 首个采样可能已被「指针移到边缘」这次 move 推进（positions[0] 不是 0），
    // 不能拿 positions[0] 当基线 —— 用拖拽前的初始位置 0；下一条仍要求越过初始窗口。
    expect(positions[positions.length - 1]).toBeGreaterThan(0)
    expect(positions[positions.length - 1]).toBeGreaterThan(initiallyVisible.length)

    // 松手：落到「此刻可见的最后一张卡」的下半 ⇒ 落点在**初始可见窗口之外**
    const visibleNow = await visibleCardIDs(page)
    const lastVisible = visibleNow[visibleNow.length - 1]
    expect(lastVisible).toBeTruthy()
    const tb = await page.locator(`[data-queue-id="${lastVisible}"]`).boundingBox()
    if (!tb) throw new Error('missing bounding box for the last visible card')
    await page.mouse.move(tb.x + tb.width / 2, tb.y + tb.height - 4)
    await page.mouse.up()

    await expect.poll(() => calls.length).toBe(1)
    expect(calls[0].msg_ids).toHaveLength(60)
    const movedIndex = calls[0].msg_ids.indexOf('q1')
    expect(movedIndex).toBeGreaterThan(initiallyVisible.length) // 真的拖到了屏幕外

    // 拖拽结束后不再继续滚动（防 rAF 泄漏）：先让落定后的重渲染稳定，再连续观测两窗
    await page.waitForTimeout(300)
    const settled = await listScrollTop(page)
    await page.waitForTimeout(300)
    expect(await listScrollTop(page)).toBe(settled)
  })

  test('holding at the list top edge scrolls back up (reverse direction)', async ({ page }) => {
    const calls: ReorderCall[] = []
    await setupMock(page, calls, hugeQueueItems)
    await login(page)
    await expect(page.getByTestId('staging-tray')).toBeVisible()
    await page.getByTestId('staging-toggle').click()
    await expect(page.locator('[data-queue-id]')).toHaveCount(60)

    // 先制造一段可回滚的滚动量（同时让想拖的卡进入窗口）
    const list = page.getByTestId('staging-list')
    await list.evaluate((node) => {
      ;(node as HTMLElement).scrollTop = 300
    })
    const before = await listScrollTop(page)
    expect(before).toBeGreaterThan(0)

    const visible = await visibleCardIDs(page)
    const dragged = visible[visible.length - 1]
    expect(dragged).toBeTruthy()

    await pressHandle(page, dragged).then(({ lb }) => moveToEdge(page, lb, 'top'))

    const samples = await sampleScroll(page, (v) => v === 0)
    // 同上：不断言"采样次数"（CI 更慢会少收样本、甚至首个样本即满足 stop），只断言数值行为。
    expect(Math.min(...samples)).toBeLessThanOrEqual(before)
    for (let i = 1; i < samples.length; i++) expect(samples[i]).toBeLessThanOrEqual(samples[i - 1])
    expect(samples[samples.length - 1]).toBeLessThan(before) // ← 未修复时恒等于 before（红灯）
    expect(samples[samples.length - 1]).toBe(0) // 一路滚到顶

    // 落到此刻第一张可见卡的上半 ⇒ 被拖的卡挪到窗口顶部
    const visibleNow = await visibleCardIDs(page)
    const firstVisible = visibleNow[0]
    const fb = await page.locator(`[data-queue-id="${firstVisible}"]`).boundingBox()
    if (!fb) throw new Error('missing bounding box for the first visible card')
    await page.mouse.move(fb.x + fb.width / 2, fb.y + 8)
    await page.mouse.up()

    await expect.poll(() => calls.length).toBe(1)
    const movedIndex = calls[0].msg_ids.indexOf(dragged)
    const originalIndex = hugeQueueItems.findIndex((i) => i.msg_id === dragged)
    expect(movedIndex).toBeLessThan(originalIndex)
  })

  test('pointerup while still inside the edge hot zone stops the loop (no rAF leak)', async ({ page }) => {
    const calls: ReorderCall[] = []
    await setupMock(page, calls, hugeQueueItems)
    await login(page)
    await expect(page.getByTestId('staging-tray')).toBeVisible()
    await page.getByTestId('staging-toggle').click()
    await expect(page.locator('[data-queue-id]')).toHaveCount(60)

    const { lb } = await pressHandle(page, 'q1')
    await moveToEdge(page, lb, 'bottom')
    // 自动滚动确实起来了
    await expect.poll(() => listScrollTop(page), { timeout: 5000 }).toBeGreaterThan(100)

    // 指针**仍停在热区内**直接松手 —— 循环必须停。
    await page.mouse.up()
    // 落定后按新顺序重渲染会让 scrollTop 小幅移动（与 rAF 泄漏无关）。先等它
    // 连续两次采样一致（慢机器上固定 300ms 可能不够 —— 这就是本用例在 CI 上
    // flaky 的原因），再按判据观测「pointerup 后不再变化」。
    const settled = await waitScrollSettled(page)
    expect(settled).toBeGreaterThan(0)
    await page.waitForTimeout(300)
    expect(await listScrollTop(page)).toBe(settled)
    await page.waitForTimeout(300)
    expect(await listScrollTop(page)).toBe(settled)
  })
})
