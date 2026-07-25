import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E test: cancel should NOT re-render the agent message.
 *
 * Bug: after cancel, the cancel ack called onAssistantComplete which
 * committed a new assistant message via appendAssistant + flushSync. This
 * caused the message to re-render with animation (flicker/jitter).
 *
 * Fix: cancel ack now calls onCancelComplete (resetProgress only) — no
 * appendAssistant, no flushSync, no reload. The streamed content stays
 * as-is.
 */

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
  __sseSeq: number
  __messageCount: number
  __renderCount: number
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
    json: { ok: true, data: { messages: [], chat_id: 'chat-1', last_seq: 0, active_progress: null } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

test.describe('Cancel does not re-render', () => {
  test.beforeEach(() => { seqCounter = 0 })

  test('cancel ack does NOT append a new assistant message', async ({ browser }) => {
    const page = await browser.newPage()

    await page.addInitScript(() => {
      const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
      const w = window as unknown as SSEMockState
      w.__sseListeners = listeners
      w.__messageCount = 0
      // Count how many assistant messages are in the DOM
      w.__renderCount = 0
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

    // ── Start a turn with streaming content ──
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 1, turn_start: { trigger: 'user', request_id: 'r1' }, chat_id: 'web:chat-1' },
    })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'thinking', iteration: 0, seq: 2, turn_id: 1, chat_id: 'web:chat-1' },
    })
    // Stream some content
    await emitSSE(page, 'stream_content', {
      type: 'stream_content',
      progress: { stream_content: 'I am thinking about this...', chat_id: 'web:chat-1', streaming: true },
    })
    await page.waitForTimeout(300)

    // ── Cancel ack ──
    await emitSSE(page, 'text', {
      type: 'text',
      content: '',
      cancelled: true,
      seq: 3,
      turn_id: 1,
      chat_id: 'web:chat-1',
      progress_history: JSON.stringify([
        { iteration: 0, thinking: 'I am thinking about this...', completed_tools: [], user_cancelled: true },
      ]),
    })
    await page.waitForTimeout(500)

    // ── Verify: NO committed assistant message should exist ──
    // The cancel ack should NOT call appendAssistant. The streamed content
    // was in the live store, which was reset — no message committed.
    const assistantCount = await page.evaluate(() => {
      // Count elements that look like committed assistant messages
      // (not live messages which have id containing "live-")
      const all = document.querySelectorAll('[data-index]')
      let count = 0
      for (const el of all) {
        const text = el.textContent || ''
        if (text.includes('thinking about this')) count++
      }
      return count
    })
    console.log('Assistant messages with content after cancel:', assistantCount)

    // THE BUG: cancel committed a new assistant message (count=1).
    // Fix: cancel does NOT commit — count should be 0.
    expect(assistantCount).toBe(0)

    await page.close()
  })
})
