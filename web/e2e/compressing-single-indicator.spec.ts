import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * 压缩期间的状态指示器必须是【唯一】的。
 *
 * 用户报告（2026-09-15 截图）：`thinking…`（ShimmerThinking）叠在
 * `Compressing context…` 上方。根因：压缩期间 streaming=true 但没有内容
 * → LiveIteration 的空内容分支渲染 ShimmerThinking，而 AssistantMessage /
 * MessageList 同时渲染压缩指示器 → 两个状态指示器上下堆叠。
 *
 * 不变量：每个状态下有且只有一个状态指示器 —— 压缩期间归压缩指示器。
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

/** 注入 mock EventSource，测试可通过 emitSSE 直接投递 SSE 事件。 */
async function installSSEMock(page: Page) {
  await page.addInitScript(() => {
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
    const w = window as unknown as SSEMockState
    w.__sseListeners = listeners
    class M {
      readyState = 1
      onopen: ((e: Event) => void) | null = null
      onerror: ((e: Event) => void) | null = null
      constructor(public url: string) { setTimeout(() => this.onopen?.(new Event('open')), 0) }
      addEventListener(t: string, h: (e: MessageEvent) => void) {
        if (!listeners[t]) listeners[t] = new Set()
        listeners[t].add(h)
      }
      removeEventListener() {}
      close() {}
    }
    ;(window as unknown as { EventSource: typeof M }).EventSource = M
  })
}

async function login(page: Page) {
  await setupMock(page)
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForTimeout(2000)
}

test.describe('压缩期间的状态指示器唯一性', () => {
  test.beforeEach(() => { seqCounter = 0 })

  test('phase=compressing 时只渲染压缩指示器，不渲染 thinking…', async ({ browser }) => {
    const page = await browser.newPage()
    await installSSEMock(page)
    await login(page)

    await emitSSE(page, 'session', {
      type: 'session',
      session: { action: 'busy', chat_id: 'chat-1', channel: 'web' },
    })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 1, chat_id: 'web:chat-1', turn_start: { trigger: 'user', request_id: 'r1' } },
    })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'compressing', iteration: 1, seq: 2, turn_id: 1, chat_id: 'web:chat-1' },
    })
    await page.waitForTimeout(1200)

    // 压缩指示器在（Loader2 + 文案）。
    await expect(page.getByText(/compressing|压缩/i).first()).toBeVisible()
    // 思考占位符（ShimmerThinking 的 .sweep-text）必须为 0。
    expect(await page.locator('.sweep-text').count()).toBe(0)
    await page.close()
  })
})
