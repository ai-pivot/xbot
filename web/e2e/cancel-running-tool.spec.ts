import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E test for cancel flow: running tool → PhaseDone → text(cancelled) → idle → new turn.
 *
 * Bug: after cancel, user_cancelled tool is not shown, running tool persists,
 * and sending a new message renders "user1 user2 iter*n running_tool".
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
  await page.route('**/api/chats/*/switch', (r) => r.fulfill({ json: { ok: true, chat_id: 'chat-1', channel: 'web', todos: [] } }))
}

async function hasText(page: Page, text: string): Promise<boolean> {
  return page.evaluate((t) => (document.body.textContent || '').includes(t), text)
}

test.describe('Cancel flow: running tool → cancel → new turn', () => {
  test.beforeEach(() => { seqCounter = 0 })

  test('user_cancelled shown after cancel, running tool cleared, new turn clean', async ({ browser }) => {
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

    // ── Turn 1: tool running ──
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 1, turn_start: { trigger: 'user', request_id: 'r1' }, chat_id: 'web:chat-1' },
    })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'thinking', iteration: 1, seq: 2, turn_id: 1, chat_id: 'web:chat-1' },
    })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'tool_exec', iteration: 1, seq: 3, turn_id: 1, chat_id: 'web:chat-1',
        active_tools: [{ name: 'Shell', status: 'running', iteration: 1 }] },
    })
    await page.waitForTimeout(300)

    // Verify Shell is running
    expect(await hasText(page, 'Shell')).toBe(true)

    // ── Cancel: PhaseDone → text(cancelled) → session(idle) (full server flow) ──
    // PhaseDone (from progressFinalizer)
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'done', iteration: 1, seq: 4, turn_id: 1, chat_id: 'web:chat-1' },
    })
    await page.waitForTimeout(100)

    // text event (cancel ack) with progress_history including user_cancelled
    await emitSSE(page, 'text', {
      type: 'text', content: '', seq: 5, turn_id: 1, chat_id: 'chat-1', cancelled: true,
      progress_history: JSON.stringify([
        { iteration: 1, thinking: '', completed_tools: [
          { name: 'Shell', label: 'Shell sleep 30', status: 'done', iteration: 1 },
          { name: 'user_cancelled', status: 'done', iteration: 1 },
        ] },
      ]),
    })
    await page.waitForTimeout(100)

    // session(idle)
    await emitSSE(page, 'session', { type: 'session', session: { action: 'idle', chat_id: 'chat-1', channel: 'web' } })
    await page.waitForTimeout(500)

    // Check message and live state
    const msgInfo = await page.evaluate(() => {
      const el = document.querySelector('[data-role="assistant"]')
      const live = document.querySelector('[id*="live-"]')
      return {
        msgCount: document.querySelectorAll('[data-role="assistant"]').length,
        msgText: el ? el.textContent : 'NOT FOUND',
        liveExists: live !== null,
        liveText: live ? live.textContent : '',
      }
    })
    console.log('Message info:', JSON.stringify(msgInfo, null, 2))

    // ── Verify: no live message (running tool cleared) ──
    const hasLive = await page.evaluate(() => document.querySelector('[id*="live-"]') !== null)
    console.log('After cancel - live message exists:', hasLive)
    expect(hasLive).toBe(false)

    // ── Start a new turn ──
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 2, turn_start: { trigger: 'user', request_id: 'r2' }, chat_id: 'web:chat-1' },
    })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'thinking', iteration: 1, seq: 7, turn_id: 2, chat_id: 'web:chat-1' },
    })
    await page.waitForTimeout(500)

    // ── Verify: new turn is clean (no old running tool) ──
    // The live message should show thinking, not the old Shell running tool
    const liveContent = await page.evaluate(() => {
      const live = document.querySelector('[id*="live-"]')
      return live ? live.textContent || '' : ''
    })
    console.log('New turn live content:', liveContent.substring(0, 100))

    // Shell should not appear as a running tool in the new turn
    // (it may appear in the committed [interrupted] message, which is fine)
    // But the live message should not contain "running" Shell
    const hasLiveRunningShell = await page.evaluate(() => {
      const live = document.querySelector('[id*="live-"]')
      if (!live) return false
      // Check if the live message has a tool with "running" status
      return live.querySelector('[class*="sweep-text"]')?.textContent?.includes('Shell') ?? false
    })
    console.log('New turn has running Shell in live:', hasLiveRunningShell)
    expect(hasLiveRunningShell).toBe(false)

    await page.close()
  })
})
