# Instructions

- Following Playwright test failed.
- Explain why, be concise, respect Playwright best practices.
- Provide a snippet of code with the fix, if possible.

# Test info

- Name: login.spec.ts >> Login flow >> should show login page
- Location: e2e/login.spec.ts:7:3

# Error details

```
Error: expect(locator).toBeVisible() failed

Locator: locator('input[name="username"]')
Expected: visible
Timeout: 10000ms
Error: element(s) not found

Call log:
  - Expect "toBeVisible" with timeout 10000ms
  - waiting for locator('input[name="username"]')

```

```yaml
- heading "xbot" [level=1]
- paragraph: Sign in to xbot
- text: Username
- textbox "Username":
  - /placeholder: Enter username
- text: Password
- textbox "Password":
  - /placeholder: Enter password
- button "Show password"
- button "Login"
- paragraph:
  - text: Don't have an account?
  - link "Register":
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
> 10 |     await expect(page.locator('input[name="username"]')).toBeVisible({ timeout: 10_000 })
     |                                                          ^ Error: expect(locator).toBeVisible() failed
  11 |     await expect(page.locator('input[name="password"]')).toBeVisible()
  12 |   })
  13 | 
  14 |   test('should login and redirect to chat', async ({ page }) => {
  15 |     await page.goto('/')
  16 |     await page.fill('input[name="username"]', username)
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