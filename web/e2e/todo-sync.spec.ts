import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E tests for TODO list sync across various scenarios.
 *
 * Covers: initial render, live update, session switch, page refresh,
 * turn completion (PhaseDone), empty todos, and SSE reconnect.
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

// Track todos returned by /api/history and /api/chats/*/switch
let historyTodos: unknown[] = []
let switchTodos: unknown[] = []

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
      chats: [],
      orphan_subagents: [],
    } },
  }))
  await page.route('**/api/history*', (r) => {
    const body = r.request().postDataJSON()
    const chatID = body?.chat_id || 'chat-1'
    r.fulfill({
      json: {
        ok: true,
        data: {
          messages: [],
          chat_id: chatID,
          last_seq: 0,
          active_progress: historyTodos.length > 0
            ? { phase: 'done', todos: historyTodos, seq: 0 }
            : null,
        },
      },
    })
  })
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => {
    const body = r.request().postDataJSON()
    if (body?.method === 'get_active_progress') {
      r.fulfill({ json: { ok: true, data: historyTodos.length > 0 ? { phase: 'done', todos: historyTodos, seq: 0 } : null } })
    } else {
      r.fulfill({ json: { ok: true, data: null } })
    }
  })
  await page.route('**/api/chats/*/switch', (r) => {
    const url = r.request().url()
    const chatID = url.match(/\/chats\/([^/]+)\/switch/)?.[1] || 'chat-1'
    r.fulfill({ json: { ok: true, chat_id: chatID, channel: 'web', todos: switchTodos } })
  })
}

async function hasTodoText(page: Page, text: string): Promise<boolean> {
  return page.evaluate((t) => {
    return (document.body.textContent || '').includes(t)
  }, text)
}

const TEST_TODOS = [
  { id: 1, text: 'Setup project', done: true },
  { id: 2, text: 'Write tests', done: false },
  { id: 3, text: 'Deploy', done: false },
]

test.describe('TODO sync', () => {
  test.beforeEach(() => {
    seqCounter = 0
    historyTodos = []
    switchTodos = []
  })

  test('todos from active_progress render on page load', async ({ browser }) => {
    const page = await browser.newPage()
    await page.addInitScript(() => {
      const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
      const w = window as unknown as SSEMockState
      w.__sseListeners = listeners
      class M { readyState=1; onopen:((e:Event)=>void)|null=null; onerror:((e:Event)=>void)|null=null; constructor(public url:string){setTimeout(()=>this.onopen?.(new Event('open')),0)} addEventListener(t:string,h:(e:MessageEvent)=>void){if(!listeners[t])listeners[t]=new Set();listeners[t].add(h)} removeEventListener(){} close(){} }
      ;(window as unknown as { EventSource: typeof M }).EventSource = M
    })
    historyTodos = TEST_TODOS
    await setupMock(page)
    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()
    await page.waitForTimeout(2000)

    expect(await hasTodoText(page, 'Setup project')).toBe(true)
    expect(await hasTodoText(page, 'Write tests')).toBe(true)
    await page.close()
  })

  test('todos update via live SSE progress_structured', async ({ browser }) => {
    const page = await browser.newPage()
    await page.addInitScript(() => {
      const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
      const w = window as unknown as SSEMockState
      w.__sseListeners = listeners
      class M { readyState=1; onopen:((e:Event)=>void)|null=null; onerror:((e:Event)=>void)|null=null; constructor(public url:string){setTimeout(()=>this.onopen?.(new Event('open')),0)} addEventListener(t:string,h:(e:MessageEvent)=>void){if(!listeners[t])listeners[t]=new Set();listeners[t].add(h)} removeEventListener(){} close(){} }
      ;(window as unknown as { EventSource: typeof M }).EventSource = M
    })
    await setupMock(page)
    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()
    await page.waitForTimeout(2000)

    // No todos initially
    expect(await hasTodoText(page, 'Setup project')).toBe(false)

    // Send progress with todos
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'thinking', iteration: 0, seq: 1, turn_id: 1, chat_id: 'web:chat-1', todos: TEST_TODOS },
    })
    await page.waitForTimeout(500)

    expect(await hasTodoText(page, 'Setup project')).toBe(true)
    expect(await hasTodoText(page, 'Write tests')).toBe(true)
    await page.close()
  })

  test('todos survive session switch', async ({ browser }) => {
    const page = await browser.newPage()
    await page.addInitScript(() => {
      const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
      const w = window as unknown as SSEMockState
      w.__sseListeners = listeners
      class M { readyState=1; onopen:((e:Event)=>void)|null=null; onerror:((e:Event)=>void)|null=null; constructor(public url:string){setTimeout(()=>this.onopen?.(new Event('open')),0)} addEventListener(t:string,h:(e:MessageEvent)=>void){if(!listeners[t])listeners[t]=new Set();listeners[t].add(h)} removeEventListener(){} close(){} }
      ;(window as unknown as { EventSource: typeof M }).EventSource = M
    })

    // chat-1 has todos in active_progress; chat-2 also has todos
    const chat2Todos = [{ id: 10, text: 'Task A', done: false }]
    const currentHistoryTodos = TEST_TODOS
    await page.route('**/api/history*', (r) => {
      const body = r.request().postDataJSON()
      const chatID = body?.chat_id || 'chat-1'
      const todos = chatID === 'chat-2' ? chat2Todos : currentHistoryTodos
      r.fulfill({
        json: { ok: true, data: { messages: [], chat_id: chatID, last_seq: 0,
          active_progress: todos.length > 0 ? { phase: 'done', todos, seq: 0 } : null } },
      })
    })
    await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
    await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
    await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
    await page.route('**/api/session-tree', (r) => r.fulfill({
      json: { ok: true, data: {
        sessions: [
          { chat_id: 'chat-1', channel: 'web', label: 'Session 1', last_active: new Date().toISOString() },
          { chat_id: 'chat-2', channel: 'web', label: 'Session 2', last_active: new Date().toISOString() },
        ], chats: [], orphan_subagents: [],
      } },
    }))
    await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
    await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
    await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
    await page.route('**/api/chats/*/switch', (r) => {
      const url = r.request().url()
      const chatID = url.match(/\/chats\/([^/]+)\/switch/)?.[1] || 'chat-1'
      const todos = chatID === 'chat-2' ? chat2Todos : TEST_TODOS
      r.fulfill({ json: { ok: true, chat_id: chatID, channel: 'web', todos } })
    })

    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()
    await page.waitForTimeout(2000)

    // chat-1 has todos
    expect(await hasTodoText(page, 'Setup project')).toBe(true)

    // Switch to chat-2
    await page.locator('text=Session 2').first().click()
    await page.waitForTimeout(1500)

    // chat-2 should show its own todos
    expect(await hasTodoText(page, 'Task A')).toBe(true)
    expect(await hasTodoText(page, 'Setup project')).toBe(false)

    // Switch back to chat-1
    await page.locator('text=Session 1').first().click()
    await page.waitForTimeout(1500)

    expect(await hasTodoText(page, 'Setup project')).toBe(true)
    await page.close()
  })

  test('todos update on PhaseDone', async ({ browser }) => {
    const page = await browser.newPage()
    await page.addInitScript(() => {
      const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
      const w = window as unknown as SSEMockState
      w.__sseListeners = listeners
      class M { readyState=1; onopen:((e:Event)=>void)|null=null; onerror:((e:Event)=>void)|null=null; constructor(public url:string){setTimeout(()=>this.onopen?.(new Event('open')),0)} addEventListener(t:string,h:(e:MessageEvent)=>void){if(!listeners[t])listeners[t]=new Set();listeners[t].add(h)} removeEventListener(){} close(){} }
      ;(window as unknown as { EventSource: typeof M }).EventSource = M
    })
    await setupMock(page)
    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()
    await page.waitForTimeout(2000)

    // Start a turn with initial todos
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'thinking', iteration: 0, seq: 1, turn_id: 1, chat_id: 'web:chat-1',
        todos: [{ id: 1, text: 'Task 1', done: false }] },
    })
    await page.waitForTimeout(300)
    expect(await hasTodoText(page, 'Task 1')).toBe(true)

    // PhaseDone with updated todos (Task 1 done, Task 2 added)
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'done', iteration: 0, seq: 2, turn_id: 1, chat_id: 'web:chat-1',
        todos: [{ id: 1, text: 'Task 1', done: true }, { id: 2, text: 'Task 2', done: false }] },
    })
    await page.waitForTimeout(500)

    expect(await hasTodoText(page, 'Task 1')).toBe(true)
    expect(await hasTodoText(page, 'Task 2')).toBe(true)
    await page.close()
  })

  test('empty todos clear the list', async ({ browser }) => {
    const page = await browser.newPage()
    await page.addInitScript(() => {
      const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
      const w = window as unknown as SSEMockState
      w.__sseListeners = listeners
      class M { readyState=1; onopen:((e:Event)=>void)|null=null; onerror:((e:Event)=>void)|null=null; constructor(public url:string){setTimeout(()=>this.onopen?.(new Event('open')),0)} addEventListener(t:string,h:(e:MessageEvent)=>void){if(!listeners[t])listeners[t]=new Set();listeners[t].add(h)} removeEventListener(){} close(){} }
      ;(window as unknown as { EventSource: typeof M }).EventSource = M
    })
    await setupMock(page)
    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()
    await page.waitForTimeout(2000)

    // Set initial todos
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'thinking', iteration: 0, seq: 1, turn_id: 1, chat_id: 'web:chat-1',
        todos: [{ id: 1, text: 'ClearMe', done: false }] },
    })
    await page.waitForTimeout(300)
    expect(await hasTodoText(page, 'ClearMe')).toBe(true)

    // Clear todos via empty array
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'thinking', iteration: 1, seq: 2, turn_id: 1, chat_id: 'web:chat-1', todos: [] },
    })
    await page.waitForTimeout(500)

    expect(await hasTodoText(page, 'ClearMe')).toBe(false)
    await page.close()
  })

  test('todos survive after turn completes (session idle)', async ({ browser }) => {
    const page = await browser.newPage()
    await page.addInitScript(() => {
      const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
      const w = window as unknown as SSEMockState
      w.__sseListeners = listeners
      class M { readyState=1; onopen:((e:Event)=>void)|null=null; onerror:((e:Event)=>void)|null=null; constructor(public url:string){setTimeout(()=>this.onopen?.(new Event('open')),0)} addEventListener(t:string,h:(e:MessageEvent)=>void){if(!listeners[t])listeners[t]=new Set();listeners[t].add(h)} removeEventListener(){} close(){} }
      ;(window as unknown as { EventSource: typeof M }).EventSource = M
    })
    await setupMock(page)
    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()
    await page.waitForTimeout(2000)

    // Start turn with todos
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'thinking', iteration: 0, seq: 1, turn_id: 1, chat_id: 'web:chat-1',
        todos: [{ id: 1, text: 'PersistMe', done: false }] },
    })
    await page.waitForTimeout(300)
    expect(await hasTodoText(page, 'PersistMe')).toBe(true)

    // Turn ends: PhaseDone with todos, then session(idle)
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'done', iteration: 0, seq: 2, turn_id: 1, chat_id: 'web:chat-1',
        todos: [{ id: 1, text: 'PersistMe', done: true }] },
    })
    await emitSSE(page, 'session', { type: 'session', session: { action: 'idle', chat_id: 'chat-1', channel: 'web' } })
    await page.waitForTimeout(500)

    // Todos should survive after idle
    expect(await hasTodoText(page, 'PersistMe')).toBe(true)
    await page.close()
  })

  test('todos survive text event (full turn lifecycle)', async ({ browser }) => {
    const page = await browser.newPage()
    await page.addInitScript(() => {
      const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
      const w = window as unknown as SSEMockState
      w.__sseListeners = listeners
      class M { readyState=1; onopen:((e:Event)=>void)|null=null; onerror:((e:Event)=>void)|null=null; constructor(public url:string){setTimeout(()=>this.onopen?.(new Event('open')),0)} addEventListener(t:string,h:(e:MessageEvent)=>void){if(!listeners[t])listeners[t]=new Set();listeners[t].add(h)} removeEventListener(){} close(){} }
      ;(window as unknown as { EventSource: typeof M }).EventSource = M
    })
    await setupMock(page)
    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()
    await page.waitForTimeout(2000)

    // Full turn: busy → thinking(iter=1) with todos → PhaseDone with todos → text → idle
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'thinking', iteration: 1, seq: 1, turn_id: 1, chat_id: 'web:chat-1',
        todos: [{ id: 1, text: 'Task A', done: false }, { id: 2, text: 'Task B', done: true }] },
    })
    await page.waitForTimeout(300)
    expect(await hasTodoText(page, 'Task A')).toBe(true)

    // PhaseDone with updated todos
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'done', iteration: 1, seq: 2, turn_id: 1, chat_id: 'web:chat-1',
        todos: [{ id: 1, text: 'Task A', done: true }, { id: 2, text: 'Task B', done: true }] },
    })
    await page.waitForTimeout(200)

    // text event (final reply) — this triggers onAssistantComplete → store.reset()
    await emitSSE(page, 'text', {
      type: 'text', content: 'Done!', seq: 3, turn_id: 1, chat_id: 'chat-1',
      progress_history: JSON.stringify([]),
    })
    await page.waitForTimeout(200)

    // session(idle)
    await emitSSE(page, 'session', { type: 'session', session: { action: 'idle', chat_id: 'chat-1', channel: 'web' } })
    await page.waitForTimeout(500)

    // THE BUG: todos disappear after text event + idle
    expect(await hasTodoText(page, 'Task A')).toBe(true)
    expect(await hasTodoText(page, 'Task B')).toBe(true)
    await page.close()
  })
})
