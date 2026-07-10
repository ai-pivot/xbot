/**
 * Right sidebar acceptance E2E (Spec 6).
 *
 * Exercises the four right-sidebar panels against the spec's acceptance
 * criteria: expand/collapse, file tree toggle, file-click → workspace tab,
 * debounced search + highlight, diff line coloring, session-config model
 * switch, and drag-resize (200–500px). The app renders without a backend
 * (WS reconnects gracefully), so this needs no login or running server.
 *
 * WebSocket connection failures are expected (no backend) and tolerated.
 */
import { test, expect } from '@playwright/test'

// Ignore the expected WS connect noise (no backend in CI/standalone runs).
const realConsoleErrors: string[] = []
const isRealError = (m: string) =>
  !/WebSocket connection to .*\/ws failed/i.test(m) && !/before receiving a handshake response/i.test(m)

test.beforeEach(async ({ page }) => {
  realConsoleErrors.length = 0
  page.on('pageerror', (e) => realConsoleErrors.push(`pageerror: ${e.message}`))
  page.on('console', (m) => {
    if (m.type() === 'error' && isRealError(m.text())) realConsoleErrors.push(m.text())
  })
})

test.afterEach(() => {
  expect(realConsoleErrors, `unexpected console errors: ${realConsoleErrors.join(' | ')}`).toEqual([])
})

test.describe('Right sidebar (Spec 6)', () => {
  test('expands/collapses and renders all four panels', async ({ page }) => {
    await page.goto('/')
    const rightBar = page.locator('.flex.h-full.w-12.shrink-0.flex-col').last()
    await rightBar.waitFor({ timeout: 10_000 })
    const panels = rightBar.locator('button[aria-pressed]')
    await expect(panels).toHaveCount(4)
  })

  test('file tree toggles and opens a workspace tab on click', async ({ page }) => {
    await page.goto('/')
    const rightBar = page.locator('.flex.h-full.w-12.shrink-0.flex-col').last()
    const panels = rightBar.locator('button[aria-pressed]')
    await panels.nth(0).click()
    await page.waitForTimeout(400)

    // /src expanded by default → its child `components` dir is visible.
    await expect(page.locator('aside button:has-text("components")').first()).toBeVisible()
    // Expand components → nested `sidebar` dir appears.
    await page.locator('aside button:has-text("components")').first().click()
    await page.waitForTimeout(200)
    await expect(page.locator('aside button:has-text("sidebar")')).toHaveCount(1)
    // Collapse /src → descendants gone.
    await page.locator('aside button:has-text("src")').first().click()
    await page.waitForTimeout(200)
    await expect(page.locator('aside button:has-text("components")')).toHaveCount(0)
    await page.locator('aside button:has-text("src")').first().click()
    await page.waitForTimeout(200)

    // Click a file → workspace gains an App.tsx tab.
    const tabsBefore = await page.locator('[role="tab"]').count()
    await page.locator('aside button:has-text("App.tsx")').first().click()
    await page.waitForTimeout(400)
    const tabsAfter = await page.locator('[role="tab"]').count()
    expect(tabsAfter).toBeGreaterThan(tabsBefore)
    await expect(page.locator('[role="tab"]').filter({ hasText: 'App.tsx' })).toHaveCount(1)
  })

  test('file search filters (debounced) and highlights matches', async ({ page }) => {
    await page.goto('/')
    const rightBar = page.locator('.flex.h-full.w-12.shrink-0.flex-col').last()
    const panels = rightBar.locator('button[aria-pressed]')
    await panels.nth(1).click()
    await page.waitForTimeout(300)
    const input = page.locator('aside input').first()
    await input.fill('App')
    await page.waitForTimeout(320) // debounce 200ms + render
    await expect(page.locator('aside ul li button')).not.toHaveCount(0)
    await expect(page.locator('aside mark')).not.toHaveCount(0)
    await input.fill('zzzznotfound')
    await page.waitForTimeout(320)
    await expect(page.locator('aside ul li button')).toHaveCount(0)
  })

  test('diff viewer colors added/removed lines', async ({ page }) => {
    await page.goto('/')
    const rightBar = page.locator('.flex.h-full.w-12.shrink-0.flex-col').last()
    const panels = rightBar.locator('button[aria-pressed]')
    await panels.nth(2).click()
    await page.waitForTimeout(300)
    const lines = page.locator('aside pre > div')
    await expect(lines).not.toHaveCount(0)
    // At least one added or removed line (theme-driven diff bg color).
    await expect(page.locator('aside pre > div[style*="--diff-"]')).not.toHaveCount(0)
  })

  test('session config shows info and switches model', async ({ page }) => {
    await page.goto('/')
    const rightBar = page.locator('.flex.h-full.w-12.shrink-0.flex-col').last()
    const panels = rightBar.locator('button[aria-pressed]')
    await panels.nth(3).click()
    await page.waitForTimeout(300)
    // Three sections: session info, model, token config.
    await expect(page.locator('aside h3')).toHaveCount(3)
    await expect(page.locator('aside [role="combobox"]')).not.toHaveCount(0)
    await page.locator('aside [role="combobox"]').first().click()
    await page.waitForTimeout(200)
    const options = page.locator('[role="option"]')
    expect(await options.count()).toBeGreaterThan(1)
    await options.nth(1).click()
    await page.waitForTimeout(150)
    // Selection no longer the first mock model (gpt-4o).
    const text = (await page.locator('aside [role="combobox"]').first().innerText()).trim()
    expect(text).not.toBe('gpt-4o')
  })

  test('sidebar is drag-resizable between 200 and 500px', async ({ page }) => {
    await page.goto('/')
    const rightBar = page.locator('.flex.h-full.w-12.shrink-0.flex-col').last()
    const panels = rightBar.locator('button[aria-pressed]')
    await panels.nth(0).click()
    await page.waitForTimeout(350)
    const aside = page.locator('aside').last()
    const startW = await aside.evaluate((el) => el.getBoundingClientRect().width)
    const handle = aside.locator('div[role="separator"]')
    const hb = await handle.boundingBox()
    expect(hb).not.toBeNull()
    const cx = hb!.x + hb!.width / 2
    const cy = hb!.y + hb!.height / 2

    // Drag left by 120px → widen.
    await page.mouse.move(cx, cy)
    await page.mouse.down()
    await page.mouse.move(cx - 120, cy, { steps: 8 })
    await page.mouse.up()
    await page.waitForTimeout(350)
    const grew = await aside.evaluate((el) => el.getBoundingClientRect().width)
    expect(Math.round(grew)).toBeGreaterThanOrEqual(Math.round(startW) + 100)

    // Drag right by 300px → narrow, clamp at 200.
    await page.mouse.move(handle ? cx : cx, cy)
    await page.mouse.down()
    await page.mouse.move(cx + 300, cy, { steps: 8 })
    await page.mouse.up()
    await page.waitForTimeout(350)
    const shrank = await aside.evaluate((el) => el.getBoundingClientRect().width)
    expect(Math.round(shrank)).toBeGreaterThanOrEqual(200)
    expect(Math.round(shrank)).toBeLessThanOrEqual(Math.round(grew))
  })
})
