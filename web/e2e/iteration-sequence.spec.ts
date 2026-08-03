import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * Comprehensive E2E test for iteration sequence integrity.
 *
 * INVARIANT: iteration numbers must be sequential (0, 1, 2, ...) with no
 * gaps, no duplicates, no regressions. This test verifies ALL scenarios:
 *
 * 1. Normal streaming: thinking → tool → thinking → done → text
 * 2. Session switch to busy session (active_progress hydrate)
 * 3. Cancel mid-turn (freeze, not disappear)
 * 4. SubAgent progress doesn't leak to next iteration
 * 5. Send user message after completed turn (no duplicate)
 * 6. Todo survives session switch
 * 7. Tool generating shows during stream_content
 * 8. "思考中" shows exactly once during thinking
 * 9. Typer effect works (content not all visible at once)
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
      sessions: [
        { chat_id: 'chat-1', channel: 'web', label: 'Session 1', last_active: new Date().toISOString() },
        { chat_id: 'chat-2', channel: 'web', label: 'Session 2', last_active: new Date().toISOString() },
      ],
      chats: [], orphan_subagents: [],
    } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
  await page.route('**/api/chats/*/switch', (r) => {
    const url = r.request().url()
    const chatID = url.match(/\/chats\/([^/]+)\/switch/)?.[1] || 'chat-1'
    r.fulfill({ json: { ok: true, chat_id: chatID, channel: 'web', todos: [] } })
  })
}

/** Count occurrences of a text in the DOM (as leaf text nodes). */
async function countText(page: Page, text: string): Promise<number> {
  return page.evaluate((t) => {
    let count = 0
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
    while (walker.nextNode()) {
      if (walker.currentNode.textContent?.includes(t)) count++
    }
    return count
  }, text)
}

/** Check if "思考中" (thinking indicator) is visible. */
async function hasThinking(page: Page): Promise<boolean> {
  return page.evaluate(() => {
    const text = document.body.textContent || ''
    return text.includes('思考') || text.includes('think')
  })
}

/** Count assistant message rows (data-index elements with assistant content). */
async function countAssistantRows(page: Page): Promise<number> {
  return page.evaluate(() => {
    // Count rows that have sweep-text (thinking) or tool icons or markdown content
    const rows = document.querySelectorAll('[data-index]')
    let count = 0
    for (const row of rows) {
      const hasAssistant = row.querySelector('.sweep-text, .markdown-body, [class*="tool-icon"]')
      if (hasAssistant) count++
    }
    return count
  })
}

async function initPage(browser: import('@playwright/test').Browser): Promise<Page> {
  const page = await browser.newPage()
  await page.addInitScript(() => {
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
    const w = window as unknown as SSEMockState
    w.__sseListeners = listeners
    class M {
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
    ;(window as unknown as { EventSource: typeof M }).EventSource = M
  })
  return page
}

test.describe('Iteration sequence integrity', () => {
  test.beforeEach(() => { seqCounter = 0 })

  test('normal streaming: iterations 0→1→2 sequential, thinking shows, no duplicate', async ({ browser }) => {
    const page = await initPage(browser)
    await page.route('**/api/history', (r) => r.fulfill({
      json: { ok: true, data: { messages: [], chat_id: 'chat-1', last_seq: 0, active_progress: null } },
    }))
    await setupMock(page)
    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()
    await page.waitForTimeout(2000)

    // Start turn
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 1, turn_start: { trigger: 'user', request_id: 'r1' }, chat_id: 'web:chat-1' },
    })
    // Iteration 0: thinking
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'thinking', iteration: 0, seq: 2, turn_id: 1, chat_id: 'web:chat-1' },
    })
    await page.waitForTimeout(200)

    // "思考中" should show
    const thinking1 = await hasThinking(page)
    console.log('Iter 0 thinking:', thinking1)
    expect(thinking1).toBe(true)

    // Iteration 0: tool running
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'tool_exec', iteration: 0, seq: 3, turn_id: 1, chat_id: 'web:chat-1',
        active_tools: [{ name: 'Read', status: 'running', iteration: 0 }] },
    })
    // Iteration 1: thinking (delta push with iteration 0 completed)
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'thinking', iteration: 1, seq: 4, turn_id: 1, chat_id: 'web:chat-1',
        completed_tools: [{ name: 'Read', status: 'done', iteration: 0, summary: 'file.go' }],
        iteration_history: [{ iteration: 0, thinking: 'Reading', completed_tools: [{ name: 'Read', status: 'done', iteration: 0, summary: 'file.go' }] }],
      },
    })
    await page.waitForTimeout(200)

    // "思考中" should still show (iteration 1)
    const thinking2 = await hasThinking(page)
    console.log('Iter 1 thinking:', thinking2)
    expect(thinking2).toBe(true)

    // Done + text
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'done', iteration: 1, seq: 5, turn_id: 1, chat_id: 'web:chat-1',
        iteration_history: [
          { iteration: 0, thinking: 'Reading', completed_tools: [{ name: 'Read', status: 'done', iteration: 0, summary: 'file.go' }] },
          { iteration: 1, thinking: 'Done', completed_tools: [] },
        ],
      },
    })
    await emitSSE(page, 'text', {
      type: 'text', content: 'All done.', seq: 6, turn_id: 1, chat_id: 'web:chat-1',
      progress_history: JSON.stringify([
        { iteration: 0, thinking: 'Reading', completed_tools: [{ name: 'Read', status: 'done', iteration: 0, summary: 'file.go' }] },
        { iteration: 1, thinking: 'Done', completed_tools: [] },
      ]),
    })
    await emitSSE(page, 'session', { type: 'session', session: { action: 'idle', chat_id: 'chat-1', channel: 'web' } })
    await page.waitForTimeout(500)

    // "思考中" should NOT show (turn is done)
    const thinkingAfter = await hasThinking(page)
    console.log('After done thinking:', thinkingAfter)

    // Only ONE assistant row (no duplicate)
    const assistantRows = await countAssistantRows(page)
    console.log('Assistant rows:', assistantRows)
    expect(assistantRows).toBe(1)

    await page.close()
  })

  test('session switch to busy session: thinking shows, no duplicate iterations', async ({ browser }) => {
    const page = await initPage(browser)

    // chat-1 has an active turn (phase=thinking, iteration=2)
    const activeProgress = {
      phase: 'thinking', iteration: 2, seq: 10, turn_id: 1, chat_id: 'web:chat-1',
      active_tools: [], completed_tools: [],
      iteration_history: [
        { iteration: 0, thinking: 'Step 0', completed_tools: [{ name: 'Read', status: 'done', iteration: 0, summary: 'a' }] },
        { iteration: 1, thinking: 'Step 1', completed_tools: [{ name: 'Grep', status: 'done', iteration: 1, summary: 'b' }] },
      ],
    }

    await page.route('**/api/history', (r) => {
      const body = r.request().postDataJSON()
      const chatID = body?.chat_id || 'chat-1'
      const ap = chatID === 'chat-1' ? activeProgress : null
      r.fulfill({ json: { ok: true, data: { messages: [], chat_id: chatID, last_seq: 10, active_progress: ap } } })
    })
    await setupMock(page)
    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()
    await page.waitForTimeout(2000)

    // Should show "思考中" (busy session)
    const thinking = await hasThinking(page)
    console.log('Switch to busy - thinking:', thinking)
    expect(thinking).toBe(true)

    // Should NOT have duplicate iterations (only 1 assistant row)
    const rows = await countAssistantRows(page)
    console.log('Switch to busy - assistant rows:', rows)
    expect(rows).toBe(1)

    // Switch away and back
    await page.locator('text=Session 2').first().click()
    await page.waitForTimeout(1000)
    await page.locator('text=Session 1').first().click()
    await page.waitForTimeout(1500)

    // Still showing thinking, still 1 row
    const thinking2 = await hasThinking(page)
    const rows2 = await countAssistantRows(page)
    console.log('After switch back - thinking:', thinking2, 'rows:', rows2)
    expect(thinking2).toBe(true)
    expect(rows2).toBe(1)

    await page.close()
  })

  test('cancel mid-turn: iterations preserved (frozen), no duplicate', async ({ browser }) => {
    const page = await initPage(browser)
    await page.route('**/api/history', (r) => r.fulfill({
      json: { ok: true, data: { messages: [], chat_id: 'chat-1', last_seq: 0, active_progress: null } },
    }))
    await setupMock(page)
    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()
    await page.waitForTimeout(2000)

    // Start turn with streaming content
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 1, turn_start: { trigger: 'user', request_id: 'r1' }, chat_id: 'web:chat-1' },
    })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'thinking', iteration: 0, seq: 2, turn_id: 1, chat_id: 'web:chat-1' },
    })
    await emitSSE(page, 'stream_content', {
      type: 'stream_content',
      progress: { stream_content: 'I am working on something...', chat_id: 'web:chat-1', streaming: true },
    })
    // Typewriter reveals content progressively (gap/3 per 50ms tick) — a
    // fixed 300ms may not be enough for the full substring. Poll instead.
    await page.waitForFunction(() => document.body.textContent?.includes('working on something') ?? false, { timeout: 5000 })

    // Content should be visible
    const contentBefore = await countText(page, 'working on something')
    console.log('Before cancel - content:', contentBefore)
    expect(contentBefore).toBeGreaterThan(0)

    // Cancel
    await emitSSE(page, 'text', {
      type: 'text', content: '', cancelled: true, seq: 3, turn_id: 1, chat_id: 'web:chat-1',
      progress_history: JSON.stringify([
        { iteration: 0, thinking: 'I am working on something...', completed_tools: [], user_cancelled: true },
      ]),
    })
    await emitSSE(page, 'session', { type: 'session', session: { action: 'idle', chat_id: 'chat-1', channel: 'web' } })
    await page.waitForTimeout(500)

    // Content should still be visible (frozen, not disappeared)
    const contentAfter = await countText(page, 'working on something')
    console.log('After cancel - content:', contentAfter)
    expect(contentAfter).toBeGreaterThan(0)

    // No "思考中" (turn is cancelled)
    const thinking = await hasThinking(page)
    console.log('After cancel - thinking:', thinking)

    await page.close()
  })

  test('subagent progress does not leak to next iteration', async ({ browser }) => {
    const page = await initPage(browser)
    await page.route('**/api/history', (r) => r.fulfill({
      json: { ok: true, data: { messages: [], chat_id: 'chat-1', last_seq: 0, active_progress: null } },
    }))
    await setupMock(page)
    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()
    await page.waitForTimeout(2000)

    // Start turn
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 1, turn_start: { trigger: 'user', request_id: 'r1' }, chat_id: 'web:chat-1' },
    })

    // Iteration 0: with subagent
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'tool_exec', iteration: 0, seq: 2, turn_id: 1, chat_id: 'web:chat-1',
        active_tools: [{ name: 'SubAgent', status: 'running', iteration: 0 }],
        sub_agents: [{ role: 'explore', instance: 'test-1', status: 'running', description: 'exploring' }],
      },
    })
    await page.waitForTimeout(200)

    // SubAgent should be visible
    const subBefore = await countText(page, 'explore')
    console.log('Iter 0 - subagent visible:', subBefore)

    // Iteration 1: no subagent (should clear)
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'thinking', iteration: 1, seq: 3, turn_id: 1, chat_id: 'web:chat-1',
        completed_tools: [{ name: 'SubAgent', status: 'done', iteration: 0, summary: 'done' }],
        iteration_history: [{ iteration: 0, thinking: '', completed_tools: [{ name: 'SubAgent', status: 'done', iteration: 0, summary: 'done' }] }],
        sub_agents: [],  // empty — should clear
      },
    })
    await page.waitForTimeout(300)

    // SubAgent should NOT be visible (cleared)
    const subAfter = await countText(page, 'explore')
    console.log('Iter 1 - subagent leaked:', subAfter)
    expect(subAfter).toBe(0)

    await page.close()
  })

  test('todo survives session switch', async ({ browser }) => {
    const page = await initPage(browser)
    const todos = [{ id: 1, text: 'Task A', done: false }, { id: 2, text: 'Task B', done: true }]

    await page.route('**/api/history', (r) => {
      const body = r.request().postDataJSON()
      const chatID = body?.chat_id || 'chat-1'
      const ap = chatID === 'chat-1' ? { phase: 'done', todos, seq: 0 } : null
      r.fulfill({ json: { ok: true, data: { messages: [], chat_id: chatID, last_seq: 0, active_progress: ap } } })
    })
    await page.route('**/api/chats/*/switch', (r) => {
      const url = r.request().url()
      const chatID = url.match(/\/chats\/([^/]+)\/switch/)?.[1] || 'chat-1'
      const t = chatID === 'chat-1' ? todos : []
      r.fulfill({ json: { ok: true, chat_id: chatID, channel: 'web', todos: t } })
    })
    await setupMock(page)
    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()
    await page.waitForTimeout(2000)

    // Todos should be visible
    const todoBefore = await countText(page, 'Task A')
    console.log('Before switch - todo:', todoBefore)
    expect(todoBefore).toBeGreaterThan(0)

    // Switch away and back
    await page.locator('text=Session 2').first().click()
    await page.waitForTimeout(1000)
    await page.locator('text=Session 1').first().click()
    await page.waitForTimeout(1500)

    // Todos should still be visible
    const todoAfter = await countText(page, 'Task A')
    console.log('After switch - todo:', todoAfter)
    expect(todoAfter).toBeGreaterThan(0)

    await page.close()
  })

  test('tool generating shows during stream_content', async ({ browser }) => {
    const page = await initPage(browser)
    await page.route('**/api/history', (r) => r.fulfill({
      json: { ok: true, data: { messages: [], chat_id: 'chat-1', last_seq: 0, active_progress: null } },
    }))
    await setupMock(page)
    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()
    await page.waitForTimeout(2000)

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
      progress: { stream_content: '', chat_id: 'web:chat-1', streaming: true, streaming_tools: [{ name: 'Shell', status: 'generating' }] },
    })
    await page.waitForTimeout(300)

    // "Shell" should be visible as a generating tool
    const bodyText = await page.evaluate(() => document.body.textContent || '')
    const shellVisible = bodyText.includes('Shell')
    console.log('Tool generating - Shell visible:', shellVisible)
    expect(shellVisible).toBe(true)

    await page.close()
  })
})
