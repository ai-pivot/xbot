import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/** 复制自 tool-pill-width.spec.ts 的 mock（同一套 /api/*）。 */
async function setupMock(page: Page, historyMessages: unknown[] = []) {
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) => r.fulfill({ json: { ok: true, data: {
    sessions: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() }],
    chats: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString(), isCurrent: true }],
    orphan_subagents: [], } } }))
  await page.route('**/api/history', (r) => r.fulfill({ json: { ok: true, data: { messages: historyMessages, chat_id: 'chat-1', last_seq: 0, active_progress: null } } }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

/**
 * 视觉语言断言（用户 2026-09-15 定稿）：
 *   ① 失败 pill = 红系 + 「失败」文字标签（成功安静、失败吵闹）；
 *   ② 假工具 pill = 虚线边框 + kind 头像 + 「系统」角标；
 *   ③ 行级失败告警 = `N 失败` chip。
 */
const ASSISTANT = {
  id: 2, role: 'assistant', content: 'done', timestamp: new Date().toISOString(), turn_id: 1,
  iterations: [
    { iteration: 1, content: 'ok', reasoning: '', tools: [
      { name: 'Shell', label: 'Shell: cargo check', status: 'done', summary: 'ok', detail: 'ok' },
      { name: 'Grep', label: 'Grep: shared_experts', status: 'error', summary: 'Error: exit 1', detail: 'Error: exit 1', exitCode: 1 },
      { name: 'background_task_result', label: '后台任务已完成', status: 'done', detail: 'build ok',
        toolHints: JSON.stringify({ kind: 'bg_task', task_id: '3f8f492a', status: 'done', elapsed_ms: 12300 }) },
    ] },
  ],
}

test('pill 视觉语言：失败吵闹 / 假工具可辨 / 行级告警', async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } })
  const page = await ctx.newPage()
  await setupMock(page, [{ id: 1, role: 'user', content: 'run check', timestamp: new Date().toISOString(), turn_id: 1 }, ASSISTANT])
  await page.goto('/')

  // ① 失败 pill：data-tool-status=error 且带「失败」标签
  const errPill = page.locator('[data-tool-status="error"]').first()
  await expect(errPill).toBeAttached()
  await expect(errPill).toContainText('失败')

  // ② 假工具 pill：虚线边框 + 「系统」角标
  // `data-tool-name` 同时在 pill 与其外层 popover 包装上 ⇒ 取**最内层**（含「系统」角标的那个）
  const synPill = page.locator('[data-tool-name="background_task_result"]', { hasText: '系统' }).last()
  await expect(synPill).toBeAttached()
  await expect(synPill).toContainText('系统')
  const borderStyle = await synPill.evaluate((el) => getComputedStyle(el).borderStyle)
  expect(borderStyle, '假工具 pill 必须是虚线边框').toBe('dashed')

  // ③ 行级失败告警
  await expect(page.locator('[data-testid="tool-group-failed"]').first()).toContainText('失败')

  // 截图仅供人工/多模态复核；注意 mock 流程下 `historyReady` 不会翻转，面板仍显示 loading 遮罩
  //（移除 DOM 节点无效 —— React 会重渲染）。视觉契约以**上面的断言**为准；真会话截图待服务端重启后补。
  await page.screenshot({ path: '/tmp/pillvis/desktop.png', fullPage: true })
  await ctx.close()
})

/**
 * 用户 2026-09-15 追加的两条硬要求（必须先有几何断言，否则"一行一个"永远抓不到）：
 *   A. **同行合并**：390px 宽手机下必须**至少有两个 pill 共享同一行**（旧实现 max-w-full 是容器百分比
 *      ⇒ 长参数时每个 pill 独占一行；修复=恒定 `max-width: min(46vw, 15rem)`）。
 *   B. **左对齐**：所有 pill 的 icon 槽与名字首字符必须**同一 x**（恒定 16px 图标槽 + 恒定 3px 左色条
 *      + 「系统」角标移到名字之后）。
 * 旧断言只查"pill ≤ 行宽"，一行一个时同样成立 —— 所以那两个断言漏掉了这个 bug。
 */
test('手机端：pill 必须同行合并 + icon/首字符左对齐', async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 } })
  const page = await ctx.newPage()
  const LONG = { name: 'Shell', label: 'Shell: cargo check --workspace --all-targets --profile release',
    status: 'done', summary: 'ok', detail: 'ok' }
  const msgs = [
    { id: 1, role: 'user', content: 'run', timestamp: new Date().toISOString(), turn_id: 1 },
    { id: 2, role: 'assistant', content: 'ok', timestamp: new Date().toISOString(), turn_id: 1, iterations: [
      { iteration: 1, content: 'ok', reasoning: '', tools: [
        LONG,
        { name: 'Grep', label: 'Grep: shared_experts.*scale', status: 'done', summary: 'ok', detail: 'ok' },
        { name: 'Read', label: 'Read: /home/smith/src/xbot/AGENTS.md', status: 'done', summary: 'ok', detail: 'ok' },
        { name: 'background_task_result', label: '后台任务已完成', status: 'done', detail: 'ok',
          toolHints: JSON.stringify({ kind: 'bg_task', task_id: '3f8f492a', status: 'done' }) },
      ] },
    ] },
  ]
  await setupMock(page, msgs)
  // 手机壳（MobileAppShell）默认视图不是 agent 面板 ⇒ 必须像通过的移动 spec 那样**真登录**，
  // 登录后才会落到 agent 视图（`goto('/')` + mock auth 会停在别的视图，pill 不在 DOM）。
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForSelector('[data-testid="tool-pill"]', { timeout: 30000 })

  const pills = page.locator('[data-testid="tool-pill"]')
  const n = await pills.count()
  expect(n).toBeGreaterThanOrEqual(3)

  // A. 同行合并：offsetTop 必须出现重复（即不止一个 pill 在同一行）
  const tops: number[] = []
  for (let i = 0; i < n; i++) {
    const box = await pills.nth(i).boundingBox()
    if (box) tops.push(Math.round(box.y))
  }
  const uniqueTops = new Set(tops).size
  expect(uniqueTops, `pill 必须同行合并（390px 下 ${n} 个 pill 占了 ${uniqueTops} 行）`).toBeLessThan(tops.length)

  // B. 左对齐：所有 icon 槽 / 名字首字符同一 x（±1px）
  for (const sel of ['[data-testid="tool-pill-icon"]', '[data-testid="tool-pill-name"]']) {
    const nodes = page.locator(sel)
    const m = await nodes.count()
    const xs: number[] = []
    for (let i = 0; i < m; i++) {
      const box = await nodes.nth(i).boundingBox()
      if (box) xs.push(box.x)
    }
    expect(m, `${sel} 至少要有 3 个样本`).toBeGreaterThanOrEqual(3)
    expect(Math.max(...xs) - Math.min(...xs), `${sel} 必须左对齐（x 差值 ${(Math.max(...xs) - Math.min(...xs)).toFixed(1)}px）`).toBeLessThanOrEqual(1)
  }

  await page.screenshot({ path: '/tmp/pillvis/mobile-row.png', fullPage: true })
  await ctx.close()
})
