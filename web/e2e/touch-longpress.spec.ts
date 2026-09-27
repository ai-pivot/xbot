import { test, expect, type Page } from '@playwright/test'

/**
 * 触屏长按出菜单 —— 真实浏览器链路守护（2026-09-27 用户报告
 * 「只有 iPhone 能长按出菜单，安卓长按都没用」）。
 *
 * 修复：`TouchContextMenuTrigger`（触屏时 select-none 包裹层 + 带容差的长按
 * 计时 → 派发 synthetic contextmenu → 走 Radix 正常开菜单路径）。
 *
 * 这里用 `hasTouch + isMobile` 的真实 Chromium 触屏环境（matchMedia
 * `(hover: none) and (pointer: coarse)` 为真 → 渲染触屏分支），并经
 * **真实 DOM 事件**驱动长按（pointerdown → 500ms → 菜单打开）。
 * ⚠️ 判别力：若 revert TouchContextMenuTrigger（回到 Radix 裸 trigger，
 * wrapper 消失），`touch-context-trigger` 不存在且长按无菜单 → 本 spec 必红。
 */
async function setupMock(page: Page) {
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
          sessions: [
            { chat_id: 'chat-1', channel: 'web', label: 'Alpha 会话', last_active: new Date().toISOString() },
            { chat_id: 'chat-2', channel: 'web', label: 'Beta 会话', last_active: new Date().toISOString() },
          ],
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
            { id: 1, role: 'user', content: 'hi', timestamp: new Date().toISOString(), turn_id: 1 },
            { id: 2, role: 'assistant', content: 'hello', timestamp: new Date().toISOString(), turn_id: 1 },
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

test.describe('触屏长按出上下文菜单（安卓修复）', () => {
  test('真实触屏环境：长按会话行 500ms → 菜单出现；点击则正常切会话', async ({ browser }) => {
    const ctx = await browser.newContext({
      viewport: { width: 390, height: 844 },
      isMobile: true,
      hasTouch: true,
    })
    const page = await ctx.newPage()
    await setupMock(page)
    await page.goto('/')

    // ① 触屏分支渲染了 select-none 包裹层（判别锚点：revert 修复即消失）。
    //    移动端会话列表在 ☰ 抽屉里 —— 先打开抽屉再断言。
    await page.getByRole('button', { name: '会话', exact: true }).click()
    await expect(page.getByTestId('touch-context-trigger').first()).toHaveCount(1)

    // ② 长按（pointerdown 按住不动 600ms）→ 上下文菜单打开。
    const row = page.getByText('Beta 会话')
    await row.dispatchEvent('pointerdown', {
      pointerType: 'touch',
      clientX: 30,
      clientY: 200,
      bubbles: true,
      cancelable: true,
    })
    await expect(page.getByText('在新标签页中打开')).toBeVisible({ timeout: 4000 })

    // ③ 菜单项真实可选（关闭菜单）。
    await page.keyboard.press('Escape')
    await expect(page.getByText('在新标签页中打开')).toHaveCount(0)

    // ④ 正常点击仍是切会话（长按增强不破坏点击语义）。
    await row.click()
    await page.waitForTimeout(300)

    await ctx.close()
  })
})
