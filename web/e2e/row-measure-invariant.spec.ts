import { test, expect, type Page } from '@playwright/test'

/**
 * 回归守护（2026-09-23 用户报告：正文互相穿插 / 两段文字压在同一 y）。
 *
 * 虚拟行是 `position: absolute; transform: translateY(start)`，`start` = 前面所有行
 * 尺寸的累加 ⇒ **只要某行的"记账尺寸"小于它的真实高度，下一行就画到它身上**。
 *
 * 根因：ResizeObserver 的 `entry.borderBoxSize` 是**观察时刻的快照**（不是当前几何），
 * 乱序/滞后投递时会比真实尺寸小；旧实现把它当作行尺寸写回虚拟器 ⇒ 行被缩矮 ⇒ 下一行
 * 压上来；此后 DOM 不再变化 ⇒ 没有下一次回调 ⇒ 错值**永久固化**（画面静止也一样错）。
 *
 * 契约（本用例守护）：
 *   ① **每一行的记账尺寸必须等于它的实际高度**（`data-row-size` vs `rect.height`）；
 *   ② **任何两行都不得重叠**（DOM 排序后逐个比较）；
 *   ③ 打字机/异步 markdown 等**不经过 React 的长高**同样必须立刻被记账
 *      （DOM 直改触发 RO → flush 读真几何）。
 */

const BASE = process.env.E2E_BASE_URL || 'http://127.0.0.1:5199'
const CHAT = 'chat-1'

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

let seq = 0
async function emitSSE(page: Page, type: string, data: Record<string, unknown>) {
  await page.evaluate(
    ({ type, data, seq }) => {
      const w = window as unknown as SSEMockState
      const handlers = w.__sseListeners?.[type]
      if (!handlers) return
      const ev = new MessageEvent(type, { data: JSON.stringify({ ...data, seq }) })
      handlers.forEach((h) => h(ev))
    },
    { type, data, seq: ++seq },
  )
}

/** 每个 turn：一条 user + 一条 assistant，后者带 N 个迭代（正文 + 工具 pill）。 */
function historyMessage(turnID: number, iters: number) {
  return [
    { id: turnID * 2 - 1, role: 'user', content: `turn ${turnID} 的用户消息`, timestamp: new Date(2026, 8, turnID, 1).toISOString(), turn_id: turnID },
    {
      id: turnID * 2,
      role: 'assistant',
      content: '',
      timestamp: new Date(2026, 8, turnID, 2).toISOString(),
      turn_id: turnID,
      iterations: Array.from({ length: iters }, (_, k) => ({
        iteration: k + 1,
        content: `这是 turn ${turnID} 第 ${k + 1} 个迭代的正文。它有好几行文字，用来把行撑高，` +
          `因为真实的高度永远不应该被估算值或过期快照替换掉 —— 一旦替换，下一行就会压到它身上。`,
        reasoning: `turn ${turnID} iter ${k + 1} 的思考内容`,
        tools: Array.from({ length: 2 }, (_, t) => ({
          name: t % 2 === 0 ? 'Shell' : 'Read',
          label: `cd /home/smith/src/xbot && ./scripts/long-running-command-${turnID}-${k}-${t} --workspace --all-targets`,
          status: 'done',
          summary: 'ok',
          detail: 'ok',
        })),
        tool_count: 2,
      })),
    },
  ]
}

const HISTORY = [1, 2, 3, 4, 5, 6, 7].flatMap((turn) => historyMessage(turn, 3 + (turn % 3)))

async function setupMock(page: Page) {
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: {
          sessions: [{ chat_id: CHAT, channel: 'web', label: 'Test', last_active: new Date().toISOString() }],
          chats: [{ chat_id: CHAT, channel: 'web', label: 'Test', last_active: new Date().toISOString() }],
          orphan_subagents: [],
        },
      },
    }),
  )
  await page.route('**/api/history', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: { messages: HISTORY, chat_id: CHAT, channel: 'web', last_seq: 10, has_more: false, oldest_id: 1, active_progress: null },
      },
    }),
  )
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

async function login(page: Page) {
  await page.addInitScript(() => {
    try { localStorage.setItem('xbot-locale', 'zh-CN') } catch { /* ignore */ }
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
    ;(window as unknown as { __sseListeners: typeof listeners }).__sseListeners = listeners
    class MockEventSource {
      readyState = 1
      onopen: ((ev: Event) => void) | null = null
      onerror: ((ev: Event) => void) | null = null
      constructor(public url: string) { setTimeout(() => this.onopen?.(new Event('open')), 0) }
      addEventListener(t: string, h: (ev: MessageEvent) => void) {
        if (!listeners[t]) listeners[t] = new Set()
        listeners[t].add(h)
      }
      removeEventListener() {}
      close() {}
    }
    ;(window as unknown as { EventSource: unknown }).EventSource = MockEventSource
  })
  await setupMock(page)
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForTimeout(1200)
}

/** 页内不变量检查：① 记账尺寸 == 实际高度；② 任意两行不重叠。 */
const CHECK = () => {
  const rows = Array.from(document.querySelectorAll('.virt-row')) as HTMLElement[]
  const boxes = rows.map((el) => {
    const rect = el.getBoundingClientRect()
    return {
      id: el.dataset.messageId ?? '?',
      size: Number(el.dataset.rowSize),
      top: rect.top,
      bottom: rect.bottom,
      h: Math.round(rect.height),
    }
  })
  const sizeMismatch = boxes
    .filter((b) => Number.isFinite(b.size) && b.size > 0 && Math.abs(b.size - b.h) > 1)
    .map((b) => ({ id: b.id, size: b.size, h: b.h }))
  const sorted = [...boxes].sort((a, b) => a.top - b.top)
  const overlap: Array<{ a: string; b: string; delta: number }> = []
  for (let i = 1; i < sorted.length; i++) {
    const d = sorted[i - 1].bottom - sorted[i].top
    if (d > 1) overlap.push({ a: sorted[i - 1].id, b: sorted[i].id, delta: Math.round(d) })
  }
  return { count: boxes.length, sizeMismatch, overlap }
}

async function expectInvariant(page: Page, where: string) {
  // 先让两帧过去：flush 是「microtask / paint 前」调度的，等两帧可确保任何已发生的尺寸
  // 变化都已写回（避免把"变更已发生、flush 尚未执行"的中间态误判为违反不变量）。
  await page.evaluate(
    () => new Promise<void>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r()))),
  )
  const r = await page.evaluate(CHECK)
  expect(r.count, `${where}: 必须有已渲染的虚拟行`).toBeGreaterThan(0)
  expect(r.sizeMismatch, `${where}: 行记账尺寸必须等于实际高度`).toEqual([])
  expect(r.overlap, `${where}: 行之间不得重叠`).toEqual([])
}

test.describe('行尺寸/位置不变量（真实浏览器几何）', () => {
  test('历史加载 + 流式追加 + 滚动 + 异步长高，全程不重叠', async ({ page }) => {
    await login(page)
    await page.waitForSelector('.virt-row', { timeout: 10_000 })
    await page.waitForTimeout(300)
    await expectInvariant(page, '历史加载后')
    expect(await page.evaluate(() => Number(document.querySelector('[data-message-list-content]')?.parentElement?.parentElement?.dataset.measurePass ?? 0))).toBeGreaterThanOrEqual(0)

    // ── 流式追加一个 turn（多个迭代、每个都带正文 + 两个工具 pill）──
    const turn = 8
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: CHAT, channel: 'web' } })
    for (let iter = 1; iter <= 4; iter++) {
      await emitSSE(page, 'progress_structured', {
        type: 'progress_structured',
        progress: {
          phase: 'tool_exec',
          iteration: iter,
          seq: 100 + iter,
          turn_id: turn,
          chat_id: `web:${CHAT}`,
          active_tools: [{ name: 'Shell', label: `streaming command ${iter}`, status: 'running', iteration: iter }],
          completed_tools: iter > 1
            ? [{ name: 'Read', label: `streamed read ${iter - 1}`, status: 'done', iteration: iter - 1, summary: 'ok' }]
            : [],
          iteration_history: Array.from({ length: iter - 1 }, (_, k) => ({
            iteration: k + 1,
            content: `流式正文 ${k + 1}：这一行也要有足够长度，让行高远大于任何估算值，` +
              `从而让"尺寸必须来自真几何"这条不变量在几何上可判定。`,
            reasoning: '',
            tools: [{ name: 'Shell', label: `streamed command ${k + 1}`, status: 'done', summary: 'ok' }],
          })),
        },
      })
      await page.waitForTimeout(120)
      await expectInvariant(page, `流式迭代 ${iter} 后`)
    }

    // ── 滚动到中部（虚拟化窗口切换）再检查 ──
    await page.evaluate(async () => {
      const anchor = document.querySelector('[data-message-list-content]') as HTMLElement | null
      let sc = anchor?.parentElement as HTMLElement | null
      while (sc) {
        const oy = getComputedStyle(sc).overflowY
        if (oy === 'auto' || oy === 'scroll') break
        sc = sc.parentElement
      }
      if (!sc) return
      for (let step = 0; step < 8; step++) {
        sc.scrollTop = Math.round((sc.scrollHeight - sc.clientHeight) * (1 - step / 8))
        await new Promise((res) => requestAnimationFrame(() => requestAnimationFrame(() => res(null))))
      }
    })
    await expectInvariant(page, '滚动后')

    // ── 不经过 React 的异步长高（打字机/异步 markdown 的等价物）──
    await page.evaluate(async () => {
      const rows = Array.from(document.querySelectorAll('.virt-row')) as HTMLElement[]
      const target = rows[rows.length - 1]
      if (!target) return
      const filler = document.createElement('div')
      filler.style.height = '260px'
      filler.dataset.injected = 'true'
      target.appendChild(filler)
      // 等两帧：RO 回调在同一帧内投递，flush 必须把它读回去
      await new Promise((res) => requestAnimationFrame(() => requestAnimationFrame(() => res(null))))
    })
    await page.waitForTimeout(150)
    await expectInvariant(page, '异步长高后')
  })
})
