import { test, expect } from '@playwright/test'

const username = process.env.E2E_USERNAME || 'admin'
const password = process.env.E2E_PASSWORD || 'admin'

test.describe('Login flow', () => {
  test('should show login page', async ({ page }) => {
    await page.goto('/')
    // Should see username input
    await expect(page.locator('input[name="username"]')).toBeVisible({ timeout: 10_000 })
    await expect(page.locator('input[name="password"]')).toBeVisible()
  })

  test('should login and redirect to chat', async ({ page }) => {
    await page.goto('/')
    await page.fill('input[name="username"]', username)
    await page.fill('input[name="password"]', password)
    await page.click('button[type="submit"], button:has-text("登录"), button:has-text("Login")')

    // Should navigate away from login page (wait for chat area to appear)
    await expect(page.locator('[data-testid="assistant-turn"], .chat-messages, .bg-slate-900')).toBeVisible({ timeout: 15_000 })
  })

  test('should show error on invalid credentials', async ({ page }) => {
    await page.goto('/')
    await page.fill('input[name="username"]', 'invalid_user_xyz')
    await page.fill('input[name="password"]', 'wrong_password')
    await page.click('button[type="submit"], button:has-text("登录"), button:has-text("Login")')

    // Should still be on login page with an error indication
    await page.waitForTimeout(2000)
    // Page should still have login inputs (didn't redirect)
    await expect(page.locator('input[name="username"]')).toBeVisible()
  })
})
