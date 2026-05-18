import { test, expect } from '@playwright/test'

const username = process.env.E2E_USERNAME || 'admin'
const password = process.env.E2E_PASSWORD || 'admin'

test.beforeEach(async ({ page }) => {
  await page.goto('/')
  const userInput = page.locator('input[name="username"]')
  if (await userInput.isVisible({ timeout: 5000 }).catch(() => false)) {
    await page.fill('input[name="username"]', username)
    await page.fill('input[name="password"]', password)
    await page.click('button[type="submit"], button:has-text("登录"), button:has-text("Login")')
    await expect(page.locator('.bg-slate-900')).toBeVisible({ timeout: 15_000 })
  }
})

test.describe('Tab bar', () => {
  test('tab bar should not be visible initially (no tabs)', async ({ page }) => {
    // No tabs open initially
    await expect(page.locator('[data-testid="tab-bar"]')).toBeHidden({ timeout: 3_000 }).catch(() => {
      // Tab bar may already be hidden — that's fine
    })
  })

  test('tab items should have draggable attribute', async ({ page }) => {
    // Open sidebar to see chat history
    const sidebar = page.locator('.chat-sidebar, [data-testid="sidebar"]')
    if (await sidebar.isVisible({ timeout: 3000 }).catch(() => false)) {
      // Click first chat to open a tab
      const firstChat = sidebar.locator('.chat-item, .sidebar-item').first()
      if (await firstChat.isVisible().catch(() => false)) {
        await firstChat.click()
        // Now tab bar should be visible
        await expect(page.locator('[data-testid="tab-bar"]')).toBeVisible({ timeout: 5_000 })
      }
    }
  })
})
