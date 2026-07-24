import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E test for AskUser iteration preservation.
 *
 * Bug: after the agent calls AskUser (WaitingUser), the backend emits
 * session(idle) in the chatProcessLoop defer block. The frontend's
 * session(idle) handler triggers a defensive finalize → store.reset(),
 * which clears iterationHistory. The iterations from before the AskUser
 * call disappear from the UI.
 *
 * Fix: chatProcessLoop skips session(idle) + snapshot deletion for
 * WaitingUser responses. The turn is paused, not ended.
 *
 * This test simulates the full flow:
 * 1. Start a turn with iterations (progress_structured + iteration_history)
 * 2. Send ask_user (WITHOUT session(idle) — the fixed backend doesn't send it)
 * 3. Verify iterations are still visible
 * 4. Submit the answer (POST /api/ask_user/respond)
 * 5. Simulate the answer's Run (turn_started + progress + text)
 * 6. Verify iterations are still visible after the answer
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

async function setupMock(page: Page) {
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
    json: { ok: true, data: { messages: mockMessages(2), chat_id: 'chat-1', last_seq: 2, active_progress: null } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
  await page.route('**/api/ask_user/respond', (r) => r.fulfill({ json: { ok: true, data: {} } }))
}

test.describe('AskUser iteration preservation', () => {
  test.beforeEach(() => { seqCounter = 0 })

  test('iterations before AskUser are preserved after ask_user + answer submission', async ({ browser }) => {
    const page = await browser.newPage()

    // Replace EventSource with a controllable mock
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

    // Wait for messages to load
    await page.waitForFunction(() => document.body.textContent?.includes('Message 1'), { timeout: 10000 })
    await page.waitForTimeout(500)

    // ── Phase 1: Start a turn with iterations ──
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: {
        phase: 'tool_exec', iteration: 0, seq: 3, turn_id: 1,
        chat_id: 'web:chat-1',
        active_tools: [{ name: 'Read', status: 'running', iteration: 0 }],
        completed_tools: [],
        iteration_history: [],
      },
    })
    // Complete iteration 0, start iteration 1 (delta push with iteration 0)
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: {
        phase: 'thinking', iteration: 1, seq: 4, turn_id: 1,
        chat_id: 'web:chat-1',
        active_tools: [],
        completed_tools: [{ name: 'Read', status: 'done', iteration: 0, summary: 'file content here' }],
        iteration_history: [{ iteration: 0, tools: [{ name: 'Read', status: 'done', summary: 'file content here' }] }],
      },
    })
    await page.waitForTimeout(300)

    // Verify iterations are visible
    const hasIterationBefore = await page.evaluate(() =>
      document.body.textContent?.includes('Read') ?? false)
    expect(hasIterationBefore).toBe(true)

    // ── Phase 2: Send ask_user (fixed backend does NOT send session(idle) first) ──
    await emitSSE(page, 'ask_user', {
      type: 'ask_user',
      progress: {
        questions: [{ question: 'Do you want to proceed?', options: ['yes', 'no'] }],
        request_id: 'ask-1',
        chat_id: 'web:chat-1',
      },
    })
    await page.waitForTimeout(300)

    // Verify iterations are STILL visible (fixed: no session(idle) → no reset)
    const hasIterationAfterAsk = await page.evaluate(() =>
      document.body.textContent?.includes('Read') ?? false)
    const hasAskUser = await page.evaluate(() =>
      document.body.textContent?.includes('Do you want to proceed?') ?? false)
    console.log('Iterations after AskUser:', hasIterationAfterAsk, '| AskUser visible:', hasAskUser)
    expect(hasAskUser).toBe(true)
    expect(hasIterationAfterAsk).toBe(true)

    // ── Phase 3: Submit the answer ──
    // Click the "yes" option button
    await page.getByRole('button', { name: 'yes' }).click()
    await page.waitForTimeout(200)
    // Click submit
    const submitBtn = page.locator('button:has-text("确认"), button:has-text("Submit"), button:has-text("确定")')
    if (await submitBtn.isVisible({ timeout: 1000 }).catch(() => false)) {
      await submitBtn.click()
    } else {
      // Fallback: find the submit button by its position (last button in the panel)
      await page.locator('[class*="rounded-lg"] button').last().click()
    }
    await page.waitForTimeout(300)

    // ── Phase 4: Simulate the answer's Run ──
    // turn_started with new TurnID
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 2, turn_start: { trigger: 'user', request_id: 'ans-1' }, chat_id: 'web:chat-1' },
    })
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    // New iteration (delta push — iteration 0, but it's a new Run)
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: {
        phase: 'thinking', iteration: 0, seq: 5, turn_id: 2,
        chat_id: 'web:chat-1',
        active_tools: [],
        completed_tools: [],
        iteration_history: [],
      },
    })
    await page.waitForTimeout(300)

    // Verify OLD iterations (Read tool) are STILL visible
    const hasIterationAfterAnswer = await page.evaluate(() =>
      document.body.textContent?.includes('Read') ?? false)
    console.log('Iterations after answer:', hasIterationAfterAnswer)

    // THE BUG: iterations disappear after answer submission
    expect(hasIterationAfterAnswer).toBe(true)

    await page.close()
  })
})
