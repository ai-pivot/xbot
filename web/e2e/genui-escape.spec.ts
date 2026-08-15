import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E test: GenUI rendering isolation (content stays inside the iframe).
 *
 * Security model (aligned with the production-proven SandboxedUI):
 *   - The component function is compiled via `new Function` and executed in
 *     the PARENT page (React + hooks injected) — LLM code has parent-page
 *     privileges by design; `window.parent === window` there, so "block
 *     parent access" is meaningless for component code.
 *   - The iframe (`allow-scripts allow-same-origin`, required for
 *     contentDocument access — the long-lived "blank GenUI iframe" bug was
 *     caused by its omission) isolates RENDERED OUTPUT: everything React
 *     mounts lands inside the iframe document.
 *
 * This test guards the rendering isolation: GenUI content must appear ONLY
 * inside the iframe — never in the parent document (no layout escape, no
 * overlay injection into the host page).
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

    // ── Inject GenUI code — rendered output must stay INSIDE the iframe ──
    // 组件代码在父页面编译执行（架构既定，与 SandboxedUI 一致）；本测试
    // 守护渲染隔离：React 挂载的全部 DOM 落在 iframe 文档内，父文档零污染。
    const code = `
export default function App() {
  return React.createElement('div', { 'data-testid': 'genui-root' },
    React.createElement('h1', null, 'GENUI_CONTENT_MARKER'),
    React.createElement('p', null, 'Safe content'));
};
`

    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: {
        phase: 'thinking', iteration: 0, seq: 1, turn_id: 1, chat_id: 'web:chat-1',
        stream_content: '',
        genui_content: code,
      },
    })
    await page.waitForTimeout(1500)

    // ── Verify: content rendered inside the iframe; parent document clean ──
    const result = await page.evaluate(() => {
      const iframe = document.querySelector('iframe[title="GenUI Preview"]') as HTMLIFrameElement | null
      let iframeHasMarker = false
      let iframeHasRoot = false
      try {
        const doc = iframe?.contentDocument
        iframeHasMarker = !!doc && doc.body.innerText.includes('GENUI_CONTENT_MARKER')
        iframeHasRoot = !!doc?.querySelector('[data-testid="genui-root"]')
      } catch { /* inaccessible */ }
      return {
        iframeHasMarker,
        iframeHasRoot,
        parentHasMarker: document.body.innerText.includes('GENUI_CONTENT_MARKER'),
        parentHasGenuiRoot: !!document.querySelector('[data-testid="genui-root"]'),
        parentHasEscapedOverlay: !!document.getElementById('escaped-content'),
      }
    })
    console.log('Isolation result:', JSON.stringify(result))

    // Rendered inside the iframe (the render pipeline works)…
    expect(result.iframeHasMarker).toBe(true)
    expect(result.iframeHasRoot).toBe(true)
    // …and ONLY inside the iframe — the parent document stays clean.
    expect(result.parentHasMarker).toBe(false)
    expect(result.parentHasGenuiRoot).toBe(false)
    expect(result.parentHasEscapedOverlay).toBe(false)

    await page.close()
  })
})
