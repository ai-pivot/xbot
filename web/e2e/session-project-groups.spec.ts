/**
 * 需求（用户）：「会话列表按项目组织。项目会话列表可以折叠/展开」。
 *
 * 契约（真实浏览器里可验）：
 *   1. 默认按项目（= 工作目录）组织 —— 分类切换器高亮「项目」；
 *   2. 组头 = 项目名 + 会话数（完整路径在 tooltip），点组头折叠该项目的会话行；
 *   3. 折叠是「按项目记住」的：刷新后仍然折叠（localStorage，纯前端）；
 *   4. 「全部折叠 / 全部展开」一次改所有组。
 */
import { test, expect, type Browser, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

const NOW = new Date().toISOString()

/** 三个项目：proj-one（2 个会话）、proj-two（1 个）、无工作路径（1 个）。 */
const SESSIONS = [
  { chat_id: 'chat-a1', channel: 'web', label: 'A1', work_dir: '/home/user/proj-one', last_active: NOW },
  { chat_id: 'chat-a2', channel: 'web', label: 'A2', work_dir: '/home/user/proj-one', last_active: NOW },
  { chat_id: 'chat-b1', channel: 'web', label: 'B1', work_dir: '/home/user/proj-two', last_active: NOW },
  { chat_id: 'chat-c1', channel: 'web', label: 'C1', last_active: NOW },
]

async function newClient(browser: Browser): Promise<Page> {
  const page = await browser.newPage()

  // EventSource stub（与其它 spec 同款）：只保证「已连接」，不推事件。
  await page.addInitScript(() => {
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
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

  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) =>
    r.fulfill({ json: { ok: true, data: { sessions: SESSIONS, chats: SESSIONS, orphan_subagents: [] } } }),
  )
  await page.route('**/api/history**', (r) =>
    r.fulfill({ json: { ok: true, data: { messages: [], chat_id: 'chat-a1', last_seq: 0, active_progress: null } } }),
  )
  await page.route('**/api/chats/*/switch**', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
  await page.route('**/api/queue/list', (r) => r.fulfill({ json: { ok: true, data: { items: [] } } }))

  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await expect(page.getByTestId('session-view-bar')).toBeVisible({ timeout: 20_000 })
  return page
}

/** 项目组头（button 内含 title=完整路径 的标题 span）。 */
function projectHeader(page: Page, workDir: string) {
  return page.locator('button').filter({ has: page.locator(`[title="${workDir}"]`) })
}

test('默认按项目分组：组头 = 项目名 + 会话数，可折叠且刷新后保持', async ({ browser }) => {
  const page = await newClient(browser)

  // 默认分类 = 项目（工作目录）。
  await expect(page.getByTestId('session-category-path')).toHaveAttribute('aria-pressed', 'true')

  const projOne = projectHeader(page, '/home/user/proj-one')
  await expect(projOne).toContainText('proj-one')
  await expect(projOne).toContainText('2')
  await expect(projectHeader(page, '/home/user/proj-two')).toContainText('proj-two')

  // 全部展开时每个会话行都可见。
  await expect(page.getByText('A1', { exact: true })).toBeVisible()
  await expect(page.getByText('A2', { exact: true })).toBeVisible()

  // 折叠 proj-one → 该项目的会话行消失，别的项目不受影响。
  await projOne.click()
  await expect(projOne).toHaveAttribute('aria-expanded', 'false')
  // 折叠形态是「内容仍在 DOM 里、被裁到 0 高」（AnimatedCollapse 的既定行为），
  // 所以判据必须是**几何**的 —— toHaveCount(0) 会永远失败（行还在 DOM 里）。
  await expect(page.getByText('A1', { exact: true }).first()).not.toBeInViewport()
  await expect(page.getByText('A2', { exact: true }).first()).not.toBeInViewport()
  await expect(page.getByText('B1', { exact: true })).toBeVisible()
  await expect(page.getByText('C1', { exact: true })).toBeVisible()

  // 刷新 → 折叠状态按项目记住（localStorage，提示：状态必须在 store 里，
  // 组件内 useState 会在重挂载时复位）。
  await page.reload()
  await expect(page.getByTestId('session-view-bar')).toBeVisible({ timeout: 20_000 })
  await expect(projectHeader(page, '/home/user/proj-one')).toHaveAttribute('aria-expanded', 'false')
  await expect(page.getByText('A1', { exact: true }).first()).not.toBeInViewport()

  // 再点一次 → 展开。
  await projectHeader(page, '/home/user/proj-one').click()
  await expect(page.getByText('A1', { exact: true })).toBeVisible()
})

test('「全部折叠 / 全部展开」一次改所有组', async ({ browser }) => {
  const page = await newClient(browser)

  const collapseAll = page.getByTestId('session-collapse-all')
  await expect(collapseAll).toBeVisible()
  await collapseAll.click()

  // 所有组都折叠（含「未设置工作路径」那组：它的组头没有 title，用行的几何判据断言）。
  await expect(page.getByText('A1', { exact: true }).first()).not.toBeInViewport()
  await expect(page.getByText('B1', { exact: true }).first()).not.toBeInViewport()
  await expect(page.getByText('C1', { exact: true }).first()).not.toBeInViewport()

  // 按钮此时是「全部展开」→ 再点一次恢复。
  await collapseAll.click()
  await expect(page.getByText('A1', { exact: true })).toBeVisible()
  await expect(page.getByText('C1', { exact: true })).toBeVisible()
})
