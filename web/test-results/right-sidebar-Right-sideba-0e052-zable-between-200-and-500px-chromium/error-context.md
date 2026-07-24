# Instructions

- Following Playwright test failed.
- Explain why, be concise, respect Playwright best practices.
- Provide a snippet of code with the fix, if possible.

# Test info

- Name: right-sidebar.spec.ts >> Right sidebar (Spec 6) >> sidebar is drag-resizable between 200 and 500px
- Location: e2e/right-sidebar.spec.ts:117:3

# Error details

```
Test timeout of 30000ms exceeded.
```

```
Error: unexpected console errors: Failed to load resource: the server responded with a status of 502 (Bad Gateway) | Failed to load resource: the server responded with a status of 502 (Bad Gateway) | Failed to load resource: the server responded with a status of 502 (Bad Gateway) | Failed to load resource: the server responded with a status of 502 (Bad Gateway)

expect(received).toEqual(expected) // deep equality

- Expected  - 1
+ Received  + 6

- Array []
+ Array [
+   "Failed to load resource: the server responded with a status of 502 (Bad Gateway)",
+   "Failed to load resource: the server responded with a status of 502 (Bad Gateway)",
+   "Failed to load resource: the server responded with a status of 502 (Bad Gateway)",
+   "Failed to load resource: the server responded with a status of 502 (Bad Gateway)",
+ ]
```

```
Error: locator.click: Test timeout of 30000ms exceeded.
Call log:
  - waiting for locator('.flex.h-full.w-12.shrink-0.flex-col').last().locator('button[aria-pressed]').first()

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
  21  |   page.on('pageerror', (e) => realConsoleErrors.push(`pageerror: ${e.message}`))
  22  |   page.on('console', (m) => {
  23  |     if (m.type() === 'error' && isRealError(m.text())) realConsoleErrors.push(m.text())
  24  |   })
  25  | })
  26  | 
  27  | test.afterEach(() => {
  28  |   expect(realConsoleErrors, `unexpected console errors: ${realConsoleErrors.join(' | ')}`).toEqual([])
  29  | })
  30  | 
  31  | test.describe('Right sidebar (Spec 6)', () => {
  32  |   test('expands/collapses and renders all four panels', async ({ page }) => {
  33  |     await page.goto('/')
  34  |     const rightBar = page.locator('.flex.h-full.w-12.shrink-0.flex-col').last()
  35  |     await rightBar.waitFor({ timeout: 10_000 })
  36  |     const panels = rightBar.locator('button[aria-pressed]')
  37  |     await expect(panels).toHaveCount(4)
  38  |   })
  39  | 
  40  |   test('file tree toggles and opens a workspace tab on click', async ({ page }) => {
  41  |     await page.goto('/')
  42  |     const rightBar = page.locator('.flex.h-full.w-12.shrink-0.flex-col').last()
  43  |     const panels = rightBar.locator('button[aria-pressed]')
  44  |     await panels.nth(0).click()
  45  |     await page.waitForTimeout(400)
  46  | 
  47  |     // /src expanded by default → its child `components` dir is visible.
  48  |     await expect(page.locator('aside button:has-text("components")').first()).toBeVisible()
  49  |     // Expand components → nested `sidebar` dir appears.
  50  |     await page.locator('aside button:has-text("components")').first().click()
  51  |     await page.waitForTimeout(200)
  52  |     await expect(page.locator('aside button:has-text("sidebar")')).toHaveCount(1)
  53  |     // Collapse /src → descendants gone.
  54  |     await page.locator('aside button:has-text("src")').first().click()
  55  |     await page.waitForTimeout(200)
  56  |     await expect(page.locator('aside button:has-text("components")')).toHaveCount(0)
  57  |     await page.locator('aside button:has-text("src")').first().click()
  58  |     await page.waitForTimeout(200)
  59  | 
  60  |     // Click a file → workspace gains an App.tsx tab.
  61  |     const tabsBefore = await page.locator('[role="tab"]').count()
  62  |     await page.locator('aside button:has-text("App.tsx")').first().click()
  63  |     await page.waitForTimeout(400)
  64  |     const tabsAfter = await page.locator('[role="tab"]').count()
  65  |     expect(tabsAfter).toBeGreaterThan(tabsBefore)
  66  |     await expect(page.locator('[role="tab"]').filter({ hasText: 'App.tsx' })).toHaveCount(1)
  67  |   })
  68  | 
  69  |   test('file search filters (debounced) and highlights matches', async ({ page }) => {
  70  |     await page.goto('/')
  71  |     const rightBar = page.locator('.flex.h-full.w-12.shrink-0.flex-col').last()
  72  |     const panels = rightBar.locator('button[aria-pressed]')
  73  |     await panels.nth(1).click()
  74  |     await page.waitForTimeout(300)
  75  |     const input = page.locator('aside input').first()
  76  |     await input.fill('App')
  77  |     await page.waitForTimeout(320) // debounce 200ms + render
  78  |     await expect(page.locator('aside ul li button')).not.toHaveCount(0)
  79  |     await expect(page.locator('aside mark')).not.toHaveCount(0)
  80  |     await input.fill('zzzznotfound')
  81  |     await page.waitForTimeout(320)
  82  |     await expect(page.locator('aside ul li button')).toHaveCount(0)
  83  |   })
  84  | 
  85  |   test('diff viewer colors added/removed lines', async ({ page }) => {
  86  |     await page.goto('/')
  87  |     const rightBar = page.locator('.flex.h-full.w-12.shrink-0.flex-col').last()
  88  |     const panels = rightBar.locator('button[aria-pressed]')
  89  |     await panels.nth(2).click()
  90  |     await page.waitForTimeout(300)
  91  |     const lines = page.locator('aside pre > div')
  92  |     await expect(lines).not.toHaveCount(0)
  93  |     // At least one added or removed line (theme-driven diff bg color).
  94  |     await expect(page.locator('aside pre > div[style*="--diff-"]')).not.toHaveCount(0)
  95  |   })
  96  | 
  97  |   test('session config shows info and switches model', async ({ page }) => {
  98  |     await page.goto('/')
  99  |     const rightBar = page.locator('.flex.h-full.w-12.shrink-0.flex-col').last()
  100 |     const panels = rightBar.locator('button[aria-pressed]')
  101 |     await panels.nth(3).click()
  102 |     await page.waitForTimeout(300)
  103 |     // Three sections: session info, model, token config.
  104 |     await expect(page.locator('aside h3')).toHaveCount(3)
  105 |     await expect(page.locator('aside [role="combobox"]')).not.toHaveCount(0)
  106 |     await page.locator('aside [role="combobox"]').first().click()
  107 |     await page.waitForTimeout(200)
  108 |     const options = page.locator('[role="option"]')
  109 |     expect(await options.count()).toBeGreaterThan(1)
  110 |     await options.nth(1).click()
  111 |     await page.waitForTimeout(150)
  112 |     // Selection no longer the first mock model (gpt-4o).
  113 |     const text = (await page.locator('aside [role="combobox"]').first().innerText()).trim()
  114 |     expect(text).not.toBe('gpt-4o')
  115 |   })
  116 | 
  117 |   test('sidebar is drag-resizable between 200 and 500px', async ({ page }) => {
  118 |     await page.goto('/')
  119 |     const rightBar = page.locator('.flex.h-full.w-12.shrink-0.flex-col').last()
  120 |     const panels = rightBar.locator('button[aria-pressed]')
> 121 |     await panels.nth(0).click()
      |                         ^ Error: locator.click: Test timeout of 30000ms exceeded.
  122 |     await page.waitForTimeout(350)
  123 |     const aside = page.locator('aside').last()
  124 |     const startW = await aside.evaluate((el) => el.getBoundingClientRect().width)
  125 |     const handle = aside.locator('div[role="separator"]')
  126 |     const hb = await handle.boundingBox()
  127 |     expect(hb).not.toBeNull()
  128 |     const cx = hb!.x + hb!.width / 2
  129 |     const cy = hb!.y + hb!.height / 2
  130 | 
  131 |     // Drag left by 120px → widen.
  132 |     await page.mouse.move(cx, cy)
  133 |     await page.mouse.down()
  134 |     await page.mouse.move(cx - 120, cy, { steps: 8 })
  135 |     await page.mouse.up()
  136 |     await page.waitForTimeout(350)
  137 |     const grew = await aside.evaluate((el) => el.getBoundingClientRect().width)
  138 |     expect(Math.round(grew)).toBeGreaterThanOrEqual(Math.round(startW) + 100)
  139 | 
  140 |     // Drag right by 300px → narrow, clamp at 200.
  141 |     await page.mouse.move(handle ? cx : cx, cy)
  142 |     await page.mouse.down()
  143 |     await page.mouse.move(cx + 300, cy, { steps: 8 })
  144 |     await page.mouse.up()
  145 |     await page.waitForTimeout(350)
  146 |     const shrank = await aside.evaluate((el) => el.getBoundingClientRect().width)
  147 |     expect(Math.round(shrank)).toBeGreaterThanOrEqual(200)
  148 |     expect(Math.round(shrank)).toBeLessThanOrEqual(Math.round(grew))
  149 |   })
  150 | })
  151 | 
```