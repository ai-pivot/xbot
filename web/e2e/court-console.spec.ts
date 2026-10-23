/**
 * 指挥室面板视觉验收：三张截图（全景 / 选中详情 / 回放中）。
 *
 * 走 `/court-demo`（内置 seed 数据，不发起认证请求），渲染的是与 `/court`
 * 完全相同的 CourtConsole 组件。产物：web/e2e/artifacts/console-*.png。
 *
 * 2026-10-23 修正两处过期前提（验收意图不变：三张截图仍覆盖 全景 / 选中详情 / 回放中）：
 *   ① 默认视图自 10-22 起是「活动流」，不再是时间轴 ⇒ 先切到「时间轴」再断言画布；
 *   ② seed 的议题已换成真实编制名（旧断言词「B300 集群今日吞吐」在 seed 里 0 处）
 *      ⇒ 线程定位改用 seed.ts 里稳定存在的议题标题（正文，不含数字/时间）。
 */
import { expect, test, type Page } from '@playwright/test'

/** 切到「时间轴」视图：全景截图要的是画布，不是默认的活动流列表。 */
async function openTimeline(page: Page) {
  await page.getByRole('button', { name: '时间轴', exact: true }).click()
  const canvas = page.locator('canvas[aria-label="通信时间轴"]')
  await expect(canvas).toBeVisible()
  return canvas
}

test.describe('Agent 通信指挥室', () => {
  test.use({ viewport: { width: 1500, height: 940 } })

  test('renders overview, selection and playback screenshots', async ({ page }) => {
    await page.goto('/court-demo')
    await openTimeline(page)
    // 等 Canvas 完成首帧 + 字体/布局稳定
    await page.waitForTimeout(1200)

    await page.screenshot({ path: 'e2e/artifacts/console-overview.png' })

    // 展开一条线程并选中其中一条消息 → 右侧详情。
    // `[data-thread]` 是线程行的稳定结构标记；标题取自 seed.ts TOPICS（如「都察院：证据抽查复算」）。
    const threadButton = page.locator('[data-thread] button', { hasText: '都察院：证据抽查复算' }).first()
    await expect(threadButton).toBeVisible()
    await threadButton.click()
    const firstMsg = page.locator('aside [data-msg-id]').first()
    await expect(firstMsg).toBeVisible()
    await firstMsg.click()
    await expect(page.getByTestId('message-detail')).toBeVisible()
    await page.waitForTimeout(500)
    await page.screenshot({ path: 'e2e/artifacts/console-selected.png' })

    // 回放：重新加载后切到时间轴、播放，截取回放中的半程状态
    await page.goto('/court-demo')
    await openTimeline(page)
    await page.waitForTimeout(1000)
    // 回放条只在有事件时渲染（空态不渲染）；demo 是 seed 数据，必须存在且能点。
    await page.getByLabel('开始回放').click()
    await expect(page.getByLabel('暂停回放')).toBeVisible()
    await page.waitForTimeout(1300)
    await page.screenshot({ path: 'e2e/artifacts/console-playback.png' })
  })
})
