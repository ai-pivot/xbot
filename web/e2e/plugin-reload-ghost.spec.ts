import { test, expect, type Browser, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E（手机 390×844）：插件面板 Reload 按钮的「残影」——用户 live bug（2026-09-19 截图）：
 * 三个带 web 模块的插件（GenUI / Git Fancy / Iteration Stats）的 Reload 按钮上，
 * `Reload` 与 `Reloading…` **两段文字叠在一起**（横向错开几像素），按钮都是 disabled。
 *
 * 本 spec 的职责 = **机器可判定的判据**（不靠截图肉眼）：
 *   ① 同一插件 id 只能有**一张**卡片（双挂载/双列表 ⇒ 残影的结构根因）；
 *   ② 点击 Reload（后端 `plugin_reload` 永不返回 ⇒ 一直处于 reloading）后，
 *      该卡片的动作行里**恰好一个**按钮标签 —— 不得同时存在 `Reload` 与 `Reloading…`；
 *   ③ 通用重叠守卫：面板内任意两个**文案不同**的可见文本元素不得大范围重叠
 *      （残影的几何签名：两个文本节点压在同一位置）。
 *
 * ⚠️ 前置断言（防假绿）：必须先断言插件面板**真的渲染出来了**（tab + 卡片可见），
 *    否则"没打开面板 ⇒ 断言真空过"会让用例撒谎（上一版 harness 的教训）。
 */

const CHAT = 'chat-ghost-1'

const PLUGINS = [
  { id: 'xbot.genui', name: 'GenUI (display_html)', version: '1.0.0', state: 'active', runtime: 'grpc' },
  { id: 'xbot.git-fancy', name: 'Git Fancy', version: '0.3.5', state: 'active', runtime: 'stdio' },
  { id: 'xbot.iteration-stats', name: 'Iteration Stats', version: '1.0.0', state: 'active', runtime: 'script' },
]

async function newContext(browser: Browser) {
  const ctx = await browser.newContext({
    viewport: { width: 390, height: 844 },
    hasTouch: true,
    isMobile: true,
  })
  await ctx.addInitScript(() => {
    try {
      localStorage.setItem('xbot-locale', 'zh-CN')
    } catch {
      /* ignore */
    }
  })
  return ctx
}

async function setupMock(page: Page, opts: { reloadResolves?: boolean } = {}): Promise<void> {
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) => r.fulfill({
    json: { ok: true, data: {
      sessions: [{ chat_id: CHAT, channel: 'web', label: 'ghost', last_active: new Date().toISOString() }],
      chats: [{ chat_id: CHAT, channel: 'web', label: 'ghost', last_active: new Date().toISOString() }],
      orphan_subagents: [],
    } },
  }))
  await page.route('**/api/history', (r) => r.fulfill({
    json: { ok: true, data: { messages: [], chat_id: CHAT, channel: 'web', last_seq: 0, active_progress: null, has_more: false } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/plugin-files/**', (r) => r.fulfill({ json: { ok: true, data: [] } }))

  let reloadCalls = 0
  await page.route('**/api/rpc', async (r) => {
    const body = r.request().postDataJSON() as { method?: string } | null
    const method = body?.method
    if (method === 'plugin_status') {
      return r.fulfill({ json: { ok: true, data: { plugins: PLUGINS, active: PLUGINS.length, total: PLUGINS.length } } })
    }
    if (method === 'web_plugin_list') {
      // 无 web.i18n 表 ⇒ 卡片名走字面量（与会话现场一致）。
      return r.fulfill({ json: { ok: true, data: { plugins: [] } } })
    }
    if (method === 'plugin_reload') {
      reloadCalls += 1
      if (opts.reloadResolves) return r.fulfill({ json: { ok: true, data: null } })
      // 永不返回：按钮一直停在 reloading 态 —— 残影就在这个窗口里出现。
      return new Promise(() => {})
    }
    return r.fulfill({ json: { ok: true, data: null } })
  })
  ;(page as unknown as { __reloadCalls?: () => number }).__reloadCalls = () => reloadCalls
}

async function login(page: Page): Promise<void> {
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  // 手机壳：顶栏「工具」（mobileTools 布局项的 aria-label）
  await expect(page.getByRole('button', { name: /工具|Tools/i }).first()).toBeVisible({ timeout: 30_000 })
}

/** 打开手机「工具」页 → 插件面板 tab。返回插件面板根（tabpanel 容器）。 */
async function openPluginPanel(page: Page) {
  await page.getByRole('button', { name: /工具|Tools/i }).first().click()
  const tab = page.getByRole('tab', { name: /插件|Plugins/i }).first()
  await expect(tab, '工具页的「插件」tab 必须存在').toBeVisible({ timeout: 15_000 })
  await tab.click()
  // 前置：面板真的渲染出卡片（防"没打开 ⇒ 真空过"）
  await expect(page.getByText('Git Fancy', { exact: true }), '插件卡片必须渲染').toBeVisible({ timeout: 15_000 })
}

test('手机：插件面板 Reload 不得出现「重载 / 重载中…」重叠残影', async ({ browser }) => {
  const ctx = await newContext(browser)
  const page = await ctx.newPage()
  await setupMock(page)
  await login(page)
  await openPluginPanel(page)

  // ① 同一插件 id 只能有一张卡片（双挂载/双列表 = 残影的结构根因）。
  for (const p of PLUGINS) {
    await expect(
      page.getByText(p.name, { exact: true }),
      `插件 ${p.id} 必须恰好一张卡片`,
    ).toHaveCount(1)
  }

  // ② 点第一张卡片的「重载」（plugin_reload 挂着不返回 ⇒ 保持重载中）。
  const geniusCard = page
    .getByText('GenUI (display_html)', { exact: true })
    .locator('xpath=ancestor::div[contains(@class,"group/card")]')
  await expect(geniusCard).toHaveCount(1)
  await geniusCard.getByRole('button', { name: /重载|Reload/ }).first().click()

  // 重载中：恰好一个标签 —— 不得同时存在「重载」与「重载中…」
  await expect(
    geniusCard.getByRole('button', { name: /重载中|Reloading/ }),
    '卡片里必须出现重载中标签',
  ).toHaveCount(1)
  await expect(
    geniusCard.getByRole('button', { name: /^重载$|^Reload$/ }),
    '重载中时不得同时残留「重载」标签（重叠残影）',
  ).toHaveCount(0)

  // ③ 通用重叠守卫：面板内任意两个「文案不同」的可见文本元素不得大范围重叠。
  const overlaps = await page.evaluate(() => {
    const root = document.querySelector('[role="tabpanel"], [data-plugin-config-key], body') as HTMLElement
    const nodes = Array.from(root.querySelectorAll<HTMLElement>('button, span, div'))
      .filter((el) => {
        const txt = (el.textContent ?? '').trim()
        if (!txt) return false
        // 只保留"叶子文本"元素（自身无元素子节点）——避免容器互相包含造成假阳性
        if (el.querySelector('button, span, div')) return false
        const r = el.getBoundingClientRect()
        return r.width > 0 && r.height > 0
      })
    const out: Array<{ a: string; b: string; ratio: number }> = []
    for (let i = 0; i < nodes.length; i++) {
      for (let j = i + 1; j < nodes.length; j++) {
        const a = nodes[i]
        const b = nodes[j]
        const ta = (a.textContent ?? '').trim()
        const tb = (b.textContent ?? '').trim()
        if (ta === tb) continue // 文案相同（同一标签的分片）不算残影
        const ra = a.getBoundingClientRect()
        const rb = b.getBoundingClientRect()
        const ix = Math.max(0, Math.min(ra.right, rb.right) - Math.max(ra.left, rb.left))
        const iy = Math.max(0, Math.min(ra.bottom, rb.bottom) - Math.max(ra.top, rb.top))
        const inter = ix * iy
        if (inter <= 0) continue
        const ratio = inter / Math.min(ra.width * ra.height, rb.width * rb.height)
        if (ratio > 0.6) out.push({ a: ta.slice(0, 40), b: tb.slice(0, 40), ratio: Math.round(ratio * 100) / 100 })
      }
    }
    return out
  })
  expect(overlaps, `文案不同的文本元素不得大范围重叠（残影签名）：${JSON.stringify(overlaps)}`).toEqual([])

  await ctx.close()
})

/**
 * 变体 C：重载**成功返回**（真实路径 —— 服务端 reload 后前端 `refresh()` 换新列表，
 * 卡片会重放 `motion.div layout` 动画）。残影若来自"列表刷新 + 布局动画期间的文本切换"，
 * 这里可能红（断言仍是结构判据：单一标签 / 单一卡片 / 无重叠）。
 */
test('手机：重载成功返回后 —— 卡片回到「重载」且无重叠残影', async ({ browser }) => {
  const ctx = await newContext(browser)
  const page = await ctx.newPage()
  await setupMock(page, { reloadResolves: true })
  await login(page)
  await openPluginPanel(page)

  const card = page
    .getByText('Git Fancy', { exact: true })
    .locator('xpath=ancestor::div[contains(@class,"group/card")]')
  await card.getByRole('button', { name: /重载|Reload/ }).first().click()

  // 重载完成后按钮回到「重载」，且仍只有一张卡片 / 一个标签。
  await expect(card.getByRole('button', { name: /^重载$|^Reload$/ })).toHaveCount(1, { timeout: 15_000 })
  await expect(card.getByRole('button', { name: /重载中|Reloading/ })).toHaveCount(0)
  await expect(page.getByText('Git Fancy', { exact: true })).toHaveCount(1)

  const labels = await page.evaluate(() => {
    const out: string[] = []
    document.querySelectorAll('button').forEach((b) => {
      const t = (b.textContent ?? '').trim()
      if (/重载|Reload/.test(t)) out.push(t)
    })
    return out
  })
  expect(labels, `刷新后每个重载按钮只应有一个标签，实测 ${JSON.stringify(labels)}`).toEqual(
    PLUGINS.map(() => '重载'),
  )

  await ctx.close()
})

/**
 * 变体：三张卡片**同时**挂起重载（用户截图里三个 Reload 按钮都是 disabled/opacity-50）。
 * 若残影是"多张卡片同时处于 pending 时的重排动画/状态串扰"，这里会红。
 */
test('手机：三张卡片同时重载 —— 每张卡片仍只有一个标签、无重叠', async ({ browser }) => {
  const ctx = await newContext(browser)
  const page = await ctx.newPage()
  await setupMock(page)
  await login(page)
  await openPluginPanel(page)

  for (const p of PLUGINS) {
    const card = page
      .getByText(p.name, { exact: true })
      .locator('xpath=ancestor::div[contains(@class,"group/card")]')
    await expect(card).toHaveCount(1)
    await card.getByRole('button', { name: /重载|Reload/ }).first().click()
  }

  // 三张卡片都进入重载中；每张卡片里"重载"与"重载中"不得共存。
  await expect(page.getByRole('button', { name: /重载中|Reloading/ })).toHaveCount(PLUGINS.length)
  await expect(page.getByRole('button', { name: /^重载$|^Reload$/ })).toHaveCount(0)
  for (const p of PLUGINS) {
    await expect(page.getByText(p.name, { exact: true })).toHaveCount(1)
  }

  const labels = await page.evaluate(() => {
    const out: string[] = []
    document.querySelectorAll('button').forEach((b) => {
      const t = (b.textContent ?? '').trim()
      if (/重载|Reload/.test(t)) out.push(t)
    })
    return out
  })
  expect(labels, `每个重载按钮只应有一个标签，实测 ${JSON.stringify(labels)}`).toEqual(
    PLUGINS.map(() => '重载中…'),
  )

  await ctx.close()
})
