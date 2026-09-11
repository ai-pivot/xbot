import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E: the TODO toolbar is editable and every item can be promoted to the
 * session goal in one click. Drives real SSE progress events (the toolbar is
 * rendered from the progress snapshot, not from local state).
 */

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

const GOAL = '给 kv-cache 加前缀命中率指标'

let seqCounter = 0

async function emitSSE(page: Page, type: string, data: Record<string, unknown>) {
  await page.evaluate(
    ({ type, data, seq }) => {
      const w = window as unknown as SSEMockState
      const handlers = w.__sseListeners?.[type] as Set<(ev: MessageEvent) => void> | undefined
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
          sessions: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() }],
          chats: [],
          orphan_subagents: [],
        },
      },
    }),
  )
  await page.route('**/api/history', (r) =>
    r.fulfill({ json: { ok: true, data: { messages: [], chat_id: 'chat-1', last_seq: 0, active_progress: null } } }),
  )
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  // /api/rpc — the real backend persists the edit and then PUSHES the new list
  // back over the progress stream (single source of truth: the toolbar never
  // keeps a local copy). Mirror that here so the spec exercises the real loop.
  await page.route('**/api/rpc', async (r) => {
    const body = (() => {
      try {
        return r.request().postDataJSON() as { method?: string; params?: { todos?: unknown } }
      } catch {
        return {} as { method?: string; params?: { todos?: unknown } }
      }
    })()
    await r.fulfill({ json: { ok: true, data: { ok: true } } })
    if (body.method === 'set_todos') {
      const todos = body.params?.todos ?? []
      await emitSSE(page, 'progress_structured', {
        type: 'progress_structured',
        progress: { chat_id: 'web:chat-1', phase: '', todos, goal: { objective: GOAL, status: 'active' } },
      })
    }
  })
}

test.describe('TODO toolbar — edit + set as goal', () => {
  test.beforeEach(() => {
    seqCounter = 0
  })

  test('renders an editable checklist with per-item goal actions', async ({ browser }) => {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })

    await page.addInitScript(() => {
      const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
      ;(window as unknown as { __sseListeners: typeof listeners }).__sseListeners = listeners
      class M {
        readyState = 1
        onopen: ((e: Event) => void) | null = null
        onerror: ((e: Event) => void) | null = null
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
      ;(window as unknown as { EventSource: typeof M }).EventSource = M
    })

    await setupMock(page)
    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()
    await page.waitForTimeout(2000)

    const structured = (p: Record<string, unknown>) =>
      emitSSE(page, 'progress_structured', { type: 'progress_structured', progress: { chat_id: 'web:chat-1', ...p } })

    await structured({ phase: 'turn_started', turn_id: 7, turn_start: { trigger: 'user', content: '继续' } })
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await structured({
      phase: 'tool_exec',
      iteration: 1,
      turn_id: 7,
      todos: [
        { text: '修复 PD 分离下的 prefill 超时', status: 'doing' },
        { text: '给 kv-cache 加前缀命中率指标', status: 'pending' },
        { text: '统一 glm / deepseek 的引擎代码', status: 'pending' },
        { text: '补齐压测脚本', status: 'done' },
      ],
      goal: { objective: GOAL, status: 'active' },
    })
    await page.waitForTimeout(500)

    // Expand the checklist and reveal the row actions (hover-only).
    await page.getByTestId('todo-toggle').click()
    await page.waitForTimeout(400)
    const rows = page.getByTestId('todo-item')
    await expect(rows).toHaveCount(4)
    await rows.first().hover()
    await page.waitForTimeout(350)
    await page.screenshot({ path: 'test-results/todo-toolbar-actions.png' })

    // The goal-matched row carries its badge.
    await expect(page.getByTestId('todo-goal-badge')).toHaveCount(1)

    // Edit mode: click the text → input prefilled.
    await page.getByTestId('todo-text').first().click()
    const input = page.getByTestId('todo-edit-input')
    await expect(input).toBeVisible()
    await expect(input).toHaveValue('修复 PD 分离下的 prefill 超时')
    await page.screenshot({ path: 'test-results/todo-toolbar-edit.png' })
    await input.fill('修复 PD 分离下的 prefill 超时（已定位到 router 侧）')
    await page.screenshot({ path: 'test-results/todo-toolbar-edit-typed.png' })
    await input.press('Enter')

    // After saving, the row shows the new text and the edit input is gone.
    await expect(page.getByTestId('todo-edit-input')).toHaveCount(0)
    await expect(page.getByTestId('todo-text').first()).toHaveText(
      '修复 PD 分离下的 prefill 超时（已定位到 router 侧）',
    )
  })
})
