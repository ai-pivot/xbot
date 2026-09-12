import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E（手机 / 触屏）：TODO 工具条的行操作**不能依赖 hover**。
 *
 * 问题：操作按钮（设为 goal / 编辑 / 删除）原本是 `opacity-0 group-hover:opacity-100`
 * —— 桌面鼠标可用，但触屏没有 hover 态，手机用户永远看不到、点不到这三个按钮。
 * 修复：`useIsTouch()` 为真时常显，并把命中区从 20px 放大到 32px。
 *
 * 断言（真实浏览器 + 触摸设备上下文）：
 *   - `matchMedia('(hover: none) and (pointer: coarse)')` 为真（确实在模拟触屏）
 *   - `todo-actions` 的 computed opacity === 1（未做任何 hover）
 *   - 三个操作按钮 boundingBox ≥ 32px
 *   - 直接 tap 编辑 → 输入框出现并可保存（无 hover 步骤）
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
  // 触摸设备上下文：没有 hover 能力（iPhone 尺寸）
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  test.beforeEach(() => {
    seqCounter = 0
  })

  test('row actions are visible without hover, are touch-sized, and are tappable', async ({ browser }) => {
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

    // 先确认我们确实处于「无 hover」的触屏环境（否则本用例没有意义）
    const hoverNone = await page.evaluate(
      () => window.matchMedia('(hover: none) and (pointer: coarse)').matches,
    )
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

    // ① 未 hover：操作区必须已可见（opacity 1）
    const opacity = await page
      .getByTestId('todo-actions')
      .first()
      .evaluate((el) => getComputedStyle(el).opacity)
    expect(opacity, 'touch: row actions must not be hover-gated (opacity:0)').toBe('1')

    // ② 触控目标 ≥ 32px（原来是 20px，手指点不准）
    for (const id of ['todo-set-goal', 'todo-edit', 'todo-delete']) {
      const box = await page.getByTestId(id).first().boundingBox()
      expect(box, `${id} must be rendered`).not.toBeNull()
      expect(Math.min(box!.width, box!.height), `${id} touch target too small`).toBeGreaterThanOrEqual(32)
    }
    await page.screenshot({ path: 'test-results/todo-mobile-actions.png', fullPage: false })

    // ③ 直接 tap 编辑（没有任何 hover 步骤）→ 输入框出现 → 保存生效
    await page.getByTestId('todo-edit').first().tap()
    const input = page.getByTestId('todo-edit-input')
    await expect(input).toBeVisible()
    await expect(input).toHaveValue('修复 PD 分离下的 prefill 超时')
    await input.fill('修复 PD 分离下的 prefill 超时（手机端编辑）')
    await input.press('Enter')
    await expect(page.getByTestId('todo-edit-input')).toHaveCount(0)
    await expect(page.getByTestId('todo-text').first()).toHaveText('修复 PD 分离下的 prefill 超时（手机端编辑）')
    await page.screenshot({ path: 'test-results/todo-mobile-edited.png', fullPage: false })

    // ④ tap 状态切换（命中区放大后依然可用）
    await page.getByTestId('todo-status').nth(1).tap()
    await page.waitForTimeout(300)
    await expect(page.getByTestId('todo-item').nth(1)).toBeVisible()
  })
})
