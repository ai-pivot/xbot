import { test, expect, type Page } from '@playwright/test'

/**
 * 验收（2026-09-18 用户报告 + 用户提供的真实 DOM 证据）：
 *
 * ① `<li>` 松散列表渲染错：源 markdown 列表项带嵌套内容（项与子列表之间有空行）
 *    ⇒ `<li><p>…</p><ul>…</ul></li>`：`li { list-style-position: inside }` 下
 *    marker 是行内盒、首个子元素是**块级** `<p>` ⇒ 编号独占一行、正文掉到下一行。
 *    修：`.markdown-body li > p { display: inline }`（首段 inline 化 ⇒ 同行；
 *    嵌套列表仍块级 ⇒ 正常另起一行缩进）。
 *
 * ② `node="[object Object]"` 泄漏：`CodeBlock` 曾把 react-markdown 的 `node` prop
 *    spread 到 DOM `<code>` 上 ⇒ 用户复制/导出消息会带出这种垃圾属性。
 *    修：`CodeBlock` 解构时剥掉 `node`。
 *
 * 判据是**几何的**（真实浏览器）：对每个 `<li>`，其首段 `<p>` 的 top 必须与 `<li>`
 * 的 top 基本重合（差 < 8px）；marker 独占一行时该差 ≈ 一个行高（> 15px）。
 * 同时截图存档作为验收产物。
 */

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/** 复刻用户消息里的形态：松散有序列表 + 项内嵌套列表 + 行内 code。 */
const LOOSE_LIST = [
  '1. **MoE/FFN 本来就是 mma** ✓ —— 它是 `mma.sync` 的 **fp8 m16n8k32**',
  '',
  '   - up/down：`moe_experts_split.cu:190/215/419/477`',
  '   - gate：`moe_segment_fused.cu:261/402/416`',
  '',
  '2. **真正的空白是"代次"** ✗ —— 全仓没有一处用 B300 原生的 `tcgen05`',
  '',
  '3. **当年停在老代次可辩护** ✓（文件头自证）',
].join('\n')

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
          messages: [{ id: 1, role: 'assistant', turn_id: 1, content: LOOSE_LIST, iterations: [] }],
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

test('松散有序列表：编号与正文同行 + DOM 无 node 泄漏属性（截图验收）', async ({ page }) => {
  await page.setViewportSize({ width: 900, height: 900 })
  await setupMock(page)
  await page.goto(BASE)

  const body = page.locator('.markdown-body').first()
  await expect(body).toBeVisible({ timeout: 20_000 })
  await expect(body.locator('ol > li').first()).toBeVisible()

  // ① 无 react-markdown 的 node prop 泄漏（用户证据里的 node="[object Object]"）。
  const leakedNodes = await body.evaluate((el) => el.querySelectorAll('[node]').length)
  expect(leakedNodes, 'markdown DOM 不得出现 node 属性泄漏').toBe(0)

  // ② 几何：每个 li 的首段与 li 顶部重合（marker 独占一行时会差一个行高）。
  const deltas = await body.evaluate((el) =>
    Array.from(el.querySelectorAll('ol > li')).map((li) => {
      const p = li.querySelector(':scope > p')
      if (!p) return null
      return Math.round(p.getBoundingClientRect().top - li.getBoundingClientRect().top)
    }),
  )
  for (const d of deltas) {
    if (d === null) continue
    expect(d, 'li 首段必须与编号同行（top 差 < 8px）').toBeLessThan(8)
  }

  // ③ 嵌套列表仍应存在（内容一个不少）。
  await expect(body.locator('ol > li').first().locator('ul li')).toHaveCount(2)

  // 验收产物：截图存档。
  await body.screenshot({ path: 'test-results/markdown-loose-list.png' })
})
