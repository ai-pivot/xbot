import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * TODO 渲染顺序必须严格等于 agent 给的顺序 —— 顺序就是计划语义。
 *
 * 历史 bug（本次修复，tools/todo.go）：SetTodos 之前有一行
 *
 *	slices.SortFunc(todos, func(a, b TodoItem) int { return cmp.Compare(a.ID, b.ID) })
 *
 * —— agent 用 id 表达执行优先级时，展示顺序被 id 重排，与它写的计划不符
 * （web todo panel 用户可见：「有时候按照 id 排序了」）。现在顺序由数组位置决定，
 * id 参数已彻底移除。
 *
 * 守护：注入顺序刻意「乱」的 todos（zebra → apple → mango，既不是字典序也不是
 * 编号序），断言 DOM 渲染顺序 === 数组顺序。
 *
 * mock 端点与 todo-sync.spec.ts 对齐（active_progress 走 /api/rpc 的
 * get_active_progress，另需 /api/session/status + /api/sse**，否则前端停在
 * welcome 空态，todo 面板根本不挂载）。
 */

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
  __sseSeq: number
}

let seqCounter = 0

async function emitSSE(page: Page, type: string, data: Record<string, unknown>) {
  await page.evaluate(({ type, data, seq }) => {
    const w = window as unknown as SSEMockState
    const handlers = w.__sseListeners?.[type]
    if (!handlers) return
    const ev = new MessageEvent(type, { data: JSON.stringify({ ...data, seq }) })
    handlers.forEach((h) => h(ev))
  }, { type, data, seq: ++seqCounter })
}

// 刻意乱序：zebra 第一、apple 第二、mango 第三（非字典序，非编号序）
const ORDERED_TODOS = [
  { text: 'zebra step', status: 'pending' },
  { text: 'apple step', status: 'doing' },
  { text: 'mango step', status: 'pending' },
]

async function setupMock(page: Page, todos: unknown[]) {
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) => r.fulfill({
    json: { ok: true, data: {
      sessions: [{ chat_id: 'chat-1', channel: 'web', label: 'Session 1', last_active: new Date().toISOString() }],
      chats: [],
      orphan_subagents: [],
    } },
  }))
  await page.route('**/api/history', (r) => r.fulfill({
    json: { ok: true, data: {
      messages: [],
      chat_id: 'chat-1',
      last_seq: 0,
      active_progress: { phase: 'done', todos, seq: 0 },
    } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  // get_active_progress 走 /api/rpc（不是独立 REST 端点）
  await page.route('**/api/rpc', (r) => {
    const body = r.request().postDataJSON()
    if (body?.method === 'get_active_progress') {
      r.fulfill({ json: { ok: true, data: { phase: 'done', todos, seq: 0 } } })
    } else {
      r.fulfill({ json: { ok: true, data: null } })
    }
  })
  await page.route('**/api/chats/*/switch', (r) => r.fulfill({
    json: { ok: true, chat_id: 'chat-1', channel: 'web', todos },
  }))
}

async function login(page: Page) {
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForTimeout(2000)
}

async function renderedOrder(page: Page): Promise<string[]> {
  return page.getByTestId('todo-item').evaluateAll((els) =>
    els.map((e) => (e as HTMLElement).dataset.todoText ?? ''))
}

test.describe('TODO order', () => {
  test.beforeEach(() => { seqCounter = 0 })

  test('renders todos in the exact order the agent provided (no re-sorting)', async ({ browser }) => {
    const page = await browser.newPage()
    await page.addInitScript(() => {
      const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
      const w = window as unknown as SSEMockState
      w.__sseListeners = listeners
      class M {
        readyState = 1
        onopen: ((e: Event) => void) | null = null
        onerror: ((e: Event) => void) | null = null
        constructor(public url: string) { setTimeout(() => this.onopen?.(new Event('open')), 0) }
        addEventListener(t: string, h: (e: MessageEvent) => void) {
          if (!listeners[t]) listeners[t] = new Set()
          listeners[t].add(h)
        }
        removeEventListener() {}
        close() {}
      }
      ;(window as unknown as { EventSource: typeof M }).EventSource = M
    })
    await setupMock(page, ORDERED_TODOS)
    await login(page)

    // 面板默认折叠 —— 展开后再断言。
    // 找不到 toggle = 面板没渲染 = 必须失败（条件断言会掩盖真实回归）。
    const todoToggle = page.getByTestId('todo-toggle').first()
    await expect(todoToggle).toBeVisible({ timeout: 5000 })
    await todoToggle.click()
    await page.waitForTimeout(300)

    // hydration 路径：顺序 === 数组顺序（不是字典序 apple/mango/zebra）
    await expect(page.getByTestId('todo-item')).toHaveCount(3)
    expect(await renderedOrder(page)).toEqual(['zebra step', 'apple step', 'mango step'])

    // live SSE 路径：同样保持顺序
    await emitSSE(page, 'session', {
      type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' },
    })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: {
        phase: 'tool_exec', iteration: 1, seq: 1, turn_id: 1,
        chat_id: 'web:chat-1', todos: ORDERED_TODOS,
      },
    })
    await page.waitForTimeout(400)

    await expect(page.getByTestId('todo-item')).toHaveCount(3)
    expect(await renderedOrder(page)).toEqual(['zebra step', 'apple step', 'mango step'])
    await page.close()
  })
})
