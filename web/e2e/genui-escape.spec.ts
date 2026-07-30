import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E test: GenUI iframe content cannot escape.
 *
 * Bug: LLM-generated code inside the GenUI iframe could access window.parent
 * (because sandbox had allow-same-origin) and inject content into the main
 * page — "content escape". This is a security issue.
 *
 * Fix: sandbox="allow-scripts" (no allow-same-origin) + overflow:hidden on
 * iframe body. LLM code runs in a fully isolated sandbox — no access to
 * parent DOM.
 */

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
  await page.route('**/api/history*', (r) => r.fulfill({
    json: { ok: true, data: { messages: [], chat_id: 'chat-1', last_seq: 0, active_progress: null } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

test.describe('GenUI iframe isolation', () => {
  test.beforeEach(() => { seqCounter = 0 })

  test('LLM code cannot access window.parent (no content escape)', async ({ browser }) => {
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

    // ── Inject GenUI code that TRIES to escape via window.parent ──
    const maliciousCode = `
export default function App() {
  React.useEffect(() => {
    // Try to access parent DOM — this should be blocked by sandbox
    try {
      if (window.parent && window.parent !== window) {
        window.parent.document.body.innerHTML += '<div id="escaped-content" style="position:fixed;top:0;left:0;z-index:99999;background:red;color:white;padding:20px">ESCAPED!</div>';
      }
    } catch (e) {
      // Cross-origin error = sandbox working correctly
    }
    // Also try to create a global on parent
    try {
      window.parent.__escaped = true;
    } catch (e) {}
  }, []);
  return React.createElement('div', null, 'Safe content');
};
`

    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: {
        phase: 'thinking', iteration: 0, seq: 1, turn_id: 1, chat_id: 'web:chat-1',
        stream_content: '',
        genui_content: maliciousCode,
      },
    })
    await page.waitForTimeout(1500)

    // ── Verify: no escaped content in the parent document ──
    const escaped = await page.evaluate(() => ({
      hasEscapedElement: !!document.getElementById('escaped-content'),
      hasEscapedGlobal: (window as unknown as { __escaped?: boolean }).__escaped === true,
      parentBodyContainsEscape: document.body.innerHTML.includes('ESCAPED!'),
    }))
    console.log('Escape attempt result:', JSON.stringify(escaped))

    // THE BUG: with allow-same-origin, the LLM code could access window.parent
    // and inject content into the main page. With allow-scripts only, this
    // throws a cross-origin error and the escape fails.
    expect(escaped.hasEscapedElement).toBe(false)
    expect(escaped.hasEscapedGlobal).toBe(false)
    expect(escaped.parentBodyContainsEscape).toBe(false)

    await page.close()
  })
})
