import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * AskUser 跨客户端失效回归（2026-09-16 用户报告）：
 *   「askuser 还是有时候走前端缓存，不该弹的时候弹出」
 *   「askuser 必须是会话级别的状态且后端保存，和 busy 互斥。并且多 channel 同步」
 *
 * 被钉死的契约（本文件就是判别实验）：
 *   ① 同一会话的多客户端共享同一 AskUser：一个客户端解答/取消后，**另一个客户端
 *      无需刷新**即收起面板 —— 服务端广播 ask_user_resolved（session-level 失效）。
 *   ② busy ⇒ 不存在 AskUser：会话进入 busy（新回合开始）后，任何陈旧面板必须收起。
 *
 * 判别力：把前端 `ask_user_resolved` handler（useSessionStore.ts 的对应 useEffect）
 * 删掉/短路，用例 1、2 必红；把 busy 分支的 dropAskUserPrompt 去掉，用例 3 必红。
 *
 * 说明：SSE 由 addInitScript 注入的 MockEventSource 驱动（沿用 askuser-iterations.spec.ts
 * 的既有做法），api/* 全部 route mock ⇒ 本用例**不触任何真实后端/数据库**。
 */

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

const QUESTION = 'Do you want to proceed?'

/** seq 是**每客户端**的 SSE 游标：必须按 page 独立计数，否则双页场景下
 *  第二个页面的首个事件会带上 >1 的 seq，被客户端的序号连续性校验丢弃
 *  （表现为"面板根本不出现"——本用例曾因此假红）。 */
const seqByPage = new WeakMap<Page, number>()

async function emitSSE(page: Page, type: string, data: Record<string, unknown>) {
  const seq = (seqByPage.get(page) ?? 0) + 1
  seqByPage.set(page, seq)
  await page.evaluate(
    ({ type, data, seq }) => {
      const w = window as unknown as SSEMockState
      const handlers = w.__sseListeners?.[type]
      if (!handlers) return
      const ev = new MessageEvent(type, { data: JSON.stringify({ ...data, seq }) })
      handlers.forEach((h) => h(ev))
    },
    { type, data, seq },
  )
}

/** 造一个带可控 SSE mock 的客户端页面（登录 + mock 全部 REST）。
 *  opts.pendingAsk：RPC get_pending_ask_user 的答复内容（默认 null = 无 pending）。
 *  用于**推送整条丢失**的判别实验（面板只能靠服务端查询恢复）。 */
async function newClient(
  browser: import('@playwright/test').Browser,
  opts: { pendingAsk?: unknown } = {},
): Promise<Page> {
  const { pendingAsk = null } = opts
  const page = await browser.newPage()
  await page.addInitScript(() => {
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
    const w = window as unknown as SSEMockState
    w.__sseListeners = listeners
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
      close() {
        for (const key of Object.keys(listeners)) listeners[key].clear()
      }
    }
    ;(window as unknown as { EventSource: typeof MockEventSource }).EventSource = MockEventSource
  })

  const now = new Date().toISOString()
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: {
          sessions: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: now }],
          chats: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: now }],
          orphan_subagents: [],
        },
      },
    }),
  )
  await page.route('**/api/history', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: {
          messages: [
            { role: 'user', content: 'Message 1', seq: 1, timestamp: now },
            { role: 'assistant', content: 'Message 2', seq: 2, timestamp: now },
          ],
          chat_id: 'chat-1',
          last_seq: 2,
          active_progress: null,
        },
      },
    }),
  )
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) =>
    r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }),
  )
  await page.route('**/api/rpc', (r) => {
    const body = r.request().postDataJSON() as { method?: string } | null
    if (body?.method === 'get_pending_ask_user') {
      return r.fulfill({ json: { ok: true, data: pendingAsk } })
    }
    return r.fulfill({ json: { ok: true, data: null } })
  })
  await page.route('**/api/ask_user/respond', (r) => r.fulfill({ json: { ok: true, data: {} } }))

  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForFunction(() => document.body.textContent?.includes('Message 2'), { timeout: 10_000 })
  // 确定性等待：mock SSE 客户端必须**已订阅**再 emit，否则事件丢失
  // （双页场景下这条曾导致面板根本不出现 —— 属本用例的时序缺陷，不是产品缺陷）。
  await page.waitForFunction(
    () => {
      const w = window as unknown as SSEMockState
      return !!w.__sseListeners?.['ask_user']
    },
    { timeout: 10_000 },
  )
  await page.waitForTimeout(200)
  return page
}

const ASK_USER_EVENT = {
  type: 'ask_user',
  progress: {
    questions: [{ question: QUESTION, options: ['yes', 'no'] }],
    request_id: 'ask-1',
    chat_id: 'web:chat-1',
  },
}

/** 服务端在 pending 解除时广播的失效事件（protocol.AskUserResolvedEvent，扁平字段）。 */
function resolvedEvent(reason: 'answered' | 'cancelled' | 'rewound' | 'cleared') {
  return {
    type: 'ask_user_resolved',
    channel: 'web',
    chat_id: 'chat-1',
    request_id: 'ask-1',
    reason,
  }
}

test.describe('AskUser 跨客户端失效 / busy 互斥', () => {
  // 无 beforeEach 需要重置：SSE seq 由 `seqByPage`（WeakMap）**按页独立**计数，
  // 每个新 Page 自动从 1 开始（跨页共享计数曾让第二个页面的事件被序号校验丢弃）。

  test('① 另一客户端应答 ⇒ 本客户端面板立即收起（无需刷新）', async ({ browser }) => {
    const a = await newClient(browser)
    const b = await newClient(browser)

    await emitSSE(a, 'ask_user', ASK_USER_EVENT)
    await emitSSE(b, 'ask_user', ASK_USER_EVENT)
    await expect(b.getByText(QUESTION)).toBeVisible({ timeout: 5_000 })

    // A 客户端回答了问题 ⇒ 服务端把"该 prompt 已不再 pending"广播给会话内所有客户端。
    await emitSSE(b, 'ask_user_resolved', resolvedEvent('answered'))

    await expect(b.getByText(QUESTION)).toHaveCount(0, { timeout: 5_000 })
    await a.close()
    await b.close()
  })

  test('② 另一客户端取消 ⇒ 本客户端面板立即收起（旧实现只有 idle，不会收起）', async ({ browser }) => {
    const b = await newClient(browser)
    await emitSSE(b, 'ask_user', ASK_USER_EVENT)
    await expect(b.getByText(QUESTION)).toBeVisible({ timeout: 5_000 })

    await emitSSE(b, 'ask_user_resolved', resolvedEvent('cancelled'))
    await expect(b.getByText(QUESTION)).toHaveCount(0, { timeout: 5_000 })
    await b.close()
  })

  test('③ busy ⇒ 不存在 AskUser：陈旧面板必须收起', async ({ browser }) => {
    const b = await newClient(browser)
    await emitSSE(b, 'ask_user', ASK_USER_EVENT)
    await expect(b.getByText(QUESTION)).toBeVisible({ timeout: 5_000 })

    // 会话进入 busy（新回合开始）⇒ 按契约不存在 pending。
    await emitSSE(b, 'session', {
      type: 'session',
      session: { action: 'busy', chat_id: 'chat-1', channel: 'web' },
    })
    await expect(b.getByText(QUESTION)).toHaveCount(0, { timeout: 5_000 })
    await b.close()
  })

  test('④ 重复投递 resolved 幂等（repeat-safe），且不影响后续新问题', async ({ browser }) => {
    const b = await newClient(browser)
    await emitSSE(b, 'ask_user', ASK_USER_EVENT)
    await expect(b.getByText(QUESTION)).toBeVisible({ timeout: 5_000 })

    await emitSSE(b, 'ask_user_resolved', resolvedEvent('answered'))
    await emitSSE(b, 'ask_user_resolved', resolvedEvent('answered'))
    await expect(b.getByText(QUESTION)).toHaveCount(0, { timeout: 5_000 })

    // 新的问题（新 request_id）仍必须能弹出面板 —— 失效是 per-request，不是"永久静音"。
    await emitSSE(b, 'ask_user', {
      type: 'ask_user',
      progress: {
        questions: [{ question: 'A brand new question?', options: ['yes', 'no'] }],
        request_id: 'ask-2',
        chat_id: 'web:chat-1',
      },
    })
    await expect(b.getByText('A brand new question?')).toBeVisible({ timeout: 5_000 })
    await b.close()
  })
})

/**
 * ③ 回归（2026-09-17 用户报告「任何情况下提问面板都无法正常显示」）：
 *
 * 旧后端（运行中的二进制）在 WaitingUser 暂停期把该行报成 `running`（不是新的
 * `waiting_input`）；而我 PR 新增的 `reconcileAskUserPrompts` 会**按行状态删掉**
 * 本地缓存的 prompt ⇒ 任何一次 refresh（visibilitychange / SSE resync / 切会话）
 * 都把刚到的 prompt 删掉 ⇒ 面板永不显示。
 *
 * 契约：**前端不得用行状态推断"没有 pending ask"** —— 解除只认
 * `ask_user_resolved` 广播与本地作答/取消。
 *
 * 判别力：把 `reconcileAskUserPrompts`（按 status 删除）加回去 ⇒ 本用例必红。
 */
test('③ 行状态为 running（旧后端）+ refresh ⇒ 面板必须仍在', async ({ browser }) => {
  // 服务端答复保持"仍 pending"（真实后端在 WaitingUser 暂停期就是这个状态）：
  // 本用例只检验「row status 不得作为删除依据」，与 RPC 权威路径互不干扰。
  const page = await newClient(browser, {
    pendingAsk: { request_id: 'ask-1', questions: [{ question: QUESTION, options: ['yes', 'no'] }] },
  })
  // 复现旧后端：该会话行 status=running / running=true（WaitingUser 暂停期仍报 running）
  await page.route('**/api/session-tree', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: {
          sessions: [
            { chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString(), status: 'running', running: true },
          ],
          chats: [
            { chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString(), status: 'running', running: true },
          ],
          orphan_subagents: [],
        },
      },
    }),
  )

  await emitSSE(page, 'ask_user', ASK_USER_EVENT)
  await expect(page.getByText(QUESTION)).toBeVisible({ timeout: 5_000 })
  await page.screenshot({ path: '/tmp/askuser-panel/01-before-refresh.png', fullPage: true })

  // 触发一次 refresh（store 的 reconcile 入口之一）：visibilitychange。
  // 确定性等待权威查询的 RPC 返回，不用固定 sleep（抖动来源）。
  const rpcDone = page.waitForResponse(
    (r) => r.url().includes('/api/rpc') && (r.request().postData() ?? '').includes('get_pending_ask_user'),
  )
  await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))
  await rpcDone

  // ⛔ 面板必须仍在（旧代码：prompt 被 status 判定删除 ⇒ 面板消失）
  await expect(page.getByText(QUESTION)).toBeVisible({ timeout: 5_000 })
  await page.screenshot({ path: '/tmp/askuser-panel/02-after-refresh.png', fullPage: true })

  // 权威解除仍然生效（契约未变）
  await emitSSE(page, 'ask_user_resolved', resolvedEvent('answered'))
  await expect(page.getByText(QUESTION)).toHaveCount(0, { timeout: 5_000 })
  await page.close()
})

/**
 * ④ 回归（2026-09-17 报告「任何情况下提问面板都无法正常显示」的**根本形态**）：
 *
 * 实时推送是 best-effort —— 提问发生在客户端**没有订阅**那个窗口时（切会话、
 * SSE 重建、息屏、页面刚打开），`ask_user` 事件整条丢失，本地 Map 里永远不会有
 * 该 prompt，面板遂永久空白。面板状态的唯一权威在服务端，前端必须**主动查询**
 * （RPC get_pending_ask_user —— 与实时发布同一条 loadPendingAskUserEntry 路径）。
 *
 * 判别力：删掉 AgentPanel 里调用 queryPendingAskUser 的 effect ⇒ 本用例必红
 * （面板根本不出现）。
 */
test('④ 实时推送整条丢失 ⇒ 服务端查询（get_pending_ask_user）必须拉起面板', async ({ browser }) => {
  // ⛔ 全程不 emit 任何 ask_user SSE：模拟"推送丢在未订阅窗口"。
  const page = await newClient(browser, {
    pendingAsk: {
      request_id: 'ask-lost',
      questions: [{ question: QUESTION, options: ['yes', 'no'], allow_other: true }],
    },
  })
  await expect(page.getByText(QUESTION)).toBeVisible({ timeout: 10_000 })
  // 选项照常渲染（与实时路径同一套面板）。
  await expect(page.getByText('yes', { exact: true })).toBeVisible()
  await page.screenshot({ path: '/tmp/askuser-panel/04-rpc-recovered.png', fullPage: true })
  await page.close()
})

/**
 * ⑤ 服务端答复即权威（反方向）：RPC 说没有 pending ⇒ 陈旧面板必须收起。
 *
 * 判别力：把 queryPendingAskUser 里 `if (!pending) dropAskUserPrompt(key)` 删掉
 * ⇒ 本用例必红。
 */
test('⑤ 服务端说没有 pending ⇒ 权威答复让陈旧面板收起', async ({ browser }) => {
  const page = await newClient(browser) // pendingAsk 默认 null
  await emitSSE(page, 'ask_user', ASK_USER_EVENT)
  await expect(page.getByText(QUESTION)).toBeVisible({ timeout: 5_000 })

  // 触发一次权威重查（visibilitychange 是 effect 的触发点之一）。
  await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))
  await expect(page.getByText(QUESTION)).toHaveCount(0, { timeout: 5_000 })
  await page.close()
})
