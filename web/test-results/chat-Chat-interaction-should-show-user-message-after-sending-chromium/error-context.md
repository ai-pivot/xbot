# Instructions

- Following Playwright test failed.
- Explain why, be concise, respect Playwright best practices.
- Provide a snippet of code with the fix, if possible.

# Test info

- Name: chat.spec.ts >> Chat interaction >> should show user message after sending
- Location: e2e/chat.spec.ts:30:3

# Error details

```
Test timeout of 30000ms exceeded.
```

```
Error: locator.click: Test timeout of 30000ms exceeded.
Call log:
  - waiting for locator('textarea')

```

# Page snapshot

```yaml
- generic [ref=e2]:
  - generic [ref=e4]:
    - generic [ref=e5]:
      - heading "xbot" [level=1] [ref=e6]
      - paragraph [ref=e7]: Sign in to xbot
    - generic [ref=e8]:
      - generic [ref=e9]:
        - generic [ref=e10]: Username
        - textbox "Username" [active] [ref=e11]:
          - /placeholder: Enter username
      - generic [ref=e12]:
        - generic [ref=e13]: Password
        - generic [ref=e14]:
          - textbox "Password" [ref=e15]:
            - /placeholder: Enter password
          - button "Show password" [ref=e16]:
            - img [ref=e17]
      - button "Login" [ref=e20]
    - paragraph [ref=e21]:
      - text: Don't have an account?
      - link "Register" [ref=e22] [cursor=pointer]:
        - /url: /register
  - region "Notifications alt+T"
```

# Test source

```ts
  1  | import { test, expect } from '@playwright/test'
  2  | 
  3  | const username = process.env.E2E_USERNAME || 'admin'
  4  | const password = process.env.E2E_PASSWORD || 'admin'
  5  | 
  6  | // Helper: login before each test
  7  | test.beforeEach(async ({ page }) => {
  8  |   await page.goto('/')
  9  |   const userInput = page.locator('input[name="username"]')
  10 |   if (await userInput.isVisible({ timeout: 5000 }).catch(() => false)) {
  11 |     await page.fill('input[name="username"]', username)
  12 |     await page.fill('input[name="password"]', password)
  13 |     await page.click('button[type="submit"], button:has-text("登录"), button:has-text("Login")')
  14 |     await expect(page.locator('.bg-slate-900')).toBeVisible({ timeout: 15_000 })
  15 |   }
  16 | })
  17 | 
  18 | test.describe('Chat interaction', () => {
  19 |   test('should have visible editor/input area', async ({ page }) => {
  20 |     const editor = page.locator('.tiptap, textarea')
  21 |     await expect(editor).toBeVisible({ timeout: 5_000 })
  22 |   })
  23 | 
  24 |   test('should have a send button', async ({ page }) => {
  25 |     const sendBtn = page.locator('button[aria-label="发送"], button[aria-label="Send"], button.send-btn')
  26 |     // At least one send mechanism should exist
  27 |     await expect(sendBtn.or(page.locator('button:has-text("发送")'))).toBeVisible({ timeout: 5_000 })
  28 |   })
  29 | 
  30 |   test('should show user message after sending', async ({ page }) => {
  31 |     const editor = page.locator('.tiptap')
  32 |     if (await editor.isVisible({ timeout: 3000 }).catch(() => false)) {
  33 |       await editor.click()
  34 |       await editor.fill('hello test')
  35 |     } else {
  36 |       const textarea = page.locator('textarea')
> 37 |       await textarea.click()
     |                      ^ Error: locator.click: Test timeout of 30000ms exceeded.
  38 |       await textarea.fill('hello test')
  39 |     }
  40 | 
  41 |     // Click send
  42 |     const sendBtn = page.locator('button[aria-label="发送"], button[aria-label="Send"], button.send-btn, button:has-text("发送")')
  43 |     await sendBtn.first().click()
  44 | 
  45 |     // Check for user message bubble
  46 |     await expect(page.locator('.message-user, [data-msg-type="user"], .bg-blue-600, .bg-blue-700')).toBeVisible({ timeout: 5_000 })
  47 |   })
  48 | })
  49 | 
```