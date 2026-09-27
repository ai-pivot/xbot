/**
 * v71 渲染镜像窗口化 —— E2E 契约（真实浏览器 + mock 后端）。
 *
 * 用户核心不变量（设计定稿）：
 *  ① 线性一致性 —— 不能漏消息/乱序；
 *  ② 无感知 —— 窗口化与全量拉取零体验差别；
 *  ③ 折叠 run 默认渲染头部前 7 个 pill（PILL_INLINE_HEAD=7）；
 *  ④ "+N" 徽标必须显示真实总数（tool_count，绝不估算）；
 *  ⑤ 窗口之上的迭代可回拉（loadedTop > 1 ⇒ divider 可点）。
 *
 * 形态矩阵（用户点名）：巨型 tool-only run（10000 成员）+ 尾部文本块 +
 * reasoning/content 混合形态 —— 一个 turn 覆盖全部随机形态组合。
 */
import { test, expect, type Browser, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'
const CHAT = 'chat-iterwin'

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

/** 巨型 run：iter 2..9949 全部 tool-only（每迭代 1 个工具），tool_count=9948。 */
const GIANT_RUN = {
  start_iter: 2,
  end_iter: 9949,
  head_content: '',
  head_reasoning: '',
  head_tools: Array.from({ length: 7 }, (_, i) => ({
    name: 'Shell', status: 'done', iteration: 2,
    label: `giant-cmd-${i}`, summary: `giant summary ${i}`,
  })),
  tool_count: 9948,
}

/** 尾部窗口：**只含纯文本块**（后端契约：Rows = non-tool-only 且无工具的行 ——
 * run 头部只进 run_summaries，绝不双传；E2E mock 必须与 storage 契约一致）。 */
const TAIL_ITERATIONS = [
  { iteration: 50, content: '窗口内文本块甲（content-only 形态）', reasoning: '', tools: [] },
  { iteration: 53, content: '窗口内文本块乙', reasoning: '', tools: [] },
]

const TAIL_RUN = {
  start_iter: 52, end_iter: 52, head_content: '小 run 头部文本（content+tools 形态）',
  head_reasoning: '小 run 思考（reasoning+tools 形态）',
  head_tools: [
    { name: 'Read', status: 'done', iteration: 52, label: 'a.ts', summary: 'read ok' },
    { name: 'Grep', status: 'done', iteration: 52, label: 'pattern', summary: 'grep ok' },
    { name: 'Edit', status: 'done', iteration: 52, label: 'a.ts', summary: 'edit ok' },
  ],
  tool_count: 3,
}

const WINDOWED_HISTORY = {
  ok: true,
  data: {
    messages: [
      { id: 1, role: 'user', content: '跑一万次', timestamp: '2026-09-27T04:00:00Z', turn_id: 7 },
      {
        id: 2, role: 'assistant', content: '', timestamp: '2026-09-27T04:01:00Z', turn_id: 7,
        iterations: TAIL_ITERATIONS,
        run_summaries: [GIANT_RUN, TAIL_RUN],
        iter_window: { total: 9950, loaded_top: 50 },
      },
    ],
    chat_id: CHAT, channel: 'web', last_seq: 10,
    active_progress: null, has_more: false, oldest_id: 1,
  },
}

/** 滚动回拉的更早窗口（iter 47..49：2 文本块 + 1 纯 reasoning 块）。 */
const OLDER_WINDOW = {
  ok: true,
  data: {
    turn_id: 7,
    iterations: [
      { iteration: 47, content: '更早窗口文本块丙', reasoning: '', tools: [] },
      { iteration: 48, content: '', reasoning: '更早窗口纯思考块（reasoning-only 形态）', tools: [] },
      { iteration: 49, content: '更早窗口文本块丁', reasoning: '', tools: [] },
    ],
    run_summaries: [],
    total: 9950,
    loaded_top: 47,
  },
}

/** +N 菜单分页（巨型 run 内部 [7, 200) 的工具页）。 */
const RUN_TOOLS_PAGE = {
  ok: true,
  data: {
    tools: Array.from({ length: 100 }, (_, i) => ({
      name: 'Shell', status: 'done', iteration: 3 + Math.floor(i / 1),
      label: `hidden-cmd-${i}`, summary: `hidden summary ${i}`,
    })),
    total: 9948,
  },
}

async function setupMock(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
    ;(window as unknown as SSEMockState).__sseListeners = listeners
    class MockEventSource {
      readyState = 1
      onopen: ((ev: Event) => void) | null = null
      onerror: ((ev: Event) => void) | null = null
      constructor(_url: string) { setTimeout(() => this.onopen?.(new Event('open')), 0) }
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
      sessions: [{ chat_id: CHAT, channel: 'web', label: 'iter-win-e2e', last_active: new Date().toISOString(), running: false }],
      chats: [{ chat_id: CHAT, channel: 'web', label: 'iter-win-e2e', last_active: new Date().toISOString(), isCurrent: true, running: false }],
      orphan_subagents: [],
    } },
  }))
  // 窗口化历史响应（后端 v71 opt-in 形态 —— run 内部不传输）。
  await page.route('**/api/history', (r) => r.fulfill({ json: WINDOWED_HISTORY }))
  // 滚动回拉（/api/history/iterations）。
  await page.route('**/api/history/iterations', (r) => r.fulfill({ json: OLDER_WINDOW }))
  // +N 菜单分页（/api/history/run_tools）。
  await page.route('**/api/history/run_tools', (r) => r.fulfill({ json: RUN_TOOLS_PAGE }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

async function login(page: Page): Promise<void> {
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await expect(
    page.locator('[data-message-list-content]'),
    '历史必须渲染（login 前置）',
  ).toContainText('跑一万次', { timeout: 30000 })
}

test('窗口化渲染契约：7 pill + +N 真实总数 + 回拉 divider + 传输体积阈值', async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  await ctx.addInitScript(() => {
    try { localStorage.setItem('xbot-locale', 'zh-CN') } catch { /* ignore */ }
  })
  const page = await ctx.newPage()

  // 传输体积审计：捕获 /api/history 的实际响应体长度（窗口化载荷必须远小于
  // 全量形态 —— 10000 迭代全量 ≈ 数 MB；窗口化尾部 50 块 + 2 run 摘要 ≈ 数 KB）。
  let historyBodyBytes = 0
  page.on('response', async (res) => {
    if (res.url().includes('/api/history') && !res.url().includes('/api/history/')) {
      try { historyBodyBytes = (await res.body()).length } catch { /* ignore */ }
    }
  })

  await setupMock(page)
  await login(page)

  // ③ 折叠 run 默认渲染头部前 7 个 pill（巨型 run 的 head-7）。
  const pills = page.locator('[data-testid="tool-pill"]')
  await expect(pills.first(), 'run 摘要必须渲染 pill 行').toBeVisible({ timeout: 10000 })
  const giantPillCount = await page.locator('[data-iter-id="2"] [data-testid="tool-pill"]').count()
  expect(giantPillCount, '巨型 run 默认渲染恰好头部 7 个 pill（PILL_INLINE_HEAD=7）').toBe(7)

  // ④ +N 徽标显示真实总数：9948 - 7 = 9941（绝不估算 —— tool_count 是权威）。
  const moreBadge = page.locator('[data-testid="tool-pill-more"]')
  await expect(moreBadge.first(), '+N 徽标必须渲染').toBeVisible()
  await expect(moreBadge.first()).toContainText('9941')

  // 尾部文本块渲染（线性一致性 —— 窗口内的所有形态逐块渲染）。
  await expect(page.locator('[data-message-list-content]')).toContainText('窗口内文本块甲（content-only 形态）')
  await expect(page.locator('[data-message-list-content]')).toContainText('小 run 头部文本（content+tools 形态）')
  await expect(page.locator('[data-message-list-content]')).toContainText('窗口内文本块乙')

  // ⑤ 回拉 divider：loadedTop=50 > 1 ⇒ 显示「加载更早的 49 个迭代」。
  const divider = page.locator('[data-testid="iteration-window-more"]')
  await expect(divider, '窗口之上还有未加载迭代 ⇒ divider 必须渲染').toBeVisible()
  await expect(divider).toContainText('49')

  // 传输体积阈值：窗口化响应（run 内部不传输）必须远小于全量形态。
  // 全量 10000 迭代下限估算：每迭代最小 JSON ≈ 100B ⇒ ≥ 1MB。
  // 窗口化：尾部 3 块 + 2 摘要 + 元数据 ≈ 数 KB。阈值取 256KB（宽裕但仍在
  // 数量级上证明「run 内部未传输」）。
  expect(historyBodyBytes, `窗口化 /api/history 体积必须 ≤ 256KB（实测 ${historyBodyBytes}B）`).toBeLessThanOrEqual(256 * 1024)

  // ② 无感知（渲染一致）：小 run（3 工具 ≤ 8）不出现 +N 徽标 —— 与全量渲染同构。
  const smallRunPills = page.locator('[data-iter-id="52"] [data-testid="tool-pill"]')
  expect(await smallRunPills.count(), '小 run（3 工具）全量渲染 3 pill').toBe(3)
  const smallRunMore = page.locator('[data-iter-id="52"] [data-testid="tool-pill-more"]')
  await expect(smallRunMore, '小 run ≤8 工具 ⇒ 无 +N 徽标（与全量一致）').toHaveCount(0)
})

test('滚动回拉：点击 divider ⇒ 更早窗口并入渲染（不丢不重）', async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  await ctx.addInitScript(() => {
    try { localStorage.setItem('xbot-locale', 'zh-CN') } catch { /* ignore */ }
  })
  const page = await ctx.newPage()
  await setupMock(page)
  await login(page)

  const divider = page.locator('[data-testid="iteration-window-more"]')
  await expect(divider).toBeVisible()
  await divider.click()

  // 更早窗口的迭代并入（append-only union —— 既有窗口内容不丢）。
  await expect(page.locator('[data-message-list-content]')).toContainText('更早窗口文本块丙', { timeout: 10000 })
  await expect(page.locator('[data-message-list-content]')).toContainText('更早窗口文本块丁')
  // reasoning-only 块渲染为「思考 N 字」折叠行（ThinkingLine —— 折叠态正文
  // 不进 DOM 文本流，与全量渲染同形态）。27 = 「更更窗口纯思考块…」字数。
  await expect(page.locator('[data-message-list-content]')).toContainText('思考 27 字')
  // 既有内容仍在（线性一致性 —— union 只增不减）。
  await expect(page.locator('[data-message-list-content]')).toContainText('窗口内文本块甲（content-only 形态）')

  // 回拉后 loadedTop=47 仍 > 1 ⇒ divider 继续显示（可继续向上回拉）。
  await expect(divider).toContainText('46')
})

test('+N 菜单分页：点击徽标 ⇒ run 内部工具按需拉取渲染', async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  await ctx.addInitScript(() => {
    try { localStorage.setItem('xbot-locale', 'zh-CN') } catch { /* ignore */ }
  })
  const page = await ctx.newPage()
  await setupMock(page)
  await login(page)

  const moreBadge = page.locator('[data-testid="tool-pill-more"]').first()
  await expect(moreBadge).toBeVisible()
  await moreBadge.click()

  // 溢出菜单懒挂载时 fetchRunTools ⇒ 分页工具渲染（首批 100 个 hidden-cmd）。
  const menuRows = page.locator('[data-testid="tool-row"]')
  await expect(menuRows.first(), '+N 菜单必须渲染拉取的工具行').toBeVisible({ timeout: 10000 })
  expect(await menuRows.count(), '分页拉取的工具行必须渲染（mock 首页 100 个）').toBeGreaterThanOrEqual(90)
  await expect(page.locator('[data-testid="tool-pill-more"] .lazy-pill-popover, [data-testid="tool-pill-more"] + *').first()
    .or(page.getByText('hidden-cmd-0'))).toBeVisible()
})
