/**
 * 指挥室面板视觉验收：三张截图（全景 / 选中详情 / 回放中）。
 *
 * 走 `/court-demo`（内置 seed 数据，不发起认证请求），渲染的是与 `/court`
 * 完全相同的 CourtConsole 组件。产物：web/e2e/artifacts/console-*.png。
 */
import { expect, test } from '@playwright/test'

test.describe('Agent 通信指挥室', () => {
  test.use({ viewport: { width: 1500, height: 940 } })

  test('renders overview, selection and playback screenshots', async ({ page }) => {
    await page.goto('/court-demo')
    const canvas = page.locator('canvas[aria-label="通信时间轴"]')
    await expect(canvas).toBeVisible()
    // 等 Canvas 完成首帧 + 字体/布局稳定
    await page.waitForTimeout(1200)

    await page.screenshot({ path: 'e2e/artifacts/console-overview.png' })

    // 展开第一个线程并选中其中一条消息 → 右侧详情
    const threadButton = page.locator('button', { hasText: 'B300 集群今日吞吐' }).first()
    await expect(threadButton).toBeVisible()
    await threadButton.click()
    const firstMsg = page.locator('aside [data-msg-id]').first()
    await expect(firstMsg).toBeVisible()
    await firstMsg.click()
    await page.waitForTimeout(500)
    await page.screenshot({ path: 'e2e/artifacts/console-selected.png' })

    // 回放：重新加载后播放，截取回放中的半程状态
    await page.goto('/court-demo')
    await page.waitForTimeout(1000)
    await page.getByLabel('开始回放').click()
    await page.waitForTimeout(1300)
    await page.screenshot({ path: 'e2e/artifacts/console-playback.png' })
  })
})
