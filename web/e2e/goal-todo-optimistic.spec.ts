import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E: 会话级编辑（goal / todos）必须**立即生效**，不能等后端 push；且覆盖必须
 * 在服务端给出新值时收敛、不得跨会话存活。
 *
 * REPRO（2026-09-12 用户报告）：web 编辑 goal 按 Enter 后 goal 恢复编辑前的内容，
 * 刷新才生效。根因：AgentPanel 的显示优先级写反（`snapshot.goal ?? override` 让
 * 旧快照压过乐观覆盖）+ `if (snapshot.goal)` 过度清除 + todos 无乐观副本。
 *
 * 覆盖（含 CR 复盘补的用例）：
 *  1. 编辑 goal + Enter → 立刻显示新目标（无 push 窗口）；
 *  2. 编辑 todo 文本 + Enter → 立刻显示新文本（无 push 窗口）；
 *  3. 反闪烁：编辑后旧值**永不**重新出现（采样 1s）；
 *  4. push 收敛：后端推新值 / 第三方改值 → 服务端值胜出；
 *  5. clear goal → banner 立刻消失（且不回弹）；
 *  6. 会话切换：A 的未收敛覆盖**不得**出现在 B；
 *  7. RPC 失败：不得留下服务端不存在的幽灵目标（回滚到旧值）；
 *  8. todos 删除：列表立刻少一项。
 */

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

const GOAL_A = '旧目标：修复 PD 分离'
const GOAL_B = '新目标：给 kv-cache 加前缀命中率指标'
const TODO_1 = '旧任务：修复 prefill 超时'
const TODO_2 = '第二个任务：补齐压测脚本'

let seqCounter = 0

async function emitSSE(page: Page, type: string, data: Record<string, unknown>) {
  await page.evaluate(
    ({ type, data, seq }) => {
      const w = window as unknown as SSEMockState
      const handlers = w.__sseListeners?.[type] as Set<(ev: MessageEvent) => void> | undefined
      if (!handlers) return
      const ev = new MessageEvent(type, { data: JSON.stringify({ ...data, seq }) })
      handlers.forEach((h) => h(ev))
    },
    { type, data, seq: ++seqCounter },
  )
}

interface RPCSpy {
  calls: { method: string; params: Record<string, unknown> }[]
}
type PageWithSpy = Page & { __rpc?: RPCSpy }

async function setupMock(page: Page, opts: { pushBack?: boolean; failRPC?: boolean } = {}) {
  const rpcSpy: RPCSpy = { calls: [] }
  ;(page as PageWithSpy).__rpc = rpcSpy

  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: {
          sessions: [
            { chat_id: 'chat-1', channel: 'web', label: 'SessionOne', last_active: new Date().toISOString() },
            { chat_id: 'chat-2', channel: 'web', label: 'SessionTwo', last_active: new Date().toISOString() },
          ],
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
  await page.route('**/api/rpc', async (r) => {
    let body: { method?: string; params?: Record<string, unknown> } = {}
    try {
      body = r.request().postDataJSON() as typeof body
    } catch {
      /* ignore */
    }
    if (body.method) rpcSpy.calls.push({ method: body.method, params: body.params ?? {} })
    if (opts.failRPC) {
      await r.fulfill({ json: { ok: false, error: 'boom' } })
      return
    }
    await r.fulfill({ json: { ok: true, data: { ok: true } } })
    if (!opts.pushBack) return
    if (body.method === 'set_goal') {
      await emitSSE(page, 'progress_structured', {
        type: 'progress_structured',
        progress: { chat_id: 'web:chat-1', phase: '', goal: { objective: body.params?.objective, status: 'active' } },
      })
    }
  })
  return rpcSpy
}

async function bootAndSeed(page: Page, opts: { pushBack?: boolean; failRPC?: boolean } = {}) {
  const spy = await setupMock(page, opts)

  await page.addInitScript(() => {
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
    ;(window as unknown as { __sseListeners: typeof listeners }).__sseListeners = listeners
    class M {
      readyState = 1
      onopen: ((e: Event) => void) | null = null
      onerror: ((e: Event) => void) | null = null
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
    ;(window as unknown as { EventSource: typeof M }).EventSource = M
  })

  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForTimeout(2000)

  const structured = (p: Record<string, unknown>) =>
    emitSSE(page, 'progress_structured', { type: 'progress_structured', progress: { chat_id: 'web:chat-1', ...p } })

  await structured({ phase: 'turn_started', turn_id: 7, turn_start: { trigger: 'user', content: '继续' } })
  await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
  await structured({
    phase: 'tool_exec',
    iteration: 1,
    turn_id: 7,
    todos: [
      { text: TODO_1, status: 'doing' },
      { text: TODO_2, status: 'pending' },
    ],
    goal: { objective: GOAL_A, status: 'active' },
  })
  await page.waitForTimeout(600)
  return { spy, structured }
}

/** 采样 banner 文本 1s，断言旧值从未出现（反闪烁）。 */
async function assertNeverShows(page: Page, text: string, ms = 1000) {
  const deadline = Date.now() + ms
  while (Date.now() < deadline) {
    const n = await page.getByText(text).count()
    expect(n, `旧值 "${text}" 不得重新出现`).toBe(0)
    await page.waitForTimeout(100)
  }
}

test.describe('会话级编辑必须立即生效（不等 push）', () => {
  test.beforeEach(() => {
    seqCounter = 0
  })

  test('REPRO: 编辑 goal 按 Enter → 立刻显示新目标（无 push）', async ({ browser }) => {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
    const { spy } = await bootAndSeed(page, { pushBack: false })

    await expect(page.getByText(GOAL_A)).toBeVisible()
    await page.getByTestId('goal-text').click()
    const input = page.getByTestId('goal-edit-input')
    await expect(input).toBeVisible()
    await input.fill(GOAL_B)
    await input.press('Enter')

    await expect(page.getByText(GOAL_B)).toBeVisible({ timeout: 2000 })
    await expect(page.getByText(GOAL_A)).toHaveCount(0)
    // 反闪烁：旧值不得在随后 1s 内重新出现
    await assertNeverShows(page, GOAL_A)
    expect(spy.calls.some((c) => c.method === 'set_goal' && c.params.objective === GOAL_B)).toBe(true)
  })

  test('REPRO: 编辑 todo 文本按 Enter → 立刻显示新文本（无 push）', async ({ browser }) => {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
    const { spy } = await bootAndSeed(page, { pushBack: false })

    await page.getByTestId('todo-toggle').click()
    await page.waitForTimeout(400)
    await expect(page.getByTestId('todo-item')).toHaveCount(2)

    const EDITED = '改过的任务文本'
    await page.getByTestId('todo-text').first().click()
    const input = page.getByTestId('todo-edit-input')
    await expect(input).toBeVisible()
    await input.fill(EDITED)
    await input.press('Enter')

    await expect(page.getByTestId('todo-text').first()).toHaveText(EDITED, { timeout: 2000 })
    await assertNeverShows(page, TODO_1)
    expect(spy.calls.some((c) => c.method === 'set_todos')).toBe(true)
  })

  test('todos 删除一项 → 列表立刻少一项（无 push）', async ({ browser }) => {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
    await bootAndSeed(page, { pushBack: false })

    await page.getByTestId('todo-toggle').click()
    await page.waitForTimeout(400)
    await expect(page.getByTestId('todo-item')).toHaveCount(2)

    await page.getByTestId('todo-item').first().hover()
    await page.waitForTimeout(300)
    await page.getByTestId('todo-delete').first().click()

    await expect(page.getByTestId('todo-item')).toHaveCount(1, { timeout: 2000 })
    await expect(page.getByTestId('todo-text').first()).toHaveText(TODO_2)
  })

  test('push 收敛：后端推新值 / 第三方改值 → 服务端值胜出', async ({ browser }) => {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
    await bootAndSeed(page, { pushBack: true })

    await page.getByTestId('goal-text').click()
    const input = page.getByTestId('goal-edit-input')
    await input.fill(GOAL_B)
    await input.press('Enter')
    await expect(page.getByText(GOAL_B)).toBeVisible({ timeout: 2000 })

    // 第三方（agent）把目标改成 C → 服务端值胜出
    const GOAL_C = 'agent 改成的新目标'
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: { chat_id: 'web:chat-1', phase: '', goal: { objective: GOAL_C, status: 'active' } },
    })
    await expect(page.getByText(GOAL_C)).toBeVisible({ timeout: 2000 })
    await expect(page.getByText(GOAL_B)).toHaveCount(0)
  })

  test('clear goal → banner 立刻消失且不回弹', async ({ browser }) => {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
    const { spy } = await bootAndSeed(page, { pushBack: false })
    await expect(page.getByTestId('goal-text')).toBeVisible()

    await page.getByTestId('goal-clear').click()
    await expect(page.getByTestId('goal-text')).toHaveCount(0, { timeout: 2000 })
    // 无 push 时也不得回弹
    await page.waitForTimeout(1000)
    await expect(page.getByTestId('goal-text')).toHaveCount(0)
    expect(spy.calls.some((c) => c.method === 'clear_goal')).toBe(true)
  })

  test('会话切换：A 的未收敛覆盖不得出现在 B（跨会话串值）', async ({ browser }) => {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
    await bootAndSeed(page, { pushBack: false })

    // A 里编辑 goal（无 push → 覆盖存活）
    await page.getByTestId('goal-text').click()
    const input = page.getByTestId('goal-edit-input')
    await input.fill(GOAL_B)
    await input.press('Enter')
    await expect(page.getByText(GOAL_B)).toBeVisible({ timeout: 2000 })

    // 切到会话 B（无 goal）—— 覆盖必须丢弃，B 不得显示 A 的目标
    await page.getByText('SessionTwo').click()
    await page.waitForTimeout(1500)
    await expect(page.getByText(GOAL_B)).toHaveCount(0)
    await expect(page.getByTestId('goal-text')).toHaveCount(0)
  })

  test('RPC 失败：不得留下幽灵目标（保持旧值）', async ({ browser }) => {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
    await bootAndSeed(page, { failRPC: true })
    await expect(page.getByText(GOAL_A)).toBeVisible()

    await page.getByTestId('goal-text').click()
    const input = page.getByTestId('goal-edit-input')
    await input.fill(GOAL_B)
    await input.press('Enter')

    // 失败 → 不写覆盖：仍是旧目标，且不出现幽灵 B
    await page.waitForTimeout(1200)
    await expect(page.getByText(GOAL_A)).toBeVisible()
    await expect(page.getByText(GOAL_B)).toHaveCount(0)
  })
})
