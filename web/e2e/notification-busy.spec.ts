import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E test for notification turn busy state.
 *
 * Bug: when a system notification (bg task / cron) triggers a turn while
 * the session is idle (no active chatWorker), drainAndProcessNotifications
 * runs directly — bypassing chatProcessLoop's session(busy) emission.
 * The notification user message is displayed via turn_started, but the
 * input box stays in "send" mode instead of switching to "cancel/busy".
 *
 * Fix: injectBgUserMessage should emit session(busy) BEFORE injecting the
 * inbound message, so the frontend enters busy state immediately.
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
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

/** Check whether the input area shows a cancel/stop button (busy) or send button (idle).
 *  Types text in the editor first so the send button is visible. */
async function getInputMode(page: Page): Promise<{ hasSend: boolean; hasCancel: boolean }> {
  // Type a character so the send button becomes visible (it's hidden when empty)
  const editor = page.locator('.tiptap, textarea, [contenteditable]').first()
  if (await editor.isVisible().catch(() => false)) {
    await editor.click()
    await page.keyboard.type('x')
    await page.waitForTimeout(100)
  }
  return page.evaluate(() => {
    // The MessageInput's action button: cancel uses variant="destructive"
    // (class includes "destructive"), send uses bg-accent.
    const buttons = Array.from(document.querySelectorAll('button'))
    const hasCancel = buttons.some(b => (b.className || '').includes('destructive'))
    const hasSend = buttons.some(b => {
      const cls = b.className || ''
      return cls.includes('bg-accent') && !cls.includes('destructive')
    })
    return { hasSend, hasCancel }
  })
}

test.describe('Notification turn busy state', () => {
  test.beforeEach(() => { seqCounter = 0 })

  test('input box switches to cancel mode when notification triggers a turn', async ({ browser }) => {
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

    // ── Simulate the FULL notification turn flow as the backend sends it ──
    // 1. turn_started (notification trigger — displays the user message)
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: {
        phase: 'turn_started', turn_id: 1, chat_id: 'web:chat-1',
        turn_start: { trigger: 'notification', content: '⏰ [定时任务触发] test notification' },
      },
    })
    // 2. session(busy) — chatProcessLoop picks up the injected message
    await emitSSE(page, 'session', {
      type: 'session',
      session: { action: 'busy', chat_id: 'chat-1', channel: 'web' },
    })
    // 3. First progress (thinking)
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'thinking', iteration: 0, seq: 2, turn_id: 1, chat_id: 'web:chat-1' },
    })
    await page.waitForTimeout(500)

    // ── After notification turn started: input should be in cancel mode (busy) ──
    const after = await getInputMode(page)
    console.log('After notification:', JSON.stringify(after))

    // THE BUG: input stays in send mode (hasCancel=false) even after turn_started
    expect(after.hasCancel).toBe(true)

    await page.close()
  })

  test('input box enters busy mode from turn_started even WITHOUT session(busy) event', async ({ browser }) => {
    // This tests the scenario where session(busy) is lost or delayed (SSE
    // coalescing). The turn_started progress event should be sufficient to
    // enter busy mode — the frontend should not rely solely on session(busy).
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

    // ── Send ONLY turn_started (no session(busy)) — simulates lost/delayed busy event ──
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: {
        phase: 'turn_started', turn_id: 1, chat_id: 'web:chat-1',
        turn_start: { trigger: 'notification', content: '⏰ test notification' },
      },
    })
    await page.waitForTimeout(500)

    const after = await getInputMode(page)
    console.log('After turn_started (no session(busy)):', JSON.stringify(after))

    // THE BUG: without session(busy), the input stays in send mode
    expect(after.hasCancel).toBe(true)
    expect(after.hasSend).toBe(false)

    await page.close()
  })
})
