import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E test: session switch restores todos.
 *
 * Bug: after switching to a session that used TodoWrite, the todo list
 * doesn't show until a page refresh. The /api/history active_progress
 * returns Phase="done" + Todos, but the useProgressStream hydrate effect
 * doesn't restore them on session switch.
 */

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

// Track which chat the /api/history mock returns
let currentChatID = 'chat-1'
let todosForChat: Record<string, unknown[]> = {}

async function setupMock(page: Page) {
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) => r.fulfill({
    json: { ok: true, data: {
      sessions: [
        { chat_id: 'chat-1', channel: 'web', label: 'Session 1', last_active: new Date().toISOString() },
        { chat_id: 'chat-2', channel: 'web', label: 'Session 2', last_active: new Date().toISOString() },
      ],
      chats: [
        { chat_id: 'chat-1', channel: 'web', label: 'Session 1', last_active: new Date().toISOString() },
        { chat_id: 'chat-2', channel: 'web', label: 'Session 2', last_active: new Date().toISOString() },
      ],
      orphan_subagents: [],
    } },
  }))
  await page.route('**/api/history', (r) => {
    const body = r.request().postDataJSON()
    const chatID = body?.chat_id || currentChatID
    const todos = todosForChat[chatID] || []
    r.fulfill({
      json: {
        ok: true,
        data: {
          messages: [],
          chat_id: chatID,
          last_seq: 0,
          // Return active_progress with Phase="done" + Todos (like the backend does
          // when GetActiveProgress finds no snapshot but todos exist)
          active_progress: todos.length > 0
            ? { phase: 'done', todos, seq: 0 }
            : null,
        },
      },
    })
  })
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
  await page.route('**/api/chats/*/switch', (r) => {
    const url = r.request().url()
    const chatID = url.match(/\/chats\/([^/]+)\/switch/)?.[1] || 'chat-1'
    r.fulfill({ json: { ok: true, chat_id: chatID, channel: 'web', todos: todosForChat[chatID] || [] } })
  })
}

test.describe('Session switch restores todos', () => {
  test.beforeEach(() => {
    currentChatID = 'chat-1'
    todosForChat = {
      'chat-1': [],
      'chat-2': [
        { id: 1, text: 'Setup project', done: true },
        { id: 2, text: 'Write tests', done: false },
      ],
    }
  })

  test('switching to a session with todos shows the todo list', async ({ browser }) => {
    const page = await browser.newPage()

    await page.addInitScript(() => {
      const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
      const w = window as unknown as SSEMockState
      w.__sseListeners = listeners
      class MockEventSource {
        readyState = 1
        onopen: ((ev: Event) => void) | null = null
        onerror: ((ev: Event) => void) | null = null
        constructor(public url: string) { setTimeout(() => this.onopen?.(new Event('open')), 0) }
        addEventListener(type: string, handler: (ev: MessageEvent) => void) {
          if (!listeners[type]) listeners[type] = new Set(); listeners[type].add(handler)
        }
        removeEventListener(type: string, handler: (ev: MessageEvent) => void) { listeners[type]?.delete(handler) }
        close() { for (const key of Object.keys(listeners)) listeners[key].clear() }
      }
      ;(window as unknown as { EventSource: typeof MockEventSource }).EventSource = MockEventSource
    })

    await setupMock(page)
    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()
    await page.waitForTimeout(2000)

    // chat-1 has no todos — verify no todo list visible
    const hasTodosBefore = await page.evaluate(() =>
      document.body.textContent?.includes('Setup project') ?? false)
    console.log('Before switch - has todos:', hasTodosBefore)
    expect(hasTodosBefore).toBe(false)

    // Switch to chat-2 (which has todos)
    currentChatID = 'chat-2'
    // Click session 2 in the sidebar
    const session2 = page.locator('text=Session 2').first()
    await session2.click()
    await page.waitForTimeout(1500)

    // Verify todos are visible
    const hasTodosAfter = await page.evaluate(() =>
      document.body.textContent?.includes('Setup project') ?? false)
    const hasTodo2 = await page.evaluate(() =>
      document.body.textContent?.includes('Write tests') ?? false)
    console.log('After switch - has todo 1:', hasTodosAfter, 'has todo 2:', hasTodo2)

    // THE BUG: todos don't show after session switch
    expect(hasTodosAfter).toBe(true)
    expect(hasTodo2).toBe(true)

    await page.close()
  })
})
