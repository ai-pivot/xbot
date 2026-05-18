import { test, expect } from '@playwright/test'

const username = process.env.E2E_USERNAME || 'admin'
const password = process.env.E2E_PASSWORD || 'admin'

// Helper: login before each test
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

test.describe('Chat interaction', () => {
  test('should have visible editor/input area', async ({ page }) => {
    const editor = page.locator('.tiptap, textarea')
    await expect(editor).toBeVisible({ timeout: 5_000 })
  })

  test('should have a send button', async ({ page }) => {
    const sendBtn = page.locator('button[aria-label="发送"], button[aria-label="Send"], button.send-btn')
    // At least one send mechanism should exist
    await expect(sendBtn.or(page.locator('button:has-text("发送")'))).toBeVisible({ timeout: 5_000 })
  })

  test('should show user message after sending', async ({ page }) => {
    const editor = page.locator('.tiptap')
    if (await editor.isVisible({ timeout: 3000 }).catch(() => false)) {
      await editor.click()
      await editor.fill('hello test')
    } else {
      const textarea = page.locator('textarea')
      await textarea.click()
      await textarea.fill('hello test')
    }

    // Click send
    const sendBtn = page.locator('button[aria-label="发送"], button[aria-label="Send"], button.send-btn, button:has-text("发送")')
    await sendBtn.first().click()

    // Check for user message bubble
    await expect(page.locator('.message-user, [data-msg-type="user"], .bg-blue-600, .bg-blue-700')).toBeVisible({ timeout: 5_000 })
  })
})
