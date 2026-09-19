/**
 * P0 回归（2026-09-19 用户报告）：
 * 「手机熄屏一段时间后解锁，看到的 busy session 中的 live 进度消失且永远不再更新」。
 *
 * 现场取证（真实 DB + 服务端日志交叉）：turn 23 在 03:45–03:52 之间每 15–30s 增量
 * 落库新迭代（后端一直在跑），而客户端渲染停在 iteration 12（= 03:45:09 那次落库的
 * 内容）之后再也不动 —— 即事件到了却被状态机**整批丢弃**，不是"没收到"。
 *
 * 机制（`chat/reduce.ts`）：迟到/误传的 turn 结尾信号（`session(idle)` / agent-idle，
 * 由 SSH 重连回放 / restoreActiveProgress 竞态产生）把运行中的 live turn **冻结**；
 * 而 `stream` case 当时**没有**遮蔽解除（`iteration` case 有）⇒ 非空壳 frozen turn 的
 * 所有流式事件被 `return s` 丢弃。LLM 生成期只有流式事件（结构化事件只在迭代边界/
 * 工具状态变化时发）⇒ live 进度永不回来（"永远不再更新"）。
 *
 * 本 E2E 用真实浏览器 + 真实 SSE 消费链路断言用户可见契约：
 * 迟到 idle 冻结后，**进行中迭代的流式事件必须恢复 live 渲染并持续更新**。
 */
import { test, expect, type Browser, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'
const CHAT = 'chat-1'

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

let seqCounter = 0

async function emitSSE(page: Page, type: string, data: Record<string, unknown>): Promise<void> {
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

/** 进行中迭代（3）的一帧流式内容（Web 通道把所有 ProgressEvent 转发为
 *  `progress_structured`，流式帧 = phase '' + stream_content）。 */
async function emitStream(page: Page, streamContent: string): Promise<void> {
  await emitSSE(page, 'progress_structured', {
    chat_id: CHAT,
    progress: {
      chat_id: `web:${CHAT}`,
      turn_id: 1,
      iteration: 3,
      phase: '',
      stream_content: streamContent,
    },
  })
}

/** busy 会话：turn 1 已落库迭代 1..2，服务端 active_progress 声明它仍在跑（迭代 3）。 */
const ACTIVE_PROGRESS = {
  phase: 'tool_exec',
  iteration: 3,
  seq: 10,
  turn_id: 1,
  chat_id: `web:${CHAT}`,
  active_tools: [{ name: 'Read', status: 'running', iteration: 3, label: 'src/lib.rs', summary: '' }],
  completed_tools: [],
  iteration_history: [
    { iteration: 1, content: 'iter1', completed_tools: [] },
    { iteration: 2, content: 'iter2', completed_tools: [] },
  ],
  todos: [],
}

const HISTORY_MESSAGES = [
  { id: 1, role: 'user', content: '继续接线', timestamp: '2026-09-19T03:41:19Z', turn_id: 1 },
  {
    id: 2,
    role: 'assistant',
    content: '',
    timestamp: '2026-09-19T03:44:00Z',
    turn_id: 1,
    iterations: [
      { iteration: 1, content: 'iter1', reasoning: '', tools: [], tool_count: 0 },
      { iteration: 2, content: 'iter2', reasoning: '', tools: [], tool_count: 0 },
    ],
  },
]

async function newContext(browser: Browser) {
  // 手机视口（用户是在手机上遇到）
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true })
  await ctx.addInitScript(() => {
    try { localStorage.setItem('xbot-locale', 'zh-CN') } catch { /* ignore */ }
  })
  return ctx
}

async function setupMock(page: Page): Promise<void> {
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
        if (!listeners[type]) listeners[type] = new Set()
        listeners[type].add(handler)
      }
      removeEventListener(type: string, handler: (ev: MessageEvent) => void) { listeners[type]?.delete(handler) }
      close() { for (const key of Object.keys(listeners)) listeners[key].clear() }
    }
    ;(window as unknown as { EventSource: typeof MockEventSource }).EventSource = MockEventSource
  })
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) => r.fulfill({
    json: { ok: true, data: {
      sessions: [{ chat_id: CHAT, channel: 'web', label: 'mtp-step-opt', last_active: new Date().toISOString() }],
      chats: [{ chat_id: CHAT, channel: 'web', label: 'mtp-step-opt', last_active: new Date().toISOString(), isCurrent: true }],
      orphan_subagents: [],
    } },
  }))
  await page.route('**/api/history', (r) => r.fulfill({
    json: { ok: true, data: {
      messages: HISTORY_MESSAGES,
      chat_id: CHAT,
      channel: 'web',
      last_seq: 10,
      active_progress: ACTIVE_PROGRESS,
      has_more: false,
      oldest_id: 1,
    } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => {
    const body = r.request().postDataJSON() as { method?: string } | null
    if (body?.method === 'get_active_progress') return r.fulfill({ json: { ok: true, data: ACTIVE_PROGRESS } })
    return r.fulfill({ json: { ok: true, data: null } })
  })
}

test('手机端：迟到 idle 冻结运行中的 busy turn 后，流式事件必须恢复 live 并持续更新', async ({ browser }) => {
  seqCounter = 10
  const ctx = await newContext(browser)
  const page = await ctx.newPage()
  await setupMock(page)
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  // hydration（active_progress）把进行中的工具渲染出来 ⇒ live 行存在。
  await page.waitForSelector('[data-testid="tool-pill"]', { timeout: 30000 })
  const list = page.locator('[data-message-list-content]')

  // ① 迟到/误传的 turn 结尾信号（SSE 回放 / restoreActiveProgress 竞态）冻结 live。
  await emitSSE(page, 'session', { chat_id: CHAT, session: { action: 'idle', chat_id: CHAT } })

  // ② 进行中迭代（3）的流式内容继续到达 —— 后端只对运行中的 turn 发流式帧。
  await emitStream(page, '流式恢复 A')
  await expect(list, '流式事件必须解冻并恢复 live 渲染（修复前被整批丢弃）').toContainText('流式恢复 A')

  // ③ 后续流式继续更新（不是一次性恢复）。
  await emitStream(page, '流式恢复 A B')
  await expect(list).toContainText('流式恢复 A B')
  await emitStream(page, '流式恢复 A B C')
  await expect(list).toContainText('流式恢复 A B C')
  await ctx.close()
})
