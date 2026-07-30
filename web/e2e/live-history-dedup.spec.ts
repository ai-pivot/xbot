import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

async function setupMock(page: Page, historyResponse: unknown) {
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) => r.fulfill({
    json: { ok: true, data: {
      sessions: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() }],
      chats: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() }],
      orphan_subagents: [],
    } },
  }))
  await page.route('**/api/history*', (r) => r.fulfill({ json: { ok: true, data: historyResponse } }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

async function login(page: Page) {
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
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForTimeout(2000)
}

async function countAssistantRows(page: Page): Promise<number> {
  return page.evaluate(() => document.querySelectorAll('[data-role="assistant"]').length)
}

test.describe('Live message does not duplicate committed history', () => {
  test.beforeEach(() => {})

  test('DB-committed message from same turn (turn_id match) does NOT duplicate', async ({ browser }) => {
    const page = await browser.newPage()

    // Simulate IncrementalPersist: server has a committed assistant message
    // with turn_id=1 (same as active_progress) + active_progress for the same turn.
    // The initial reload fetches BOTH — committed message → messages,
    // active_progress → progress store → liveMessage.
    // Without turn_id matching, both render — duplicating content + tools.
    const historyResponse = {
      messages: [
        { role: 'user', content: 'verify the data flow', timestamp: new Date().toISOString() },
        {
          role: 'assistant',
          content: '两个问题。让我仔细看代码，先理解渲染流程。',
          timestamp: new Date().toISOString(),
          turn_id: 1,
          iterations: [{
            iteration: 0,
            content: '两个问题。让我仔细看代码，先理解渲染流程。',
            tools: [{ name: 'Read', label: 'Read', status: 'done', iteration: 0 }],
          }],
        },
      ],
      chat_id: 'chat-1', last_seq: 3,
      active_progress: {
        phase: 'thinking', iteration: 1, seq: 3, turn_id: 1, chat_id: 'web:chat-1',
        iteration_history: [{
          iteration: 0,
          content: '两个问题。让我仔细看代码，先理解渲染流程。',
          tools: [{ name: 'Read', label: 'Read', status: 'done', iteration: 0 }],
        }],
      },
    }

    await setupMock(page, historyResponse)
    await login(page)
    await page.waitForTimeout(500)

    // The committed message has turn_id=1, liveMessage has turnID=1 → match → 1 row.
    const assistantRows = await countAssistantRows(page)
    expect(assistantRows).toBe(1)

    await page.close()
  })

  test('committed message from DIFFERENT turn (turn_id mismatch) DOES show live', async ({ browser }) => {
    const page = await browser.newPage()

    // Committed message has turn_id=5 (previous turn), active_progress has turn_id=6 (current).
    // They should NOT dedup — both render (different turns).
    const historyResponse = {
      messages: [
        { role: 'user', content: 'previous question', timestamp: new Date().toISOString() },
        {
          role: 'assistant',
          content: 'Previous turn answer',
          timestamp: new Date().toISOString(),
          turn_id: 5,
        },
      ],
      chat_id: 'chat-1', last_seq: 3,
      active_progress: {
        phase: 'thinking', iteration: 0, seq: 4, turn_id: 6, chat_id: 'web:chat-1',
        iteration_history: [],
      },
    }

    await setupMock(page, historyResponse)
    await login(page)
    await page.waitForTimeout(500)

    // Different turns → both the committed message AND liveMessage render.
    const assistantRows = await countAssistantRows(page)
    expect(assistantRows).toBe(2)

    await page.close()
  })

  test('committed message with turn_id=0 (old data) renders alongside live (no false dedup)', async ({ browser }) => {
    const page = await browser.newPage()

    // Old data (pre-v50): turn_id=0. Must NOT false-match liveMessage (turnID=1).
    // Both render — this is the safe fallback (no data loss).
    const historyResponse = {
      messages: [
        { role: 'user', content: 'hello', timestamp: new Date().toISOString() },
        {
          role: 'assistant',
          content: 'Old answer (no turn_id)',
          timestamp: new Date().toISOString(),
          turn_id: 0,
        },
      ],
      chat_id: 'chat-1', last_seq: 2,
      active_progress: {
        phase: 'thinking', iteration: 0, seq: 3, turn_id: 1, chat_id: 'web:chat-1',
        iteration_history: [],
      },
    }

    await setupMock(page, historyResponse)
    await login(page)
    await page.waitForTimeout(500)

    // turn_id=0 (untracked) → no match → both render (safe fallback).
    const assistantRows = await countAssistantRows(page)
    expect(assistantRows).toBe(2)

    await page.close()
  })
})
