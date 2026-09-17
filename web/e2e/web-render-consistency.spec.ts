/**
 * REPRO（用户 2026-09-17，严重）：
 *  ① 刷新后**同一段 CoT 渲染两次**（历史 + active_progress hydration 双份）；
 *  ② **AskUser 出现后已渲染的 CoT 消失**。
 *
 * 判据（几何/结构，不依赖文案）：
 *  - 每个迭代块 [data-iter-id] 在 DOM 中**恰好出现一次**（同一 turn 内）；
 *  - 迭代块总数 == 历史里的迭代数（不多不少）；
 *  - 收到 ask_user 事件后，已渲染的迭代块**仍在**（AskUser 是暂停，不是擦除）。
 */
import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://127.0.0.1:5199'

const ITERS = [
  { iteration: 1, content: '第一迭代正文', reasoning: '第一迭代思考', tools: [], tool_count: 0 },
  { iteration: 2, content: '第二迭代正文', reasoning: '第二迭代思考', tools: [], tool_count: 0 },
  { iteration: 3, content: '第三迭代正文', reasoning: '第三迭代思考', tools: [], tool_count: 0 },
]

async function newClient(browser: import('@playwright/test').Browser): Promise<Page> {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
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
  // 一个 turn：user + assistant（3 个迭代，来自 iteration_history）
  const assistantRow = {
    id: 2,
    role: 'assistant',
    content: '',
    seq: 2,
    turn_id: 42,
    timestamp: now,
    iteration_history: ITERS,
    iterations: ITERS,
  }
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: {
          sessions: [{ chat_id: 'chat-1', channel: 'web', label: 'S1', last_active: now }],
          chats: [{ chat_id: 'chat-1', channel: 'web', label: 'S1', last_active: now }],
          orphan_subagents: [],
        },
      },
    }),
  )
  await page.route('**/api/history**', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: {
          chat_id: 'chat-1',
          last_seq: 2,
          messages: [
            { id: 1, role: 'user', content: 'hello', seq: 1, turn_id: 42, timestamp: now },
            assistantRow,
          ],
          // ⚠️ 同一 turn 的 active_progress 快照（hydration 也会写入 live）——
          // 这正是"刷新后重复渲染"的竞态输入。
          active_progress: {
            chat_id: 'chat-1',
            turn_id: 42,
            phase: 'done',
            iteration: 3,
            iteration_history: ITERS,
            event_seq: 2,
            content: '',
            reasoning_stream_content: '',
            active_tools: [],
            completed_tools: [],
            streaming_tools: [],
          },
        },
      },
    }),
  )
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
  await page.route('**/api/queue/list', (r) => r.fulfill({ json: { ok: true, data: { items: [] } } }))

  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForFunction(() => document.body.textContent?.includes('第一迭代正文'), { timeout: 20_000 })
  return page
}

/** 每个迭代号在 DOM 中出现几次（>1 即重复渲染）。 */
async function iterCounts(page: Page): Promise<Record<string, number>> {
  return page.evaluate(() => {
    const out: Record<string, number> = {}
    for (const el of Array.from(document.querySelectorAll('[data-iter-id]'))) {
      const id = el.getAttribute('data-iter-id') || '?'
      out[id] = (out[id] ?? 0) + 1
    }
    return out
  })
}

test.describe('Web 渲染一致性（REPRO）', () => {
  test('① 刷新后同一 turn 的每个迭代只渲染一次（不得重复）', async ({ browser }) => {
    const page = await newClient(browser)
    const counts = await iterCounts(page)
    const dupes = Object.entries(counts).filter(([, n]) => n > 1)
    expect(dupes, `重复渲染的迭代: ${JSON.stringify(counts)}`).toEqual([])
    // 总数也必须等于历史里的迭代数（不多不少）
    const total = Object.values(counts).reduce((a, b) => a + b, 0)
    expect(total, `迭代块总数应等于 ${ITERS.length}`).toBe(ITERS.length)
    // 再刷一次（用户现象：刷新后出现两份）
    await page.reload()
    await page.waitForFunction(() => document.body.textContent?.includes('第一迭代正文'), { timeout: 20_000 })
    const after = await iterCounts(page)
    expect(Object.entries(after).filter(([, n]) => n > 1), `刷新后重复: ${JSON.stringify(after)}`).toEqual([])
    await page.close()
  })

  test('② ask_user 事件不得让已渲染的迭代消失', async ({ browser }) => {
    const page = await newClient(browser)
    const before = await iterCounts(page)
    expect(Object.keys(before).length, '前置：CoT 已渲染').toBeGreaterThan(0)
    // 推送 ask_user（WaitingUser 暂停）
    await page.evaluate(() => {
      const ls = (window as unknown as { __sseListeners: Record<string, Set<(ev: MessageEvent) => void>> }).__sseListeners
      const payload = {
        type: 'ask_user',
        chat_id: 'chat-1',
        turn_id: 42,
        ask_user: { questions: [{ question: '选一个', options: ['A', 'B'] }] },
        seq: 3,
      }
      for (const h of ls['message'] ?? []) h(new MessageEvent('message', { data: JSON.stringify(payload) }))
    })
    await page.waitForTimeout(800)
    const after = await iterCounts(page)
    expect(Object.keys(after).length, `ask_user 后迭代块消失: before=${JSON.stringify(before)} after=${JSON.stringify(after)}`).toBeGreaterThan(0)
    expect(Object.values(after).reduce((a, b) => a + b, 0)).toBe(ITERS.length)
    await page.close()
  })
})
