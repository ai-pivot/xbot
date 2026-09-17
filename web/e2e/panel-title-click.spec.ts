import { test, expect, type Page } from '@playwright/test'

/**
 * 回归：点面板标题文字【不该】折叠面板（2026-09-15 用户：「点 `Sessions` 这个词有
 * bug，别的位置没有」）。折叠只有 ⌄ 显式按钮；折叠后左栏必须给空态提示，不能是
 * 一整片黑的"坏掉"观感。
 */

async function setupMock(page: Page) {
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: {
          sessions: [
            { chat_id: 'chat-1', channel: 'web', label: 'Test Session One', last_active: new Date().toISOString() },
            { chat_id: 'chat-2', channel: 'web', label: 'Test Session Two', last_active: new Date().toISOString() },
          ],
          chats: [
            { chat_id: 'chat-1', channel: 'web', label: 'Test Session One', last_active: new Date().toISOString(), isCurrent: true },
            { chat_id: 'chat-2', channel: 'web', label: 'Test Session Two', last_active: new Date().toISOString() },
          ],
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
}

async function login(page: Page) {
  await page.goto('/login')
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForSelector('[data-panel-id="core.sessions"]', { timeout: 30_000 })
}

test('点面板标题文字不折叠面板；常驻会话面板不渲染浮窗/折叠按钮', async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } })
  const page = await ctx.newPage()
  await setupMock(page)
  await login(page)

  const panel = page.locator('[data-panel-id="core.sessions"]')
  await expect(panel).toBeVisible()
  await expect(panel.getByTestId('panel-title')).toHaveText(/Sessions|会话/)

  // ① 点标题文字 → 不折叠（面板与列表仍在）。
  await panel.getByTestId('panel-title').click()
  await expect(panel).toBeVisible()
  await expect(page.locator('[data-testid="panel-dock-stack"] [data-panel-id]')).toHaveCount(1)

  // ② 点 header 空白/图标 → 也不折叠。
  await panel.locator('header svg').first().click()
  await expect(panel).toBeVisible()

  // ③ 常驻面板（PINNED_DEFAULTS）不渲染「升为浮窗」/「折叠」按钮 —— 它们都会让
  //    会话面板离开左栏（用户 2026-09-15：「sessions 这一行还有一个有完全一样的
  //    bug 的按钮」）。隐藏的拖拽把手（`panel-grip`：opacity-0 仍占位，把右端控件
  //    顶偏）也一并删除 —— 标题行只剩标题本身与显式控件。
  const hdr = panel.locator('header')
  await expect(hdr.locator('svg.lucide-picture-in-picture-2')).toHaveCount(0)
  await expect(hdr.locator('svg.lucide-chevron-right')).toHaveCount(0)
  await expect(hdr.locator('[data-testid="panel-grip"]')).toHaveCount(0)
  // 鼠标划过（按钮本该在 hover 时显现）也不该冒出来。
  await hdr.hover()
  await expect(hdr.locator('svg.lucide-picture-in-picture-2')).toHaveCount(0)
  await expect(hdr.locator('svg.lucide-chevron-right')).toHaveCount(0)
  await expect(hdr.locator('[data-testid="panel-grip"]')).toHaveCount(0)

  await ctx.close()
})

/**
 * 回归：会话面板搜索框曾用 readOnly-until-focus 反自动填充 —— readOnly 输入框在
 * 手机上**永远不弹软键盘**（用户：「不要 disable 弹出键盘，这导致手机端都不会弹出
 * 键盘」）。这里用真机形态的触屏上下文验证：点开搜索 → 输入框获得焦点（手势内同步
 * focus）且**真的能输入**（readOnly 时 Playwright 的 fill 会直接失败）。
 */
test('会话搜索：点开即聚焦且可输入（手机端必须能弹键盘）', async ({ browser }) => {
  const ctx = await browser.newContext({
    viewport: { width: 390, height: 844 },
    hasTouch: true,
    isMobile: true,
    userAgent:
      'Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1',
  })
  await ctx.addInitScript(() => {
    try {
      localStorage.setItem('xbot-ui-mode', 'desktop')
      localStorage.setItem('xbot-locale', 'zh-CN')
    } catch {
      /* ignore */
    }
  })
  const page = await ctx.newPage()
  await setupMock(page)
  await login(page)

  // 桌面外壳（强制 desktop 模式）才有左栏会话面板；触屏能力由上下文提供。
  await expect(page.locator('[data-panel-id="core.sessions"]')).toBeVisible()

  const input = page.locator('input[name="xbot-session-search"]')
  await expect(input).toHaveCount(1)
  // 反自动填充不许再靠 readOnly（手机键盘的开关）。
  expect(await input.evaluate((el) => (el as HTMLInputElement).readOnly)).toBe(false)
  // inputMode=search（移动端键盘类型）+ autoComplete=off。
  expect(await input.getAttribute('inputmode')).toBe('search')
  expect(await input.getAttribute('autocomplete')).toBe('off')

  // 点开搜索按钮（真机触屏路径）→ 手势内同步 focus。
  await page.locator('[data-testid="session-search-toggle"]').tap()
  await expect(input).toBeFocused({ timeout: 5000 })

  // 真的能输入（readOnly 时这一步会因 "not editable" 失败）。
  await input.fill('tall')
  await expect(input).toHaveValue('tall')

  await ctx.close()
})
