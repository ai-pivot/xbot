import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E test: Mermaid diagrams render to SVG in the Web UI.
 *
 * Before this feature, ```mermaid code blocks were rendered as plain
 * syntax-highlighted source (no diagram). This test verifies that:
 *  1. A valid mermaid block renders an <svg> inside .mermaid-container
 *  2. An invalid mermaid block falls back to showing the source (not blank)
 *  3. Normal code blocks are unaffected (still syntax-highlighted, no svg)
 *  4. During streaming, mermaid shows source (no async render storm) — the
 *     performance optimization that keeps mermaid out of the hot typewriter path.
 *     When streaming ends, the diagram renders.
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

async function setupMock(page: Page, historyMessages: unknown[]) {
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
    json: { ok: true, data: { messages: historyMessages, chat_id: 'chat-1', last_seq: 0, active_progress: null } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

async function loginAndNavigate(page: Page) {
  await page.addInitScript(() => {
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
    const w = window as unknown as SSEMockState
    w.__sseListeners = listeners
    w.__sseSeq = 0
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
}

const VALID_MERMAID = 'graph TD\n    A[Start] --> B[Process]\n    B --> C{Decision}\n    C -->|Yes| D[Result 1]\n    C -->|No| E[Result 2]'
const INVALID_MERMAID = 'this is not valid mermaid syntax !!!\n    %%&&&'

test.describe('Mermaid diagram rendering', () => {
  test.beforeEach(() => { seqCounter = 0 })

  test('valid mermaid block renders an SVG diagram', async ({ page }) => {
    const messages = [{
      role: 'assistant',
      content: 'Here is a flowchart:\n\n```mermaid\n' + VALID_MERMAID + '\n```',
      seq: 1,
      turn_id: 1,
      timestamp: new Date().toISOString(),
    }]
    await setupMock(page, messages)
    await loginAndNavigate(page)

    const container = page.locator('.mermaid-container')
    await expect(container).toBeVisible({ timeout: 15_000 })
    // Use :not(.lucide) to exclude the CopyButton icon SVG.
    await expect(container.locator('svg:not(.lucide)')).toBeVisible({ timeout: 10_000 })
  })

  test('fullscreen button opens overlay and Esc closes it', async ({ page }) => {
    const messages = [{
      role: 'assistant',
      content: '```mermaid\n' + VALID_MERMAID + '\n```',
      seq: 1,
      turn_id: 1,
      timestamp: new Date().toISOString(),
    }]
    await setupMock(page, messages)
    await loginAndNavigate(page)

    // Wait for the diagram to render.
    const container = page.locator('.mermaid-container')
    await expect(container.locator('svg:not(.lucide)')).toBeVisible({ timeout: 15_000 })

    // Click the fullscreen button.
    await page.locator('button[aria-label="Fullscreen diagram"]').click()

    // The fullscreen overlay should appear (portal to document.body, fixed inset-0).
    const overlay = page.locator('.fixed.inset-0')
    await expect(overlay).toBeVisible({ timeout: 5_000 })
    // The overlay should contain a copy of the SVG.
    await expect(overlay.locator('svg:not(.lucide)')).toBeVisible({ timeout: 5_000 })

    // Press Esc to close.
    await page.keyboard.press('Escape')
    await expect(overlay).toHaveCount(0)
  })

  test('invalid mermaid falls back to source display, not blank', async ({ page }) => {
    const messages = [{
      role: 'assistant',
      content: 'Bad diagram:\n\n```mermaid\n' + INVALID_MERMAID + '\n```',
      seq: 1,
      turn_id: 1,
      timestamp: new Date().toISOString(),
    }]
    await setupMock(page, messages)
    await loginAndNavigate(page)

    // On error, the fallback shows the source in a <pre><code> with the mermaid label.
    // It should NOT render an SVG.
    const mermaidLabel = page.locator('text=mermaid').first()
    await expect(mermaidLabel).toBeVisible({ timeout: 15_000 })
    await expect(page.locator('.mermaid-container svg:not(.lucide)')).toHaveCount(0)
  })

  test('normal code blocks are unaffected (no svg, syntax highlighted)', async ({ page }) => {
    const messages = [{
      role: 'assistant',
      content: 'Some code:\n\n```go\nfunc main() {\n    fmt.Println("hello")\n}\n```',
      seq: 1,
      turn_id: 1,
      timestamp: new Date().toISOString(),
    }]
    await setupMock(page, messages)
    await loginAndNavigate(page)

    // A normal code block should have the GO label, no mermaid-container, no svg.
    await expect(page.locator('text=GO').first()).toBeVisible({ timeout: 10_000 })
    await expect(page.locator('.mermaid-container')).toHaveCount(0)
  })

  test('during streaming mermaid shows source (no async render), then renders on settle', async ({ page }) => {
    // Start with empty history; the mermaid content arrives via streaming.
    await setupMock(page, [])
    await loginAndNavigate(page)

    // ── Phase 1: streaming — mermaid source is incomplete ──
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'stream_content', {
      type: 'stream_content',
      progress: { stream_content: '```mermaid\n' + VALID_MERMAID + '\n```', chat_id: 'web:chat-1', streaming: true },
    })

    // During streaming: the mermaid label is visible (source block), but NO svg.
    await expect(page.locator('text=mermaid').first()).toBeVisible({ timeout: 10_000 })
    await expect(page.locator('.mermaid-container svg')).toHaveCount(0)

    // ── Phase 2: streaming ends — history reload delivers the committed message ──
    // Override the history route to return the complete mermaid message.
    await page.route('**/api/history', (r) => r.fulfill({
      json: { ok: true, data: {
        messages: [{
          role: 'assistant',
          content: '```mermaid\n' + VALID_MERMAID + '\n```',
          seq: 1,
          turn_id: 1,
          timestamp: new Date().toISOString(),
        }],
        chat_id: 'chat-1',
        last_seq: 1,
        active_progress: null,
      } },
    }), { times: 1 })

    await emitSSE(page, 'session', { type: 'session', session: { action: 'idle', chat_id: 'chat-1', channel: 'web' } })

    // After streaming ends: the SVG diagram should render.
    const container = page.locator('.mermaid-container')
    await expect(container.locator('svg:not(.lucide)')).toBeVisible({ timeout: 15_000 })
  })
})
