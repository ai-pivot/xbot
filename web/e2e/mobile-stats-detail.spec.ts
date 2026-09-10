import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * 手机端「统计详情」必须打开【详情面板】，而不是侧边栏紧凑版的复刻。
 *
 * 用户报告：手机端点侧边栏「统计详情」后，打开的全屏视图与侧边栏一模一样。
 * 根因链（本次修复）：
 *   1) PluginView 的 builtin 分支不把 panelParams 传给内置视图 → viewParams
 *      永远到不了 SessionStatsPanel；
 *   2) 面板只按【容器宽度】判定形态（>=640px 才算详情），手机容器恒 ~375px；
 *   3) openOverview 也没传 params。
 * 修复：详情形态由【打开方式】显式声明（params:{mode:'full'}），不猜宽度。
 *
 * 断言点：
 *   - 侧边栏（紧凑）有「统计详情」入口（stats-open-detail），无时间范围切换
 *   - 详情视图有时间范围切换（stats-range）
 */

test.use({ viewport: { width: 375, height: 812 } })

async function mockSessionStatsRpc(page: Page) {
  await page.route('**/api/rpc', (r) => {
    const body = r.request().postDataJSON()
    switch (body?.method) {
      case 'get_session_usage_stats':
        r.fulfill({ json: { ok: true, data: {
          iteration_count: 3, turn_count: 2,
          input_tokens: 12300, output_tokens: 4500, cached_tokens: 8000,
          llm_total_ms: 12345, avg_ttft_ms: 850, avg_tpot_ms: 40, avg_tokens_per_sec: 25,
          last_prompt_tokens: 5000, last_completion_tokens: 300, current_model: 'glm-5.2',
          by_model: [], recent_iterations: [], daily: [],
        } } })
        return
      case 'get_user_token_usage':
        r.fulfill({ json: { ok: true, data: { total_input_tokens: 12300, total_output_tokens: 4500, total_cached_tokens: 8000, total_requests: 3, by_model: [] } } })
        return
      case 'get_daily_token_usage':
        r.fulfill({ json: { ok: true, data: [] } })
        return
      case 'get_active_progress':
        r.fulfill({ json: { ok: true, data: null } })
        return
      default:
        r.fulfill({ json: { ok: true, data: null } })
    }
  })
}

async function setupMock(page: Page) {
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) => r.fulfill({
    json: { ok: true, data: {
      sessions: [{ chat_id: 'chat-1', channel: 'web', label: 'Session 1', last_active: new Date().toISOString() }],
      chats: [], orphan_subagents: [],
    } },
  }))
  await page.route('**/api/history', (r) => r.fulfill({
    json: { ok: true, data: { messages: [], chat_id: 'chat-1', last_seq: 0, active_progress: null } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await mockSessionStatsRpc(page)
}

async function login(page: Page) {
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForTimeout(2000)
}

test.describe('mobile stats detail', () => {
  test('「统计详情」打开的是详情布局，而不是侧边栏紧凑版复刻', async ({ browser }) => {
    const page = await browser.newPage({ viewport: { width: 375, height: 812 } })
    await setupMock(page)
    await login(page)

    // 手机端进入「工具/详情」视图（顶栏或抽屉里的入口）
    const toolsEntry = page.getByRole('button', { name: /工具|Tools/i }).first()
    if (await toolsEntry.count() > 0) {
      await toolsEntry.click()
    } else {
      const drawer = page.getByRole('button', { name: /menu|菜单|☰/i }).first()
      if (await drawer.count() > 0) {
        await drawer.click()
        await page.waitForTimeout(300)
        const t2 = page.getByRole('button', { name: /工具|Tools/i }).first()
        if (await t2.count() > 0) await t2.click()
      }
    }
    await page.waitForTimeout(600)

    // 选中「统计」面板 tab（session-stats view 的标题）
    const statsTab = page.getByRole('tab', { name: /统计|Stats/i }).first()
    if (await statsTab.count() > 0) await statsTab.click()
    await page.waitForTimeout(600)

    // 侧边栏形态：有「统计详情」入口，且没有时间范围切换
    const openDetail = page.getByTestId('stats-open-detail').first()
    await expect(openDetail).toBeVisible({ timeout: 5000 })
    expect(await page.getByTestId('stats-range').count()).toBe(0)

    // 点「统计详情」→ 打开全屏详情视图
    await openDetail.click()
    await page.waitForTimeout(800)

    // 【核心断言】详情视图渲染详情布局（时间范围切换），而不是紧凑版复刻
    await expect(page.getByTestId('stats-range').first()).toBeVisible({ timeout: 5000 })

    await page.close()
  })
})
