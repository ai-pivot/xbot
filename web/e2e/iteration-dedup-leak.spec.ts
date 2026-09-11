import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E regression tests for two iteration rendering bugs:
 *
 * Bug 1 — Iteration doubling:
 *   On the web channel (batchProgressByIteration=true), snapshotCompletedIteration
 *   step 4 sends a structured event with BOTH completed_tools (Iteration=0,
 *   omitted by omitempty → undefined) AND iteration_history (same tools).
 *   LiveIteration's !t.iteration filter passes undefined → tools render twice.
 *
 * Bug 2 — Cross-turn iteration leak after cancel:
 *   After PhaseDone, session(idle) doesn't reset the store (waiting for cancel
 *   ack). If turn_started is lost (SSE gap), the new turn's structured events
 *   append to the old turn's iterationHistory → assistant2 shows assistant1's tools.
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

/** Count how many times a tool name appears as a leaf text node. */
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

async function login(page: Page) {
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
}

test.describe('Iteration dedup and cross-turn leak', () => {
  test.beforeEach(() => { seqCounter = 0 })

  test('Bug 1: completed_tools + iteration_history in same event does NOT double tools', async ({ browser }) => {
    const page = await browser.newPage()
    await login(page)

    // Turn 1, iteration 0: tools TodoWrite, Read, Grep
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 1, turn_start: { trigger: 'user', request_id: 'r1' }, chat_id: 'web:chat-1' },
    })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'thinking', iteration: 0, seq: 2, turn_id: 1, chat_id: 'web:chat-1' },
    })

    // Tools complete (active → completed)
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: {
        phase: 'tool_exec', iteration: 0, seq: 3, turn_id: 1, chat_id: 'web:chat-1',
        active_tools: [
          { name: 'TodoWrite', status: 'done', iteration: 0 },
          { name: 'Read', status: 'done', iteration: 0 },
          { name: 'Grep', status: 'done', iteration: 0 },
        ],
        completed_tools: [
          { name: 'TodoWrite', status: 'done', iteration: 0, summary: 'wrote todos' },
          { name: 'Read', status: 'done', iteration: 0, summary: 'main.go' },
          { name: 'Grep', status: 'done', iteration: 0, summary: 'found 3' },
        ],
      },
    })
    await page.waitForTimeout(200)

    // ── THE BUG: snapshotCompletedIteration step 4 event ──
    // Sends BOTH completed_tools (Iteration=0, omitempty→undefined) AND
    // iteration_history (the completed iteration with same tools).
    // Without the fix, LiveIteration renders completed_tools (undefined passes
    // !t.iteration) ALONGSIDE iterationHistory → 6 tools instead of 3.
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: {
        phase: 'tool_exec', iteration: 0, seq: 4, turn_id: 1, chat_id: 'web:chat-1',
        completed_tools: [
          { name: 'TodoWrite', status: 'done', iteration: 0, summary: 'wrote todos' },
          { name: 'Read', status: 'done', iteration: 0, summary: 'main.go' },
          { name: 'Grep', status: 'done', iteration: 0, summary: 'found 3' },
        ],
        iteration_history: [
          { iteration: 0, thinking: '', completed_tools: [
            { name: 'TodoWrite', status: 'done', summary: 'wrote todos' },
            { name: 'Read', status: 'done', summary: 'main.go' },
            { name: 'Grep', status: 'done', summary: 'found 3' },
          ] },
        ],
      },
    })
    await page.waitForTimeout(300)

    // Each tool should appear EXACTLY once (not doubled)
    const todoCount = await countToolLabels(page, 'TodoWrite')
    const readCount = await countToolLabels(page, 'Read')
    const grepCount = await countToolLabels(page, 'Grep')

    console.log('Tool counts:', { TodoWrite: todoCount, Read: readCount, Grep: grepCount })
    expect(todoCount).toBe(1)
    expect(readCount).toBe(1)
    expect(grepCount).toBe(1)
  })

  test('Bug 2: after cancel, new turn does NOT show old turn iterations', async ({ browser }) => {
    const page = await browser.newPage()
    await login(page)

    // ── Turn 1: iteration 0 with tools Read, Grep ──
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 1, turn_start: { trigger: 'user', request_id: 'r1' }, chat_id: 'web:chat-1' },
    })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'thinking', iteration: 0, seq: 2, turn_id: 1, chat_id: 'web:chat-1' },
    })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: {
        phase: 'tool_exec', iteration: 0, seq: 3, turn_id: 1, chat_id: 'web:chat-1',
        active_tools: [
          { name: 'Read', status: 'done', iteration: 0 },
          { name: 'Grep', status: 'done', iteration: 0 },
        ],
        completed_tools: [
          { name: 'Read', status: 'done', iteration: 0, summary: 'main.go' },
          { name: 'Grep', status: 'done', iteration: 0, summary: 'found 3' },
        ],
      },
    })
    await page.waitForTimeout(200)

    // PhaseDone (turn 1 ends)
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'done', iteration: 0, seq: 4, turn_id: 1, chat_id: 'web:chat-1' },
    })
    await page.waitForTimeout(100)

    // ── Cancel ack ──
    await emitSSE(page, 'text', {
      type: 'text',
      cancelled: true,
      content: '',
      seq: 5,
      turn_id: 1,
      chat_id: 'web:chat-1',
      progress_history: JSON.stringify([
        { iteration: 0, thinking: '', completed_tools: [
          { name: 'Read', status: 'done', summary: 'main.go' },
          { name: 'Grep', status: 'done', summary: 'found 3' },
        ] },
      ]),
    })
    await page.waitForTimeout(300)

    // session(idle) — arrives after cancel ack
    await emitSSE(page, 'session', { type: 'session', session: { action: 'idle', chat_id: 'chat-1', channel: 'web' } })
    await page.waitForTimeout(100)

    // ── Turn 2: DIFFERENT tools (Shell, Write) ──
    // NO turn_started (simulating SSE gap) — only session(busy)
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })

    // First structured event for turn 2 has turn_id=2 (different from turn 1's turn_id=1)
    // The TurnID guard should detect the change and reset iterationHistory
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: {
        phase: 'thinking', iteration: 0, seq: 7, turn_id: 2, chat_id: 'web:chat-1',
      },
    })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: {
        phase: 'tool_exec', iteration: 0, seq: 8, turn_id: 2, chat_id: 'web:chat-1',
        active_tools: [
          { name: 'Shell', status: 'done', iteration: 0 },
          { name: 'Write', status: 'done', iteration: 0 },
        ],
        completed_tools: [
          { name: 'Shell', status: 'done', iteration: 0, summary: 'ran command' },
          { name: 'Write', status: 'done', iteration: 0, summary: 'wrote file' },
        ],
      },
    })
    await page.waitForTimeout(300)

    // Turn 2 should show Shell and Write — NOT Read and Grep (turn 1's tools)
    const shellCount = await countToolLabels(page, 'Shell')
    const writeCount = await countToolLabels(page, 'Write')
    const readCount = await countToolLabels(page, 'Read')
    const grepCount = await countToolLabels(page, 'Grep')

    console.log('Turn 2 tool counts:', { Shell: shellCount, Write: writeCount, Read: readCount, Grep: grepCount })
    expect(shellCount).toBe(1)
    expect(writeCount).toBe(1)
    // Old turn's tools should NOT appear in the live message
    expect(readCount).toBe(0)
    expect(grepCount).toBe(0)
  })
})
