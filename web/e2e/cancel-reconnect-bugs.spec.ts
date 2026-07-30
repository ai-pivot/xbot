import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

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
      sessions: [{ chat_id: 'chat-1', channel: 'web', label: 'S1', last_active: new Date().toISOString() }],
      chats: [], orphan_subagents: [],
    } },
  }))
  await page.route('**/api/history*', (r) => r.fulfill({
    json: { ok: true, data: { messages: [], chat_id: 'chat-1', last_seq: 0, active_progress: null } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
  await page.route('**/api/chats/*/switch', (r) => { r.fulfill({ json: { ok: true, chat_id: 'chat-1', channel: 'web', todos: [] } }) })
}

test.describe('Cancel + reconnect iteration bugs', () => {
  test.beforeEach(() => { seqCounter = 0 })

  test('cancel preserves iterations (exact backend sequence, no PhaseDone)', async ({ browser }) => {
    const page = await browser.newPage()
    await page.addInitScript(() => {
      const l: Record<string, Set<(e: MessageEvent) => void>> = {}; (window as unknown as Record<string, unknown>).__sseListeners = l
      class M { readyState = 1; onopen: ((e: Event) => void) | null = null; onerror: ((e: Event) => void) | null = null; constructor(public u: string) { setTimeout(() => this.onopen?.(new Event('open')), 0) } addEventListener(t: string, h: (e: MessageEvent) => void) { if (!l[t]) l[t] = new Set(); l[t].add(h) } removeEventListener() {} close() {} }
      ;(window as unknown as Record<string, unknown>).EventSource = M
    })
    await setupMock(page)
    await page.goto(`${BASE}/login`); await page.locator('input').first().fill('test'); await page.locator('input[type=password]').fill('test'); await page.locator('button[type=submit]').click()
    await page.waitForTimeout(2000)

    // Exact backend cancel sequence:
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', { type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 1, turn_start: { trigger: 'user' }, chat_id: 'web:chat-1' } })
    // Iteration 0: thinking
    await emitSSE(page, 'progress_structured', { type: 'progress_structured', progress: { phase: 'thinking', iteration: 0, seq: 2, turn_id: 1, chat_id: 'web:chat-1' } })
    // Iteration 0: tool running
    await emitSSE(page, 'progress_structured', { type: 'progress_structured', progress: { phase: 'tool_exec', iteration: 0, seq: 3, turn_id: 1, chat_id: 'web:chat-1', active_tools: [{ name: 'Read', status: 'running', iteration: 0 }] } })
    // Iteration 1: tool done (delta push iter 0)
    await emitSSE(page, 'progress_structured', { type: 'progress_structured', progress: { phase: 'tool_exec', iteration: 1, seq: 4, turn_id: 1, chat_id: 'web:chat-1', active_tools: [{ name: 'Shell', status: 'running', iteration: 1 }], iteration_history: [{ iteration: 0, thinking: 'Reading file', completed_tools: [{ name: 'Read', status: 'done', iteration: 0, summary: 'main.go' }] }] } })
    await page.waitForTimeout(200)

    // Verify iteration 0 visible (Read)
    const readBefore = await page.evaluate(() => (document.body.textContent || '').includes('Read'))
    console.log('Before cancel - Read visible:', readBefore)
    expect(readBefore).toBe(true)

    // Cancel ack (NO PhaseDone — cancel bypasses it)
    await emitSSE(page, 'text', {
      type: 'text', content: '', cancelled: true, seq: 5, turn_id: 1, chat_id: 'web:chat-1',
      progress_history: JSON.stringify([{ iteration: 0, thinking: 'Reading file', completed_tools: [{ name: 'Read', status: 'done', iteration: 0, summary: 'main.go' }] }, { iteration: 1, thinking: '', completed_tools: [], user_cancelled: true }]),
    })
    await emitSSE(page, 'session', { type: 'session', session: { action: 'idle', chat_id: 'chat-1', channel: 'web' } })
    await page.waitForTimeout(500)

    // Iteration 0 should STILL be visible — the cancel ack committed the
    // message with progress_history iterations. Check for the committed
    // message's summary text (tools are folded at 'all' level).
    const hasCommitted = await page.evaluate(() => {
      const text = document.body.textContent || ''
      // Committed message shows "Processed N iterations" or similar
      return text.includes('Processed') || text.includes('processed') || text.includes('已处理')
    })
    console.log('After cancel - committed message:', hasCommitted)
    expect(hasCommitted).toBe(true)

    await page.close()
  })

  test('SSE reconnect restores iterations (not stuck)', async ({ browser }) => {
    const page = await browser.newPage()
    await page.addInitScript(() => {
      const l: Record<string, Set<(e: MessageEvent) => void>> = {}; (window as unknown as Record<string, unknown>).__sseListeners = l
      class M { readyState = 1; onopen: ((e: Event) => void) | null = null; onerror: ((e: Event) => void) | null = null; constructor(public u: string) { setTimeout(() => this.onopen?.(new Event('open')), 0) } addEventListener(t: string, h: (e: MessageEvent) => void) { if (!l[t]) l[t] = new Set(); l[t].add(h) } removeEventListener() {} close() {} }
      ;(window as unknown as Record<string, unknown>).EventSource = M
    })

    // Mock RPC: get_active_progress returns the snapshot
    const activeSnapshot = {
      phase: 'tool_exec', iteration: 1, seq: 10, turn_id: 1, chat_id: 'web:chat-1',
      active_tools: [{ name: 'Shell', status: 'running', iteration: 1 }],
      completed_tools: [{ name: 'Read', status: 'done', iteration: 0, summary: 'file.go' }],
      iteration_history: [
        { iteration: 0, thinking: 'Reading', completed_tools: [{ name: 'Read', status: 'done', iteration: 0, summary: 'file.go' }] },
      ],
    }
    await page.route('**/api/rpc', (r) => {
      const body = r.request().postDataJSON()
      if (body?.method === 'get_active_progress') { r.fulfill({ json: { ok: true, data: activeSnapshot } }) }
      else { r.fulfill({ json: { ok: true, data: null } }) }
    })
    await page.route('**/api/history*', (r) => r.fulfill({
      json: { ok: true, data: { messages: [], chat_id: 'chat-1', last_seq: 0, active_progress: activeSnapshot } },
    }))
    await setupMock(page)
    await page.goto(`${BASE}/login`); await page.locator('input').first().fill('test'); await page.locator('input[type=password]').fill('test'); await page.locator('button[type=submit]').click()
    await page.waitForTimeout(2000)

    // Simulate SSE reconnect: dispatch restoreActiveProgress snapshot
    await emitSSE(page, 'progress_structured', { type: 'progress_structured', progress: activeSnapshot })
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await page.waitForTimeout(500)

    // Iteration 0 (Read) should be visible from restored snapshot
    const readVisible = await page.evaluate(() => (document.body.textContent || '').includes('Read'))
    const shellVisible = await page.evaluate(() => (document.body.textContent || '').includes('Shell'))
    console.log('After reconnect - Read:', readVisible, 'Shell:', shellVisible)
    expect(readVisible).toBe(true)
    expect(shellVisible).toBe(true)

    // Simulate continued streaming after reconnect
    await emitSSE(page, 'stream_content', { type: 'stream_content', progress: { stream_content: 'Continuing work...', chat_id: 'web:chat-1', streaming: true } })
    await page.waitForTimeout(300)
    const contentVisible = await page.evaluate(() => (document.body.textContent || '').includes('Continuing'))
    console.log('After reconnect + stream - content:', contentVisible)
    expect(contentVisible).toBe(true)

    await page.close()
  })
})
