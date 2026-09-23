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
    content: '## 今日要点\n1. 推理集群扩容完成\n2. [新的基准评测](https://example.com/bench) 出炉',
    tools: [{ name: 'Shell', label: 'Shell ls -la', status: 'done', detail: 'second tool output' }],
  },
]

async function setupMock(page: Page) {
  // 菜单标签走 i18n ⇒ 浏览器语言必须与断言语言一致（Playwright 默认 en-US → 英文标签）。
  await page.addInitScript(() => {
    try {
      localStorage.setItem('xbot-locale', 'zh-CN')
    } catch {
      /* ignore */
    }
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
    // 右键要点在**迭代块自身**（左上角的思考/留白区），而不是内层 tools 目标上 ——
    // 否则按"嵌套最内层优先"设计会开工具菜单（这是正确行为，不是 bug）。
    await iterTargets.nth(0).click({ button: 'right', position: { x: 6, y: 6 } })
    const menu0 = page.locator('[data-testid="copy-menu"]')
    await expect(menu0).toBeVisible()
    await expect(menu0).toContainText('复制这段思考')
    await expect(menu0).toContainText('复制该迭代（含工具）')
    await page.screenshot({ path: `${SHOTS}/desktop-iteration-menu.png`, fullPage: true })
    await page.keyboard.press('Escape')

    // ④ 右键**只有正文**的迭代 → 「复制该迭代正文」只复制该迭代（不串到别的迭代）
    await iterTargets.nth(1).click({ button: 'right', position: { x: 6, y: 6 } })
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
    // ⚠️ 回归守护（2026-09-15 我自己截图发现的缺陷）：面板必须**贴在视口底部**，
    // 而不是渲染在对话流中间（根因：虚拟行带 transform ⇒ fixed 相对该行定位，
    // 且 .virt-row{contain:layout} 会裁剪）。修法是 portal 到 document.body。
    const sheetBox = await sheet.boundingBox()
    const vh = page.viewportSize()?.height ?? 844
    expect(sheetBox, 'sheet must have a box').toBeTruthy()
    expect(sheetBox!.y + sheetBox!.height, 'sheet 必须贴住视口底部').toBeGreaterThan(vh - 24)
    // 面板要列出**全部**候选（该工具组的每个工具各一项 + 「复制全部工具输出」），
    // 不能被裁剪成一行。注意：这里取的是**第 1 个迭代**的工具组（1 个 WebSearch）⇒ 应为 2 项。
    expect(await sheet.locator('button').count()).toBe(2)
    await expect(sheet).toContainText('复制：WebSearch 今日重要新闻')
    await expect(sheet).toContainText('复制全部工具输出')
    await page.screenshot({ path: `${SHOTS}/mobile-sheet.png`, fullPage: true })
    await ctx.close()
  })

  test('打开链接 / 复制选区（落点相关的两项）', async ({ browser }) => {
    const ctx = await browser.newContext({
      viewport: { width: 1440, height: 900 },
      permissions: ['clipboard-read', 'clipboard-write'],
    })
    // 站外链接不真出去：stub 掉 example.com（既避免依赖外网，也能拿到最终 URL）。
    await ctx.route('**example.com/**', (r) => r.fulfill({ status: 200, contentType: 'text/html', body: 'stub' }))
    const page = await ctx.newPage()
    await setupMock(page)
    await page.goto('/')

    // ① 右键链接 → 「打开链接 / 复制链接地址」
    const link = page.getByRole('link', { name: '新的基准评测' })
    await expect(link).toBeAttached()
    await link.click({ button: 'right' })
    const menu = page.locator('[data-testid="copy-menu"]')
    await expect(menu).toContainText('打开链接')
    await expect(menu).toContainText('复制链接地址')
    await page.screenshot({ path: `${SHOTS}/desktop-link-menu.png`, fullPage: true })

    // 打开链接 → 新标签打开（noopener,noreferrer）。
    // ⚠️ noopener 弹窗在导航 commit 之前 `url()` 是空串，必须 waitForURL 等真实导航
    //（等 about:blank 的 loadstate 等于没等 —— 首版就是这么假绿的）。
    const popupPromise = ctx.waitForEvent('page')
    await menu.getByText('打开链接').click()
    const popup = await popupPromise
    await popup.waitForURL(/example\.com\/bench/, { timeout: 10_000 })
    expect(popup.url(), '打开链接应打开该 href').toContain('example.com/bench')
    await popup.close()

    // 复制链接地址 → 绝对地址进剪贴板
    await link.click({ button: 'right' })
    const menu2 = page.locator('[data-testid="copy-menu"]')
    await expect(menu2).toContainText('复制链接地址')
    await menu2.getByText('复制链接地址').click()
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe('https://example.com/bench')

    // ② 有选区 → 「复制选区」；右键落在**选区内**（选区才会保留）
    const target = page.locator('[data-copy-target="message"]').last()
    await target.evaluate((el) => {
      const range = document.createRange()
      range.selectNodeContents(el)
      const sel = window.getSelection()
      sel?.removeAllRanges()
      sel?.addRange(range)
    })
    await target.click({ button: 'right' })
    const selMenu = page.locator('[data-testid="copy-menu"]')
    await expect(selMenu).toContainText('复制选区')
    await selMenu.getByText('复制选区').click()
    const copied = await page.evaluate(() => navigator.clipboard.readText())
    expect(copied, '复制选区应拿到选中的正文').toContain('今日要点')
    await ctx.close()
  })

  // 触屏长按是需求的两条主路径之一；上面的用例只覆盖了桌面右键。
  // CR 实测：把长按回调的 target 置空（等价于"手机长按链接再也出不来『打开链接』"）时，
  // 全量单测 + E2E 会**全绿** ⇒ 这条必须有（长按路径的落点也要给链接入口）。
  test('触屏长按落在链接上 → sheet 含「打开链接 / 复制链接地址」', async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })
    const page = await ctx.newPage()
    await setupMock(page)
    await page.goto('/')
    const link = page.getByRole('link', { name: '新的基准评测' })
    await expect(link).toBeAttached()
    await link.evaluate((el) => {
      el.dispatchEvent(
        new PointerEvent('pointerdown', { pointerType: 'touch', bubbles: true, clientX: 120, clientY: 300 }),
      )
    })
    const sheet = page.locator('[data-testid="copy-sheet"]')
    await expect(sheet).toBeVisible({ timeout: 3000 })
    await expect(sheet).toContainText('打开链接')
    await expect(sheet).toContainText('复制链接地址')
    await page.screenshot({ path: `${SHOTS}/mobile-link-sheet.png`, fullPage: true })
    await ctx.close()
  })
})
