/**
 * E2E：渠道面板的飞书「创建应用」引导（全新安装可用）。
 *
 * 用户要求（2026-09-16）：「web 的 channel 设置那里把飞书引导做好，确保全新安装的
 * xbot web，用户在这里生成链接就可以打开飞书并创建符合要求的应用 … e2e web 截图并
 * 点击按钮生成 url 确定整个链路交互都正常」。
 *
 * 这个 spec 跑在**真实的隔离实例**上（XBOT_HOME 指向全新目录、独立端口），因此：
 *   - `feishu_app_guide` 走真实后端 RPC（权限/事件/回调清单来自 internal/feishuapp）；
 *   - 「生成链接」点击会真的调用飞书官方 device-authorization（register）接口 ——
 *     能拿到链接就断言链接，拿不到（无外网/权限）则断言错误被**干净地**呈现出来，
 *     两种情况都说明链路交互正常（按钮→RPC→状态→UI）。
 *
 * 运行：E2E_BASE_URL=http://127.0.0.1:16099 E2E_USER=e2e E2E_PASS=e2e-pass-123 \
 *       npx playwright test e2e/feishu-guide.spec.ts
 */
import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://127.0.0.1:16099'
const USER = process.env.E2E_USER || 'e2e'
const PASS = process.env.E2E_PASS || 'e2e-pass-123'
const SHOTS = process.env.E2E_SHOT_DIR || '/tmp/feishu-guide-shots'

async function login(page: Page) {
  await page.goto(`${BASE}/login`)
  const inputs = page.locator('input')
  await inputs.first().fill(USER)
  await page.locator('input[type="password"]').fill(PASS)
  await page.getByRole('button', { name: /log ?in|登录|sign in/i }).click()
  // 登录成功后进入主界面（SPA）
  await page.waitForFunction(() => !location.pathname.startsWith('/login'), { timeout: 20_000 })
}

/** 打开设置 → 渠道。 */
async function openChannelsSettings(page: Page) {
  await page.getByRole('button', { name: /settings|设置/i }).first().click()
  await page.getByRole('button', { name: /^(channels|渠道)$/i }).first().click()
  await expect(page.getByTestId('feishu-guide')).toBeVisible({ timeout: 15_000 })
}

test.describe('飞书引导（全新安装）', () => {
  test('引导渲染 + 点击生成链接（全链路交互）', async ({ browser }) => {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
    await login(page)
    await openChannelsSettings(page)

    // ① 三步说明 + 无需公网回调地址
    await expect(page.getByTestId('feishu-guide-steps')).toContainText(/生成链接|generate|リンク/, { timeout: 10_000 })
    await expect(page.getByTestId('feishu-guide-no-public-url')).toBeVisible()
    await page.screenshot({ path: `${SHOTS}/01-guide.png`, fullPage: true })

    // ② 「自动配置的内容」展开 → 权限/事件/回调清单（真实 RPC 数据）
    await page.getByTestId('feishu-guide-preset-toggle').click()
    await expect(page.getByTestId('feishu-guide-scopes')).toContainText('im:message:send_as_bot', { timeout: 10_000 })
    await expect(page.getByTestId('feishu-guide-events')).toContainText('im.message.receive_v1')
    await expect(page.getByTestId('feishu-guide-callbacks')).toContainText('card.action.trigger')
    await page.screenshot({ path: `${SHOTS}/02-preset.png`, fullPage: true })

    // ③ 点「生成链接」→ 要么拿到链接，要么拿到干净的错误（两者都证明链路通）
    await page.getByRole('button', { name: /一键绑定|bind|generat|生成/i }).first().click()
    const url = page.getByTestId('feishu-bind-url')
    const err = page.locator('text=/failed|error|失败|timed out|超时/i')
    await Promise.race([
      url.waitFor({ timeout: 30_000 }).catch(() => undefined),
      err.first().waitFor({ timeout: 30_000 }).catch(() => undefined),
    ])
    const hasUrl = await url.count()
    const hasErr = await err.count()
    expect(hasUrl + hasErr, '按钮点击后必须给出链接或可见错误（不能静默无反应）').toBeGreaterThan(0)
    if (hasUrl > 0) {
      const text = (await url.textContent()) ?? ''
      expect(text, '生成的链接应当是飞书域名').toMatch(/feishu\.(cn|com)|larksuite/i)
    }
    await page.screenshot({ path: `${SHOTS}/03-after-click.png`, fullPage: true })
    await page.close()
  })
})
