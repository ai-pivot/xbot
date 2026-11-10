import { test, expect, type Page } from '@playwright/test'

import { askRow, bootStructured } from './helpers/structured-mock'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * 回归守护（2026-09-20 CI 9 个 spec 全红的根因；2026-10-22 按新架构重写）：
 * **ask_user 载荷不含任何问题时，不得被当成一个"提问"渲染出来。**
 *
 * 现场（旧架构）：`goal-todo-optimistic` / `todo-edit-goal` / `todo-mobile` 都用通用
 * `/api/rpc` 通配路由 mock 回 `{ok:true, data:{ok:true}}`。AskUser 的 DB 水合 effect
 * （AgentPanel）拿到这个真值后合成为 `{requestId, questions: []}` ⇒ `AskUserPanel`
 * 以 `questions[0] === undefined` 渲染 ⇒ 读 `.allowOther` 抛异常 ⇒ 崩溃边界把整块
 * 面板换成 `#crash-overlay` ⇒ goal banner / todo 面板全部不在 DOM ⇒ 那些 spec 永远
 * 找不到元素（60s 超时型失败，一次红 9 个）。
 *
 * 新架构（本分支 514ec7b3）：`AskUserPanel` 与其水合路径已删除，ask_user 由
 * **结构化消息** + `AskUserCard` 渲染（`web/src/structured/StructuredCards.tsx`），
 * 页面在 `/structured`。原契约原样搬到新组件上：
 *   ① 没有问题时 ⇒ 不渲染卡片、应用不崩、消息流其余部分照常；
 *   ② 真·提问（payload.questions 非空）⇒ 卡片照常渲染（修复不得把功能一起掐掉）；
 *   ③ 通用 `/api/rpc` mock（`{ok:true}`）仍不得把会话页搞崩（旧崩溃现场的守门）。
 *
 * 判别力：
 *   - 用例 ①：把 `AskUserCard` 的「questions 为空 ⇒ return null」删掉 ⇒ 必红
 *     （出现一张没问题却带提交按钮的空卡片）。这是本文件的主判据。
 *   - 用例 ③：旧的水合崩溃路径已随面板一起删除，所以它现在是**冒烟守卫**（不崩 + 会话页
 *     不得凭空长出 ask_user 控件），不再声称有强变异自证 —— 留着是因为它钉的现场
 *     （通用 `/api/rpc` mock 回 `{ok:true}`）在别的 spec 里仍在复用，一旦有人把 ask_user
 *     水合搬回会话页，这条会立刻把"整块面板被崩溃边界抹掉"暴露出来。
 */

const GOAL = '回归守护：无问题时不得渲染 AskUser 卡片'

test.describe('AskUser 载荷契约：没有问题 ⇒ 不渲染卡片、应用不崩', () => {
  test('① 空 questions 的结构化 ask_user 行不渲染卡片，也不崩', async ({ browser }) => {
    const page = await bootStructured(browser, [
      // 污染行（现场形状：questions 为空 / 缺字段）+ 一条正常事件行（证明消息流没被搞挂）。
      { message_id: 10, kind: 'ask_user', correlation_id: 'bad-1', text: '空问题', payload: { questions: [] } },
      { message_id: 11, kind: 'ask_user', correlation_id: 'bad-2', text: '没有 payload' },
      { message_id: 12, kind: 'event', text: '健康事件：节点心跳正常', payload: { event_type: 'heartbeat' } },
    ])

    await expect(page.locator('#crash-overlay')).toHaveCount(0)
    await expect(page.getByTestId('ask-user-card')).toHaveCount(0)
    // 消息流其余部分照常渲染（崩溃边界没有抹掉整块面板）。
    await expect(page.getByText('健康事件：节点心跳正常')).toBeVisible()
    await page.context().close()
  })

  test('② 真·提问（payload.questions 非空）仍照常渲染卡片 —— 修复不得掐掉功能', async ({ browser }) => {
    const question = '是否按计划今夜扩容？'
    const page = await bootStructured(browser, [
      askRow({ payload: { questions: [{ id: 'q1', question, options: [{ id: 'yes', label: '准' }, { id: 'no', label: '驳' }] }] } }),
    ])

    const card = page.getByTestId('ask-user-card').filter({ hasText: question }).first()
    await expect(card).toBeVisible()
    await expect(card.getByText('准')).toBeVisible()
    await expect(page.locator('#crash-overlay')).toHaveCount(0)
    await page.context().close()
  })

  test('③ 通用 /api/rpc mock（{ok:true}）不得把会话页搞崩，也不得伪造 AskUser 卡片', async ({ browser }) => {
    const page = await bootChat(browser)
    await seedSessionState(page)

    // ① 崩溃覆盖层必须不存在（旧实现读 questions[0].allowOther 抛异常 ⇒ 整块面板被抹）。
    await expect(page.locator('#crash-overlay')).toHaveCount(0)

    // ② 会话页上不得出现 AskUser 卡片（旧 `ask-user-panel` 已随 514ec7b3 删除；
    //    新的交互面是 `/structured` 的 `ask-user-card`，会话页不该有任何 ask_user 控件）。
    await expect(page.getByTestId('ask-user-card')).toHaveCount(0)

    // ③ 面板其余部分照常：goal banner 仍在（这正是当初失败 spec 的定位器）。
    await expect(page.getByTestId('goal-text')).toBeVisible()
    await expect(page.getByTestId('goal-text')).toHaveText(GOAL)
    await page.context().close()
  })
})

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

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

/** 登录 + 用**通用** `/api/rpc` mock（正是 CI 里触发崩溃的那个形状）。 */
async function bootChat(browser: import('@playwright/test').Browser): Promise<Page> {
  const page = await (await browser.newContext({ viewport: { width: 1280, height: 900 } })).newPage()
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
      addEventListener(t: string, h: (ev: MessageEvent) => void) {
        if (!listeners[t]) listeners[t] = new Set()
        listeners[t].add(h)
      }
      removeEventListener() {}
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
  // ⛔ 通用 mock：任何 RPC（含 get_pending_ask_user）都回 {ok:true, data:{ok:true}}。
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: { ok: true } } }))
  await page.route('**/api/messages**', (r) =>
    r.fulfill({ json: { ok: true, data: { items: [], has_more: false, next_cursor: 0 } } }),
  )
  await page.route('**/api/inbox**', (r) =>
    r.fulfill({ json: { ok: true, data: { items: [], has_more: false, next_cursor: 0, unanswered: 0 } } }),
  )

  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForTimeout(2000)
  return page
}

/** 播种会话级状态（goal）—— 与失败 spec 同一路径（SSE progress_structured）。 */
async function seedSessionState(page: Page) {
  await emitSSE(page, 'progress_structured', {
    type: 'progress_structured',
    progress: {
      chat_id: 'web:chat-1',
      phase: 'tool_exec',
      iteration: 1,
      turn_id: 7,
      goal: { objective: GOAL, status: 'active' },
    },
  })
  await page.waitForTimeout(600)
}
