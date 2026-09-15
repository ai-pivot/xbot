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

test('点面板标题文字不折叠面板；折叠只走 ⌄ 且左栏给空态提示', async ({ browser }) => {
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

  // ③ ⌄ 显式折叠 → 面板从堆叠移除，但左栏必须给空态提示（不是一片黑）。
  await page.locator('[data-panel-id="core.sessions"] header button:has(svg.lucide-chevron-right)').click()
  await expect(page.locator('[data-testid="panel-dock-stack"] [data-panel-id]')).toHaveCount(0)
  await expect(page.locator('[data-testid="panel-dock-stack"]')).not.toBeEmpty()
})
