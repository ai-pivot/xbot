import { test, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://127.0.0.1:18599'

/** 临时排查：中文 locale 下，思考块 label 到底渲染成什么。 */
async function setupMock(page: Page) {
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) => r.fulfill({
    json: { ok: true, data: {
      sessions: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() }],
      chats: [], orphan_subagents: [],
    } },
  }))
  await page.route('**/api/history', (r) => r.fulfill({
    json: { ok: true, data: {
      messages: [
        { role: 'user', content: '看一下这个错误', seq: 1, turn_id: 1 },
        { role: 'assistant', content: '', seq: 2, turn_id: 1, iterations: [
          { iteration: 1, reasoning: '先确认 gateway 的报错，再检查请求体里是否带 reasoning_text。', content: '',
            tools: [{ name: 'Shell', label: 'Shell: cargo check', status: 'done', iteration: 1, detail: 'ok' }] },
        ] },
      ],
      chat_id: 'chat-1', last_seq: 0, active_progress: null,
    } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

for (const locale of ['zh-CN', 'en', 'ja']) {
  test(`思考块 label @ ${locale}`, async ({ page }) => {
    await page.addInitScript((loc) => {
      localStorage.setItem('xbot-locale', loc)
    }, locale)
    await setupMock(page)
    await page.goto(BASE)
    await page.waitForTimeout(2500)
    const labels = await page.locator('[data-testid="thinking-line"]').allInnerTexts()
    const html = await page.locator('[data-testid="thinking-line"] span').first().innerHTML()
    console.log(`[${locale}] LABELS=${JSON.stringify(labels)} HTML=${html}`)
  })
}
