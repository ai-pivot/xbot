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

// History returned by /api/history — simulates a completed cancelled turn
// from the DB (cancelMsg with Detail, now display_only=false after migration).
const cancelledTurnHistory = [
  { role: 'user', content: 'do something', timestamp: new Date().toISOString() },
  // assistant with iterations from Detail (the [interrupted] cancel message)
  {
    role: 'assistant',
    content: '',
    iterations: [
      {
        iteration: 0,
        thinking: 'Reading file',
        tools: [
          { name: 'TodoWrite', status: 'done', summary: 'TODO updated' },
          { name: 'Read', status: 'done', summary: 'main.go', label: 'Read(main.go)' },
        ],
        toolCount: 2,
      },
      {
        iteration: 1,
        thinking: '',
        tools: [
          { name: 'Read', status: 'done', summary: 'other.go', label: 'Read(other.go)' },
          { name: 'Read', status: 'done', summary: 'third.go', label: 'Read(third.go)' },
          { name: 'user_cancelled', status: 'done', summary: 'cancelled by user' },
        ],
        toolCount: 3,
      },
    ],
    timestamp: new Date().toISOString(),
  },
]

async function setupMock(page: Page, history: unknown[]) {
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) => r.fulfill({
    json: { ok: true, data: {
      sessions: [
        { chat_id: 'chat-1', channel: 'web', label: 'S1', last_active: new Date().toISOString() },
        { chat_id: 'chat-2', channel: 'web', label: 'S2', last_active: new Date().toISOString() },
      ],
      chats: [], orphan_subagents: [],
    } },
  }))
  await page.route('**/api/history', (r) => {
    const body = r.request().postDataJSON()
    const chatID = body?.chat_id || 'chat-1'
    const msgs = chatID === 'chat-1' ? history : []
    r.fulfill({ json: { ok: true, data: { messages: msgs, chat_id: chatID, last_seq: 0, active_progress: null } } })
  })
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
  await page.route('**/api/chats/*/switch', (r) => { r.fulfill({ json: { ok: true, chat_id: 'c', channel: 'web', todos: [] } }) })
}

test.describe('No duplicate after cancel + reload', () => {
  test.beforeEach(() => { seqCounter = 0 })

  test('cancelled turn from history renders once, not twice', async ({ browser }) => {
    const page = await browser.newPage()
    await page.addInitScript(() => {
      const l: Record<string, Set<(e: MessageEvent) => void>> = {}; (window as any).__sseListeners = l
      class M { readyState=1; onopen:((e:Event)=>void)|null=null; onerror:((e:Event)=>void)|null=null; constructor(public u:string){setTimeout(()=>this.onopen?.(new Event('open')),0)} addEventListener(t:string,h:(e:MessageEvent)=>void){if(!l[t])l[t]=new Set();l[t].add(h)} removeEventListener(){} close(){} }
      ;(window as any).EventSource = M
    })
    await setupMock(page, cancelledTurnHistory)
    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()
    await page.waitForTimeout(2000)

    // Count assistant rows with data-role=assistant
    const assistantRows = await page.evaluate(() =>
      document.querySelectorAll('[data-role="assistant"]').length)
    console.log('Initial assistant rows:', assistantRows)
    expect(assistantRows).toBe(1)

    // Switch away and back (triggers reload)
    await page.locator('text=S2').first().click()
    await page.waitForTimeout(1000)
    await page.locator('text=S1').first().click()
    await page.waitForTimeout(1500)

    // Should STILL be 1 assistant row (not 2)
    const assistantRowsAfter = await page.evaluate(() =>
      document.querySelectorAll('[data-role="assistant"]').length)
    console.log('After switch assistant rows:', assistantRowsAfter)
    expect(assistantRowsAfter).toBe(1)

    await page.close()
  })

  test('cancel ack commits message, then reload does not duplicate', async ({ browser }) => {
    const page = await browser.newPage()
    await page.addInitScript(() => {
      const l: Record<string, Set<(e: MessageEvent) => void>> = {}; (window as any).__sseListeners = l
      class M { readyState=1; onopen:((e:Event)=>void)|null=null; onerror:((e:Event)=>void)|null=null; constructor(public u:string){setTimeout(()=>this.onopen?.(new Event('open')),0)} addEventListener(t:string,h:(e:MessageEvent)=>void){if(!l[t])l[t]=new Set();l[t].add(h)} removeEventListener(){} close(){} }
      ;(window as any).EventSource = M
    })

    // Track reload count — each /api/history call
    let historyCallCount = 0
    await page.route('**/api/history', (r) => {
      historyCallCount++
      r.fulfill({ json: { ok: true, data: { messages: cancelledTurnHistory, chat_id: 'chat-1', last_seq: 0, active_progress: null } } })
    })
    await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
    await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
    await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
    await page.route('**/api/session-tree', (r) => r.fulfill({ json: { ok: true, data: { sessions: [{ chat_id: 'chat-1', channel: 'web', label: 'S1', last_active: new Date().toISOString() }, { chat_id: 'chat-2', channel: 'web', label: 'S2', last_active: new Date().toISOString() }], chats: [], orphan_subagents: [] } } }))
    await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
    await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
    await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
    await page.route('**/api/chats/*/switch', (r) => { r.fulfill({ json: { ok: true, chat_id: 'c', channel: 'web', todos: [] } }) })

    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()
    await page.waitForTimeout(2000)

    // Simulate a cancel: stream content, then cancel ack
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', { type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 2, turn_start: { trigger: 'user' }, chat_id: 'web:chat-1' } })
    await emitSSE(page, 'progress_structured', { type: 'progress_structured', progress: { phase: 'thinking', iteration: 0, seq: 2, turn_id: 2, chat_id: 'web:chat-1' } })
    await emitSSE(page, 'stream_content', { type: 'stream_content', progress: { stream_content: 'Working on it...', chat_id: 'web:chat-1', streaming: true } })
    await page.waitForTimeout(200)
    await emitSSE(page, 'text', {
      type: 'text', content: '', cancelled: true, seq: 3, turn_id: 2, chat_id: 'web:chat-1',
      progress_history: JSON.stringify([{ iteration: 0, thinking: 'Working on it...', completed_tools: [], user_cancelled: true }]),
    })
    await emitSSE(page, 'session', { type: 'session', session: { action: 'idle', chat_id: 'chat-1', channel: 'web' } })
    await page.waitForTimeout(500)

    // Cancel ack should have committed a message
    const rowsAfterCancel = await page.evaluate(() => document.querySelectorAll('[data-role="assistant"]').length)
    console.log('After cancel - assistant rows:', rowsAfterCancel)

    // Switch away and back (triggers reload)
    await page.locator('text=S2').first().click()
    await page.waitForTimeout(1000)
    await page.locator('text=S1').first().click()
    await page.waitForTimeout(1500)

    // Should NOT have duplicate — DB history returns 1 assistant, cancel-committed
    // message is discarded by destructiveMutationGen
    const rowsAfterReload = await page.evaluate(() => document.querySelectorAll('[data-role="assistant"]').length)
    console.log('After reload - assistant rows:', rowsAfterReload)
    expect(rowsAfterReload).toBe(1)

    await page.close()
  })
})
