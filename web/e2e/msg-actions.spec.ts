import { test, expect, type Page } from '@playwright/test'

/**
 * 复制入口（用户 2026-09-15 二次定稿）：**电脑右键 / 手机长按**，无任何常驻或 hover 悬浮条。
 * 覆盖三层：message（整条回复）/ iteration（**每个迭代**都有）/ tools（该迭代每个工具各一项）。
 */
const SHOTS = '/tmp/msgactions'

const ITERATIONS = [
  {
    iteration: 1,
    reasoning: '先确认今天的日期与新闻源，再决定检索关键词……',
    tools: [{ name: 'WebSearch', label: 'WebSearch 今日重要新闻', status: 'done', summary: 'first tool output' }],
  },
  { iteration: 2, content: '我搜一下今天的新闻。' },
  {
    iteration: 3,
    content: '## 今日要点\n1. 推理集群扩容完成\n2. 新的基准评测出炉',
    tools: [{ name: 'Shell', label: 'Shell ls -la', status: 'done', detail: 'second tool output' }],
  },
]

async function setupMock(page: Page) {
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
    r.fulfill({
      json: {
        ok: true,
        data: {
          messages: [
            { id: 1, role: 'user', content: '帮我看一下今天的新闻', timestamp: new Date().toISOString(), turn_id: 7 },
            { id: 2, role: 'assistant', content: '', timestamp: new Date().toISOString(), turn_id: 7, iterations: ITERATIONS },
          ],
          chat_id: 'chat-1',
          last_seq: 2,
          active_progress: null,
        },
      },
    }),
  )
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

test.describe('复制入口（右键 / 长按）', () => {
  test('每个迭代都有独立目标 + 右键菜单 + 无悬浮条', async ({ browser }) => {
    const ctx = await browser.newContext({
      viewport: { width: 1440, height: 900 },
      permissions: ['clipboard-read', 'clipboard-write'],
    })
    const page = await ctx.newPage()
    await setupMock(page)
    await page.goto('/')

    // ① 不再有常驻/悬浮工具条（用户：这个悬浮太丑了还挡着）
    expect(await page.locator('[data-testid="msg-actions"]').count()).toBe(0)
    expect(await page.locator('[data-testid="msg-copy"]').count()).toBe(0)

    // ② **每个迭代**都有独立复制目标（用户：不是说每个 iter 都有吗）
    const iterTargets = page.locator('[data-copy-target="iteration"]')
    await expect(iterTargets.first()).toBeAttached()
    expect(await iterTargets.count(), 'iterations 数量 == 复制目标数量').toBe(ITERATIONS.length)

    // ③ 右键**有思考**的迭代 → 菜单含「复制这段思考」；空项按设计被过滤（不给无内容的复制项）
    await iterTargets.nth(0).click({ button: 'right' })
    const menu0 = page.locator('[data-testid="copy-menu"]')
    await expect(menu0).toBeVisible()
    await expect(menu0).toContainText('复制这段思考')
    await expect(menu0).toContainText('复制该迭代（含工具）')
    await page.screenshot({ path: `${SHOTS}/desktop-iteration-menu.png`, fullPage: true })
    await page.keyboard.press('Escape')

    // ④ 右键**只有正文**的迭代 → 「复制该迭代正文」只复制该迭代（不串到别的迭代）
    await iterTargets.nth(1).click({ button: 'right' })
    const menu1 = page.locator('[data-testid="copy-menu"]')
    await expect(menu1).toContainText('复制该迭代正文')
    await menu1.getByText('复制该迭代正文').click()
    const copied = await page.evaluate(() => navigator.clipboard.readText())
    console.log('[E2E] iteration clipboard =', JSON.stringify(copied))
    expect(copied).toBe('我搜一下今天的新闻。')

    // ④ 工具级：右键工具组 → 每个工具一项（可单独复制该工具输出）
    const toolsTarget = page.locator('[data-copy-target="tools"]').first()
    await toolsTarget.click({ button: 'right' })
    const tmenu = page.locator('[data-testid="copy-menu"]')
    await expect(tmenu).toContainText('WebSearch 今日重要新闻')
    await page.screenshot({ path: `${SHOTS}/desktop-tools-menu.png`, fullPage: true })
    await tmenu.getByText('复制：WebSearch 今日重要新闻').click()
    const toolCopied = await page.evaluate(() => navigator.clipboard.readText())
    console.log('[E2E] tool clipboard =', JSON.stringify(toolCopied))
    expect(toolCopied).toBe('first tool output')
    await ctx.close()
  })

  test('触屏：长按 → 底部面板（带文字标签，≥44px）', async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })
    const page = await ctx.newPage()
    await setupMock(page)
    await page.goto('/')
    const target = page.locator('[data-copy-target="tools"]').first()
    await expect(target).toBeAttached()
    // 浏览器合成指针事件：pointerdown(touch) → 等过长按阈值 → 面板
    await target.evaluate((el) => {
      el.dispatchEvent(
        new PointerEvent('pointerdown', { pointerType: 'touch', bubbles: true, clientX: 120, clientY: 400 }),
      )
    })
    const sheet = page.locator('[data-testid="copy-sheet"]')
    await expect(sheet).toBeVisible({ timeout: 3000 })
    await expect(sheet).toContainText('复制：WebSearch 今日重要新闻')
    const box = await sheet.locator('button').first().boundingBox()
    expect(box && box.height >= 44).toBeTruthy()
    await page.screenshot({ path: `${SHOTS}/mobile-sheet.png`, fullPage: true })
    await ctx.close()
  })
})
