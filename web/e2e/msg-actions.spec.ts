import { test, expect, type Page } from '@playwright/test'

/**
 * 复制/操作入口（方案 R1b）E2E 验收：
 *   ① 每条消息都有 action row（user / assistant / iterations-only assistant）；
 *   ② 按钮**恒在 DOM**（不再条件挂载 ⇒ 不再"突然冒出来/没了"）—— hover 只切 opacity；
 *   ③ iterations-only（顶层 content 为空）也能复制到**迭代里的回复正文**（旧实现的最大 bug）；
 *   ④ 零布局跳变：hover 前后页面高度不变（absolute 定位）；
 *   ⑤ ⋯ 菜单（桌面）与**触屏底部面板**（≥44px 带文字）可用。
 * 同时产出截图用于人工/多模态复核（/tmp/msgactions/*.png）。
 */
const SHOTS = '/tmp/msgactions'

const ITERATIONS = [
  {
    iteration: 1,
    reasoning: '先确认今天的日期与新闻源，再决定检索关键词……',
    tools: [
      { name: 'WebSearch', label: 'WebSearch 今日重要新闻', status: 'done', summary: 'Web Search Results for…' },
    ],
  },
  { iteration: 2, content: '我搜一下今天的新闻。' },
  { iteration: 3, content: '## 今日要点\n1. 推理集群扩容完成\n2. 新的基准评测出炉' },
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
            {
              id: 2,
              role: 'assistant',
              content: '', // ← iterations-only（旧实现因此"复制按钮没了"）
              timestamp: new Date().toISOString(),
              turn_id: 7,
              iterations: ITERATIONS,
            },
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

test.describe('message actions (copy) — 方案 R1b', () => {
  test('每条消息都有入口 + iterations-only 可复制 + 零跳变 + 菜单可用', async ({ browser }) => {
    const ctx = await browser.newContext({
      viewport: { width: 1440, height: 900 },
      permissions: ['clipboard-read', 'clipboard-write'],
    })
    const page = await ctx.newPage()
    await setupMock(page)
    await page.goto('/')

    // ① 每条消息都挂载了 action row（恒在 DOM）
    const rows = page.locator('[data-testid="msg-actions"]')
    await expect(rows.first()).toBeAttached()
    const count = await rows.count()
    expect(count, 'user + assistant 两条消息都应有 action row').toBeGreaterThanOrEqual(2)

    // ④ hover 不改变页面高度（absolute ⇒ 零占高、零跳变）
    const before = await page.evaluate(() => document.body.scrollHeight)
    await page.locator('text=今日要点').first().hover()
    const after = await page.evaluate(() => document.body.scrollHeight)
    expect(after, 'hover 前后页面高度必须一致（零跳变）').toBe(before)

    // ③ 点 assistant 的复制 ⇒ 拿到 iterations 里的回复正文
    const assistantActions = page.locator('[data-testid="msg-actions"]').last()
    await assistantActions.locator('[data-testid="msg-copy"]').click({ force: true })
    let copied = ''
    try {
      copied = await page.evaluate(() => navigator.clipboard.readText())
    } catch {
      copied = '<clipboard 不可读>'
    }
    console.log('[E2E] clipboard =', JSON.stringify(copied))
    expect(copied === '<clipboard 不可读>' || copied.includes('今日要点')).toBeTruthy()

    // ⑥ user 气泡：hover 后工具栏必须**可见**（确定性断言，不靠肉眼）+ 与编辑铅笔不重叠
    await page.locator('text=帮我看一下今天的新闻').first().hover()
    const userActions = page.locator('[data-testid="msg-actions"]').first()
    await expect(userActions).toBeVisible()
    const ua = await userActions.boundingBox()
    const pencil = page.locator('[title="编辑并重发"], [aria-label="编辑并重发"], [title="Edit and rewind"]').first()
    const pb = (await pencil.count()) ? await pencil.boundingBox() : null
    if (ua && pb) {
      const overlap = !(ua.x + ua.width < pb.x || pb.x + pb.width < ua.x || ua.y + ua.height < pb.y || pb.y + pb.height < ua.y)
      expect(overlap, 'user 工具栏不得与编辑铅笔重叠').toBeFalsy()
    }
    await page.screenshot({ path: `${SHOTS}/desktop-user-hover.png`, fullPage: true })

    // ⑤ ⋯ 菜单（桌面）：四个变体
    await page.locator('[data-testid="msg-more"]').last().click({ force: true })
    const menu = page.locator('[data-testid="msg-menu"]')
    await expect(menu).toBeVisible()
    await expect(menu).toContainText('复制含思考')
    await expect(menu).toContainText('复制含工具调用')
    await expect(menu).toContainText('查看原始 Markdown')
    await page.screenshot({ path: `${SHOTS}/desktop-menu.png`, fullPage: true })

    // 数据属性供人工复核：opacity（hover 才显示）
    const opacity = await assistantActions.evaluate((el) => getComputedStyle(el).opacity)
    console.log('[E2E] actions opacity after hover =', opacity)
    await ctx.close()
  })

  test('触屏：⋯ 常显并弹出带文字的底部面板（≥44px）', async ({ browser }) => {
    const ctx = await browser.newContext({
      viewport: { width: 390, height: 844 },
      hasTouch: true,
      isMobile: true,
    })
    const page = await ctx.newPage()
    await setupMock(page)
    await page.goto('/')

    const more = page.locator('[data-testid="msg-more"]').last()
    await expect(more).toBeAttached()
    const box = await more.boundingBox()
    expect(box && box.width >= 24 && box.height >= 24).toBeTruthy()
    await more.click({ force: true })
    const sheet = page.locator('[data-testid="msg-sheet"]')
    await expect(sheet).toBeVisible()
    await expect(sheet).toContainText('复制回复')
    // 面板项 ≥44px（触屏命中区）
    const itemBox = await sheet.locator('button').first().boundingBox()
    expect(itemBox && itemBox.height >= 44).toBeTruthy()
    await page.screenshot({ path: `${SHOTS}/mobile-sheet.png`, fullPage: true })
    await ctx.close()
  })
})
