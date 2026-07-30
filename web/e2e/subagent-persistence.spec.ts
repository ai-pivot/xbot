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
  await page.route('**/api/history*', (r) => r.fulfill({
    json: { ok: true, data: { messages: [], chat_id: 'chat-1', last_seq: 0, active_progress: null } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
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

/** Check if a SubAgent card with the given role is currently rendered. */
async function hasSubAgentCard(page: Page, role: string): Promise<boolean> {
  return page.evaluate((r) => {
    // SubAgentProgressTree renders cards with sweep-text containing the role name.
    // Each card has a button with the role in its text content.
    const buttons = document.querySelectorAll('button[aria-label*="SubAgent"]')
    for (const btn of buttons) {
      if (btn.textContent?.includes(r)) return true
    }
    return false
  }, role)
}

test.describe('SubAgent persistence across iterations', () => {
  test.beforeEach(() => { seqCounter = 0 })

  test('completed SubAgent does NOT render in subsequent iterations', async ({ browser }) => {
    const page = await browser.newPage()
    await login(page)

    // Turn 1: start
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 1, turn_start: { trigger: 'user' }, chat_id: 'web:chat-1' },
    })

    // Iteration 0: SubAgent running
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: {
        phase: 'tool_exec', iteration: 0, seq: 2, turn_id: 1, chat_id: 'web:chat-1',
        active_tools: [{ name: 'SubAgent', label: 'SubAgent: explore', status: 'running', iteration: 0 }],
        sub_agents: [{ role: 'explore', instance: 'oneshot-1', status: 'running', desc: 'reading files', session_key: 'agent:explore/oneshot-1' }],
      },
    })
    await page.waitForTimeout(500)

    // Verify SubAgent card is visible
    expect(await hasSubAgentCard(page, 'explore')).toBe(true)

    // SubAgent completes: tool done. The SubAgent itself is no longer running.
    // beginIteration(next) clears subAgentNodes entirely.
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: {
        phase: 'tool_exec', iteration: 0, seq: 3, turn_id: 1, chat_id: 'web:chat-1',
        completed_tools: [{ name: 'SubAgent', label: 'SubAgent: explore', status: 'done', iteration: 0 }],
        sub_agents: [{ role: 'explore', instance: 'oneshot-1', status: 'running', desc: 'finished', session_key: 'agent:explore/oneshot-1' }],
      },
    })
    await page.waitForTimeout(500)

    // Iteration 1: new iteration — beginIteration clears subAgentNodes.
    // No sub_agents in the event (they were cleared at iteration boundary).
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: {
        phase: 'thinking', iteration: 1, seq: 4, turn_id: 1, chat_id: 'web:chat-1',
        active_tools: [],
        completed_tools: [],
        sub_agents: [],
      },
    })
    await page.waitForTimeout(500)

    // SubAgent card should NOT be visible in iteration 1
    expect(await hasSubAgentCard(page, 'explore')).toBe(false)

    await page.close()
  })
})
