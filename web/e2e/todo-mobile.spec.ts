import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E（手机 / 触屏）：TODO 行操作在手机上必须**不挤占正文**。
 *
 * 迭代 1（被用户否掉）：把三个操作做成 32px 常显按钮 → 3×32+gaps ≈104px，
 *   手机行宽仅 ~390px，正文几乎被挤没（"按钮加一起占的位置快比文本还宽了"）。
 * 迭代 2（当前）：只留一个 ⋯（32px），三个操作收进弹出菜单（h-10 + 文字标签）。
 *
 * 断言：
 *   - 触屏环境：matchMedia('(hover: none) and (pointer: coarse)') === true
 *   - 行内**只有一个**操作按钮（⋯），≥32px，且其右侧不留其他按钮
 *   - **正文宽度占该行 ≥60%**（核心诉求：操作区不许比文本还宽）
 *   - 未点击不渲染菜单；tap ⋯ → 菜单出现，菜单项 ≥40px 且带文字
 *   - 无 hover 步骤直接 tap「编辑」→ 输入框出现并可保存
 */

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

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

async function setupMock(page: Page) {
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
    const body = (() => {
      try {
        return r.request().postDataJSON() as { method?: string; params?: { todos?: unknown } }
      } catch {
        return {} as { method?: string; params?: { todos?: unknown } }
      }
    })()
    await r.fulfill({ json: { ok: true, data: { ok: true } } })
    if (body.method === 'set_todos') {
      await emitSSE(page, 'progress_structured', {
        type: 'progress_structured',
        progress: { chat_id: 'web:chat-1', phase: '', todos: body.params?.todos ?? [] },
      })
    }
  })
}

test.describe('TODO toolbar on touch devices (no hover)', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  test.beforeEach(() => {
    seqCounter = 0
  })

  test('row actions collapse into one ⋯ and never crowd out the text', async ({ browser }) => {
    const page = await browser.newPage({
      viewport: { width: 390, height: 844 },
      hasTouch: true,
      isMobile: true,
    })

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

    await setupMock(page)
    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()
    await page.waitForTimeout(2000)

    // 确认处于「无 hover」的触屏环境（否则本用例无意义）
    const hoverNone = await page.evaluate(() => window.matchMedia('(hover: none) and (pointer: coarse)').matches)
    expect(hoverNone, 'touch context must report (hover: none) and (pointer: coarse)').toBe(true)

    const structured = (p: Record<string, unknown>) =>
      emitSSE(page, 'progress_structured', { type: 'progress_structured', progress: { chat_id: 'web:chat-1', ...p } })

    await structured({ phase: 'turn_started', turn_id: 7, turn_start: { trigger: 'user', content: '继续' } })
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await structured({
      phase: 'tool_exec',
      iteration: 1,
      turn_id: 7,
      todos: [
        { text: '修复 PD 分离下的 prefill 超时', status: 'doing' },
        { text: '给 kv-cache 加前缀命中率指标', status: 'pending' },
        { text: '更新文档', status: 'done' },
      ],
    })
    await page.waitForTimeout(500)

    await page.getByTestId('todo-toggle').click()
    await page.waitForTimeout(400)

    const row = page.getByTestId('todo-item').first()
    const text = page.getByTestId('todo-text').first()
    const more = page.getByTestId('todo-more').first()

    // ① 行内只有「一个」操作按钮（⋯），且是 32px 触控目标
    await expect(page.getByTestId('todo-more')).toHaveCount(3) // 每行一个
    await expect(page.getByTestId('todo-actions')).toHaveCount(0) // 桌面 hover 容器不渲染
    await expect(page.getByTestId('todo-edit')).toHaveCount(0) // 未点击不内联
    await expect(page.getByTestId('todo-delete')).toHaveCount(0)
    const moreBox = await more.boundingBox()
    expect(moreBox).not.toBeNull()
    expect(Math.min(moreBox!.width, moreBox!.height)).toBeGreaterThanOrEqual(32)
    expect(moreBox!.width).toBeLessThanOrEqual(44) // 不能是一个宽按钮

    // ② 正文必须占行宽主导（核心诉求：操作区不许比文本还宽）
    const rowBox = await row.boundingBox()
    const textBox = await text.boundingBox()
    expect(rowBox).not.toBeNull()
    expect(textBox).not.toBeNull()
    const textRatio = textBox!.width / rowBox!.width
    expect(textRatio, `text must dominate the row (got ${(textRatio * 100).toFixed(1)}%)`).toBeGreaterThanOrEqual(0.6)
    await page.screenshot({ path: 'test-results/todo-mobile-row.png' })

    // ③ 未点击不渲染菜单
    await expect(page.getByTestId('todo-actions-menu')).toHaveCount(0)

    // ④ tap ⋯ → 菜单出现：菜单项 ≥40px 且带文字标签（不是光秃秃的图标）
    await more.tap()
    const menu = page.getByTestId('todo-actions-menu')
    await expect(menu).toBeVisible()
    await page.waitForTimeout(250)
    await page.screenshot({ path: 'test-results/todo-mobile-menu.png' })
    for (const id of ['todo-set-goal', 'todo-edit', 'todo-delete']) {
      const item = page.getByTestId(id)
      await expect(item).toBeVisible()
      const box = await item.boundingBox()
      expect(box!.height, `${id} menu row too short`).toBeGreaterThanOrEqual(40)
      expect(((await item.textContent()) ?? '').trim().length).toBeGreaterThan(1)
    }

    // ⑤ 无 hover 步骤直接 tap「编辑」→ 输入框出现 → 保存生效
    await page.getByTestId('todo-edit').tap()
    const input = page.getByTestId('todo-edit-input')
    await expect(input).toBeVisible()
    await expect(input).toHaveValue('修复 PD 分离下的 prefill 超时')
    await input.fill('修复 PD 分离下的 prefill 超时（手机端编辑）')
    await input.press('Enter')
    await expect(page.getByTestId('todo-edit-input')).toHaveCount(0)
    await expect(page.getByTestId('todo-text').first()).toHaveText('修复 PD 分离下的 prefill 超时（手机端编辑）')
    await page.screenshot({ path: 'test-results/todo-mobile-edited.png' })
  })
})
