import { test, expect, type Page } from '@playwright/test'

/**
 * 复现（2026-09-17 用户报告 + 真实 DOM 实证）：
 *
 * 用户截图红框 = `🧠 Thought 384 chars` 下面一片空白。真实 DOM：
 *
 *   <div class="iter-block" data-iter-id="24" data-turn-id="8">        ← committed 迭代 24（完整）
 *      <button data-testid="thinking-line">Thought 384 chars</button>
 *      <div class="markdown-body">…正文…</div>
 *      <div data-copy-target="tools">AskUser pill</div>
 *   </div>
 *   <div class="iter-block" data-iter-id="live" data-iter-num="24" data-turn-id="8">   ← ⚠️ live 区又一份
 *      <button data-testid="thinking-line">Thought 384 chars</button>   ← 只有标题，正文不渲染 ⇒ 空壳
 *   </div>
 *
 * 触发条件：**AskUser 在迭代 N 内部调用** ⇒ 迭代 N 已进 iteration_history（committed），
 * 而 turn 暂停在 WaitingUser（没有下一次迭代推进）⇒ live 的 iteration 仍是 N
 * ⇒ live 区重复渲染 N（live 的内容体是折叠态 ⇒ 只看到标题 + 空白）。
 *
 * 契约：live 区渲染的语义是「尚未成为历史记录的进行中部分」——同一迭代号已 committed
 * 时，live 区**不得**再渲染它（否则每次 AskUser 都会多出一个空壳思考块）。
 *
 * 判别力：TurnBody 的 live 渲染处去掉「跳过已 committed 同号迭代」判断 ⇒ 本用例必红。
 */

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

let seqCounter = 0

async function emitSSE(page: Page, type: string, data: Record<string, unknown>) {
  await page.evaluate(
    ({ type, data, seq }) => {
      const w = window as unknown as SSEMockState
      const handlers = w.__sseListeners?.[type]
      if (!handlers) return
      const ev = new MessageEvent(type, { data: JSON.stringify({ ...data, seq }) })
      handlers.forEach((h) => h(ev))
    },
    { type, data, seq: ++seqCounter },
  )
}

/** 真实现场内容（384 字符；与截图 `Thought 384 chars` 对应） */
const REAL_REASONING =
  "Let me write the final answer now, covering all the user's questions with the evidence I gathered.\n\n" +
  'Structure:\n' +
  '1. 「丢迭代」的真因（最重要，用户可直接验证）\n' +
  '2. 和「防止覆盖 db」有没有关系\n' +
  "3. 「连续升两个 migration version」的真相\n" +
  '4. conf 已改（含做了/没做什么）\n' +
  '5. WAL 的问题：我实测了，不是"WAL 没恢复"\n' +
  '6. 待你定的修复方案 + 询问\n\n' +
  'Let me write it concisely but with the evidence.'

async function newPage(browser: import('@playwright/test').Browser): Promise<Page> {
  const page = await browser.newPage()
  await page.addInitScript(() => {
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
    ;(window as unknown as SSEMockState).__sseListeners = listeners
    class MockEventSource {
      readyState = 1
      onopen: ((ev: Event) => void) | null = null
      onerror: ((ev: Event) => void) | null = null
      constructor(public url: string) {
        setTimeout(() => this.onopen?.(new Event('open')), 0)
      }
      addEventListener(type: string, handler: (ev: MessageEvent) => void) {
        if (!listeners[type]) listeners[type] = new Set()
        listeners[type].add(handler)
      }
      removeEventListener(type: string, handler: (ev: MessageEvent) => void) {
        listeners[type]?.delete(handler)
      }
      close() {}
    }
    ;(window as unknown as { EventSource: typeof MockEventSource }).EventSource = MockEventSource
  })

  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: {
          sessions: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() }],
          chats: [],
          orphan_subagents: [],
        },
      },
    }),
  )
  await page.route('**/api/history', (r) =>
    r.fulfill({ json: { ok: true, data: { messages: [], chat_id: 'chat-1', last_seq: 0, active_progress: null } } }),
  )
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))

  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForFunction(
    () => {
      const w = window as unknown as SSEMockState
      return !!w.__sseListeners?.['progress_structured']
    },
    { timeout: 10_000 },
  )
  await page.waitForTimeout(300)
  return page
}

const structured = (page: Page, p: Record<string, unknown>) =>
  emitSSE(page, 'progress_structured', { type: 'progress_structured', progress: { chat_id: 'web:chat-1', ...p } })

const stream = (page: Page, p: Record<string, unknown>) =>
  emitSSE(page, 'stream_content', { type: 'stream_content', progress: { chat_id: 'web:chat-1', ...p } })

test.describe('AskUser 暂停时的重复渲染', () => {
  test('同一迭代已 committed 后，live 区不得再渲染一份空壳（红框空白的真因）', async ({ browser }) => {
    seqCounter = 0
    const page = await newPage(browser)

    await structured(page, { phase: 'turn_started', turn_id: 1, turn_start: { trigger: 'user', content: 'go' } })
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })

    // 迭代 1：reasoning 流式 + AskUser 工具执行中
    await stream(page, { iteration: 1, reasoning_stream_content: REAL_REASONING, streaming: true })
    await structured(page, {
      phase: 'tool_exec',
      iteration: 1,
      turn_id: 1,
      active_tools: [{ name: 'AskUser', status: 'running', iteration: 1 }],
    })
    await page.waitForTimeout(400)

    // 迭代 1 完成 ⇒ 进入 iteration_history（committed）。但 AskUser 让 turn 暂停在
    // WaitingUser：**没有下一次迭代推进** ⇒ live 的 iteration 仍为 1。
    await structured(page, {
      phase: 'tool_exec',
      iteration: 1,
      turn_id: 1,
      // ⚠️ 现场关键：structured 的 `reasoning`（→ LiveIteration 的 `lastReasoning`）
      // 与 committed 迭代的 reasoning 相同 ⇒ live 区仍会渲染一个思考块。
      reasoning: REAL_REASONING,
      completed_tools: [
        { name: 'AskUser', status: 'done', iteration: 1, summary: 'ask', args: '{"questions":[{"question":"x"}]}' },
      ],
      iteration_history: [
        {
          iteration: 1,
          reasoning: REAL_REASONING,
          completed_tools: [{ name: 'AskUser', status: 'done', iteration: 1, summary: 'ask' }],
        },
      ],
    })
    await page.waitForTimeout(800)

    const diag = await page.evaluate(() => {
      const blocks = Array.from(document.querySelectorAll('[data-iter-id]'))
      const text = document.body.textContent || ''
      return {
        blocks: blocks.map(
          (b) => `${b.getAttribute('data-iter-id')}#${b.getAttribute('data-iter-num') ?? ''}`,
        ),
        thoughtLabels: (text.match(/Thought \d+ chars|思考 \d+ 字/g) || []).length,
        thinkingLines: document.querySelectorAll('[data-testid="thinking-line"]').length,
      }
    })
    console.log('ASKUSER_PAUSE_DIAG:', JSON.stringify(diag, null, 2))
    test.info().annotations.push({ type: 'diagn', description: JSON.stringify(diag) })
    await page.screenshot({ path: '/tmp/reasoning/03-askuser-pause.png', fullPage: true })

    // ⛔ 契约：只有 committed 迭代 1 那一个思考块；live 区不得再渲染一份空壳。
    expect(diag.thinkingLines).toBe(1)
    expect(diag.thoughtLabels).toBe(1)

    await page.close()
  })
})
