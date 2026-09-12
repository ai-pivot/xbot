import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E: 会话级编辑（goal / todos）必须**立即生效**，不能等后端 push。
 *
 * REPRO（2026-09-12 用户报告）：web 编辑 goal 按 Enter 后 goal 恢复编辑前的内容，
 * 刷新才生效。根因：AgentPanel 的显示优先级写反 ——
 *   `const goal = progressSnapshot.goal ?? goalOverride`
 * 服务端快照（旧值）压过乐观覆盖（新值），且 `if (progressSnapshot.goal)` 的
 * effect 把乐观值也清掉。后端 push 到达前（或丢失时）用户看到的就是旧值。
 * todos 同类：`handleUpdateTodos` 不留本地副本，只靠 push → push 未到时列表回退。
 *
 * 本 spec 刻意**不**在 RPC 后回推 SSE（模拟真实丢事件/延迟窗口），断言编辑立刻可见；
 * 另外保留一个用例验证"后端 push 到达后仍是新值 / 后续第三方改动会覆盖"。
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

/** 记录 /api/rpc 的请求体（断言 RPC 真的发出去了）。 */
interface RPCSpy {
  calls: { method: string; params: Record<string, unknown> }[]
}

async function setupMock(page: Page, opts: { pushBack: boolean }) {
  const rpcSpy: RPCSpy = { calls: [] }
  ;(page as unknown as { __rpc?: RPCSpy }).__rpc = rpcSpy

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
  await page.route('**/api/rpc', async (r) => {
    let body: { method?: string; params?: Record<string, unknown> } = {}
    try {
      body = r.request().postDataJSON() as typeof body
    } catch {
      /* ignore */
    }
    if (body.method) rpcSpy.calls.push({ method: body.method, params: body.params ?? {} })
    await r.fulfill({ json: { ok: true, data: { ok: true } } })
    // 真实后端会 push；这里只在 pushBack=true 时模拟（另一用例覆盖 push 收敛）。
    if (!opts.pushBack) return
    if (body.method === 'set_goal') {
      await emitSSE(page, 'progress_structured', {
        type: 'progress_structured',
        progress: {
          chat_id: 'web:chat-1',
          phase: '',
          goal: { objective: body.params?.objective, status: 'active' },
        },
      })
    }
  })
  return rpcSpy
}

async function bootAndSeed(page: Page, opts: { pushBack: boolean }) {
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

test.describe('会话级编辑必须立即生效（不等 push）', () => {
  test.beforeEach(() => {
    seqCounter = 0
  })

  test('REPRO: 编辑 goal 按 Enter → 立刻显示新目标（无 push）', async ({ browser }) => {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
    const { spy } = await bootAndSeed(page, { pushBack: false })

    // 旧目标可见
    await expect(page.getByText(GOAL_A)).toBeVisible()

    // 点文本进入编辑 → 全选替换 → Enter
    await page.getByTestId('goal-text').click()
    const input = page.getByTestId('goal-edit-input')
    await expect(input).toBeVisible()
    await input.fill(GOAL_B)
    await input.press('Enter')

    // 关键断言：**没有**任何后端 push，也必须立刻显示新目标
    await expect(page.getByText(GOAL_B)).toBeVisible({ timeout: 2000 })
    await expect(page.getByText(GOAL_A)).toHaveCount(0)

    // RPC 确实发出（带新目标）
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

    // 关键断言：无 push 也要立刻显示新文本
    await expect(page.getByTestId('todo-text').first()).toHaveText(EDITED, { timeout: 2000 })
    expect(spy.calls.some((c) => c.method === 'set_todos')).toBe(true)
  })

  test('后端 push 到达后仍显示新目标（乐观值被服务端值收敛）', async ({ browser }) => {
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
  })
})
