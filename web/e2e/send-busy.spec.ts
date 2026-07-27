import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E test for optimistic busy state on message send.
 *
 * Bug: after sending a message, the web UI didn't immediately enter busy
 * state. The input box stayed in "send" mode until the SSE session(busy)
 * event arrived (which could be delayed or lost). Only a page refresh
 * showed the correct busy state.
 *
 * Root cause: onSendSuccess is called AFTER the HTTP POST /api/message
 * resolves (line 584 of useChatMessages.ts), but it only calls
 * store.setStatus — which updates the session tree. However, if a
 * session-tree refresh() runs concurrently (e.g. from session-switch or
 * periodic poll), the HTTP response resets the session to idle, overriding
 * the optimistic 'running' state set by onSendSuccess.
 *
 * The fix: use the turn_started SSE event as the authoritative busy signal
 * (already implemented in onTurnStarted). But turn_started may arrive late.
 * The real fix is to mark the session as running IMMEDIATELY when the
 * sendMessage callback fires — before the HTTP POST resolves — not in
 * onSendSuccess (which fires after the POST resolves, leaving a window).
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
  await page.route('**/api/history', (r) => r.fulfill({
    json: { ok: true, data: { messages: [], chat_id: 'chat-1', last_seq: 0, active_progress: null } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  // Mock /api/message to resolve successfully but with a slight delay
  // to simulate the window where busy hasn't arrived yet
  await page.route('**/api/message', (r) => r.fulfill({ json: { ok: true, chat_id: 'chat-1', channel: 'web', timestamp: Date.now() } }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
  await page.route('**/api/chats/*/switch', (r) => r.fulfill({ json: { ok: true, chat_id: 'chat-1', channel: 'web', todos: [] } }))
}

/** Check whether the input area shows a cancel/stop button (busy) or send button (idle). */
async function getInputMode(page: Page): Promise<{ hasSend: boolean; hasCancel: boolean }> {
  const editor = page.locator('.tiptap, textarea, [contenteditable]').first()
  if (await editor.isVisible().catch(() => false)) {
    await editor.click()
    await page.keyboard.type('x')
    await page.waitForTimeout(100)
  }
  return page.evaluate(() => {
    const buttons = Array.from(document.querySelectorAll('button'))
    const hasCancel = buttons.some(b => (b.className || '').includes('destructive'))
    const hasSend = buttons.some(b => {
      const cls = b.className || ''
      return cls.includes('bg-accent') && !cls.includes('destructive')
    })
    return { hasSend, hasCancel }
  })
}

test.describe('Optimistic busy on message send', () => {
  test.beforeEach(() => { seqCounter = 0 })

  test('input enters busy mode immediately after sending a message (before SSE busy)', async ({ browser }) => {
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

    // Type and send a message
    const editor = page.locator('.tiptap, textarea, [contenteditable]').first()
    await editor.click()
    await page.keyboard.type('hello world')
    await page.waitForTimeout(100)

    // Click send button
    const sendButton = page.locator('button.bg-accent:not(.destructive)').first()
    await sendButton.click()

    // Wait a moment for the HTTP POST to resolve
    await page.waitForTimeout(300)

    // ── Check: input should be in cancel (busy) mode ──
    // WITHOUT the fix, onSendSuccess fires after POST resolves, but if a
    // refresh() is racing, the session may be reset to idle.
    const mode = await getInputMode(page)
    console.log('After send - hasSend:', mode.hasSend, 'hasCancel:', mode.hasCancel)

    // The input should show cancel button (busy), NOT send button
    expect(mode.hasCancel).toBe(true)

    await page.close()
  })
})
