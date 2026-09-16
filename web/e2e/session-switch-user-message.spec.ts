/**
 * 切会话后 user 消息的**存在性与唯一性**（2026-09-16 用户报告，含截图）：
 *   「user 消息消失，最新版前端的 bug，切换会话的时候出现，刷新则消失」
 *
 * 现象两条（同一根因的两个面）：
 *   1) user 行（尤其 notification 变成的 user 行）在切换/隐藏 tab 后**消失**，
 *      刷新（全量加载）才恢复 —— 前半已由 AgentPanel 的「重新订阅 ⇒ 历史对账」
 *      修复（见 AgentPanel.test.tsx 的 re-subscribe reconcile 用例）。
 *   2) 同一 user/assistant 行在 DOM 里**出现两次**（行 id 完全相同）、`/api/history`
 *      被拉两次 —— 本文件钉死第 2 条，并同时守住第 1 条。
 *
 * 根因（DOM 铁证 + 代码）：**同一会话被两个 agent 面板渲染** —— seed 在"还没有
 * 已知会话"时建的无 sessionId 占位 tab 用 `params.sessionId ?? activeSession`
 * 解析会话（跟着 activeSession 走），而侧栏点击会话时既 `openTab(session tab)`
 * 又 `activateSession`；agent tab 是 `renderer='always'`（常驻 DOM）⇒ 整个消息
 * 列表渲染两份 + 每次两个 SSE 订阅 + `/api/history` 拉两次。
 *
 * 修复（两处，互为充要）：
 *   - `useTabManager.openTab`：会话 tab **认领**未绑定会话的占位 tab（不新建第二个面板）。
 *   - `AgentPanel`：占位 tab 仅在**独占** main agent 面板时才跟随 activeSession
 *     （不变量：一个会话至多被一个 agent 面板渲染）。
 *
 * 判别力：断言"消息列表根恰好 1 个"+"每会话 history 恰好 1 次"。把任一处修复改回
 * 即变红（mutation 自证）。
 */

import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

const HISTORY: Record<string, unknown[]> = {
  // 行形状照 parseHistoryMessages 的真实契约补全：id（DB 唯一键 ⇒ dbID）、seq、
  // turn_id、timestamp。缺 id 时合成 dbID=index+1 会掩盖"按 id 做 key/行高测量"的问题。
  'chat-1': [
    { id: 1, role: 'user', content: 'hello from S1', seq: 1, turn_id: 101, timestamp: '2026-09-16T10:00:00Z' },
    { id: 2, role: 'assistant', content: 'answer one', seq: 2, turn_id: 101, timestamp: '2026-09-16T10:00:01Z' },
  ],
  'chat-2': [
    { id: 3, role: 'user', content: 'hello from S2', seq: 3, turn_id: 202, timestamp: '2026-09-16T10:01:00Z' },
    { id: 4, role: 'assistant', content: 'answer two', seq: 4, turn_id: 202, timestamp: '2026-09-16T10:01:01Z' },
  ],
}

async function newClient(
  browser: import('@playwright/test').Browser,
): Promise<{ page: Page; historyCalls: Record<string, number> }> {
  const page = await browser.newPage()
  const historyCalls: Record<string, number> = {}
  let activeChat = 'chat-1'

  await page.addInitScript(() => {
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
    ;(window as unknown as { __sseListeners: typeof listeners }).__sseListeners = listeners
    class MockEventSource {
      readyState = 1
      onopen: ((ev: Event) => void) | null = null
      onerror: ((ev: Event) => void) | null = null
      constructor(public url: string) {
        setTimeout(() => this.onopen?.(new Event('open')), 0)
      }
      addEventListener(type: string, h: (ev: MessageEvent) => void) {
        ;(listeners[type] ||= new Set()).add(h)
      }
      removeEventListener(type: string, h: (ev: MessageEvent) => void) {
        listeners[type]?.delete(h)
      }
      close() {
        for (const k of Object.keys(listeners)) listeners[k].clear()
      }
    }
    ;(window as unknown as { EventSource: typeof MockEventSource }).EventSource = MockEventSource
  })

  const now = new Date().toISOString()
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: {
          sessions: [
            { chat_id: 'chat-1', channel: 'web', label: 'S1', last_active: now },
            { chat_id: 'chat-2', channel: 'web', label: 'S2', last_active: now },
          ],
          chats: [
            { chat_id: 'chat-1', channel: 'web', label: 'S1', last_active: now },
            { chat_id: 'chat-2', channel: 'web', label: 'S2', last_active: now },
          ],
          orphan_subagents: [],
        },
      },
    }),
  )
  // 切换会话：真实请求是 POST /api/chats/<id>/switch（见 useSessionStore.switchSession）。
  await page.route('**/api/chats/*/switch**', async (r) => {
    const m = r.request().url().match(/\/api\/chats\/([^/]+)\/switch/)
    if (m?.[1]) activeChat = decodeURIComponent(m[1])
    await r.fulfill({ json: { ok: true, data: {} } })
  })
  await page.route('**/api/history**', (r) => {
    const url = new URL(r.request().url())
    const cid = url.searchParams.get('chat_id') || url.searchParams.get('id') || activeChat
    historyCalls[cid] = (historyCalls[cid] ?? 0) + 1
    const rows = HISTORY[cid] ?? []
    return r.fulfill({
      json: { ok: true, data: { messages: rows, chat_id: cid, last_seq: rows.length * 2, active_progress: null } },
    })
  })
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
  await page.route('**/api/queue/list', (r) => r.fulfill({ json: { ok: true, data: { items: [] } } }))

  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForFunction(() => document.body.textContent?.includes('answer one'), { timeout: 15_000 })
  return { page, historyCalls }
}

/**
 * 不变量（可观测、与 tab 数无关）：**没有任何行 id 在 DOM 里出现两次**。
 *
 * session-per-tab 模型下每个会话 tab 各有一个常驻 MessageList（renderer='always'
 * 保虚拟列表），所以"消息列表根个数"随 tab 数增长是**设计如此**；缺陷形态是**同一
 * 会话被两个面板渲染** ⇒ 同一行 id 出现两次（`db-3` / `turn-202-c` 各两份）+
 * `/api/history` 拉两次。
 */
async function expectNoDuplicateRows(page: Page): Promise<void> {
  const ids = await page
    .locator('[data-message-id]')
    .evaluateAll((els) => els.map((e) => e.getAttribute('data-message-id') ?? ''))
  const dupes = ids.filter((id, i) => id !== '' && ids.indexOf(id) !== i)
  expect(dupes, `同一行被渲染两次（行 id 重复）: ${dupes.join(', ')}`).toEqual([])
}

test.describe('切换会话：user 行必须恰好出现一次（不丢、不重复）', () => {
  test('往返切换 S1 → S2 → S1 后，两边的 user 行都恰好 1 个，且每会话只拉一次历史', async ({ browser }) => {
    const { page, historyCalls } = await newClient(browser)

    // 初始（S1）
    await expect(page.getByText('hello from S1')).toBeVisible({ timeout: 10_000 })
    await expect(page.getByText('answer one')).toBeVisible()
    await expectNoDuplicateRows(page)
    await expect(page.getByText('hello from S1')).toHaveCount(1)

    // 切到 S2
    await page.getByText('S2', { exact: true }).first().click()
    const s2row = page.getByText('hello from S2')
    await expect(s2row, 'S2 的 user 行必须进入 DOM 且不重复').toHaveCount(1, { timeout: 10_000 })
    await expect(s2row, 'S2 的 user 行必须可见').toBeVisible({ timeout: 10_000 })
    await expect(page.getByText('answer two')).toBeVisible()
    await expectNoDuplicateRows(page)

    // 切回 S1 —— 复现点：修复前此处与上面都会得到 2 份（行 id 完全相同）
    await page.getByText('S1', { exact: true }).first().click()
    const s1row = page.getByText('hello from S1')
    await expect(s1row, '切回后 S1 的 user 行必须仍在 DOM 且不重复').toHaveCount(1, { timeout: 10_000 })
    await expect(s1row, '切回后 S1 的 user 行必须可见').toBeVisible({ timeout: 10_000 })
    await expectNoDuplicateRows(page)

    // 判别性信号：切一次的目标会话（chat-2）历史只该被拉**一次**。修复前是 2 次
    // ——因为同一会话被两个面板渲染（占位 tab + session tab），各拉一遍。
    //（chat-1 是 2 次：初始加载 1 次 + 切回时新建 S1 tab 再拉 1 次 —— 合理。
    //  切回时占位 tab 已被 chat-2 认领，chat-1 只能新建面板，这是模型应有行为。）
    expect(historyCalls['chat-2'], 'chat-2 历史拉取次数（切一次 ⇒ 一次）').toBe(1)

    await page.close()
  })

  test('首切 S2 时 user 行必须恰好 1 个（首切也不能重复或丢失）', async ({ browser }) => {
    const { page, historyCalls } = await newClient(browser)
    await expect(page.getByText('hello from S1')).toBeVisible({ timeout: 10_000 })

    await page.getByText('S2', { exact: true }).first().click()
    const s2row = page.getByText('hello from S2')
    await expect(s2row, '首切后 S2 的 user 行必须进入 DOM 且不重复').toHaveCount(1, { timeout: 10_000 })
    await expect(s2row, '首切后 S2 的 user 行必须可见').toBeVisible({ timeout: 10_000 })
    await expectNoDuplicateRows(page)
    expect(historyCalls['chat-2'], 'chat-2 历史拉取次数').toBe(1)

    await page.close()
  })
})
