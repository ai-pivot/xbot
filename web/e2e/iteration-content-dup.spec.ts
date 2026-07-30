import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E test for content/tool duplication when hydrating from active_progress
 * at the iteration boundary.
 *
 * Bug: when GetActiveProgress is called after snapshotCompletedIteration but
 * before prepareForIteration, the snapshot carries the PREVIOUS iteration's
 * Content and CompletedTools. historyProgressToLive hydrates the store with
 * these stale values, and LiveIteration renders them as if they belong to the
 * current (in-flight) iteration — duplicating content and tools.
 *
 * Expected: "content 1" and "tool 1" from iteration 1 should NOT appear in the
 * live (streaming) iteration 2.
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

async function setupMock(page: Page, activeProgress: Record<string, unknown> | null) {
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
    json: { ok: true, data: {
      messages: [
        { role: 'user', content: 'do something', seq: 1, turn_id: 1 },
        { role: 'assistant', content: '', seq: 2, turn_id: 1, detail: JSON.stringify([
          { iteration: 1, content: 'content 1', reasoning: 'reason 1', tools: [{ name: 'Read', label: 'Read file.go', status: 'done', iteration: 1 }] },
        ]) },
      ],
      chat_id: 'chat-1',
      last_seq: 0,
      active_progress: activeProgress,
    } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

test.describe('Iteration content/tool duplication on hydrate', () => {
  test.beforeEach(() => { seqCounter = 0 })

  test('content and tools from completed iteration are NOT duplicated in live iteration', async ({ browser }) => {
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

    // ── Mock active_progress at the iteration boundary ──
    // The snapshot has Content="content 1" and CompletedTools=[Read] from
    // iteration 1 (captured after snapshotCompletedIteration, before
    // prepareForIteration). iterationHistory is empty (delta not yet recorded).
    const boundaryProgress = {
      phase: 'tool_exec',
      iteration: 1,
      seq: 5,
      turn_id: 1,
      content: 'content 1',
      active_tools: [],
      completed_tools: [{ name: 'Read', label: 'Read file.go', status: 'done', iteration: 1, summary: 'ok' }],
      iteration_history: [],
    }

    await setupMock(page, boundaryProgress)
    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()
    await page.waitForTimeout(2000)

    // ── Emit SSE events for iteration 2 (thinking phase, streaming reasoning) ──
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'thinking', iteration: 2, seq: 6, turn_id: 1, chat_id: 'web:chat-1',
        iteration_history: [{ iteration: 1, content: 'content 1', reasoning: 'reason 1',
          completed_tools: [{ name: 'Read', label: 'Read file.go', status: 'done', iteration: 1 }] }] },
    })
    await emitSSE(page, 'stream_content', {
      type: 'stream_content',
      progress: { reasoning_stream_content: 'reason 2', chat_id: 'web:chat-1', streaming: true },
    })
    await page.waitForTimeout(500)

    // ── Count occurrences of "content 1" ──
    // It should appear ONCE (in iterationHistory via TurnBody), NOT in the live iteration.
    const content1Count = await page.evaluate(() => {
      let count = 0
      const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
      while (walker.nextNode()) {
        const text = walker.currentNode.textContent?.trim() || ''
        // Match "content 1" as a standalone text node (not inside a tool summary)
        if (text === 'content 1') count++
      }
      return count
    })

    // ── Count occurrences of "Read" tool label ──
    // It should appear ONCE (in iterationHistory via TurnBody), NOT in the live iteration.
    const readCount = await page.evaluate(() => {
      let count = 0
      const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
      while (walker.nextNode()) {
        const text = walker.currentNode.textContent?.trim() || ''
        if (text === 'Read') count++
      }
      return count
    })

    console.log('content 1 count:', content1Count, 'Read count:', readCount)

    // "content 1" should appear exactly once (in iterationHistory)
    expect(content1Count).toBe(1)
    // "Read" should appear exactly once (in iterationHistory)
    expect(readCount).toBe(1)

    await page.close()
  })
})
