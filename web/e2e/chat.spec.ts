import { test, expect } from '@playwright/test'

const username = process.env.E2E_USERNAME || 'admin'
const password = process.env.E2E_PASSWORD || 'admin'

// Helper: login before each test
test.beforeEach(async ({ page }) => {
  await page.goto('/')
  // Wait for either the login form or the chat editor (already authed).
  const userInput = page.locator('#login-username')
  const editor = page.locator('textarea, [contenteditable]')
  await userInput.or(editor).first().waitFor({ timeout: 15_000 }).catch(() => {})
  if (await userInput.isVisible().catch(() => false)) {
    await page.fill('#login-username', username)
    await page.fill('#login-password', password)
    await page.click('button[type="submit"], button:has-text("登录"), button:has-text("Login")')
    await expect(page.locator('textarea, [contenteditable]')).toBeVisible({ timeout: 15_000 })
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
    // Wait for the session tree to finish loading — deterministic rendering
    // matches user_echo by chatID, so sending before the frontend binds a
    // chatID (session tree still loading under parallel load) makes the echo
    // unroutable and the user message never renders.
    await expect(page.locator('text=Loading...')).toHaveCount(0, { timeout: 15_000 }).catch(() => {})

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

    // Check for user message bubble (the newly sent one with our text)
    await expect(page.locator('[data-role="user"]', { hasText: 'hello test' }).first()).toBeVisible({ timeout: 5_000 })
  })
})
