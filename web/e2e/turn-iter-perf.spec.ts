import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E guard：**渲染代价与 turn 内迭代数无关**（用户要求：随着 turn 里 iter 增加，
 * 性能没有任何下降）。trace 归因（111.gz，12.4s 主线程，构建 index-B1MrMIM6.js）：
 *
 *   - App JS 不随时间增长（index bundle 554ms，`Zi` x0.46）——React 渲染不是主因；
 *   - 浏览器侧在涨：Layout x1.70 / Paint x1.69 / RasterTask x3.26 / GPUTask x1.82；
 *   - `Layout.dirtyObjects` 每 1/10 桶 16→64（x4）——失效范围随迭代数膨胀；
 *   - 7 次 `UpdateLayoutTree` 单次重算 ~4,700–4,800 个元素（≈ 整个 turn 子树），
 *     每次都落在 70–84ms 的 React 提交里。
 *
 * 修复：迭代块 `iter-block` 自带 `contain: layout paint` + `content-visibility: auto`
 * （离屏块跳过 style/layout/paint），进行中迭代 `iter-block-live` 恒渲染。
 * ⇒ 参与渲染的块数由**视口**决定，与 turn 的迭代总数无关。
 *
 * 两个已踩过的坑（改这个测试前必读）：
 *   1. **块必须是真的高**。Chrome 的 "relevant to the user" 判定窗口涵盖视口
 *      及其上下若干千像素；块太矮时整个 turn 都落在窗口内，跳过数会合理地是 0
 *      （不是 bug）。所以每个迭代块给 ~40 段文本（≈ 1,000px）。
 *   2. **跳过判据必须用 `checkVisibility({ contentVisibilityAuto: true })`**。
 *      被跳过的子树在 Chrome 里**仍报告上次布局的几何** —— 用
 *      `getClientRects()` 判断会把跳过的块误判成"已渲染"。
 */

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

let seqCounter = 0

async function emitSSE(page: Page, type: string, data: Record<string, unknown>) {
  await page.evaluate(
    ({ type, data, seq }) => {
      const w = window as unknown as SSEMockState
      const handlers = w.__sseListeners?.[type]
      if (!handlers) return
      const ev = new MessageEvent(type, { data: JSON.stringify({ ...data, seq }) })
      handlers.forEach((h) => h(ev))
    },
    { type, data, seq: ++seqCounter },
  )
}

async function setupMock(page: Page) {
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: {
          sessions: [
            { chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() },
          ],
          chats: [
            { chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() },
          ],
          orphan_subagents: [],
        },
      },
    }),
  )
  await page.route('**/api/history', (r) =>
    r.fulfill({
      json: { ok: true, data: { messages: [], chat_id: 'chat-1', last_seq: 0, active_progress: null } },
    }),
  )
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) =>
    r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }),
  )
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

/** 一个"已完成迭代"的结构化事件（与后端 push 协议同形：0-1 个迭代 delta）。
 *  内容给足体量（~40 段）——太矮的块会让整个 turn 落在浏览器 relevant 窗口内。 */
async function emitIteration(page: Page, n: number) {
  await emitSSE(page, 'progress_structured', {
    type: 'progress_structured',
    progress: {
      phase: 'content',
      iteration: n,
      seq: n + 10,
      turn_id: 1,
      chat_id: 'web:chat-1',
      content: `answer ${n}`,
      iteration_history: [
        {
          iteration: n,
          thinking: `thinking ${n}`,
          content: Array.from(
            { length: 40 },
            (_, i) => `line ${i} of answer ${n} — lorem ipsum dolor sit amet, consectetur adipiscing elit.`,
          ).join('\n\n'),
          completed_tools: [],
        },
      ],
    },
  })
}

/** DOM 里的迭代块数 / 其中"真正参与渲染"的块数（跳过判据见文件头注释）。 */
async function blockStats(page: Page): Promise<{ total: number; rendered: number }> {
  return page.evaluate(() => {
    const blocks = Array.from(document.querySelectorAll('.iter-block'))
    let rendered = 0
    for (const b of blocks) {
      const inner = b.firstElementChild as HTMLElement | null
      if (!inner) continue
      if (
        inner.checkVisibility({
          contentVisibilityAuto: true,
          opacityProperty: true,
          visibilityProperty: true,
        })
      ) {
        rendered++
      }
    }
    return { total: blocks.length, rendered }
  })
}

test.describe('turn iteration rendering cost is independent of iteration count', () => {
  test('off-screen iteration blocks are skipped; rendered count stays viewport-bounded', async ({
    browser,
  }) => {
    const page = await browser.newPage({ viewport: { width: 900, height: 700 } })

    await page.addInitScript(() => {
      const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
      const w = window as unknown as SSEMockState
      w.__sseListeners = listeners
      class MockEventSource {
        readyState = 1
        onopen: ((ev: Event) => void) | null = null
        onerror: ((ev: Event) => void) | null = null
        constructor(public url: string) {
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
    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()
    await page.waitForTimeout(2000)

    await emitSSE(page, 'session', {
      type: 'session',
      session: { action: 'busy', chat_id: 'chat-1', channel: 'web' },
    })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: {
        phase: 'turn_started',
        turn_id: 1,
        turn_start: { trigger: 'user', request_id: 'r1' },
        chat_id: 'web:chat-1',
      },
    })

    // 阶段 A：15 个真实体量迭代（≈ 15,000px ≫ 700px 视口）
    for (let n = 1; n <= 15; n++) await emitIteration(page, n)
    await page.waitForTimeout(500)
    const at15 = await blockStats(page)

    // 阶段 B：加到 45 个（3 倍迭代数）
    for (let n = 16; n <= 45; n++) await emitIteration(page, n)
    await page.waitForTimeout(500)
    const at45 = await blockStats(page)

    console.log('iter blocks @15:', at15, ' @45:', at45)

    // 1) DOM 保留全部迭代（内容不丢、浏览器内搜索可用）
    expect(at15.total).toBeGreaterThanOrEqual(15)
    expect(at45.total).toBeGreaterThanOrEqual(45)

    // 2) 离屏跳过生效：参与渲染的块数远小于迭代总数
    expect(at15.rendered).toBeLessThan(at15.total / 2)
    expect(at45.rendered).toBeLessThan(at45.total / 2)

    // 3) **与 N 无关**：迭代数 3 倍，参与渲染的块数不随之增长（由视口决定）
    expect(at45.rendered).toBeLessThanOrEqual(at15.rendered + 3)
    expect(at45.rendered).toBeLessThan(10)

    await page.close()
  })
})
