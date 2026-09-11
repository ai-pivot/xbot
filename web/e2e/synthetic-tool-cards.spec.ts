import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E: injected (synthetic) system notifications render as structured cards.
 *
 * The backend injects bg-task / sub-agent completion, cron fires, etc. as fake
 * tool-call pairs and ships a UI-only payload in tool_hints
 * (tools.SyntheticToolHints — never sent to the model). The web card must show
 * the ORIGINAL task/command + status + duration + preview instead of dumping
 * the notification text, and must degrade gracefully for legacy rows that have
 * no payload.
 */

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

let seqCounter = 0

async function emitSSE(page: Page, type: string, data: Record<string, unknown>) {
  await page.evaluate(({ type, data, seq }) => {
    const w = window as unknown as SSEMockState
    const handlers = w.__sseListeners?.[type] as Set<(ev: MessageEvent) => void> | undefined
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

test.describe('Injected (synthetic) tool cards', () => {
  test.beforeEach(() => { seqCounter = 0 })

  test('bg-task and sub-agent completion cards show the ORIGINAL task', async ({ browser }) => {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })

    await page.addInitScript(() => {
      const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
      ;(window as unknown as { __sseListeners: typeof listeners }).__sseListeners = listeners
      class M {
        readyState = 1
        onopen: ((e: Event) => void) | null = null
        onerror: ((e: Event) => void) | null = null
        constructor(public url: string) { setTimeout(() => this.onopen?.(new Event('open')), 0) }
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

    const structured = (p: Record<string, unknown>) =>
      emitSSE(page, 'progress_structured', { type: 'progress_structured', progress: { chat_id: 'web:chat-1', ...p } })

    await structured({ phase: 'turn_started', turn_id: 1, turn_start: { trigger: 'user', content: '跑一下构建' } })
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })

    // Iteration 1 completes carrying the injected notifications.
    // NOTE: completed tools are rendered from `iteration_history` (the committed
    // iteration), not from `completed_tools` — see chat/integrate.ts:235.
    await structured({
      phase: 'tool_exec',
      iteration: 1,
      turn_id: 1,
      iteration_history: [
        {
          iteration: 1,
          content: '',
          reasoning: '',
          tools: [
            {
              name: 'background_task_result',
              label: 'bg:3f8f492a',
              status: 'done',
              elapsed_ms: 1234,
              summary: '背景任务 3f8f492a · done',
              tool_hints: JSON.stringify({
                kind: 'bg_task', task_id: '3f8f492a', task: 'make build -j8',
                status: 'done', exit_code: 0, elapsed_ms: 1234,
                output: 'go build ./...\nbuilt 3 targets in 1.2s',
              }),
              iteration: 1,
            },
            {
              name: 'bg_subagent_completed',
              label: 'bgsub:explore/mem-1',
              status: 'done',
              summary: '子代理 explore/mem-1 已完成',
              tool_hints: JSON.stringify({
                kind: 'subagent', role: 'explore', instance: 'mem-1',
                task: '找出登录流程的入口并在文档里标注', status: 'done', elapsed_ms: 42000,
                output: '入口在 channel/web/web_auth.go:handleLogin',
              }),
              iteration: 1,
            },
            {
              name: 'cron_fired',
              label: 'cron',
              status: 'done',
              summary: '定时任务已触发',
              tool_hints: JSON.stringify({ kind: 'cron', message: 'nightly build 检查' }),
              iteration: 1,
            },
          ],
        },
      ],
    })
    await page.waitForTimeout(600)

    // ── assertions ──
    // Iteration tools render as compact pills; the fancy card is the pill's
    // click-through detail (the design's pill → detail interaction).
    const bgPill = page.locator('[data-testid="tool-pill"]', { hasText: 'background_task_result' }).first()
    await expect(bgPill).toBeVisible()
    await bgPill.click()
    await page.waitForTimeout(400)

    // The card must show the ORIGINAL command, its exit code and the preview.
    await expect(page.getByText('make build -j8')).toBeVisible()
    await expect(page.getByText(/built 3 targets/)).toBeVisible()
    await page.screenshot({ path: 'test-results/synthetic-bgtask-card.png', fullPage: false })

    // Sub-agent card: role/instance + the ORIGINAL task it was spawned with.
    const subPill = page.locator('[data-testid="tool-pill"]', { hasText: 'bg_subagent_completed' }).first()
    await subPill.click()
    await page.waitForTimeout(400)
    await expect(page.getByText('找出登录流程的入口并在文档里标注')).toBeVisible()
    await expect(page.getByText(/explore\/mem-1/).first()).toBeVisible()

    // Cron card: the fired message.
    const cronPill = page.locator('[data-testid="tool-pill"]', { hasText: 'cron_fired' }).first()
    await cronPill.click()
    await page.waitForTimeout(400)
    await expect(page.getByText('nightly build 检查')).toBeVisible()

    await page.screenshot({ path: 'test-results/synthetic-tool-cards.png', fullPage: false })
  })
})
