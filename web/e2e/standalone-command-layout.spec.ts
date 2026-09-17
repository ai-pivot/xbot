import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * 真实布局回归（2026-09-17 用户报告「`!pwd` 输出看不见」）—— **断言几何，不只看
 * DOM/text 存在**。
 *
 * 真实浏览器取证（用户）：命令输出确实进了 DOM（`data-message-id="cmd-1"` 3 秒后
 * 仍在，console 无 error/warn），滚动容器也已在最底部（scrollTop=5031 =
 * scrollHeight - clientHeight），**但**：
 *   - 上一条 assistant 行（data-index=5）DOM 实测 1118.78px（y=-373 → 745）
 *   - 追加的 cmd-1 行（data-index=6）起点只比它多 ~115px（y=-258）
 *   → 两个绝对定位行重叠 ~1004px ⇒ 输出被上一行盖住（"在 DOM 里但看不见"）。
 *
 * 根因：TanStack Virtual 的行尺寸缓存停在旧值（该行早期的小尺寸），ResizeObserver
 * 的 entry 乱序/滞后把真值覆盖回去后尺寸不再变化 → 永久固化；`getMeasurements` 的
 * memo 又不依赖 estimateSize。修复 = 追加行时权威重测（MessageList 的 append
 * effect：`measure()` 清缓存 → 逐个已挂载行读真实几何 → 校正后贴底）。
 *
 * 本用例的两条硬断言（缺一不可）：
 *   ① 任意相邻已挂载行不得重叠（后一行 top >= 前一行 bottom - 1px）；
 *   ② 命令输出的行 bbox 必须落在滚动视口内（不只是存在于 DOM）。
 */

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

let seqCounter = 0

async function emitSSE(page: Page, type: string, data: Record<string, unknown>) {
  await page.evaluate(
    ({ type, data, seq }) => {
      const w = window as unknown as SSEMockState
      const handlers = w.__sseListeners?.[type]
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
          chats: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() }],
          orphan_subagents: [],
        },
      },
    }),
  )
  await page.route('**/api/history', (r) =>
    r.fulfill({
      json: { ok: true, data: { messages: [], chat_id: 'chat-1', last_seq: 0, active_progress: null } },
    }),
  )
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

/** 8 个高迭代（每迭代 40 段长文本）—— 让 live assistant 行真的有几千 px 高。 */
function tallIterations(): unknown[] {
  return Array.from({ length: 8 }, (_, i) => ({
    iteration: i + 1,
    thinking: `thinking ${i + 1}`,
    content: Array.from(
      { length: 40 },
      (_, k) => `line ${k} of answer ${i + 1} — lorem ipsum dolor sit amet, consectetur adipiscing elit sed do.`,
    ).join('\n\n'),
    completed_tools: [],
  }))
}

/** 滚动容器（overlayY=auto/scroll 的祖先）。 */
async function scrollerMetrics(page: Page, cmdSel = '[data-message-id^="cmd-"]') {
  return page.evaluate((sel) => {
    const cmd = document.querySelector(sel) as HTMLElement | null
    const rows = Array.from(document.querySelectorAll('.virt-row[data-index]')) as HTMLElement[]
    let sc = document.querySelector('[data-message-list-content]') as HTMLElement | null
    sc = sc?.parentElement ?? null
    while (sc) {
      const oy = getComputedStyle(sc).overflowY
      if (oy === 'auto' || oy === 'scroll') break
      sc = sc.parentElement
    }
    if (!sc) return null
    const scRect = sc.getBoundingClientRect()
    // 相邻行重叠量（>1px 即为"被上一行遮挡"）：取所有相邻对的最大重叠。
    let maxOverlap = -Infinity
    let worst: { a: number; b: number; overlap: number } | null = null
    for (let i = 1; i < rows.length; i++) {
      const prev = rows[i - 1].getBoundingClientRect()
      const cur = rows[i].getBoundingClientRect()
      const overlap = prev.bottom - cur.top
      if (overlap > maxOverlap) {
        maxOverlap = overlap
        worst = { a: Number(rows[i - 1].dataset.index), b: Number(rows[i].dataset.index), overlap }
      }
    }
    const cmdRect = cmd?.getBoundingClientRect()
    const wrapper = document.querySelector('[data-measure-pass]') as HTMLElement | null
    const rowInfo = rows.map((r) => {
      const b = r.getBoundingClientRect()
      return {
        idx: Number(r.dataset.index),
        top: Math.round(b.top),
        bottom: Math.round(b.bottom),
        h: Math.round(b.height),
        transform: r.style.transform,
        msgId: r.dataset.messageId,
      }
    })
    return {
      scrollerFound: true,
      atBottom: sc.scrollTop >= sc.scrollHeight - sc.clientHeight - 2,
      scrollTop: sc.scrollTop,
      scrollHeight: sc.scrollHeight,
      clientHeight: sc.clientHeight,
      rowsMounted: rows.length,
      maxOverlap,
      worst,
      cmdFound: !!cmd,
      cmdTop: cmdRect?.top ?? null,
      cmdBottom: cmdRect?.bottom ?? null,
      cmdInViewport: !!cmdRect && cmdRect.top >= scRect.top - 1 && cmdRect.bottom <= scRect.bottom + 1,
      scTop: scRect.top,
      scBottom: scRect.bottom,
      cmdText: cmd?.textContent ?? '',
      // ── 修复的可观测证据（排障用）──
      measurePass: wrapper?.dataset.measurePass ?? null, // 权威重测执行次数（应 > 0）
      virtTotal: wrapper?.dataset.virtTotal ?? null, // 校正后的虚拟总高
      wrapperHeight: wrapper?.style.height ?? null, // = getTotalSize()
      rowInfo,
    }
  }, cmdSel)
}

async function login(page: Page) {
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForTimeout(2000)
}

test.describe('turn-less command output layout（行重叠 guard）', () => {
  test.beforeEach(async ({ page }) => {
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
    await setupMock(page)
    await login(page)
  })

  test('长 assistant 行仍在跑时追加无 turn 的命令输出：不得重叠，必须在视口内可见', async ({
    page,
  }) => {
    await page.setViewportSize({ width: 900, height: 700 })

    await emitSSE(page, 'session', {
      type: 'session',
      session: { action: 'busy', chat_id: 'chat-1', channel: 'web' },
    })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: {
        phase: 'turn_started',
        turn_id: 1,
        turn_start: { trigger: 'user', request_id: 'r1' },
        chat_id: 'web:chat-1',
      },
    })
    // 长 live assistant 行（turn 仍在跑）。
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: {
        phase: 'tool_exec',
        turn_id: 1,
        iteration: 9,
        iteration_history: tallIterations(),
        chat_id: 'web:chat-1',
      },
    })
    await page.waitForTimeout(800)

    // 无 turn 的命令回复（真实后端：命令回复绝不继承 activeTurn ⇒ 无 turn_id）。
    // 同一帧内连续两条：让"追加行"与"上一行高度变化"落在同一 commit（真实现场）。
    await emitSSE(page, 'text', {
      type: 'text',
      content: '```\n/root/projects/xbot\n```',
      chat_id: 'web:chat-1',
    })
    await page.waitForTimeout(3000) // 用户实测等 3 秒后仍被遮挡

    const m = await scrollerMetrics(page)
    expect(m, 'message scroller not found').not.toBeNull()
    console.log(
      `layout: rows=${m!.rowsMounted} maxOverlap=${m!.maxOverlap} worst=${JSON.stringify(m!.worst)} cmdInViewport=${m!.cmdInViewport} atBottom=${m!.atBottom} top=${m!.cmdTop} bottom=${m!.cmdBottom} sc=[${m!.scTop},${m!.scBottom}] measurePass=${m!.measurePass} virtTotal=${m!.virtTotal} wrapperHeight=${m!.wrapperHeight} rows=${JSON.stringify(m!.rowInfo)}`,
    )

    // 修复必须真的执行过（否则断言"不重叠"只是运气）：权威重测至少跑过一次。
    expect(Number(m!.measurePass ?? 0), '追加行时权威重测未执行（data-measure-pass 缺失）').toBeGreaterThan(0)
    expect(m!.cmdFound, '命令输出的 standalone 行必须存在（data-message-id="cmd-*"）').toBe(true)
    // ① 相邻行不得重叠 —— 这正是"输出在 DOM 里但被上一行盖住"的判据。
    expect(m!.maxOverlap, `相邻行发生重叠（最坏 ${JSON.stringify(m!.worst)}）`).toBeLessThanOrEqual(1)
    // ② 输出 bbox 必须落在滚动视口内（可见），不只是存在于 DOM。
    expect(m!.cmdInViewport, '命令输出必须落在滚动视口内（可见）').toBe(true)
    // ③ 贴底状态下输出必须可见（用户的实测状态：已在最底部）。
    expect(m!.atBottom, '追加后应自动贴底').toBe(true)
    expect(m!.cmdText).toContain('/root/projects/xbot')
  })

  test('手动滚到底后输出仍必须可见（不允许"到底了却看不到"）', async ({ page }) => {
    await page.setViewportSize({ width: 900, height: 700 })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: {
        phase: 'turn_started',
        turn_id: 1,
        turn_start: { trigger: 'user', request_id: 'r1' },
        chat_id: 'web:chat-1',
      },
    })
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: {
        phase: 'tool_exec',
        turn_id: 1,
        iteration: 9,
        iteration_history: tallIterations(),
        chat_id: 'web:chat-1',
      },
    })
    await page.waitForTimeout(600)
    await emitSSE(page, 'text', {
      type: 'text',
      content: '```\n/root/projects/xbot\n```',
      chat_id: 'web:chat-1',
    })
    await page.waitForTimeout(1200)

    // 显式滚到底（模拟用户"继续向下滚"），再断言几何。
    await page.evaluate(() => {
      const anchor = document.querySelector('[data-message-list-content]') as HTMLElement | null
      let sc = anchor?.parentElement as HTMLElement | null
      while (sc) {
        const oy = getComputedStyle(sc).overflowY
        if (oy === 'auto' || oy === 'scroll') break
        sc = sc.parentElement
      }
      if (sc) sc.scrollTop = sc.scrollHeight
    })
    await page.waitForTimeout(600)

    const m = await scrollerMetrics(page)
    expect(m, 'message scroller not found').not.toBeNull()
    expect(m!.maxOverlap, `相邻行发生重叠（最坏 ${JSON.stringify(m!.worst)}）`).toBeLessThanOrEqual(1)
    expect(m!.cmdInViewport, '滚到底后命令输出仍必须可见').toBe(true)
  })
})
