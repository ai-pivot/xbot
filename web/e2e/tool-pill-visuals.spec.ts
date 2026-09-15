import { test, expect, type Page } from '@playwright/test'

/** 复制自 tool-pill-width.spec.ts 的 mock（同一套 /api/*）。 */
async function setupMock(page: Page, historyMessages: unknown[] = []) {
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) => r.fulfill({ json: { ok: true, data: {
    sessions: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() }],
    chats: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString(), isCurrent: true }],
    orphan_subagents: [], } } }))
  await page.route('**/api/history', (r) => r.fulfill({ json: { ok: true, data: { messages: historyMessages, chat_id: 'chat-1', last_seq: 0, active_progress: null } } }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

/**
 * 视觉语言断言（用户 2026-09-15 定稿）：
 *   ① 失败 pill = 红系 + 「失败」文字标签（成功安静、失败吵闹）；
 *   ② 假工具 pill = 虚线边框 + kind 头像 + 「系统」角标；
 *   ③ 行级失败告警 = `N 失败` chip。
 */
const ASSISTANT = {
  id: 2, role: 'assistant', content: 'done', timestamp: new Date().toISOString(), turn_id: 1,
  iterations: [
    { iteration: 1, content: 'ok', reasoning: '', tools: [
      { name: 'Shell', label: 'Shell: cargo check', status: 'done', summary: 'ok', detail: 'ok' },
      { name: 'Grep', label: 'Grep: shared_experts', status: 'error', summary: 'Error: exit 1', detail: 'Error: exit 1', exitCode: 1 },
      { name: 'background_task_result', label: '后台任务已完成', status: 'done', detail: 'build ok',
        toolHints: JSON.stringify({ kind: 'bg_task', task_id: '3f8f492a', status: 'done', elapsed_ms: 12300 }) },
    ] },
  ],
}

test('pill 视觉语言：失败吵闹 / 假工具可辨 / 行级告警', async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } })
  const page = await ctx.newPage()
  await setupMock(page, [{ id: 1, role: 'user', content: 'run check', timestamp: new Date().toISOString(), turn_id: 1 }, ASSISTANT])
  await page.goto('/')

  // ① 失败 pill：data-tool-status=error 且带「失败」标签
  const errPill = page.locator('[data-tool-status="error"]').first()
  await expect(errPill).toBeAttached()
  await expect(errPill).toContainText('失败')

  // ② 假工具 pill：虚线边框 + 「系统」角标
  // `data-tool-name` 同时在 pill 与其外层 popover 包装上 ⇒ 取**最内层**（含「系统」角标的那个）
  const synPill = page.locator('[data-tool-name="background_task_result"]', { hasText: '系统' }).last()
  await expect(synPill).toBeAttached()
  await expect(synPill).toContainText('系统')
  const borderStyle = await synPill.evaluate((el) => getComputedStyle(el).borderStyle)
  expect(borderStyle, '假工具 pill 必须是虚线边框').toBe('dashed')

  // ③ 行级失败告警
  await expect(page.locator('[data-testid="tool-group-failed"]').first()).toContainText('失败')

  await page.screenshot({ path: '/tmp/pillvis/desktop.png', fullPage: true })
  await ctx.close()
})
