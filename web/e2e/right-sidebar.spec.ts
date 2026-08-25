/**
 * Right sidebar acceptance E2E (Spec 6, updated for current UI).
 *
 * Exercises the right-sidebar panels: expand/collapse, file tree toggle,
 * file-click → workspace tab, debounced search + highlight, session info,
 * and drag-resize (200–500px).
 *
 * NOTE: the diff-viewer panel was removed from the UI; the Info panel now
 * shows session metadata + model. Runs against a real backend in CI
 * (login handled in beforeEach) or standalone with WS failures tolerated.
 */
import { test, expect } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

// Ignore expected console noise: WS connect failures (no backend in
// standalone runs) and 401s from pre-login page loads (unauthenticated
// /api/* calls are expected before the login redirect completes).
const realConsoleErrors: string[] = []
const isRealError = (m: string) =>
  !/WebSocket connection to .*\/ws failed/i.test(m) &&
  !/before receiving a handshake response/i.test(m) &&
  !/status of 401/i.test(m)

test.beforeEach(async ({ page }) => {
  realConsoleErrors.length = 0
  page.on('pageerror', (e) => realConsoleErrors.push(`pageerror: ${e.message}`))
  page.on('console', (m) => {
    if (m.type() === 'error' && isRealError(m.text())) realConsoleErrors.push(m.text())
  })
  await page.goto(`${BASE}/`)
  // Login if the login page is shown (real backend in CI).
  const userInput = page.locator('#login-username')
  const editor = page.locator('textarea, [contenteditable]')
  await userInput.or(editor).first().waitFor({ timeout: 15_000 }).catch(() => {})
  if (await userInput.isVisible().catch(() => false)) {
    await page.fill('#login-username', process.env.E2E_USERNAME || 'admin')
    await page.fill('#login-password', process.env.E2E_PASSWORD || 'admin')
    await page.click('button[type="submit"], button:has-text("登录"), button:has-text("Login")')
    await expect(editor).toBeVisible({ timeout: 15_000 })
  }
})

test.afterEach(() => {
  expect(realConsoleErrors, `unexpected console errors: ${realConsoleErrors.join(' | ')}`).toEqual([])
})

test.describe('Right sidebar (Spec 6)', () => {
  test('expands/collapses and renders all right-sidebar panels', async ({ page }) => {
    const rightBar = page.locator('.flex.h-full.w-12.shrink-0.flex-col').last()
    await rightBar.waitFor({ timeout: 10_000 })
    const panels = rightBar.locator('button[aria-pressed]')
    // 5 内置面板（files/search/info/tasks/terminal）+ 2 内置插件 view
    // （xbot.plugin-manager + xbot.skill-manager，均 container:right_sidebar
    //  → desktop.sidebar slot）。skill-manager 加入后由 6 变 7（正确行为）。
    await expect(panels).toHaveCount(7)
  })

  test('file tree toggles and opens a workspace tab on click', async ({ page }) => {
    const rightBar = page.locator('.flex.h-full.w-12.shrink-0.flex-col').last()
    const panels = rightBar.locator('button[aria-pressed]')
    await panels.nth(0).click()
    await page.waitForTimeout(800)

    // The backend workdir is the repo root — `web` is a top-level dir.
    await expect(page.locator('aside button:has-text("web")').first()).toBeVisible()
    // Expand web → src appears.
    await page.locator('aside button:has-text("web")').first().click()
    await page.waitForTimeout(600)
    await expect(page.locator('aside button:has-text("src")')).toHaveCount(1)
    // Expand src → App.tsx appears.
    await page.locator('aside button:has-text("src")').first().click()
    await page.waitForTimeout(600)
    await expect(page.locator('aside button:has-text("App.tsx")')).toHaveCount(1)

    // Click a file → workspace gains an App.tsx tab. Dockview renders the
    // tab in both the main and subagent docks, so assert >= 1 (not exactly 1).
    const tabsBefore = await page.locator('[role="tab"]').count()
    await page.locator('aside button:has-text("App.tsx")').first().click()
    await page.waitForTimeout(600)
    const tabsAfter = await page.locator('[role="tab"]').count()
    expect(tabsAfter).toBeGreaterThan(tabsBefore)
    await expect(page.locator('[role="tab"]').filter({ hasText: 'App.tsx' })).not.toHaveCount(0)
  })

  test('file search filters (debounced) and highlights matches', async ({ page }) => {
    const rightBar = page.locator('.flex.h-full.w-12.shrink-0.flex-col').last()
    const panels = rightBar.locator('button[aria-pressed]')
    await panels.nth(1).click()
    await page.waitForTimeout(800)
    const input = page.locator('aside input').first()
    // Search a top-level file (flatFiles only contains loaded tree nodes —
    // deep files under unexpanded dirs are not in the search index yet).
    await input.fill('AGENTS')
    await page.waitForTimeout(800) // debounce 200ms + file tree load + render
    await expect(page.locator('aside ul li button')).not.toHaveCount(0)
    // Matched substring is highlighted with <mark>.
    await expect(page.locator('aside mark')).not.toHaveCount(0)
    await input.fill('zzzznotfound')
    await page.waitForTimeout(800)
    await expect(page.locator('aside ul li button')).toHaveCount(0)
  })

  test('info panel shows session metadata and model', async ({ page }) => {
    const rightBar = page.locator('.flex.h-full.w-12.shrink-0.flex-col').last()
    const panels = rightBar.locator('button[aria-pressed]')
    await panels.nth(2).click()
    await page.waitForTimeout(600)
    // Two sections: session info, model.
    await expect(page.locator('aside h3')).toHaveCount(2)
    await expect(page.locator('aside').last()).toContainText('Session info')
    await expect(page.locator('aside').last()).toContainText('Model')
  })

  test('sidebar is drag-resizable between 200 and 500px', async ({ page }) => {
    const rightBar = page.locator('.flex.h-full.w-12.shrink-0.flex-col').last()
    const panels = rightBar.locator('button[aria-pressed]')
    await panels.nth(0).click()
    await page.waitForTimeout(600)
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
    await page.waitForTimeout(400)
    const grew = await aside.evaluate((el) => el.getBoundingClientRect().width)
    expect(Math.round(grew)).toBeGreaterThanOrEqual(Math.round(startW) + 100)

    // Drag right by 300px → narrow, clamp at 200.
    await page.mouse.move(cx, cy)
    await page.mouse.down()
    await page.mouse.move(cx + 300, cy, { steps: 8 })
    await page.mouse.up()
    await page.waitForTimeout(400)
    const shrank = await aside.evaluate((el) => el.getBoundingClientRect().width)
    expect(Math.round(shrank)).toBeGreaterThanOrEqual(200)
    expect(Math.round(shrank)).toBeLessThanOrEqual(Math.round(grew))
  })
})
