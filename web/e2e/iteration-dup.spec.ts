import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E test for iteration duplication after PhaseDone.
 *
 * Bug: when a tool is "generating" (streaming_tools) and the turn ends
 * (PhaseDone), a late stream_content arrives and re-sets streamingTools.
 * The same iteration now renders twice — once from iterationHistory (tool
 * "done") and once from streamingTools (tool "generating").
 *
 * Root cause: stream_content reset phaseDoneRef to false after PhaseDone,
 * re-opening the store and re-displaying stale generating tools.
 *
 * Fix: stream_content returns early when phaseDoneRef is true (PhaseDone
 * already fired, turn is ending).
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

/** Count how many times a tool name appears as a leaf text node (tool label, not prose). */
async function countToolLabels(page: Page, toolName: string): Promise<number> {
  return page.evaluate((name) => {
    let count = 0
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
    while (walker.nextNode()) {
      const text = walker.currentNode.textContent?.trim() || ''
      if (text === name) count++
    }
    return count
  }, toolName)
}

test.describe('Iteration duplication', () => {
  test.beforeEach(() => { seqCounter = 0 })

  test('late stream_content after PhaseDone does NOT re-create generating tool', async ({ browser }) => {
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

    // ── Turn 1: iteration 0 with a tool ──
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 1, turn_start: { trigger: 'user', request_id: 'r1' }, chat_id: 'web:chat-1' },
    })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'thinking', iteration: 0, seq: 2, turn_id: 1, chat_id: 'web:chat-1' },
    })
    // Tool generating
    await emitSSE(page, 'stream_content', {
      type: 'stream_content',
      progress: { stream_content: 'Let me read the file.', chat_id: 'web:chat-1', streaming: true, streaming_tools: [{ name: 'Read', status: 'generating' }] },
    })
    await page.waitForTimeout(200)
    // Tool running → done
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'tool_exec', iteration: 0, seq: 3, turn_id: 1, chat_id: 'web:chat-1', active_tools: [{ name: 'Read', status: 'running', iteration: 0 }] },
    })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'tool_exec', iteration: 0, seq: 4, turn_id: 1, chat_id: 'web:chat-1', active_tools: [{ name: 'Read', status: 'done', iteration: 0 }], completed_tools: [{ name: 'Read', status: 'done', iteration: 0, summary: 'main.go' }] },
    })
    await page.waitForTimeout(200)

    // Verify "Read" appears as a tool
    const readBefore = await countToolLabels(page, 'Read')
    console.log('Before PhaseDone - Read count:', readBefore)
    expect(readBefore).toBeGreaterThanOrEqual(1)

    // ── PhaseDone: turn ends ──
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'done', iteration: 0, seq: 5, turn_id: 1, chat_id: 'web:chat-1' },
    })
    await page.waitForTimeout(200)

    // ── text event: commits the message → store.reset() ──
    await emitSSE(page, 'text', {
      type: 'text',
      content: 'I read the file.',
      seq: 6,
      turn_id: 1,
      chat_id: 'web:chat-1',
      progress_history: JSON.stringify([
        { iteration: 0, thinking: 'Let me read the file.', completed_tools: [{ name: 'Read', status: 'done', iteration: 0, summary: 'main.go' }] },
      ]),
    })
    await page.waitForTimeout(300)

    // Verify live message is gone (store was reset by text event)
    const hasLiveAfterText = await page.evaluate(() =>
      document.querySelector('[id*="live-"]') !== null)
    console.log('After text event - live message exists:', hasLiveAfterText)
    expect(hasLiveAfterText).toBe(false)

    // ── Late stream_content arrives (race: stream callback fires once more) ──
    // Without the fix, this re-opens the store and creates a liveMessage
    // that duplicates the committed message's iteration.
    await emitSSE(page, 'stream_content', {
      type: 'stream_content',
      progress: { stream_content: 'Let me read the file.', chat_id: 'web:chat-1', streaming: true, streaming_tools: [{ name: 'Read', status: 'generating' }] },
    })
    await page.waitForTimeout(500)

    // ── Verify: NO live message should exist (turn is over) ──
    const hasLiveAfterLate = await page.evaluate(() =>
      document.querySelector('[id*="live-"]') !== null)
    console.log('After late stream_content - live message exists:', hasLiveAfterLate)

    // THE BUG: late stream_content re-opens the store, creating a duplicate
    // liveMessage alongside the committed message
    expect(hasLiveAfterLate).toBe(false)

    await page.close()
  })
})
