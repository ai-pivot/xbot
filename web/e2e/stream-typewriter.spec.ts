import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E: 打字机全链路（真实 sseConnection.handleEvent → useAgentChatState →
 * normalizeEvent → reduce → deriveRows → MessageList 渲染）。
 *
 * 用户报告（两轮修复后仍复现）："看不到打字机，一瞬间完整出现"。
 * 单元/集成测试全绿但真机不工作 → 断点只能在 mock 未覆盖的环节
 * （SSE 解析/桥接/MessageList 对 live 行的消费）。本测试用真实组件栈 +
 * 真实 SSE 信封形状复现，DOM 直接暴露断点。
 */

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

let seqCounter = 0

async function emitSSE(page: Page, type: string, data: Record<string, unknown>) {
  await page.evaluate(({ type, data, seq }) => {
    const w = window as unknown as SSEMockState
    const handlers = w.__sseListeners?.[type]
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

test.describe('打字机全链路', () => {
  test.beforeEach(() => { seqCounter = 0 })

  test('stream_content SSE 帧 → DOM 逐帧渲染', async ({ browser }) => {
    const page = await browser.newPage()
    const errors: string[] = []
    page.on('pageerror', (e) => errors.push(String(e)))
    page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()) })

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

    // Turn 生命周期：busy → turn_started → stream 帧×3（真实信封：progress 包装）。
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy' }, chat_id: 'chat-1' })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      chat_id: 'chat-1',
      progress: { phase: 'turn_started', turn_id: 1, seq: 1, turn_start: { trigger: 'user', content: 'hi' } },
    })

    await emitSSE(page, 'stream_content', {
      type: 'stream_content',
      chat_id: 'chat-1',
      progress: { turn_id: 1, iteration: 1, stream_content: '打字机帧一' },
    })
    await page.waitForTimeout(600)
    const frame1 = await page.evaluate(() => document.body.innerText.includes('打字机帧一'))
    const diag1 = await page.evaluate(() => (window as unknown as { __xbotChatDiag?: { counts(): Record<string, number> } }).__xbotChatDiag?.counts())

    await emitSSE(page, 'stream_content', {
      type: 'stream_content',
      chat_id: 'chat-1',
      progress: { turn_id: 1, iteration: 1, stream_content: '打字机帧一打字机帧二' },
    })
    await page.waitForTimeout(600)
    const frame2 = await page.evaluate(() => document.body.innerText.includes('打字机帧一打字机帧二'))
    const diag2 = await page.evaluate(() => (window as unknown as { __xbotChatDiag?: { counts(): Record<string, number> } }).__xbotChatDiag?.counts())

    console.log('FRAME1 visible:', frame1, 'FRAME2 visible:', frame2)
    console.log('diag after frame1:', JSON.stringify(diag1))
    console.log('diag after frame2:', JSON.stringify(diag2))
    console.log('errors:', JSON.stringify(errors))

    // 打字机帧必须逐帧出现在 DOM。
    expect(frame1, `frame1 missing; diag=${JSON.stringify(diag1)}; errors=${errors.join('|')}`).toBe(true)
    expect(frame2, `frame2 missing; diag=${JSON.stringify(diag2)}`).toBe(true)

    await page.close()
  })
})
