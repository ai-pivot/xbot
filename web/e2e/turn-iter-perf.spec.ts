import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E guard：**长 turn 向上滚动不得出现"鬼打墙"** —— 滚动容器总高必须在
 * 整个向上滚动过程中保持恒定，且真的到得了最上方。
 *
 * REPRO（2026-09-13 用户报告）：「向上快速滚动出现鬼打墙，看起来一直在向上滚
 * 其实几乎一点没动，永远到不了最上方」。触发条件是迭代块用了
 * `content-visibility: auto` + `contain-intrinsic-size: auto 320px`：
 * **从未渲染过的块**（长 turn 一次性提交/历史加载，视口外的那些）只能拿
 * 320px 占位，而真实块高得多 → 向上滚时块逐个兑现真实高度 → 总高持续变大 →
 * 滚动锚定把正在看的内容往下推，每滚一格又被推回一格 → 原地打转。
 *
 * ⚠️ 复现要点（写测试的人必读）：必须是**从未渲染过**的块。若像流式追加那样
 * 边加边钉在底部，每个块都在视口里渲染过一次，`contain-intrinsic-size: auto`
 * 会记住真实高度 → 滚动稳定 → 测试**抓不到**这个 bug。所以这里用**一次性提交**
 * 40 个高迭代（`text` 事件携带 progress_history）——视口外的块从未渲染。
 *
 * 修复：迭代块只保留 `contain: layout paint`（渲染隔离照旧，代价与迭代数无关），
 * 不得再出现 content-visibility / contain-intrinsic-size（CSS 契约守护在
 * src/index.test.ts）。
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
          sessions: [
            { chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() },
          ],
          chats: [
            { chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() },
          ],
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
  await page.route('**/api/sse**', (r) =>
    r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }),
  )
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

/** 40 个高迭代，**一次性提交**（不是流式追加）——视口外的块从未渲染过。 */
function longTurnHistory(): unknown[] {
  return Array.from({ length: 40 }, (_, i) => ({
    iteration: i + 1,
    thinking: `thinking ${i + 1}`,
    content: Array.from(
      { length: 30 },
      (_, k) => `line ${k} of answer ${i + 1} — lorem ipsum dolor sit amet, consectetur adipiscing elit.`,
    ).join('\n\n'),
    completed_tools: [],
  }))
}

/** 从底到顶分步滚动，记录每一步的 scrollHeight（鬼打墙 = 总高持续变化）。 */
async function scrollUpHeightSpread(page: Page) {
  return page.evaluate(async () => {
    const anchor = document.querySelector('[data-message-list-content]') as HTMLElement | null
    let sc = anchor?.parentElement as HTMLElement | null
    while (sc) {
      const oy = getComputedStyle(sc).overflowY
      if (oy === 'auto' || oy === 'scroll') break
      sc = sc.parentElement
    }
    if (!sc) return null
    const frame = () =>
      new Promise<void>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r())))

    sc.scrollTop = sc.scrollHeight
    await frame()
    const heights: number[] = []
    let offset = sc.scrollHeight
    for (let step = 0; step < 25; step++) {
      offset = Math.max(0, offset - sc.clientHeight * 2)
      sc.scrollTop = offset
      await frame()
      heights.push(sc.scrollHeight)
    }
    sc.scrollTop = 0
    await frame()
    heights.push(sc.scrollHeight)

    const first = document.querySelector('[data-iter-id="1"]') as HTMLElement | null
    const firstBox = first?.getBoundingClientRect()
    const scBox = sc.getBoundingClientRect()

    return {
      scrollTopAfter: sc.scrollTop,
      minHeight: Math.min(...heights),
      maxHeight: Math.max(...heights),
      firstVisibleInScroller:
        !!firstBox && firstBox.bottom > scBox.top && firstBox.top < scBox.bottom,
    }
  })
}

test.describe('long turn scroll stability (鬼打墙 guard)', () => {
  test('scrollHeight stays constant while scrolling up, and the top is reachable', async ({
    browser,
  }) => {
    const page = await browser.newPage({ viewport: { width: 900, height: 700 } })

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
    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()
    await page.waitForTimeout(2000)

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
    // 一次性提交 40 个高迭代 —— 视口外的块从未渲染过（复现用户场景的关键）。
    await emitSSE(page, 'text', {
      type: 'text',
      content: 'done',
      seq: 999,
      turn_id: 1,
      chat_id: 'web:chat-1',
      progress_history: JSON.stringify(longTurnHistory()),
    })
    await page.waitForTimeout(1200)

    const m = await scrollUpHeightSpread(page)
    expect(m, 'message scroller not found').not.toBeNull()
    console.log(
      `scroll: minHeight=${m!.minHeight} maxHeight=${m!.maxHeight} scrollTopAfter=${m!.scrollTopAfter} firstVisible=${m!.firstVisibleInScroller}`,
    )

    // 1) **总高在向上滚动过程中必须恒定（±1%）** —— 总高变化正是"鬼打墙"的成因。
    const spread = m!.maxHeight - m!.minHeight
    expect(spread).toBeLessThan(m!.minHeight * 0.01)

    // 2) 真的到得了最上方：滚动归零 + 第一个迭代块落在滚动视口内。
    expect(m!.scrollTopAfter).toBeLessThanOrEqual(1)
    expect(m!.firstVisibleInScroller).toBe(true)

    await page.close()
  })
})

/**
 * 迭代级窗口化守护（2026-09-13「手机上 iter 多了还是很卡，点什么交互都要等几秒」）：
 *
 * 移动端实测（390×844 + CPU 4×）——交互成本 ∝ DOM 规模，且 `contain: layout paint`
 * 三变体无差别（274/267/265ms）。根治 = 迭代级窗口化：远离视口的块只留外壳 +
 * 固定高度（实测缓存，未测到则用内容估算），内容卸载。
 *
 *   N      节点(修前)   全局样式失效(修前)   打开设置(修前)  → 修后
 *   15     653          89ms                 307ms
 *   60     2348         262ms                655ms         → 节点 ~230、耗时与 N 无关
 *
 * 这里只断言**确定性**的因果量（节点数 / 挂载内容数 / 外壳数），不断言计时
 * （CI 抖动会假红）；滚动稳定性另有上面那条守护。
 */
async function mountStats(page: Page) {
  return page.evaluate(() => {
    const blocks = Array.from(document.querySelectorAll('.iter-block'))
    return {
      nodes: document.querySelectorAll('*').length,
      blocks: blocks.length,
      mountedContents: blocks.filter((b) => (b as HTMLElement).dataset.windowMuted !== 'true').length,
    }
  })
}

function historyWith(n: number): unknown[] {
  return Array.from({ length: n }, (_, i) => ({
    iteration: i + 1,
    thinking: `thinking ${i + 1}`,
    content: Array.from(
      { length: 30 },
      (_, k) => `line ${k} of answer ${i + 1} — lorem ipsum dolor sit amet, consectetur adipiscing elit.`,
    ).join('\n\n'),
    completed_tools: [{ name: 'Shell', status: 'done', summary: `cmd ${i + 1}` }],
  }))
}

test.describe('iteration windowing keeps mounted DOM independent of iteration count', () => {
  for (const n of [15, 60]) {
    test(`N=${n}: bounded mounted contents on a mobile viewport`, async ({ browser }) => {
      const context = await browser.newContext({
        viewport: { width: 390, height: 844 },
        isMobile: true,
        hasTouch: true,
        deviceScaleFactor: 2,
      })
      const page = await context.newPage()

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
            for (const k of Object.keys(listeners)) listeners[k].clear()
          }
        }
        ;(window as unknown as { EventSource: typeof MockEventSource }).EventSource = MockEventSource
      })

      await setupMock(page)
      await page.goto(`${BASE}/login`)
      await page.locator('input').first().fill('test')
      await page.locator('input[type="password"]').fill('test')
      await page.locator('button[type="submit"]').click()
      await page.waitForTimeout(2000)

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
      await emitSSE(page, 'text', {
        type: 'text',
        content: 'done',
        seq: 999,
        turn_id: 1,
        chat_id: 'web:chat-1',
        progress_history: JSON.stringify(historyWith(n)),
      })
      await page.waitForTimeout(1500)

      const stats = await mountStats(page)
      console.log(`WINDOW N=${n}`, JSON.stringify(stats))

      // 1) 外壳全在（结构/滚动高度/调试属性不被窗口化破坏）
      expect(stats.blocks).toBeGreaterThanOrEqual(n)
      // 2) 真正挂载内容的块数由视口决定（远小于 N）
      expect(stats.mountedContents).toBeLessThan(Math.max(12, n / 3))
      // 3) DOM 规模有界（修前 N=60 是 2348）
      expect(stats.nodes).toBeLessThan(900)

      await context.close()
    })
  }
})
