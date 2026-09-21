/**
 * P0 不变量（用户 2026-09-19 反复点名）：
 *
 *   **「只要输入框是 cancel 按钮，就一定不能上面渲染的内容是 idle 内容」**
 *
 * ⚠️ 方向澄清（用户二次纠正）：「这个迭代**就是在跑**」—— 会话/迭代确实在运行
 *（busy 是对的），bug 是 **busy 的会话渲染出来像 idle**（看不到任何进行中信号）。
 * 所以**绝不能**反向修（把 idle 伪装成 busy —— 那会在 P0 上再加一个 P0）。
 *
 * 两个真实破坏点（本 spec 用真实浏览器 + 真实 SSE 消费链路覆盖）：
 *  (A) 渲染层：`frozen` 行（cancel / idle 兜底定格）同样 `isPartial=true`，被当作
 *      live 行（`liveId`）⇒ ① 它拿到的 `liveProgress` 是空快照（frozen ⇒
 *      activeTurn===null ⇒ EMPTY）⇒ 自身不渲染进行中信号；② busy 占位符的
 *      `liveId === null` 条件因此不成立 ⇒ 也被抑制 ⇒ **busy + 内容像 idle**。
 *      修复：`liveId` 排除 frozen 行；占位符判据改为**看尾行**（只有"live 行正好是
 *      尾行且自己在渲染信号"才不需要占位符）。
 *  (B) 状态层：迟到/误传/重放的 coarse `session(idle)`（不带 turn 身份）冻结运行中
 *      的 turn ⇒ 渲染层失去 live。修复：`sessionRunning`（服务端 reconcile 权威）
 *      作闸门 —— running=true 时忽略 coarse idle（**不伪造任何状态**）。
 *
 * 断言（用户可见契约）：输入框 = cancel ⟹ 列表尾部必须能看到进行中信号。
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

async function emitStream(page: Page, streamContent: string): Promise<void> {
  await emitSSE(page, 'progress_structured', {
    chat_id: CHAT,
    progress: { chat_id: `web:${CHAT}`, turn_id: 1, iteration: 3, phase: '', stream_content: streamContent },
  })
}

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
  { id: 1, role: 'user', content: '继续接线', timestamp: '2026-09-19T04:00:00Z', turn_id: 1 },
  {
    id: 2,
    role: 'assistant',
    content: '',
    timestamp: '2026-09-19T04:01:00Z',
    turn_id: 1,
    iterations: [
      { iteration: 1, content: 'iter1', reasoning: '', tools: [], tool_count: 0 },
      { iteration: 2, content: 'iter2', reasoning: '', tools: [], tool_count: 0 },
    ],
  },
]

async function newContext(browser: Browser) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  await ctx.addInitScript(() => {
    try { localStorage.setItem('xbot-locale', 'zh-CN') } catch { /* ignore */ }
  })
  return ctx
}

/** `activeProgress` 可传 null —— 模拟"切到 busy 会话但 active_progress 快照缺失"。 */
async function setupMock(page: Page, activeProgress: unknown = ACTIVE_PROGRESS): Promise<void> {
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
      sessions: [{ chat_id: CHAT, channel: 'web', label: 'mtp-step-opt', last_active: new Date().toISOString(), running: true }],
      chats: [{ chat_id: CHAT, channel: 'web', label: 'mtp-step-opt', last_active: new Date().toISOString(), isCurrent: true, running: true }],
      orphan_subagents: [],
    } },
  }))
  await page.route('**/api/history', (r) => r.fulfill({
    json: { ok: true, data: {
      messages: HISTORY_MESSAGES,
      chat_id: CHAT,
      channel: 'web',
      last_seq: 10,
      active_progress: activeProgress,
      has_more: false,
      oldest_id: 1,
    } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => {
    const body = r.request().postDataJSON() as { method?: string } | null
    if (body?.method === 'get_active_progress') return r.fulfill({ json: { ok: true, data: activeProgress } })
    return r.fulfill({ json: { ok: true, data: null } })
  })
}

async function login(page: Page): Promise<void> {
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  // 等历史渲染完成（用历史正文，而不是 live tool pill —— active_progress 缺失时
  // 没有 pill）。
  await expect(
    page.locator('[data-message-list-content]'),
    '历史必须渲染（login 完成的前置）',
  ).toContainText('继续接线', { timeout: 30000 })
}

/** 用户可见契约的判据：输入框 = cancel（busy） ⟹ 列表尾部必须有进行中信号。 */
async function expectBusyImpliesInProgress(page: Page, label: string): Promise<void> {
  const stop = page.getByRole('button', { name: /停止|Stop|Cancel|取消/i }).first()
  await expect(stop, `${label}：前提 —— busy ⇒ 输入框是停止（cancel）按钮`).toBeVisible({ timeout: 10000 })
  // 进行中信号：live 行自身的 sweep 指示器，或尾部 busy 占位符（思考中…）。
  await expect(
    page.locator('[data-message-list-content] .sweep-text'),
    `${label}：busy ⟹ 列表必须有可见的进行中信号（不得渲染成 idle 内容）`,
  ).not.toHaveCount(0)
}

test('busy 会话：frozen 行不得吞掉进行中信号（用户 2026-09-19 现场）', async ({ browser }) => {
  seqCounter = 10
  const ctx = await newContext(browser)
  const page = await ctx.newPage()
  await setupMock(page)
  await login(page)

  // 会话在跑 + turn 1 流式（live 行 + live 进度）。
  await emitSSE(page, 'session', { chat_id: CHAT, session: { action: 'busy', chat_id: CHAT } })
  await emitSSE(page, 'progress_structured', {
    chat_id: CHAT,
    progress: {
      chat_id: `web:${CHAT}`, phase: 'turn_started', turn_id: 1, seq: ++seqCounter,
      turn_start: { trigger: 'user', content: '继续接线', request_id: null },
    },
  })
  await emitStream(page, '进行中 A')
  await expectBusyImpliesInProgress(page, '初始 live')

  // ★ 制造 **frozen 行**（本 bug 的关键形态）：先让会话状态回到 idle
  // （agent-idle 是 useSessionStore 的 running 清除通道 ⇒ ChatStore 的
  // sessionRunning 也变 false），此时到达的 coarse session(idle) 才会把运行中的
  // turn 定格；随后服务端又报 busy（会话确实在跑）⇒ 输入框 cancel + 列表里是
  // frozen 行。
  // 修复前：frozen 行 isPartial=true ⇒ 占着 liveId ⇒ 它拿到的是空 liveProgress
  //（activeTurn===null ⇒ EMPTY）自身无信号，busy 占位符又被 `liveId===null` 挡掉
  // ⇒ 「cancel + 完全没有进行中信号」（用户截图现场）。
  await page.evaluate(() => {
    window.dispatchEvent(new CustomEvent('agent-idle', { detail: { chatID: 'chat-1', channel: 'web' } }))
  })
  await emitSSE(page, 'session', { chat_id: CHAT, session: { action: 'idle', chat_id: CHAT } })
  await emitSSE(page, 'session', { chat_id: CHAT, session: { action: 'busy', chat_id: CHAT } })
  await expectBusyImpliesInProgress(page, 'frozen 行 + busy')

  // （frozen 之后的流式"内容继续更新"由 `mobile-frozen-live-revive.spec.ts` 覆盖；
  //  本 spec 只锁用户契约：cancel ⟹ 必须有可见的进行中信号。）
  await ctx.close()
})

test('切到 busy 会话且 active_progress 缺失：历史是 committed，但尾部必须有进行中信号', async ({ browser }) => {
  seqCounter = 10
  const ctx = await newContext(browser)
  const page = await ctx.newPage()
  // active_progress = null：服务端快照缺失（切会话竞态）—— 历史全是 committed 行。
  await setupMock(page, null)
  await login(page)

  // 会话 running（服务端权威）⇒ 输入框 cancel；列表只有已提交历史。
  await emitSSE(page, 'session', { chat_id: CHAT, session: { action: 'busy', chat_id: CHAT } })
  await expectBusyImpliesInProgress(page, 'active_progress 缺失的 busy 会话')

  // 随后到达的流式事件必须让内容继续更新（live 不被遮蔽）。
  await emitStream(page, '恢复的进行中内容')
  await expect(page.locator('[data-message-list-content]')).toContainText('恢复的进行中内容')
  await ctx.close()
})
