/**
 * 手机端「设置」面板全 tab 溢出/截断回归。
 *
 * 覆盖 SettingsDialog 的全部移动端分类（不包含桌面专属 shortcuts 分类）：
 * 逐 tab 打开，检测「可见且不可达」的元素溢出（rect.right > 视口宽）。
 *
 * Measure visible text/control geometry and assert the aggregate is empty.
 * Ignore accessible scrolling, screen-reader descriptions, deliberate ellipsis,
 * and entirely offscreen translated drawers; do not silently log failures.
 */
import { test, expect } from '@playwright/test'
import { collectSettingsOverflow } from './helpers/settings-overflow'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

async function setupMock(page: import('@playwright/test').Page) {
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: {
          sessions: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() }],
          chats: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() }],
          orphan_subagents: [],
        },
      },
    }),
  )
  await page.route('**/api/history', (r) =>
    r.fulfill({ json: { ok: true, data: { messages: [], chat_id: 'chat-1', last_seq: 0, active_progress: null } } }),
  )
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/queue/list', (r) => r.fulfill({ json: { ok: true, data: { queued: [] } } }))

  await page.route('**/api/rpc', (route) => {
    const body = route.request().postDataJSON() as { method?: string }
    switch (body.method) {
      case 'list_subscriptions':
        return route.fulfill({
          json: {
            ok: true,
            data: [
              { id: 'sub-1', name: 'OpenAI 官方', provider: 'openai', base_url: 'https://api.openai.com/v1', api_key: 'sk-****', model: 'gpt-4o', enabled: true, active: true, api_type: 'chat_completions', per_model_configs: { 'gpt-4o': { max_context: 128000, max_output_tokens: 8192, api_type: '', enabled: true, vision: true } } },
              { id: 'sub-2', name: 'Anthropic 官方订阅（很长的订阅名称用来测试截断行为）', provider: 'anthropic', base_url: 'https://api.anthropic.com', api_key: 'sk-ant-****', model: 'claude-sonnet-4', enabled: true, active: false, api_type: '' },
            ],
          },
        })
      case 'list_all_model_entries':
        return route.fulfill({
          json: {
            ok: true,
            data: [
              { sub_id: 'sub-1', sub_name: 'OpenAI 官方', model: 'gpt-4o', status: 'normal', vision: true },
              { sub_id: 'sub-1', sub_name: 'OpenAI 官方', model: 'gpt-4o-mini-with-a-very-long-model-name-for-truncation-testing', status: 'normal' },
              { sub_id: 'sub-2', sub_name: 'Anthropic 官方订阅（很长的订阅名称用来测试截断行为）', model: 'claude-sonnet-4', status: 'normal' },
            ],
          },
        })
      case 'get_user_thinking_mode':
        return route.fulfill({ json: { ok: true, data: 'auto' } })
      case 'get_llm_concurrency':
        return route.fulfill({ json: { ok: true, data: 4 } })
      case 'get_settings':
        return route.fulfill({ json: { ok: true, data: {} } })
      case 'plugin_status':
      case 'plugin_list':
        return route.fulfill({ json: { ok: true, data: [] } })
      case 'list_runners':
        return route.fulfill({ json: { ok: true, data: [] } })
      default:
        return route.fulfill({ json: { ok: true, data: null } })
    }
  })
}

async function loginAndOpenSettings(page: import('@playwright/test').Page) {
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForTimeout(2500)
  await page
    .locator('button[aria-label="设置"], button[aria-label="打开设置"], button[aria-label="Settings"]')
    .first()
    .click()
  await page.waitForTimeout(500)
}

for (const viewport of [{ width: 1440, height: 1000 }, { width: 1024, height: 600 }]) {
  test(`shortcut settings header stays compact at ${viewport.width}px`, async ({ page }) => {
    await page.setViewportSize(viewport)
    await page.addInitScript(() => {
      localStorage.setItem('xbot-locale', 'zh-CN')
      localStorage.setItem('xbot-session-shortcuts', JSON.stringify({ openInTab: 'f8' }))
    })
    await setupMock(page)
    let creations = 0
    await page.route('**/api/chats/create', route => {
      creations++
      return route.fulfill({ json: { ok: true, data: {} } })
    })
    await loginAndOpenSettings(page)
    const dialog = page.getByRole('dialog', { name: '设置' })
    await dialog.locator('nav').getByRole('button', { name: '交互', exact: true }).click()
    await expect(dialog.getByRole('textbox')).toHaveCount(0)
    await expect(dialog.getByRole('heading', { name: '代码块自动换行' })).toBeVisible()
    await dialog.locator('nav').getByRole('button', { name: '快捷键', exact: true }).click()
    const section = dialog.getByRole('region', { name: '会话快捷键' })
    await expect(section.getByRole('textbox')).toHaveCount(7)
    const createField = section.getByRole('textbox', { name: '创建新会话' })
    await expect(createField).toHaveValue('F8')
    await expect(section.getByRole('textbox', { name: '在新标签页中打开' })).toHaveValue(
      await page.evaluate(() => /^(Linux|Win)/i.test(navigator.platform) ? 'Ctrl+Shift+2' : 'Ctrl+N'),
    )
    const heading = section.getByRole('heading', { name: '会话快捷键' })
    const reset = section.getByRole('button', { name: '全部恢复默认' })
    await expect(async () => {
      const titleRect = await heading.boundingBox()
      const resetRect = await reset.boundingBox()
      expect(titleRect).not.toBeNull()
      expect(resetRect).not.toBeNull()
      expect(Math.abs(titleRect!.y + titleRect!.height / 2 - resetRect!.y - resetRect!.height / 2)).toBeLessThanOrEqual(2)
      expect(resetRect!.x).toBeGreaterThanOrEqual(titleRect!.x + titleRect!.width)
      expect(resetRect!.x + resetRect!.width).toBeLessThanOrEqual(viewport.width)
    }).toPass()
    const headerHeight = await section.locator('header').evaluate(el => el.getBoundingClientRect().height)
    expect(headerHeight).toBeLessThanOrEqual(36)
    const overflow = await section.evaluate(el => el.scrollWidth - el.clientWidth)
    expect(overflow).toBeLessThanOrEqual(1)
    if (viewport.width >= 640) {
      const nav = dialog.locator('nav')
      const about = nav.getByRole('button', { name: '关于', exact: true })
      await about.scrollIntoViewIfNeeded()
      const navRect = await nav.boundingBox()
      const aboutRect = await about.boundingBox()
      expect(aboutRect!.y).toBeGreaterThanOrEqual(navRect!.y)
      expect(aboutRect!.y + aboutRect!.height).toBeLessThanOrEqual(navRect!.y + navRect!.height)
    }
    await createField.focus()
    await page.keyboard.press('F8')
    expect(creations).toBe(0)
    await page.keyboard.press('Escape')
    await expect(dialog).toBeHidden()
    const tabs = page.context().pages().length
    await page.keyboard.press('F8')
    await expect.poll(() => creations).toBe(1)
    expect(page.context().pages()).toHaveLength(tabs)
  })
}

for (const { viewport, mode } of [
  { viewport: { width: 390, height: 844 }, mode: 'auto' },
  { viewport: { width: 320, height: 700 }, mode: 'auto' },
  { viewport: { width: 1440, height: 1000 }, mode: 'mobile' },
]) {
  test(`mobile shortcut settings are absent at ${viewport.width}px in ${mode} mode`, async ({ page }) => {
    await page.setViewportSize(viewport)
    await page.addInitScript(mode => {
      localStorage.setItem('xbot-locale', 'zh-CN')
      localStorage.setItem('xbot-ui-mode', mode)
      localStorage.setItem('xbot-session-shortcuts', JSON.stringify({ openInTab: 'f8', star: 'f9' }))
    }, mode)
    await setupMock(page)
    let creations = 0
    await page.route('**/api/chats/create', route => {
      creations++
      return route.fulfill({ json: { ok: true, data: {} } })
    })
    await loginAndOpenSettings(page)
    const dialog = page.getByRole('dialog', { name: '设置' })
    await expect(dialog.locator('nav').getByRole('button', { name: '快捷键', exact: true })).toHaveCount(0)
    await expect(dialog.locator('[data-shortcut-action]')).toHaveCount(0)
    await dialog.locator('nav').getByRole('button', { name: '交互', exact: true }).click()
    await expect(dialog.getByRole('heading', { name: '代码块自动换行' })).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(dialog).toBeHidden()
    await page.keyboard.press('F8')
    await page.keyboard.press('F2')
    await expect(page.getByRole('dialog')).toHaveCount(0)
    expect(creations).toBe(0)
    await page.getByRole('button', { name: '会话', exact: true }).click()
    const row = page.locator('[data-session-row="web:chat-1"]')
    await row.click({ button: 'right' })
    const menu = page.getByRole('menu')
    await expect(menu).toBeVisible()
    await expect(menu.locator('[data-slot="context-menu-shortcut"]')).toHaveCount(0)
    await page.keyboard.press('F2')
    await expect(page.getByRole('dialog', { name: '重命名' })).toHaveCount(0)
    await menu.getByRole('menuitem', { name: '重命名', exact: true }).click()
    const rename = page.getByRole('dialog', { name: '重命名' })
    await expect(rename).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(rename).toBeHidden()
    await row.click()
    await expect(page.getByRole('dialog', { name: '会话', exact: true })).toBeHidden()
    await page.getByRole('button', { name: '新建会话', exact: true }).click()
    await expect.poll(() => creations).toBe(1)
    expect(await page.evaluate(() => JSON.parse(localStorage.getItem('xbot-session-shortcuts')!))).toEqual({ openInTab: 'f8', star: 'f9' })
  })
}

test('shortcut settings follow viewport changes without rewriting desktop bindings', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.addInitScript(() => {
    localStorage.setItem('xbot-locale', 'zh-CN')
    localStorage.setItem('xbot-session-shortcuts', JSON.stringify({ newSession: 'f8' }))
  })
  await setupMock(page)
  await loginAndOpenSettings(page)
  let dialog = page.getByRole('dialog', { name: '设置' })
  await dialog.locator('nav').getByRole('button', { name: '快捷键', exact: true }).click()
  await expect(dialog.getByRole('textbox', { name: '创建新会话' })).toHaveValue('F8')
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(dialog).toBeHidden()
  await page.getByRole('button', { name: '设置', exact: true }).click()
  dialog = page.getByRole('dialog', { name: '设置' })
  await expect(dialog.locator('nav').getByRole('button', { name: '快捷键', exact: true })).toHaveCount(0)
  await expect(dialog.locator('[data-shortcut-action]')).toHaveCount(0)
  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
  await page.setViewportSize({ width: 1440, height: 1000 })
  await expect(dialog).toBeVisible()
  await dialog.locator('nav').getByRole('button', { name: '快捷键', exact: true }).click()
  await expect(dialog.getByRole('textbox', { name: '创建新会话' })).toHaveValue('F8')
  expect(await page.evaluate(() => JSON.parse(localStorage.getItem('xbot-session-shortcuts')!))).toEqual({ newSession: 'f8' })
})

test('browser-tab shortcut opens the row and menu target without creating a session', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.addInitScript(() => {
    localStorage.setItem('xbot-locale', 'zh-CN')
    if (!localStorage.getItem('xbot-session-shortcuts')) {
      localStorage.setItem('xbot-session-shortcuts', JSON.stringify({ newSession: 'f8' }))
    }
  })
  await setupMock(page)
  let creations = 0
  await page.route('**/api/chats/create', route => {
    creations++
    return route.fulfill({ json: { ok: true, data: {} } })
  })
  await loginAndOpenSettings(page)
  const dialog = page.getByRole('dialog', { name: '设置' })
  await dialog.locator('nav').getByRole('button', { name: '快捷键', exact: true }).click()
  const field = dialog.getByRole('textbox', { name: '在新标签页中打开' })
  await field.fill('F9')
  await field.press('Enter')
  await expect(field).toHaveValue('F9')
  await expect(dialog.getByRole('textbox', { name: '创建新会话' })).toHaveValue('F8')
  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
  const row = page.locator('[data-session-row="web:chat-1"]')
  for (const scope of ['row', 'menu']) {
    if (scope === 'row') await row.focus()
    else {
      await row.click({ button: 'right' })
      await expect(page.getByRole('menuitem', { name: '在新标签页中打开' }).locator('[data-slot="context-menu-shortcut"]')).toHaveText('F9')
    }
    const popupPromise = page.waitForEvent('popup')
    await page.keyboard.press('F9')
    const popup = await popupPromise
    await expect(popup).toHaveURL(/\?session=web%3Achat-1$/)
    expect(await popup.evaluate(() => window.opener)).toBeNull()
    await popup.close()
    await expect(page.getByRole('menu')).toBeHidden()
    expect(creations).toBe(0)
  }
  await page.reload()
  await row.waitFor({ state: 'visible' })
  await row.focus()
  const popupPromise = page.waitForEvent('popup')
  await page.keyboard.press('F9')
  const popup = await popupPromise
  await expect(popup).toHaveURL(/\?session=web%3Achat-1$/)
  await popup.close()
  expect(await page.evaluate(() => JSON.parse(localStorage.getItem('xbot-session-shortcuts')!))).toEqual({ newSession: 'f8', openInBrowserTab: 'f9' })
  expect(creations).toBe(0)
})

test('mobile: every settings tab fits the viewport (no truncation)', async ({ browser }) => {
  for (const viewport of [{ width: 375, height: 812 }, { width: 320, height: 700 }]) {
  const page = await browser.newPage({ viewport })
  await page.addInitScript(() => {
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
    class MockEventSource {
      readyState = 1
      onopen: ((ev: Event) => void) | null = null
      onerror: ((ev: Event) => void) | null = null
      constructor(_url: string) {
        setTimeout(() => this.onopen?.(new Event('open')), 0)
      }
      addEventListener(type: string, handler: (ev: MessageEvent) => void) {
        if (!listeners[type]) listeners[type] = new Set()
        listeners[type].add(handler)
      }
      removeEventListener(type: string, handler: (ev: MessageEvent) => void) {
        listeners[type]?.delete(handler)
      }
      close() {
        for (const key of Object.keys(listeners)) listeners[key].clear()
      }
    }
    ;(window as unknown as { EventSource: typeof MockEventSource }).EventSource = MockEventSource
  })
  await setupMock(page)
  await loginAndOpenSettings(page)

  const navButtons = page.locator('[data-slot="dialog-content"] nav button')
  const count = await navButtons.count()
  expect(count).toBeGreaterThan(5)

  const problems: string[] = []
  for (let i = 0; i < count; i++) {
    const label = ((await navButtons.nth(i).textContent()) ?? `tab-${i}`).trim()
    await navButtons.nth(i).click()
    await page.waitForTimeout(450)
    const overflow = await page.evaluate(collectSettingsOverflow)
    if (overflow.length) problems.push(`[${viewport.width}px|${label}] ${overflow.join(' || ')}`)
  }

  expect(problems, `Settings overflow at ${viewport.width}px`).toEqual([])
  await page.close()
  }
})

test('settings overflow guard detects a visible regression while permitting intentional clipping', async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 700 })
  await page.setContent(`<div data-slot="dialog-content" style="position:relative;width:280px;overflow:hidden">
    <p class="sr-only" style="position:absolute;width:1px;height:1px;overflow:hidden">Accessible description</p>
    <p style="width:80px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis">An intentionally truncated label</p>
    <nav style="width:280px;overflow-x:auto"><button style="width:500px">Reachable by scrolling</button></nav>
    <aside style="position:absolute;width:280px;transform:translateX(100%)"><button>Closed drawer</button></aside>
  </div>`)
  expect(await page.evaluate(collectSettingsOverflow)).toEqual([])
  await page.evaluate(() => {
    const button = document.createElement('button'); button.textContent = 'Oversized visible control'; button.style.width = '600px'
    document.querySelector('[data-slot="dialog-content"]')!.appendChild(button)
  })
  expect(await page.evaluate(collectSettingsOverflow)).toEqual(expect.arrayContaining([expect.stringContaining('Oversized visible control')]))
  await page.evaluate(() => {
    const root = document.querySelector('[data-slot="dialog-content"]')!
    root.lastElementChild!.remove()
    root.insertAdjacentHTML('beforeend', '<div style="width:250px;overflow:auto"><div style="width:80px;height:10px;overflow:hidden"><span>Clipped inner text</span></div></div>')
  })
  expect(await page.evaluate(collectSettingsOverflow)).toEqual(expect.arrayContaining([expect.stringContaining('Clipped inner text')]))
})
