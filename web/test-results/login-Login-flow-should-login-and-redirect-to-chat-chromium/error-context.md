# Instructions

- Following Playwright test failed.
- Explain why, be concise, respect Playwright best practices.
- Provide a snippet of code with the fix, if possible.

# Test info

- Name: login.spec.ts >> Login flow >> should login and redirect to chat
- Location: e2e/login.spec.ts:14:3

# Error details

```
Test timeout of 30000ms exceeded.
```

```
Error: page.fill: Test timeout of 30000ms exceeded.
Call log:
  - waiting for locator('input[name="username"]')

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
  6  | test.describe('Login flow', () => {
  7  |   test('should show login page', async ({ page }) => {
  8  |     await page.goto('/')
  9  |     // Should see username input
  10 |     await expect(page.locator('input[name="username"]')).toBeVisible({ timeout: 10_000 })
  11 |     await expect(page.locator('input[name="password"]')).toBeVisible()
  12 |   })
  13 | 
  14 |   test('should login and redirect to chat', async ({ page }) => {
  15 |     await page.goto('/')
> 16 |     await page.fill('input[name="username"]', username)
     |                ^ Error: page.fill: Test timeout of 30000ms exceeded.
  17 |     await page.fill('input[name="password"]', password)
  18 |     await page.click('button[type="submit"], button:has-text("登录"), button:has-text("Login")')
  19 | 
  20 |     // Should navigate away from login page (wait for chat area to appear)
  21 |     await expect(page.locator('[data-testid="assistant-turn"], .chat-messages, .bg-slate-900')).toBeVisible({ timeout: 15_000 })
  22 |   })
  23 | 
  24 |   test('should show error on invalid credentials', async ({ page }) => {
  25 |     await page.goto('/')
  26 |     await page.fill('input[name="username"]', 'invalid_user_xyz')
  27 |     await page.fill('input[name="password"]', 'wrong_password')
  28 |     await page.click('button[type="submit"], button:has-text("登录"), button:has-text("Login")')
  29 | 
  30 |     // Should still be on login page with an error indication
  31 |     await page.waitForTimeout(2000)
  32 |     // Page should still have login inputs (didn't redirect)
  33 |     await expect(page.locator('input[name="username"]')).toBeVisible()
  34 |   })
  35 | })
  36 | 
```