import { test, expect, type Page } from '@playwright/test'

/**
 * 回归守护（用户报告 2026-09-16）：**web 端上传图片后，用户自己的消息被改掉**。
 *
 * 现场 DOM：turn 904 的 user 行内容是「📷 以下图片已通过 view_image 工具加载…」，
 * `<img src>` 也从 `/api/files/download?key=uploads%2F…`（用户上传的原件）变成
 * `/api/files/viewimg/<uuid>`（注入副本）。
 *
 * 根因（DB 实证 tenant=140480 turn=904，id 升序）：
 *   1780461 user turn=904 "![IMG_5001.png](/api/files/download?key=uploads%2F4%2F….png&inline=1)激活你的技能"
 *   1780467 user turn=904 "📷 … ![ref_dog.jpg](/api/files/viewimg/2497df58….j…"        ← view_image 注入
 *   1780477 user turn=904 "📷 … ![dog_chicken.jpg](/api/files/viewimg/9e356d4d….j…"     ← view_image 注入
 *
 * `injectViewImages`（agent/engine_run.go）把图片引用作为**一条新的 user 消息**
 * 持久化（OpenAI tool role 不能带图），且**复用触发它的用户消息的 turn_id**；
 * 渲染层每个 turn 只有一个 user 槽位：`MessageStore.mergeHistory` 旧实现无条件
 * 后写覆盖 ⇒ 最后一条注入顶掉了用户真实消息。
 *
 * 契约：**一个 turn 的 user 行 = 该 turn 最早那条 user 行（dbID 最小 = 用户自己发的）**。
 * 注入行不再渲染（后端 v67 起标记 internal_only，渲染转换直接剔除；对存量数据由
 * 上述"取最早一条"兜住）。
 */
const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

const REAL_MSG =
  '![IMG_5001.png](/api/files/download?key=uploads%2F4%2F68b355c3-ddb3-4354-aebb-e81f58730600.png&inline=1)激活你的技能'
const INJ_1 =
  '📷 以下图片已通过 view_image 工具加载，可直接进行视觉分析：\n\n![ref_dog.jpg](/api/files/viewimg/2497df58-8339-485d-80ee-5ae2340609a7.jpeg)'
const INJ_2 =
  '📷 以下图片已通过 view_image 工具加载，可直接进行视觉分析：\n\n![dog_chicken.jpg](/api/files/viewimg/9e356d4d-71d5-4dbd-8051-b2670e913300.jpeg)'

/** turn 904 的真实 DB 行（题目里的现场数据）。 */
const HISTORY_MESSAGES = [
  { id: 1780461, role: 'user', content: REAL_MSG, timestamp: '2026-09-16T00:00:00Z', turn_id: 904, iterations: [] },
  {
    id: 1780463,
    role: 'assistant',
    content: '技能已激活（image-gen）。先把图里的狗和鸡裁出来当参考图：',
    timestamp: '2026-09-16T00:00:01Z',
    turn_id: 904,
    iterations: [],
  },
  { id: 1780467, role: 'user', content: INJ_1, timestamp: '2026-09-16T00:00:02Z', turn_id: 904, iterations: [] },
  { id: 1780477, role: 'user', content: INJ_2, timestamp: '2026-09-16T00:00:03Z', turn_id: 904, iterations: [] },
  {
    id: 1780478,
    role: 'assistant',
    content: '两张参考图都对。现在生成合照：',
    timestamp: '2026-09-16T00:00:04Z',
    turn_id: 904,
    iterations: [],
  },
]

async function setupMock(page: Page) {
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
      addEventListener(t: string, h: (ev: MessageEvent) => void) {
        if (!listeners[t]) listeners[t] = new Set()
        listeners[t].add(h)
      }
      removeEventListener() {}
      close() {}
    }
    ;(window as unknown as { EventSource: unknown }).EventSource = MockEventSource
  })
  // 图片本体：MarkdownImage 加载失败会降级成紧凑占位（无 <img>）——给一张 1×1 PNG，
  // 才能真正断言 <img src> 是用户上传的原件而不是注入副本。
  const PNG_1X1 = Buffer.from(
    'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg==',
    'base64',
  )
  await page.route('**/api/files/**', (r) => r.fulfill({ status: 200, contentType: 'image/png', body: PNG_1X1 }))
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
    r.fulfill({
      json: {
        ok: true,
        data: {
          messages: HISTORY_MESSAGES,
          chat_id: 'chat-1',
          last_seq: 0,
          has_more: false,
          active_progress: null,
        },
      },
    }),
  )
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

async function login(page: Page) {
  await setupMock(page)
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForTimeout(1500)
}

test('turn 内多条 user 行：渲染的是用户自己那条（uploads 引用），不是 view_image 注入行', async ({ page }) => {
  await login(page)

  const userRows = page.locator('[data-message-list-content] [data-role="user"]')
  await expect(userRows).toHaveCount(1)
  await expect(userRows.first()).toContainText('激活你的技能')
  await expect(userRows.first()).not.toContainText('view_image 工具加载')

  // 图片地址必须是用户上传的原件（uploads/…），不是注入副本（viewimg/…）
  const img = userRows.first().locator('img')
  await expect(img).toHaveAttribute('src', /\/api\/files\/download\?key=uploads%2F/)
  await expect(img).not.toHaveAttribute('src', /viewimg/)

  // 注入行完全不出现在渲染里
  await expect(page.locator('[data-message-list-content]')).not.toContainText('view_image 工具加载')
})
