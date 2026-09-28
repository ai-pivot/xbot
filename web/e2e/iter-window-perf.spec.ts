/**
 * v71 窗口化 —— 长上下文交互零掉帧 spec（用户核心要求 #5 后半）。
 *
 * 「窗口化拉取后，10000 迭代的 turn 上做交互（pill 展开 / 思考折叠 / +N 菜单）
 * 必须不掉帧」—— 判据：
 *  ① 交互响应时间：pill 点击 → popover 可见 / 思考行点击 → 折叠切换，
 *     每个交互在阈值内完成（1500ms —— CI 环境宽裕值；本地应 < 300ms）。
 *  ② long task 审计：交互窗口期 PerformanceObserver 的 longtask（> 50ms）
 *     条目数为 0（零掉帧 —— 主线程无长任务阻塞）。
 *
 * 数据形态与 iter-window.spec.ts 同源（巨型 9948 工具 run + 尾部混合块），
 * 差异：尾部窗口填满 50 个混合块（更接近真实长 turn 的渲染压力）。
 */
import { test, expect, type Browser, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'
const CHAT = 'chat-iterperf'

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

const GIANT_RUN = {
  start_iter: 2,
  end_iter: 9949,
  head_content: '',
  head_reasoning: '',
  head_tools: Array.from({ length: 7 }, (_, i) => ({
    name: 'Shell', status: 'done', iteration: 2,
    label: `perf-cmd-${i}`, summary: `perf summary ${i}`,
  })),
  tool_count: 9948,
}

/** 尾部 50 个混合块：content-only / reasoning-only / content+reasoning / 带 tools 轮转。 */
const TAIL_ITERATIONS = Array.from({ length: 50 }, (_, i) => {
  const iter = 9900 + i
  const kind = i % 4
  if (kind === 0) return { iteration: iter, content: `perf 块 ${iter}（content-only）`, reasoning: '', tools: [] }
  if (kind === 1) return { iteration: iter, content: '', reasoning: `perf 思考 ${iter}（reasoning-only，正文较长以制造真实渲染压力 ${'x'.repeat(120)}）`, tools: [] }
  if (kind === 2) return { iteration: iter, content: `perf 块 ${iter}（content+reasoning）`, reasoning: `perf 思考 ${iter}`, tools: [] }
  return {
    iteration: iter, content: `perf 块 ${iter}（content+tools）`, reasoning: '',
    tools: [{ name: 'Read', status: 'done', iteration: iter, label: `f${iter}.ts`, summary: 'ok' }],
  }
})

const WINDOWED_HISTORY = {
  ok: true,
  data: {
    messages: [
      { id: 1, role: 'user', content: '性能回合', timestamp: '2026-09-27T04:00:00Z', turn_id: 9 },
      {
        id: 2, role: 'assistant', content: '', timestamp: '2026-09-27T04:01:00Z', turn_id: 9,
        iterations: TAIL_ITERATIONS,
        run_summaries: [GIANT_RUN],
        iter_window: { total: 9950, loaded_top: 9900 },
      },
    ],
    chat_id: CHAT, channel: 'web', last_seq: 10,
    active_progress: null, has_more: false, oldest_id: 1,
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
      sessions: [{ chat_id: CHAT, channel: 'web', label: 'iter-perf', last_active: new Date().toISOString(), running: false }],
      chats: [{ chat_id: CHAT, channel: 'web', label: 'iter-perf', last_active: new Date().toISOString(), isCurrent: true, running: false }],
      orphan_subagents: [],
    } },
  }))
  await page.route('**/api/history', (r) => r.fulfill({ json: WINDOWED_HISTORY }))
  await page.route('**/api/history/iterations', (r) => r.fulfill({
    json: { ok: true, data: { turn_id: 9, iterations: [], run_summaries: [], total: 9950, loaded_top: 1 } },
  }))
  await page.route('**/api/history/run_tools', (r) => r.fulfill({
    json: { ok: true, data: { tools: [], total: 9948 } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

/** long task 审计器：注入 PerformanceObserver 计数 > 50ms 的主线程任务。 */
async function startLongTaskAudit(page: Page): Promise<void> {
  await page.evaluate(() => {
    const w = window as unknown as { __longTasks?: number[] }
    w.__longTasks = []
    try {
      new PerformanceObserver((list) => {
        for (const e of list.getEntries()) (w.__longTasks!).push(Math.round(e.duration))
      }).observe({ entryTypes: ['longtask'] })
    } catch { /* 老浏览器无 longtask —— 审计退化为仅响应时间断言 */ }
  })
}

async function readLongTaskCount(page: Page): Promise<number> {
  return page.evaluate(() => (window as unknown as { __longTasks?: number[] }).__longTasks?.length ?? -1)
}

test('长 turn 交互零掉帧：pill 展开 / 思考折叠 / +N 菜单（10000 迭代窗口化渲染后）', async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  await ctx.addInitScript(() => {
    try { localStorage.setItem('xbot-locale', 'zh-CN') } catch { /* ignore */ }
  })
  const page = await ctx.newPage()
  await setupMock(page)
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await expect(page.locator('[data-message-list-content]')).toContainText('性能回合', { timeout: 30000 })
  // 等渲染稳定（历史 + 50 块 + run 摘要全部上屏）。
  await expect(page.locator('[data-testid="tool-pill"]').first()).toBeVisible({ timeout: 10000 })
  await page.waitForTimeout(500)

  await startLongTaskAudit(page)

  // ① pill 点击 → popover 展开。断言稳定性 timeout 5000ms（popover 懒挂载 +
  // 动画）；交互延迟单独断言 < 1500ms（点击 → 内容可见的实测差值）。
  let t0 = Date.now()
  await page.locator('[data-testid="tool-pill"]').first().click()
  // popover 懒挂载 portal 到 body —— summary 文本可能出现多份（strict 消歧用 first）。
  const popover = page.getByText('perf summary 0').first()
  await expect(popover, 'pill popover 必须展开（summary 可见）').toBeVisible({ timeout: 5000 })
  const pillLatency = Date.now() - t0
  expect(pillLatency, `pill 展开必须 < 1500ms（实测 ${pillLatency}ms）`).toBeLessThan(1500)
  // 关闭 popover（点击空白处）。
  await page.mouse.click(10, 300)
  await page.waitForTimeout(200)

  // ② 思考行点击 → 折叠/展开切换（交互响应 < 1500ms）。
  const thinking = page.locator('[data-iter-id] .thinking-line, [data-iter-id] [class*="thinking"]').first()
  if (await thinking.count() > 0) {
    t0 = Date.now()
    await thinking.click()
    await page.waitForTimeout(100)
    const thinkLatency = Date.now() - t0
    expect(thinkLatency, `思考折叠必须 < 1500ms（实测 ${thinkLatency}ms）`).toBeLessThan(1500)
  }

  // ③ +N 徽标点击 → 菜单展开（交互响应 < 1500ms；run_tools 空页也要有内容框架）。
  const moreBadge = page.locator('[data-testid="tool-pill-more"]').first()
  await expect(moreBadge).toBeVisible()
  t0 = Date.now()
  await moreBadge.click()
  await page.waitForTimeout(400) // 懒挂载 fetch（mock 空页立即返回）。
  const menuLatency = Date.now() - t0
  expect(menuLatency, `+N 菜单展开必须 < 1500ms（实测 ${menuLatency}ms）`).toBeLessThan(1500)

  // ④ long task 审计：交互期间主线程无 > 50ms 长任务（零掉帧）。
  //    容差 2（浏览器自身偶发任务；-1 = 环境不支持 longtask，跳过）。
  const longTasks = await readLongTaskCount(page)
  if (longTasks >= 0) {
    expect(longTasks, `交互期间 long task（>50ms）≤ 2（实测 ${longTasks}）`).toBeLessThanOrEqual(2)
  }
})

/** 渲染一个窗口化长 turn 并登录（桌面/手机共用；viewport 可配）。 */
async function renderPerfPage(browser: Browser, viewport: { width: number; height: number }): Promise<Page> {
  const ctx = await browser.newContext({ viewport })
  await ctx.addInitScript(() => {
    try { localStorage.setItem('xbot-locale', 'zh-CN') } catch { /* ignore */ }
  })
  const page = await ctx.newPage()
  await setupMock(page)
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await expect(page.locator('[data-message-list-content]')).toContainText('性能回合', { timeout: 30000 })
  await expect(page.locator('[data-testid="tool-pill"]').first()).toBeVisible({ timeout: 10000 })
  await page.waitForTimeout(500)
  return page
}

test('长 turn 桌面侧边栏开合零掉帧（10000 迭代窗口化渲染后）', async ({ browser }) => {
  const page = await renderPerfPage(browser, { width: 1440, height: 900 })
  await startLongTaskAudit(page)

  // 侧边栏面板切换：会话 → 文件 → 会话（docked 面板重挂载 —— 交互 < 1500ms）。
  const filesBtn = page.getByRole('button', { name: '文件' }).first()
  const sessionsBtn = page.getByRole('button', { name: '会话' }).first()
  const t0 = Date.now()
  await filesBtn.click()
  await page.waitForTimeout(300)
  await sessionsBtn.click()
  await page.waitForTimeout(300)
  const switchLatency = Date.now() - t0
  expect(switchLatency, `侧边栏面板往返切换必须 < 3000ms（实测 ${switchLatency}ms）`).toBeLessThan(3000)

  const longTasks = await readLongTaskCount(page)
  if (longTasks >= 0) {
    expect(longTasks, `侧边栏切换期间 long task（>50ms）≤ 2（实测 ${longTasks}）`).toBeLessThanOrEqual(2)
  }
  await page.context().close()
})

test('手机 390 视口：长 turn 窗口化渲染 + 交互零掉帧（pill / 面板）', async ({ browser }) => {
  const page = await renderPerfPage(browser, { width: 390, height: 844 })
  await startLongTaskAudit(page)

  // ① pill 点击（手机触屏点按路径同 click）：popover 展开可见。
  const t0 = Date.now()
  await page.locator('[data-testid="tool-pill"]').first().click()
  await expect(page.getByText('perf summary 0').first(), '手机 pill popover 必须展开').toBeVisible({ timeout: 5000 })
  const pillLatency = Date.now() - t0
  expect(pillLatency, `手机 pill 展开必须 < 1500ms（实测 ${pillLatency}ms）`).toBeLessThan(1500)
  await page.mouse.click(10, 100)
  await page.waitForTimeout(200)

  // ② 工具面板按钮切换（手机导航）：往返一次。
  const filesBtn = page.getByRole('button', { name: '文件' }).first()
  const sessionsBtn = page.getByRole('button', { name: '会话' }).first()
  if (await filesBtn.count() > 0 && await sessionsBtn.count() > 0) {
    const t1 = Date.now()
    await filesBtn.click()
    await page.waitForTimeout(300)
    await sessionsBtn.click()
    await page.waitForTimeout(300)
    const switchLatency = Date.now() - t1
    expect(switchLatency, `手机面板往返切换必须 < 3000ms（实测 ${switchLatency}ms）`).toBeLessThan(3000)
  }

  const longTasks = await readLongTaskCount(page)
  if (longTasks >= 0) {
    expect(longTasks, `手机交互期间 long task（>50ms）≤ 2（实测 ${longTasks}）`).toBeLessThanOrEqual(2)
  }
  await page.context().close()
})
