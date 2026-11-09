import { test, expect, type Page } from '@playwright/test'

/**
 * 验收（2026-11-09 用户报告，真实截图）：
 *
 * 模型回复里裸 URL 紧贴 Markdown 语法时，GFM 的字面 autolink 会一直吞到下一个
 * 空白，于是
 *
 *   PR 已提: **https://github.com/MindLab-Research/mint-ops/pull/92**（`ops/prd-console-20261107` → `main`）
 *
 * 被渲染成字面 `**` + 一个 href/label 都是
 * `https://…/92**（`ops/prd-console-20261107`` 的链接（点开 404），后面的行内
 * 代码也失去样式。修：源级把它变成硬 autolink（`<https://…>`），URL 末尾由 `>`
 * 显式结束，外围语法归内联解析器处理。
 *
 * 判据（真实浏览器）：`<strong><a>` 存在且 href 恰好是 URL、页面文本无 `**`、
 * 两个行内 code 都在。
 */

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

const REPORTED =
  'PR 已提: **https://github.com/MindLab-Research/mint-ops/pull/92**（`ops/prd-console-20261107` → `main`, 1 文件 +9/-5, mergeable, 工作树干净）。'

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

test('裸 URL 紧贴 ** 与行内代码时，链接只包住 URL', async ({ page }) => {
  await page.setViewportSize({ width: 1000, height: 700 })
  await setupMock(page)
  await page.goto(BASE)

  const body = page.locator('.markdown-body').first()
  await expect(body).toBeVisible({ timeout: 20_000 })

  // ① 加粗链接存在，且 href / 文本都恰好是 URL。
  const link = body.locator('strong a').first()
  await expect(link).toBeVisible()
  await expect(link).toHaveAttribute('href', 'https://github.com/MindLab-Research/mint-ops/pull/92')
  await expect(link).toHaveText('https://github.com/MindLab-Research/mint-ops/pull/92')

  // ② 不再有字面 `**`（修复前整行会带四处）。
  await expect(body).not.toContainText('**')

  // ③ URL 后面的行内代码仍然是代码（修复前被吞进链接）。
  const code = body.locator('p code')
  await expect(code).toHaveCount(2)
  await expect(code.nth(0)).toHaveText('ops/prd-console-20261107')
  await expect(code.nth(1)).toHaveText('main')

  // ④ 链接宽度不得覆盖到后面的正文（修复前 underline/label 一直拖到 `main`）。
  const linkBox = (await link.boundingBox())!
  const paragraphBox = (await body.locator('p').first().boundingBox())!
  expect(linkBox.width).toBeLessThan(paragraphBox.width * 0.8)
})
