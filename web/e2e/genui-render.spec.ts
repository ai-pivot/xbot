import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E test: GenUI actually RENDERS inside the iframe.
 *
 * Regression: user reported a permanently blank iframe
 * (<html><head></head><body></body></html>) for every GenUI — even after
 * multiple fixes. This test renders the streaming path (genui_content) and
 * asserts the iframe document contains the compiled component's DOM, so the
 * failure mode (blank head = doc.write never ran; empty body = component
 * failed to compile/render) is caught in a REAL browser.
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
  await page.route('**/api/history', (r) => r.fulfill({
    json: { ok: true, data: { messages: [], chat_id: 'chat-1', last_seq: 0, active_progress: null } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

async function getIframeState(page: Page) {
  return page.evaluate(() => {
    // GenUI no longer uses iframes — it renders inline (SandboxedUI inline mode).
    // Find the rendered GenUI content by data-testid or the sandboxed-ui container.
    const containers = Array.from(document.querySelectorAll('[data-testid="genui-root"], .sandboxed-ui'))
    if (containers.length > 0) {
      const el = containers[0] as HTMLElement
      return [{
        headHTML: '',
        bodyHTML: el.innerHTML.slice(0, 300),
        bodyText: (el.textContent || '').slice(0, 200),
        docAccess: true,
      }]
    }
    // Fallback: legacy iframe mode (kept for backward compat).
    const iframes = Array.from(document.querySelectorAll('iframe[title="GenUI Preview"]'))
    return iframes.map((f) => {
      let headHTML = ''
      let bodyHTML = ''
      let bodyText = ''
      let docAccess = true
      try {
        const doc = (f as HTMLIFrameElement).contentDocument
        if (doc) {
          headHTML = doc.head ? doc.head.innerHTML : ''
          bodyHTML = doc.body ? doc.body.innerHTML : ''
          bodyText = doc.body ? doc.body.textContent || '' : ''
        } else {
          docAccess = false
        }
      } catch {
        docAccess = false
      }
      return { headHTML: headHTML.slice(0, 120), bodyHTML: bodyHTML.slice(0, 300), bodyText: bodyText.slice(0, 200), docAccess }
    })
  })
}

test.describe('GenUI render', () => {
  test.beforeEach(() => { seqCounter = 0 })

  test('plain React GenUI renders visible DOM inside the iframe', async ({ browser }) => {
    const page = await browser.newPage()
    const errors: string[] = []
    page.on('console', (msg) => { if (msg.type() === 'error') errors.push(msg.text()) })
    page.on('pageerror', (err) => errors.push(String(err)))

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

    // Inject a simple GenUI via the streaming path (no XBOT_UI deps — pure React).
    const code = `export default function App() {
  const [n, setN] = useState(0)
  return (
    <div className="p-4" data-testid="genui-root">
      <h1>RENDER_OK_MARKER</h1>
      <p>count: {n}</p>
      <button onClick={() => setN(n + 1)}>inc</button>
    </div>
  )
}`
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: {
        phase: 'thinking', iteration: 0, seq: 1, turn_id: 1, chat_id: 'web:chat-1',
        stream_content: '',
        genui_content: code,
      },
    })
    await page.waitForTimeout(2500)

    const frames = await getIframeState(page)
    console.log('iframe state:', JSON.stringify(frames, null, 2))
    console.log('console errors:', JSON.stringify(errors))

    // There must be a GenUI rendered (inline mode — no iframe).
    expect(frames.length).toBeGreaterThan(0)
    const f = frames[0]
    // The compiled component must render visible DOM.
    expect(f.bodyText).toContain('RENDER_OK_MARKER')

    await page.close()
  })

  test('XBOT_UI GenUI (Icon + Button + Chart stub) renders without undefined-component errors', async ({ browser }) => {
    const page = await browser.newPage()
    const errors: string[] = []
    page.on('pageerror', (err) => errors.push(String(err)))

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

    // XBOT_UI is no longer injected (0-injection: only React + hooks).
    // The LLM now writes standard React/TSX + Tailwind. This test verifies
    // that plain React renders correctly (no XBOT_UI dependency).
    const code = `export default function App() {
  return (
    <div className="p-4">
      <button className="rounded bg-indigo-500 px-3 py-1 text-white">CLICK_ME_MARKER</button>
      <span className="ml-2 rounded bg-green-500 px-2 py-0.5 text-xs text-white">NEW</span>
      <div className="mt-2 text-sm text-gray-500">users: 42</div>
    </div>
  )
}`
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: {
        phase: 'thinking', iteration: 0, seq: 1, turn_id: 1, chat_id: 'web:chat-1',
        stream_content: '',
        genui_content: code,
      },
    })
    await page.waitForTimeout(2500)

    const frames = await getIframeState(page)
    console.log('XBOT_UI iframe state:', JSON.stringify(frames, null, 2))
    console.log('pageerrors:', JSON.stringify(errors))

    expect(frames.length).toBeGreaterThan(0)
    const f = frames[0]
    expect(f.bodyText).toContain('CLICK_ME_MARKER')
    expect(f.bodyText).toContain('NEW')
    // No "Element type is invalid" (undefined component) errors.
    const invalid = errors.filter((e) => e.includes('Element type is invalid'))
    expect(invalid, `undefined component errors: ${invalid.join(' | ')}`).toHaveLength(0)

    await page.close()
  })

  test('streaming render failure falls back to last-good WITHOUT #321 hook crash', async ({ browser }) => {
    const page = await browser.newPage()
    const errors: string[] = []
    page.on('pageerror', (err) => errors.push(String(err)))

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

    // ── Phase 1: a GOOD component that uses hooks (useState) → becomes lastGood ──
    const goodCode = `export default function App() {
  const [n, setN] = useState(0)
  return <div data-testid="hook-root"><h1>HOOK_GOOD_MARKER</h1><p>count: {n}</p></div>
}`
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: {
        phase: 'thinking', iteration: 0, seq: 1, turn_id: 1, chat_id: 'web:chat-1',
        stream_content: '',
        genui_content: goodCode,
      },
    })
    await page.waitForTimeout(1500)

    // ── Phase 2: a component that uses a hook AND throws at render time.
    //   On successful compile it OVERWRITES lastGood (current == lastGood == this
    //   component). Render throws → UIErrorBoundary goes hasError → streaming
    //   fallback renders lastGood. Before the fix it direct-called lastGood(props)
    //   INSIDE a class component's render() (no dispatcher) → the useState inside
    //   throws #321 "Invalid hook call" → escapes the (self-render) boundary → the
    //   ENTIRE app crashes (user report: "stream 期间整个界面出错"). After the fix
    //   it mounts lastGood via createElement (dispatcher set) → no #321. ──
    const throwingCode = `export default function App() {
  const [n, setN] = useState(0)
  return <div>{doesNotExist.map(function (x) { return x; }).join('')}</div>
}`
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: {
        phase: 'thinking', iteration: 0, seq: 2, turn_id: 1, chat_id: 'web:chat-1',
        stream_content: '',
        genui_content: throwingCode,
      },
    })
    await page.waitForTimeout(1500)

    const crashErrors = errors.filter(
      (e) => e.includes('#321') || e.includes('Invalid hook') || e.includes('hook') ,
    )
    console.log('pageerrors:', JSON.stringify(errors))
    console.log('crashErrors:', JSON.stringify(crashErrors))
    // The #321 crash would take down the ENTIRE app. Assert it did NOT happen.
    expect(crashErrors, `hook crash: ${crashErrors.join(' | ')}`).toHaveLength(0)

    await page.close()
  })
})
