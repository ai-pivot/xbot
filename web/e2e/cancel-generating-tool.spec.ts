import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E test for generating tool cleanup on cancel.
 *
 * Bug: when a tool is in "generating" state (tool name detected during LLM
 * streaming, before arguments finish), the UI shows a tool marker with a
 * generating animation. If the user cancels at this point, the cancel
 * succeeds but the generating tool animation keeps rendering.
 *
 * Root cause: the cancel ack path commits iterations + calls store.reset(),
 * but the committed assistant message may still carry the generating tool
 * from the live snapshot's streamingTools, or store.reset() is skipped when
 * there's no streamContent (generating tools have no text).
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

test.describe('Cancel clears generating tool', () => {
  test.beforeEach(() => { seqCounter = 0 })

  test('generating tool animation is cleared after cancel', async ({ browser }) => {
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

    // ── Phase 1: Start a turn ──
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 1, turn_start: { trigger: 'user', request_id: 'r1' }, chat_id: 'web:chat-1' },
    })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'thinking', iteration: 0, seq: 2, turn_id: 1, chat_id: 'web:chat-1' },
    })

    // ── Phase 2: Simulate tool generating (streaming_tools with status=generating) ──
    // This is what happens when the LLM starts emitting a tool call but hasn't
    // finished the arguments yet — the tool name is detected via stream callbacks.
    await emitSSE(page, 'stream_content', {
      type: 'stream_content',
      progress: {
        stream_content: 'Let me run a command to check the build.',
        chat_id: 'web:chat-1', streaming: true,
        streaming_tools: [{ name: 'Shell', status: 'generating' }],
      },
    })
    await page.waitForTimeout(300)

    // Verify the generating tool is visible
    const hasGeneratingTool = await page.evaluate(() =>
      document.body.textContent?.includes('Shell') ?? false)
    expect(hasGeneratingTool).toBe(true)
    console.log('Before cancel - generating tool visible:', hasGeneratingTool)

    // ── Phase 3: Cancel ──
    // The backend sends: text event with cancelled=true + progress_history.
    // progress_history contains the completed iteration (with reasoning text)
    // but NOT the generating tool (it was never completed).
    await emitSSE(page, 'text', {
      type: 'text',
      content: '',
      cancelled: true,
      seq: 3,
      turn_id: 1,
      chat_id: 'web:chat-1',
      progress_history: JSON.stringify([
        {
          iteration: 0,
          thinking: 'Let me run a command to check the build.',
          completed_tools: [],
          user_cancelled: true,
        },
      ]),
    })
    await page.waitForTimeout(300)

    // ── Phase 4: Late stream_content arrives (race: cancel is async, the
    //    stream callback may fire one more chunk before the ctx is cancelled) ──
    // This stream_content reopens the store (stream_content resets finalizedRef)
    // and re-displays the generating tool.
    await emitSSE(page, 'stream_content', {
      type: 'stream_content',
      progress: {
        stream_content: 'Let me run a command to check the build.',
        chat_id: 'web:chat-1', streaming: true,
        streaming_tools: [{ name: 'Shell', status: 'generating' }],
      },
    })
    await page.waitForTimeout(500)

    // ── Verify: generating tool should disappear (not real content) ──
    const result = await page.evaluate(() => {
      const body = document.body.textContent || ''
      return {
        hasShell: body.includes('Shell'),
        hasContent: body.includes('Let me run a command to check the build'),
        bodyLen: body.length,
        bodyPreview: body.slice(0, 500),
      }
    })
    console.log('After cancel result:', JSON.stringify(result))

    // Generating tool disappears (it's not real content — never completed)
    expect(result.hasShell).toBe(false)
    // Content stays visible (frozen) — check a prefix since the typewriter
    // may not have fully revealed all characters at the time of cancel.
    expect(result.bodyPreview).toContain('Let me run a command')

    await page.close()
  })
})
