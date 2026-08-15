import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E test: notification turn doesn't cause flicker/iteration loss.
 *
 * Bug: after a system notification (bg task/cron) turn renders on web,
 * every subsequent turn flickers and loses all but the current iteration.
 *
 * Root cause: normal user messages are eager-saved to DB BEFORE Run()
 * (web.go sets user_msg_eager_saved=true). But notification messages
 * (injectBgUserMessage → injectInboundWithMetadata) bypass eager-save.
 * processMessage saves the user msg AFTER Run() starts. When reload()
 * fires on turn_started, the notification user msg is missing from DB
 * history → reconcileHistoryWithLiveRows can't match it → the entire
 * live message list is wiped by the fresh (incomplete) history.
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
      chats: [], orphan_subagents: [],
    } },
  }))
  await page.route('**/api/history', (r) => r.fulfill({
    json: { ok: true, data: { messages: [], chat_id: 'chat-1', last_seq: 0, active_progress: null } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

async function hasContent(page: Page, text: string): Promise<boolean> {
  return page.evaluate((t) => (document.body.textContent || '').includes(t), text)
}

test.describe('Notification turn iteration preservation', () => {
  test.beforeEach(() => { seqCounter = 0 })

  test('notification turn preserves iterations after subsequent turns', async ({ browser }) => {
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

    // ── Turn 1: notification turn with 2 iterations ──
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 1, turn_start: { trigger: 'notification', content: '⏰ bg task done' }, chat_id: 'web:chat-1' },
    })
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    // Iteration 0: Read tool
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'tool_exec', iteration: 1, seq: 2, turn_id: 1, chat_id: 'web:chat-1',
        active_tools: [{ name: 'Read', status: 'running', iteration: 1 }] },
    })
    // Iteration 0 done, iteration 1 starts
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'thinking', iteration: 2, seq: 3, turn_id: 1, chat_id: 'web:chat-1',
        completed_tools: [{ name: 'Read', status: 'done', iteration: 1, summary: 'file.go' }],
        iteration_history: [{ iteration: 1, thinking: 'Reading file', completed_tools: [{ name: 'Read', status: 'done', iteration: 1, summary: 'file.go' }] }],
      },
    })
    // Grep tool in iteration 1
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'tool_exec', iteration: 2, seq: 4, turn_id: 1, chat_id: 'web:chat-1',
        active_tools: [{ name: 'Grep', status: 'running', iteration: 2 }] },
    })
    // Turn 1 done
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'done', iteration: 2, seq: 5, turn_id: 1, chat_id: 'web:chat-1',
        completed_tools: [{ name: 'Grep', status: 'done', iteration: 2, summary: 'found 3 matches' }],
        iteration_history: [
          { iteration: 1, thinking: 'Reading file', completed_tools: [{ name: 'Read', status: 'done', iteration: 1, summary: 'file.go' }] },
          { iteration: 2, thinking: 'Searching', completed_tools: [{ name: 'Grep', status: 'done', iteration: 2, summary: 'found 3 matches' }] },
        ],
      },
    })
    // Text event: commits the assistant message
    await emitSSE(page, 'text', {
      type: 'text', content: 'Done processing notification.', seq: 6, turn_id: 1, chat_id: 'web:chat-1',
      progress_history: JSON.stringify([
        { iteration: 1, thinking: 'Reading file', completed_tools: [{ name: 'Read', status: 'done', iteration: 1, summary: 'file.go' }] },
        { iteration: 2, thinking: 'Searching', completed_tools: [{ name: 'Grep', status: 'done', iteration: 2, summary: 'found 3 matches' }] },
      ]),
    })
    await emitSSE(page, 'session', { type: 'session', session: { action: 'idle', chat_id: 'chat-1', channel: 'web' } })
    await page.waitForTimeout(500)

    // Verify assistant text is visible (tools may be folded at 'all' level)
    expect(await hasContent(page, 'Done processing notification')).toBe(true)
    console.log('After turn 1: assistant text visible')

    // ── Turn 2: user-typed turn ──
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 2, turn_start: { trigger: 'user', request_id: 'r2' }, chat_id: 'web:chat-1' },
    })
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'thinking', iteration: 1, seq: 7, turn_id: 2, chat_id: 'web:chat-1' },
    })
    await page.waitForTimeout(300)

    // ── Verify: turn 1's assistant message should STILL be visible ──
    const textAfterTurn2 = await hasContent(page, 'Done processing notification')
    console.log('After turn 2 started: assistant text visible:', textAfterTurn2)

    // THE BUG: turn_started triggers store.reset() which clears live progress,
    // but the committed assistant message from turn 1 should NOT disappear.
    // If notification user msg wasn't eager-saved, reload() wipes it.
    expect(textAfterTurn2).toBe(true)

    await page.close()
  })

  test('REPRO: 弱网 notification —— turn_started(turn_start.content) 必须渲染通知 user 行', async ({ browser }) => {
    // 弱网场景：inject_user WS 消息丢失/延迟，只有 SSE turn_started 到达。
    // 后端 turn_started 事件携带 TurnStart{Trigger:'notification', Content}——
    // 通知内容在事件里。若前端只建 live turn 不构造 user 行，用户只看到
    // "思考中"，看不到 system notification 本身（用户报告的 bug）。
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

    // notification turn：turn_started 带通知内容 + thinking（思考中）
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 5, turn_start: { trigger: 'notification', content: '⏰ bg task done: build passed' }, chat_id: 'web:chat-1' },
    })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { phase: 'thinking', iteration: 1, seq: 2, turn_id: 5, chat_id: 'web:chat-1' },
    })
    await page.waitForTimeout(500)

    // 通知 user 行必须显示（不是只有"思考中"）
    expect(await hasContent(page, 'bg task done: build passed')).toBe(true)
    await page.close()
  })
})
