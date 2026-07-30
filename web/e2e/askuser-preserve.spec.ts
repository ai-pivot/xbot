import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E test: AskUser answer does NOT clear iterations.
 *
 * Bug: when the user answers an AskUser prompt, the backend processes the
 * answer as a new message → emits turn_started → frontend calls store.reset()
 * → clears ALL iterationHistory. The iterations from before the AskUser
 * (tools, reasoning) disappear.
 *
 * Root cause: turn_started handler unconditionally calls store.reset(), which
 * wipes iterationHistory. AskUser answer is a continuation of the SAME turn,
 * not a new turn — the previous iterations must survive.
 */

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
  await page.route('**/api/history*', (r) => r.fulfill({
    json: { ok: true, data: { messages: [], chat_id: 'chat-1', last_seq: 0, active_progress: null } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
  await page.route('**/api/ask_user/respond', (r) => r.fulfill({ json: { ok: true, data: {} } }))
}

test.describe('AskUser answer preserves iterations', () => {
  test.beforeEach(() => { seqCounter = 0 })

  test('iterations before AskUser survive after answering', async ({ browser }) => {
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

    // ── Phase 1: Start a turn with iteration 0 (a completed Read tool) ──
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 1, turn_start: { trigger: 'user', request_id: 'r1' }, chat_id: 'web:chat-1' },
    })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'thinking', iteration: 0, seq: 2, turn_id: 1, chat_id: 'web:chat-1' },
    })
    // Tool running → done
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'tool_exec', iteration: 0, seq: 3, turn_id: 1, chat_id: 'web:chat-1', active_tools: [{ name: 'Read', status: 'running', iteration: 0 }] },
    })
    // Delta push: iteration 0 completed (Read done), iteration 1 starts (AskUser)
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: {
        phase: 'tool_exec', iteration: 1, seq: 4, turn_id: 1, chat_id: 'web:chat-1',
        active_tools: [{ name: 'AskUser', status: 'running', iteration: 1 }],
        completed_tools: [{ name: 'Read', status: 'done', iteration: 0, summary: 'main.go' }],
        iteration_history: [{ iteration: 0, thinking: 'Let me read the file.', completed_tools: [{ name: 'Read', status: 'done', iteration: 0, summary: 'main.go' }] }],
      },
    })
    await page.waitForTimeout(300)

    // ── Phase 2: AskUser prompt appears ──
    await emitSSE(page, 'ask_user', {
      type: 'ask_user',
      progress: {
        questions: [{ question: 'Proceed?', options: ['yes', 'no'] }],
        request_id: 'ask-1',
        chat_id: 'web:chat-1',
      },
    })
    await page.waitForTimeout(300)

    // Verify "Read" tool is visible (from iteration 0)
    const hasReadBefore = await page.evaluate(() =>
      document.body.textContent?.includes('Read') ?? false)
    console.log('Before answer - Read visible:', hasReadBefore)
    expect(hasReadBefore).toBe(true)

    // ── Phase 3: User answers the AskUser ──
    // Click "yes" option
    await page.getByRole('button', { name: 'yes' }).click()
    await page.waitForTimeout(200)
    // Click submit
    await page.locator('button:has-text("确认"), button:has-text("Submit"), button:has-text("确定")').click().catch(async () => {
      // Fallback: find the submit button in the AskUser panel
      const buttons = page.locator('button')
      const count = await buttons.count()
      for (let i = 0; i < count; i++) {
        const text = await buttons.nth(i).textContent()
        if (text && !['yes', 'no', '取消', 'Cancel'].includes(text.trim())) {
          await buttons.nth(i).click()
          break
        }
      }
    })
    await page.waitForTimeout(300)

    // ── Phase 4: Backend processes answer → emits turn_started (TurnID=2, trigger=resume) ──
    // The backend marks AskUser answers as trigger="resume" so the frontend
    // preserves iterationHistory from before the AskUser call.
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 2, turn_start: { trigger: 'resume', request_id: 'r2' }, chat_id: 'web:chat-1' },
    })
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'thinking', iteration: 0, seq: 5, turn_id: 2, chat_id: 'web:chat-1' },
    })
    await page.waitForTimeout(500)

    // ── Verify: "Read" tool should STILL be visible ──
    const hasReadAfter = await page.evaluate(() =>
      document.body.textContent?.includes('Read') ?? false)
    console.log('After answer - Read visible:', hasReadAfter)

    // THE BUG: turn_started store.reset() cleared iterationHistory → "Read" disappeared
    expect(hasReadAfter).toBe(true)

    await page.close()
  })
})
