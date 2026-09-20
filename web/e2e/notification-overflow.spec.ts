import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E（手机 390×844）：系统通知（后台任务命令 + 输出）**不得**让页面横向溢出。
 *
 * 真因（用户给的 DOM 实证）：通知正文是 shell 命令，命令里成对 `$`（`echo A=$?; … $D/x`）
 * 被 remark-math 当数学公式交给 KaTeX，而 KaTeX 的 `.katex-html` 是 `white-space: nowrap`
 * ⇒ 内容不换行，气泡被 `max-w-full` 限住也没用 ⇒ 手机上整页横向溢出。
 *
 * 判据（任一为真即回归）：
 *   ① 通知气泡内出现 .katex / .katex-html（说明正文又被送进 markdown/数学解析）
 *   ② 页面可横向滚动（scrollWidth > innerWidth）——用户看到的"溢出"
 *   ③ 气泡内容宽于自身盒子（scrollWidth > clientWidth）
 */

const CMD = `[System Notification] Background task 7cffe5ac completed.
Command: ssh -o BatchMode=yes ubuntu@43.202.208.136 'export TMPDIR=/opt/dlami/nvme/tmp && cd /opt/dlami/nvme/fc-q12 && cargo build --release --features cuda --bin ferrite-graph > /tmp/q12_cargo2.log 2>&1; echo CARGO_RC=$?; ls -l --time-style=+%H:%M:%S target/release/ferrite-graph; D=/opt/dlami/nvme/dbg/q12; S=$(ls /opt/dlami/nvme/dsv41_mp8/model*-mp8.safetensors | paste -sd,) && env FERRITE_OUT_IDS=$D/oracle.ids ./target/release/ferrite-graph --shards "$S" > $D/oracle.log 2>&1'
Output:
CARGO_RC=127
== 结论：坏跑/不可引用（FAIL）==`

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

let seqCounter = 0

async function emitSSE(page: Page, type: string, data: Record<string, unknown>) {
  await page.evaluate(({ type, data, seq }) => {
    const w = window as unknown as SSEMockState
    const handlers = w.__sseListeners?.[type] as Set<(ev: MessageEvent) => void> | undefined
    if (!handlers) return
    handlers.forEach((h) => h(new MessageEvent(type, { data: JSON.stringify({ ...data, seq }) })))
  }, { type, data, seq: ++seqCounter })
}

async function setupMock(page: Page) {
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) => r.fulfill({
    json: { ok: true, data: {
      sessions: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() }],
      chats: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() }],
      orphan_subagents: [],
    } },
  }))
  await page.route('**/api/history', (r) => r.fulfill({
    json: { ok: true, data: { messages: [], chat_id: 'chat-1', last_seq: 0, active_progress: null } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
  await page.addInitScript(() => {
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
    ;(window as unknown as SSEMockState).__sseListeners = listeners
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
      close() { for (const k of Object.keys(listeners)) listeners[k].clear() }
    }
    ;(window as unknown as { EventSource: typeof MockEventSource }).EventSource = MockEventSource
  })
}

test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

test('手机：系统通知（shell 命令）不得触发整页横向溢出', async ({ page }) => {
  await setupMock(page)
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForTimeout(2000)

  // 通知 user 行：progress_structured 的 turn_started 携带 trigger=notification + 正文
  // （与后端实际下发形状一致，见 e2e/notification-busy.spec.ts）
  await emitSSE(page, 'progress_structured', {
    type: 'progress_structured',
    progress: {
      phase: 'turn_started', turn_id: 7, chat_id: 'web:chat-1',
      turn_start: { trigger: 'notification', content: CMD },
    },
  })
  await page.waitForTimeout(800)

  const bubble = page.locator('[data-testid="user-bubble"]').first()
  await expect(bubble).toBeVisible({ timeout: 5000 })
  // 正文必须原样可见（没被 markdown/数学解析吃掉）
  await expect(bubble).toContainText('echo CARGO_RC=$?')

  const m = await page.evaluate(() => {
    const b = document.querySelector('[data-testid="user-bubble"]') as HTMLElement | null
    const scroller = document.scrollingElement as HTMLElement
    return {
      katex: document.querySelectorAll('[data-testid="user-bubble"] .katex, [data-testid="user-bubble"] .katex-html').length,
      pageOverflow: scroller.scrollWidth - window.innerWidth,
      bubbleOverflow: b ? b.scrollWidth - b.clientWidth : -1,
    }
  })
  expect(m.katex, '通知正文不得出现 KaTeX（.katex-html 是 nowrap ⇒ 必溢出）').toBe(0)
  expect(m.pageOverflow, '页面不得横向溢出').toBeLessThanOrEqual(1)
  expect(m.bubbleOverflow, '气泡内容不得宽于自身盒子').toBeLessThanOrEqual(1)
})
