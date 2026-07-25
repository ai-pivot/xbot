import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E test for active tool preservation on page refresh.
 *
 * Bug: when the agent is mid-tool-execution (iteration 1, tool running) and
 * the user refreshes the page, the running tool disappears. Only iteration 1's
 * content is shown, followed by "思考中..." (thinking indicator).
 *
 * Root cause: the progress snapshot from GetActiveProgress includes active_tools,
 * but the frontend hydration or SSE reconnect overwrites it.
 */

function mockMessages(n: number) {
  return Array.from({ length: n }, (_, i) => ({
    role: i % 2 === 0 ? 'user' : 'assistant',
    content: `Message ${i}. `.repeat(10),
    seq: i + 1,
    timestamp: new Date(Date.now() - (n - i) * 60000).toISOString(),
  }))
}

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
  __sseSeq: number
}

let seqCounter = 0

async function emitSSE(page: Page, type: string, data: Record<string, unknown>) {
  await page.evaluate(({ type, data, seq }) => {
    const w = window as unknown as SSEMockState
    const listeners = w.__sseListeners
    if (!listeners) return
    const handlers = listeners[type] as Set<(ev: MessageEvent) => void> | undefined
    if (!handlers) return
    const ev = new MessageEvent(type, { data: JSON.stringify({ ...data, seq }) })
    handlers.forEach((h) => h(ev))
  }, { type, data, seq: ++seqCounter })
}

async function setupMock(page: Page, activeProgress: Record<string, unknown> | null) {
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
  await page.route('**/api/history', (r) => r.fulfill({
    json: { ok: true, data: { messages: mockMessages(2), chat_id: 'chat-1', last_seq: 10, active_progress: activeProgress } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

test.describe('Active tool preservation on refresh', () => {
  test.beforeEach(() => { seqCounter = 10 })

  test('running tool is visible after page refresh (hydration from active_progress)', async ({ browser }) => {
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

    // Mock history with active_progress: agent in iteration 1, tool "Shell" running
    const activeProgress = {
      phase: 'tool_exec',
      iteration: 1,
      seq: 10,
      turn_id: 1,
      chat_id: 'web:chat-1',
      active_tools: [{ name: 'Shell', status: 'running', iteration: 1, label: 'go build', summary: '' }],
      completed_tools: [],
      iteration_history: [
        {
          iteration: 0,
          thinking: 'Let me check the build status.',
          completed_tools: [{ name: 'Read', status: 'done', iteration: 0, summary: 'main.go' }],
        },
      ],
      todos: [],
    }

    // Mock the get_active_progress RPC to return the same snapshot (SSE reconnect recovery)
    await page.route('**/api/rpc', (r) => {
      const body = r.request().postDataJSON()
      if (body?.method === 'get_active_progress') {
        r.fulfill({ json: { ok: true, data: activeProgress } })
      } else {
        r.fulfill({ json: { ok: true, data: null } })
      }
    })

    await setupMock(page, activeProgress)
    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()

    // Wait for messages to load + progress to hydrate
    await page.waitForFunction(() => document.body.textContent?.includes('Message 1'), { timeout: 10000 })
    await page.waitForTimeout(1000)

    // ── Simulate SSE reconnect: restoreActiveProgress dispatches progress_structured + session(busy) ──
    // This is what happens ~1.5s after SSE connects (scheduleReplayFallback).
    // The session(busy) handler calls resetStreamingState() which clears activeTools.
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: activeProgress,
    })
    await emitSSE(page, 'session', {
      type: 'session',
      session: { action: 'busy', chat_id: 'chat-1', channel: 'web' },
    })
    await page.waitForTimeout(500)

    // ── Verify: the running tool "Shell" should STILL be visible ──
    const hasRunningTool = await page.evaluate(() =>
      document.body.textContent?.includes('Shell') ?? false)

    // ── Verify: "思考中" / thinking indicator should NOT be shown ──
    const hasShimmer = await page.evaluate(() =>
      document.querySelector('[class*="shimmer"], [class*="animate-pulse"]') !== null)

    console.log('hasRunningTool:', hasRunningTool)
    console.log('hasShimmer:', hasShimmer)

    // THE BUG: session(busy) from restoreActiveProgress clears activeTools via resetStreamingState()
    expect(hasRunningTool).toBe(true)

    await page.close()
  })
})
