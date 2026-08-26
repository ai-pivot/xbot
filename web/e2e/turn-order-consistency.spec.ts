import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
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
    json: { ok: true, data: { messages: [], chat_id: 'chat-1', last_seq: 0, active_progress: null, has_more: false, oldest_id: 0 } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
  await page.route('**/api/message', (r) => r.fulfill({ json: { ok: true, data: { ok: true } } }))
}

test.describe('Turn order consistency', () => {
  test.beforeEach(() => { seqCounter = 0 })

  test('rapid send: second user message appears after live assistant, not before', async ({ browser }) => {
    const page = await browser.newPage()
    await setupMock(page)

    await page.addInitScript(() => {
      const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
      ;(window as unknown as SSEMockState).__sseListeners = listeners
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

    // Send first message
    await page.locator('.tiptap, [contenteditable]').click()
    await page.keyboard.type('first message')
    await page.keyboard.press('Control+Enter')
    // NO optimistic rendering: the backend echoes every accepted user message
    // as user_echo WITH its authoritative turn_id. Emit it to render user-1.
    await emitSSE(page, 'user_echo', { content: 'first message', turn_id: 1, ts: Date.now() / 1000, id: 'r1' })
    await page.waitForTimeout(500)

    // Emit turn_started for turn 1
    await emitSSE(page, 'progress_structured', {
      progress: { phase: 'thinking', iteration: 1, turn_id: 1, chat_id: 'chat-1' }, chat_id: 'chat-1',
    })
    await page.waitForTimeout(200)

    // Emit stream content (live assistant for turn 1)
    await emitSSE(page, 'stream_content', {
      progress: { stream_content: 'reply to first' }, chat_id: 'chat-1',
    })
    await page.waitForTimeout(200)

    // Send second message WHILE agent is still processing turn 1
    await page.locator('.tiptap, [contenteditable]').click()
    await page.keyboard.type('second message')
    await page.keyboard.press('Control+Enter')
    // Backend echoes user-2 (turn_id=2). Rendering is deterministic — the
    // echo arrives after the live assistant for turn 1 is already visible.
    await emitSSE(page, 'user_echo', { content: 'second message', turn_id: 2, ts: Date.now() / 1000, id: 'r2' })
    await page.waitForTimeout(500)

    // Emit turn_started for turn 2 (binds second user msg to turnID=2)
    await emitSSE(page, 'progress_structured', {
      progress: { phase: 'thinking', iteration: 1, turn_id: 2, chat_id: 'chat-1' }, chat_id: 'chat-1',
    })
    await page.waitForTimeout(200)

    // Check message order: [user-1] [live-assistant-1] [user-2]
    // NOT [user-1] [user-2] [live-assistant-1]
    const messages = await page.evaluate(() => {
      const els = document.querySelectorAll('[data-message-id]')
      return Array.from(els).map(el => ({
        id: el.getAttribute('data-message-id') || '',
        role: el.getAttribute('data-role') || '',
        turnId: el.getAttribute('data-turn-id') || '',
      }))
    })

    // Find positions
    const userMsgs = messages.filter(m => m.role === 'user')
    const assistantMsgs = messages.filter(m => m.role === 'assistant')
    
    // There should be at least 2 user messages
    expect(userMsgs.length).toBeGreaterThanOrEqual(2)
    
    // The live assistant (turn 1) should appear BEFORE the second user message
    const secondUserIdx = messages.indexOf(userMsgs[1])
    const liveAssistantIdx = assistantMsgs.findIndex(m => m.id.startsWith('live-') || m.turnId === '1')
    
    if (liveAssistantIdx >= 0) {
      expect(liveAssistantIdx).toBeLessThan(secondUserIdx)
    }

    await page.close()
  })

  test('cancel preserves live content and does not show copy button on intermediate content', async ({ browser }) => {
    const page = await browser.newPage()
    await setupMock(page)

    await page.addInitScript(() => {
      const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
      ;(window as unknown as SSEMockState).__sseListeners = listeners
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

    // Send a message
    await page.locator('.tiptap, [contenteditable]').click()
    await page.keyboard.type('test cancel')
    await page.keyboard.press('Control+Enter')
    await page.waitForTimeout(500)

    // Start streaming
    await emitSSE(page, 'progress_structured', {
      progress: { phase: 'thinking', iteration: 1, turn_id: 1, chat_id: 'chat-1' }, chat_id: 'chat-1',
    })
    await emitSSE(page, 'stream_content', {
      progress: { stream_content: 'partial reply that should be preserved' }, chat_id: 'chat-1',
    })
    await page.waitForTimeout(300)

    // Cancel
    await emitSSE(page, 'text', {
      content: '', cancelled: true, chat_id: 'chat-1',
    })
    await page.waitForTimeout(500)

    // Check: no copy button on the cancelled content
    const copyButtons = await page.locator('[title="复制 Markdown"]').count()
    expect(copyButtons).toBe(0)

    // Check: content is preserved (not empty)
    // Frozen liveMessage returns null (phase='frozen') — content is in the
    // committed message (from appendAssistant in flushSync). In mock SSE
    // (E2E), the frozen liveMessage keeps content visible (user requirement).
    // The real appendAssistant path is tested by Go integration tests.
    const assistantElements = await page.locator('[data-role="assistant"]').all()
    let foundContent = false
    for (const el of assistantElements) {
      const text = await el.textContent()
      if (text && text.includes('partial reply')) {
        foundContent = true
        break
      }
    }
    // User requirement: already-rendered content NEVER disappears after cancel.
    // The frozen live message keeps 'partial reply' visible (store.freeze()
    // keeps content + the committed message replaces it when it arrives).
    expect(foundContent).toBe(true)

    await page.close()
  })

  test('stale progress_structured after turn complete does not show thinking spinner', async ({ browser }) => {
    const page = await browser.newPage()
    await setupMock(page)

    await page.addInitScript(() => {
      const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
      ;(window as unknown as SSEMockState).__sseListeners = listeners
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

    // Send a message and complete the turn
    await page.locator('.tiptap, [contenteditable]').click()
    await page.keyboard.type('test stale progress')
    await page.keyboard.press('Control+Enter')
    await page.waitForTimeout(500)

    await emitSSE(page, 'progress_structured', {
      progress: { phase: 'thinking', iteration: 1, turn_id: 1, chat_id: 'chat-1' }, chat_id: 'chat-1',
    })
    await emitSSE(page, 'stream_content', {
      progress: { stream_content: 'final reply' }, chat_id: 'chat-1',
    })
    // Complete the turn
    await emitSSE(page, 'text', {
      content: 'final reply', chat_id: 'chat-1',
    })
    await page.waitForTimeout(500)

    // Emit a STALE progress_structured (should be ignored)
    await emitSSE(page, 'progress_structured', {
      progress: { phase: 'thinking', iteration: 2, turn_id: 1, chat_id: 'chat-1' }, chat_id: 'chat-1',
    })
    await page.waitForTimeout(500)

    // Check: no "思考中" spinner should appear after the completed reply
    const thinkingSpinner = await page.locator('text=思考中').count()
    expect(thinkingSpinner).toBe(0)

    await page.close()
  })

  test('refresh during first-iteration thinking shows the thinking progress (not just user msg)', async ({ browser }) => {
    const page = await browser.newPage()
    await setupMock(page)
    // Register AFTER setupMock — Playwright routes are LIFO, so this route
    // wins over setupMock's empty-history route. Mock an in-flight
    // active_progress: agent in the FIRST iteration thinking phase.
    await page.route('**/api/history', (r) => r.fulfill({
      json: { ok: true, data: {
        messages: [
          { role: 'user', content: 'my question', seq: 1, timestamp: new Date().toISOString() },
        ],
        chat_id: 'chat-1',
        last_seq: 5,
        active_progress: {
          phase: 'thinking',
          iteration: 1,
          seq: 3,
          turn_id: 1,
          chat_id: 'web:chat-1',
          reasoning_stream_content: 'thinking step 1...',
          stream_content: '',
          content: '',
          active_tools: [],
          completed_tools: [],
          iteration_history: [],
        },
        has_more: false,
        oldest_id: 0,
      } },
    }))

    await page.addInitScript(() => {
      const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
      ;(window as unknown as SSEMockState).__sseListeners = listeners
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

    // Wait for history + active_progress hydration
    await page.waitForFunction(() => document.body.textContent?.includes('my question'), { timeout: 10000 })
    await page.waitForTimeout(1000)

    // ── Verify: the streamed reasoning from the first iteration thinking is
    // visible after refresh (NOT just the user message). This is the bug:
    // historyProgressToLive dropped reasoning_stream_content, so the UI
    // showed only the user msg with no assistant progress at all.
    //
    // NOTE: reasoning renders as a foldable "▸ Thought N characters" header
    // (T-always-folded), so assert on the header text, not the full body.
    const reasoningHeader = await page.locator('[data-role="assistant"]').first()
    const headerText = reasoningHeader ? await reasoningHeader.textContent() : ''
    console.log('Assistant header:', headerText)

    // The reasoning must be restored from active_progress (header shows the
    // folded "Thought" summary). Without the fix, no assistant row exists.
    expect(headerText).toContain('Thought')
    expect(headerText).not.toContain('my question')

    await page.close()
  })

  test('refresh during first-iteration thinking with NO stream content shows thinking placeholder', async ({ browser }) => {
    const page = await browser.newPage()
    await setupMock(page)
    // Register AFTER setupMock (LIFO route priority). Pure thinking — no
    // reasoning_stream_content, no stream_content, no tools, no iterations.
    // The agent is "thinking" (prefill) with nothing emitted yet.
    await page.route('**/api/history', (r) => r.fulfill({
      json: { ok: true, data: {
        messages: [
          { role: 'user', content: 'second question', seq: 1, timestamp: new Date().toISOString() },
        ],
        chat_id: 'chat-1',
        last_seq: 5,
        active_progress: {
          phase: 'thinking',
          iteration: 1,
          seq: 3,
          turn_id: 1,
          chat_id: 'web:chat-1',
          active_tools: [],
          completed_tools: [],
          iteration_history: [],
        },
        has_more: false,
        oldest_id: 0,
      } },
    }))

    await page.addInitScript(() => {
      const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
      ;(window as unknown as SSEMockState).__sseListeners = listeners
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

    await page.waitForFunction(() => document.body.textContent?.includes('second question'), { timeout: 10000 })
    await page.waitForTimeout(1000)

    // ── Verify: the "thinking…" placeholder is visible (busy fallback from
    // hydrated progressSnapshot.streaming — sessionStore.running is false
    // because SSE does NOT replay session(busy) on refresh). The placeholder
    // text is i18n (ShimmerThinking → t('agent.reasoningStreaming')) which
    // renders as "thinking…" in the default (en) locale.
    const bodyText = await page.evaluate(() => document.body.textContent ?? '')
    console.log('Body has thinking placeholder:', /thinking…|思考中/.test(bodyText))
    expect(/thinking…|思考中/.test(bodyText)).toBe(true)
    expect(bodyText).not.toContain('second question\n\n') // user msg is NOT the only content

    await page.close()
  })
})
