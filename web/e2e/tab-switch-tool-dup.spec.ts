import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E: 切 tab 后同一工具 generating+running 双渲染（2026-09-04 用户实录，
 * turn-580-live DOM dump：task_wait 一个无参 generating pill + 一个带参数
 * running pill 同时渲染）。
 *
 * 事故链：
 *   1. turn 流式输出 content + task_wait 参数生成中（stream_content 携带
 *      streaming_tools=[task_wait generating]）。
 *   2. 用户切 tab → SSE 断开 → 清除 generating 的结构化事件
 *      （activeTools=[task_wait running]）在断连窗口丢失（ring 驱逐）。
 *   3. 切回 → resync_required → reload → /api/history 携带 active_progress
 *      快照（active_tools=[task_wait running]）。
 *   4. BUG：history_replaced step 3.5 合并各取所长 —— activeTools 取快照
 *      （running）、streamingTools 保留 live 的 stale generating → 双渲染。
 *      修复：合并点强制 streamingTools ∩ activeTools = ∅（与 reduce 的
 *      stream/iteration case 同语义）。
 */

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
  __sseSeq: number
}

let seqCounter = 0
/** /api/history fetch 计数（closure 数组 —— route handler 间共享，beforeEach 重置）。 */
const historyFetches: number[] = []

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
  // 首次 fetch（active_progress=null）+ reload 后携带 active 快照
  // （active_tools=[task_wait running] —— 工具已开始执行，模拟断连窗口
  // 期间服务端推进到执行态）。label 用后端格式 "name: param"——前端
  // toolParam 从 ": " 之后截取参数。
  await page.route('**/api/history', (r) => {
    const calls = historyFetches.push(Date.now())
    const isFirst = calls === 1
    return r.fulfill({
      json: {
        ok: true,
        data: isFirst
          ? { messages: [], chat_id: 'chat-1', last_seq: 0, active_progress: null }
          : {
              messages: [],
              chat_id: 'chat-1',
              last_seq: 0,
              active_progress: {
                phase: 'tool_exec',
                turn_id: 580,
                iteration: 1,
                seq: 99,
                active_tools: [{ name: 'task_wait', status: 'running', iteration: 1, label: 'task_wait: {"task_id":["5352cf55"]}' }],
                completed_tools: [],
                iteration_history: [],
              },
            },
      },
    })
  })
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

test.describe('Tab switch: same tool must not render generating + running', () => {
  test.beforeEach(() => {
    seqCounter = 0
    historyFetches.length = 0
  })

  test('resync reload merges snapshot activeTools without duplicating stale streamingTools', async ({ browser }) => {
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

    // ── Phase 1: turn 580 开始（turn_started + thinking）──
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 580, turn_start: { trigger: 'user', request_id: 'r1' }, chat_id: 'web:chat-1' },
    })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'thinking', iteration: 1, seq: 2, turn_id: 580, chat_id: 'web:chat-1' },
    })

    // ── Phase 2: 流式输出 + task_wait 参数生成中（用户 DOM 现场）──
    await emitSSE(page, 'stream_content', {
      type: 'stream_content',
      progress: {
        stream_content: '继续冲 100 tok/s。当前 FERRITE_LAYER_DEV 已把 decode 提速。',
        chat_id: 'web:chat-1', streaming: true,
        streaming_tools: [{ name: 'task_wait', status: 'generating', iteration: 1 }],
      },
    })
    await page.waitForTimeout(300)
    const hasTool = await page.evaluate(() => document.body.textContent?.includes('task_wait') ?? false)
    expect(hasTool).toBe(true)

    // ── Phase 3: 切 tab 回来 —— resync_required 触发 reload ──
    // （真实链路：断连窗口丢失结构化事件 → ring 驱逐 → resync_required →
    //  useChatMessages.reload() → /api/history 的 active_progress 快照。）
    await emitSSE(page, 'resync_required', { type: 'resync_required', chat_id: 'web:chat-1' })
    await page.waitForTimeout(1000)

    // ── Verify: task_wait 只渲染一次 ──
    // DOM 断言：tool-pill 里 aria-label 含 task_wait 的元素数（修复前 = 2：
    // 一个裸名 generating + 一个带参数 running；修复后 = 1）。
    const pillCount = await page.evaluate(() => {
      const pills = Array.from(document.querySelectorAll('[data-testid="tool-pill"]'))
      return pills.filter((p) => (p.textContent ?? '').includes('task_wait')).length
    })
    expect(pillCount).toBe(1)

    // 且该 pill 是 running（带参数 label）而非裸名 generating。
    const runningVisible = await page.evaluate(() => {
      const pills = Array.from(document.querySelectorAll('[data-testid="tool-pill"]'))
      const tw = pills.find((p) => (p.textContent ?? '').includes('task_wait'))
      return tw ? (tw.textContent ?? '').includes('5352cf55') : false
    })
    expect(runningVisible).toBe(true)

    await page.close()
  })
})
