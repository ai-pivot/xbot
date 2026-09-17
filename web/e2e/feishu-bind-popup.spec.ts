/**
 * E2E：渠道面板「一键绑定飞书」必须**自动打开浏览器窗口**引导用户创建符合要求的应用。
 *
 * 用户要求（2026-09-17）：「e2e 验证用户新安装 xbot web 后进 channel 点击设置飞书的按钮，
 * 能自动弹到对应浏览器窗口引导用户创建符合要求的应用」。
 *
 * 跑法（无需后端，全部 RPC 走 route mock；playwright.config 会自动起 dev server 5199）：
 *   cd web && npx playwright test e2e/feishu-bind-popup.spec.ts
 *
 * 断言（用户视角的完整交互）：
 *   ① 渠道面板渲染飞书引导（权限/事件/回调预设来自 feishu_app_guide）；
 *   ② 点「一键绑定」→ 立刻弹出新窗口（window.open 在用户手势内），且该窗口被导航到
 *      feishu_bind_start 返回的授权链接（飞书创建应用页）；
 *   ③ 拿到链接后按钮脱离「正在获取链接…」并可再次点击（不再永久禁用）；
 *   ④ 链接过期后给出明确提示，仍可重新生成。
 */
import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'
const BIND_URL = 'https://open.feishu.cn/page/launcher?addons=H4sIAAAA_e2e'

async function setupMock(page: Page, bindStart: Record<string, unknown> = {}) {
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

  await page.route('**/api/rpc', (route) => {
    const body = route.request().postDataJSON() as { method?: string }
    switch (body.method) {
      case 'get_channel_config':
        return route.fulfill({
          json: {
            ok: true,
            data: {
              feishu: { enabled: 'true', app_id: 'cli_new', app_secret: '', _builtin: 'true' },
            },
          },
        })
      case 'feishu_app_guide':
        return route.fulfill({
          json: {
            ok: true,
            data: {
              scopes: ['im:message:send_as_bot', 'cardkit:card:write'],
              events: ['im.message.receive_v1'],
              callbacks: ['card.action.trigger'],
              create_app_url: 'https://open.feishu.cn/app',
              needs_public_url: false,
            },
          },
        })
      case 'feishu_bind_start':
        return route.fulfill({
          json: { ok: true, data: { url: BIND_URL, expires_in: 600, app_id: 'cli_new', ...bindStart } },
        })
      case 'feishu_bind_status':
        return route.fulfill({ json: { ok: true, data: { state: 'waiting', url: BIND_URL, expires_in: 600 } } })
      default:
        return route.fulfill({ json: { ok: true, data: null } })
    }
  })
}

async function loginAndOpenChannels(page: Page) {
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForTimeout(2000)
  await page
    .locator('button[aria-label="设置"], button[aria-label="打开设置"], button[aria-label="Settings"]')
    .first()
    .click()
  await page.getByRole('button', { name: /^(channels|渠道)$/i }).first().click()
  await expect(page.getByTestId('feishu-guide')).toBeVisible({ timeout: 15_000 })
}

test.describe('飞书一键绑定：自动打开授权窗口', () => {
  test('点击按钮 → 自动弹出浏览器窗口并导航到飞书创建应用页', async ({ page, context }) => {
    await context.route('https://open.feishu.cn/**', (r) =>
      r.fulfill({ status: 200, contentType: 'text/html', body: '<html><body>feishu stub</body></html>' }),
    )
    await setupMock(page)
    await loginAndOpenChannels(page)

    // 预设清单必须来自 feishu_app_guide（引导用户创建「符合要求」的应用）
    await page.getByTestId('feishu-guide-preset-toggle').click()
    await expect(page.getByTestId('feishu-guide-scopes')).toContainText('cardkit:card:write')
    await expect(page.getByTestId('feishu-guide-callbacks')).toContainText('card.action.trigger')

    const popupPromise = context.waitForEvent('page')
    await page.getByTestId('feishu-bind').click()
    const popup = await popupPromise

    // ① 窗口自动打开（无需用户手动复制链接）
    expect(popup).toBeTruthy()
    // ② 该窗口被导航到授权链接（feishu_bind_start 返回后才导航 —— 用 waitForURL
    //    等这次导航，不能立刻读 url()：窗口先以 about:blank 打开，导航发生在
    //    RPC 解析之后）。
    await popup.waitForURL(/open\.feishu\.cn\/page\/launcher/, { timeout: 15_000 })
    expect(popup.url()).toContain('open.feishu.cn/page/launcher')
    await popup.close()

    // ③ 面板侧：链接展示 + 等待确认提示，按钮不再禁用
    await expect(page.getByTestId('feishu-bind-url')).toContainText('open.feishu.cn/page/launcher')
    await expect(page.getByTestId('feishu-bind-waiting')).toBeVisible()
    await expect(page.getByTestId('feishu-bind')).toBeEnabled()
    await expect(page.getByTestId('feishu-open-link')).toBeVisible()
    await page.screenshot({ path: '/tmp/feishu-bind-popup-shots/01-bound.png', fullPage: true })
  })

  test('弹窗被拦截时如实提示，并提供「打开链接」（不静默失败）', async ({ page }) => {
    await page.addInitScript(() => {
      // 模拟弹窗拦截器：window.open 返回 null
      window.open = () => null
    })
    await setupMock(page)
    await loginAndOpenChannels(page)

    await page.getByTestId('feishu-bind').click()
    await expect(page.getByTestId('feishu-popup-blocked')).toBeVisible()
    await expect(page.getByTestId('feishu-bind-url')).toContainText('open.feishu.cn/page/launcher')
    await expect(page.getByTestId('feishu-bind')).toBeEnabled()
  })

  test('链接过期 → 明确提示并允许重新生成', async ({ page }) => {
    await setupMock(page, { expires_in: 1 })
    await loginAndOpenChannels(page)

    await page.getByTestId('feishu-bind').click()
    await expect(page.getByTestId('feishu-link-expired')).toBeVisible({ timeout: 15_000 })
    await expect(page.getByTestId('feishu-bind')).toBeEnabled()
  })
})
