import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E（手机 390×844）：**系统通知**（后台任务/子代理完成通知）里含
 * 「超长不可断 token」（长路径、长 URL、`|`/`///` 混排、CJK+ASCII 混排）时，
 * 通知气泡**不得**宽于视口、不得把页面撑出横向滚动、不得溢出自身盒子。
 *
 * 用户报告（截图）：通知消息气泡比视口宽，左右两侧都被裁（文字从中间开始、右侧被切）。
 *
 * 判据（全部几何，不看颜色/文案）：
 *   ① 气泡 right ≤ window.innerWidth（不越界）
 *   ② document.scrollingElement.scrollWidth ≤ innerWidth + 1（无横向滚动）
 *   ③ 气泡内 scrollWidth ≤ clientWidth + 1（内容不溢出自身盒子）
 *   ④ 正文完整可见（不被 overflow:hidden 裁掉——数量级断言）
 */

/** 与用户截图同形：长路径 + 反引号 + `|` + CJK/ASCII 混排 + 超长不可断 token。 */
const CONTENT = [
  '[System Notification] 子代理已完成（background task 3f8f492a）',
  '',
  'Worktree: /home/smith/src/.xbot-worktrees/peer-rw7ms-natural-rewrite',
  'Command: `cargo build --release --features cuda,flashinfer | tee /tmp/q12_cargo_natural_rewrite_build_output.log`',
  '',
  '结果：RC=127 | 结论：坏跑/不可引用（FAIL）',
  '产物：/home/smith/src/.xbot-worktrees/peer-rw7ms-natural-rewrite/target/release/ferrite-graph-inference-benchmark',
  '备注：详情见 https://artifacts.internal.example.com/very/deep/nested/path/builds/peer-rw7ms-natural-rewrite/logs/full_build_output.txt',
].join('\n')

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

/** 诊断：从气泡向上收集祖先链的尺寸/关键计算样式，定位收缩链断点。 */
async function dumpChain(page: Page) {
  return page.evaluate(() => {
    const bubble = document.querySelector('[data-testid="user-bubble"]') as HTMLElement | null
    if (!bubble) return { missing: true }
    const chain: Array<Record<string, unknown>> = []
    let el: HTMLElement | null = bubble
    for (let i = 0; i < 8 && el; i++) {
      const cs = getComputedStyle(el)
      chain.push({
        tag: el.tagName.toLowerCase(),
        testid: el.getAttribute('data-testid'),
        cls: (el.className || '').toString().slice(0, 110),
        w: el.getBoundingClientRect().width,
        scrollW: el.scrollWidth,
        clientW: el.clientWidth,
        maxW: cs.maxWidth,
        minW: cs.minWidth,
        wrap: cs.overflowWrap,
        ws: cs.whiteSpace,
        display: cs.display,
        minWidth: cs.minWidth,
      })
      el = el.parentElement
    }
    const scroller = document.scrollingElement as HTMLElement
    return {
      innerWidth: window.innerWidth,
      pageScrollWidth: scroller.scrollWidth,
      bubble: (() => {
        const r = bubble.getBoundingClientRect()
        return { left: r.left, right: r.right, width: r.width, scrollWidth: bubble.scrollWidth, clientWidth: bubble.clientWidth }
      })(),
      chain,
    }
  })
}

test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

test('手机：系统通知里的超长不可断 token 不得让气泡/页面横向溢出', async ({ page }) => {
  seqCounter = 0
  await setupMock(page)
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForTimeout(2000)

  await emitSSE(page, 'progress_structured', {
    type: 'progress_structured',
    progress: {
      phase: 'turn_started', turn_id: 7, chat_id: 'web:chat-1',
      turn_start: { trigger: 'notification', content: CONTENT },
    },
  })
  await page.waitForTimeout(800)

  const bubble = page.locator('[data-testid="user-bubble"]').first()
  await expect(bubble).toBeVisible({ timeout: 5000 })

  const diag = await dumpChain(page)
  console.log('DIAG ' + JSON.stringify(diag, null, 2))

  // 正文必须完整可见（不能被 overflow:hidden 裁掉——用户要看全文）
  await expect(bubble).toContainText('peer-rw7ms-natural-rewrite')
  await expect(bubble).toContainText('坏跑/不可引用（FAIL）')
  await expect(bubble).toContainText('full_build_output.txt')

  const m = diag as {
    innerWidth: number
    pageScrollWidth: number
    bubble: { left: number; right: number; scrollWidth: number; clientWidth: number }
  }
  expect(m.bubble.right, '① 气泡右边缘不得越过视口').toBeLessThanOrEqual(m.innerWidth + 1)
  expect(m.bubble.left, '① 气泡左边缘不得为负').toBeGreaterThanOrEqual(-1)
  expect(m.pageScrollWidth, '② 页面不得横向滚动').toBeLessThanOrEqual(m.innerWidth + 1)
  expect(
    m.bubble.scrollWidth - m.bubble.clientWidth,
    '③ 气泡内容不得宽于自身盒子',
  ).toBeLessThanOrEqual(1)
})
