import { test, expect } from '@playwright/test'

/** 验证：设置 Dialog（z-50）打开时，消息列表的导航按钮（z-10）不可点。 */
test('nav button is blocked while a dialog overlay is open', async ({ page }) => {
  await page.goto('/')
  await page.fill('#login-username', 'e2e_tester')
  await page.fill('#login-password', 'e2e-pass-123')
  await page.click('button[type="submit"]')
  await page.waitForSelector('textarea, [contenteditable]', { timeout: 20_000 })
  await page.waitForTimeout(2500)

  const navBtn = page.locator('button[title="Scroll to top"]').first()
  await expect(navBtn).toHaveCount(1)
  // 无 overlay 时可点
  await expect(navBtn).toBeEnabled()

  // 打开设置 Dialog
  const gear = page.locator('button[aria-label*="Settings"], button[title*="Settings"], button[aria-label*="设置"], button[title*="设置"]').first()
  await gear.click()
  await page.waitForTimeout(1000)

  // trial click：只检查可点性，不真的点击
  let clickable = true
  try {
    await navBtn.click({ trial: true, timeout: 2500 })
  } catch {
    clickable = false
  }
  console.log('NAV_CLICKABLE_WITH_OVERLAY ' + clickable)
  expect(clickable, 'dialog 打开时导航按钮必须被遮挡（不可点）').toBe(false)
})
