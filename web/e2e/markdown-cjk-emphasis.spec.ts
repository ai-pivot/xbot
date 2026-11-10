import { test, expect, type Page } from '@playwright/test'

/**
 * 验收（2026-11-10 用户截图）：
 *
 * `` `reserve_sidecar_trace` 是** “每个 maintenance tick 最多把 N 个 sidecar delete
 * RPC 挂回本 tick 的 trace” 的原子配额领取器**——… `` 渲染成字面 `**`、没有加粗。
 *
 * 原因（不是我们的 parser bug）：CommonMark 的 left-flanking 规则要求 `**` 不是
 * 「紧贴文字 + 后面紧跟标点/空白」；`是**“…”` 前面是汉字、后面是引号 ⇒ 不构成
 * 开标记，收尾 `**“…”**的` 同理。GitHub 自己的 Markdown API 也渲染成字面。
 * 修复：解析后把「CJK 相邻 + 引号/括号/冒号开头」的 `**…**` 文本节点重建成
 * strong（字符一个不改，只是补回强调语义）。
 *
 * 判据（真实浏览器）：`<strong>` 存在且内容为引号短语、页面文本没有 `**`。
 */

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

const REPORTED =
  '`reserve_sidecar_trace` 是** “每个 maintenance tick 最多把 N 个 sidecar delete RPC 挂回本 tick 的 trace” 的原子配额领取器**——它不控制请求是否执行、也不控制并发，只控制是否注入 traceparent。'

async function setupMock(page: Page) {
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: {
          sessions: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() }],
          chats: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString(), isCurrent: true }],
          orphan_subagents: [],
        },
      },
    }),
  )
  await page.route('**/api/history', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: {
          messages: [{ id: 1, role: 'assistant', turn_id: 1, content: REPORTED, iterations: [] }],
          chat_id: 'chat-1',
          last_seq: 0,
          active_progress: null,
        },
      },
    }),
  )
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

test('CJK 相邻的 ** 引号短语会加粗，不再显示字面星号', async ({ page }) => {
  await page.setViewportSize({ width: 1100, height: 700 })
  await setupMock(page)
  await page.goto(BASE)

  const body = page.locator('.markdown-body').first()
  await expect(body).toBeVisible({ timeout: 20_000 })

  const strong = body.locator('strong').first()
  await expect(strong).toBeVisible()
  await expect(strong).toContainText('“每个 maintenance tick')
  await expect(strong).toContainText('的原子配额领取器')
  await expect(body).not.toContainText('**')
  await expect(body).toContainText('——它不控制请求是否执行')
})
