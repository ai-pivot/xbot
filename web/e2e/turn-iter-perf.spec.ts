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

async function setupMock(page: Page, historyMessages: unknown[] = []) {
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
      json: { ok: true, data: { messages: historyMessages, chat_id: 'chat-1', last_seq: 0, active_progress: null } },
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

test.describe('iteration windowing keeps mounted DOM independent of iteration count', () => {  for (const n of [15, 60]) {
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

/**
 * ⛔ 窗口化正确性守护（2026-09-13 用户现场：
 * `<div class="iter-block" data-window-muted="true" style="height: 26.6562px">` ——
 * 「部分 tool 渲染为空」）。
 *
 * 根因：首版窗口化把**瞬态测量**当成可信高度 —— 块刚挂载时 RO 可能先报出过小高度
 * （字体/异步 markdown 未定形），据此卸载内容后再无测量机会 → 内容与高度双永久错误。
 *
 * 本测试复刻该时序：**先让内容被压成 26px，250ms 后放开**（模拟"先测小、后定形"），
 * 断言被窗口化卸载的块不得停留在瞬态高度上（否则内容就是空的）。
 */
test.describe('windowing never freezes a transient (collapsed) height', () => {
  test('blocks measure small first then grow: muted blocks must not stay collapsed', async ({
    browser,
  }) => {
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

    // 先压扁：模拟"字体/异步 markdown 未定形"时的过小高度
    await page.addStyleTag({ content: '.iter-block > * { max-height: 26px; overflow: hidden; }' })
    await emitSSE(page, 'text', {
      type: 'text',
      content: 'done',
      seq: 999,
      turn_id: 1,
      chat_id: 'web:chat-1',
      progress_history: JSON.stringify(historyWith(40)),
    })
    await page.waitForTimeout(250)
    // 放开（"定形"）
    await page.addStyleTag({ content: '.iter-block > * { max-height: none; }' })
    await page.waitForTimeout(2500)

    const stats = await page.evaluate(() => {
      const blocks = Array.from(document.querySelectorAll('.iter-block'))
      const muted = blocks.filter((b) => (b as HTMLElement).dataset.windowMuted === 'true')
      const mutedHeights = muted.map((b) => Math.round(b.getBoundingClientRect().height))
      const mounted = blocks.filter((b) => (b as HTMLElement).dataset.windowMuted !== 'true')
      const mountedHeights = mounted
        .map((b) => Math.round(b.getBoundingClientRect().height))
        .filter((h) => h > 0)
      return {
        blocks: blocks.length,
        muted: muted.length,
        mutedMin: mutedHeights.length ? Math.min(...mutedHeights) : -1,
        mutedSample: mutedHeights.slice(0, 8),
        mountedSample: mountedHeights.slice(0, 5),
      }
    })
    console.log('COLLAPSE-GUARD', JSON.stringify(stats))

    // 窗口化确实生效
    expect(stats.muted).toBeGreaterThan(0)
    // 被冻结的块不得停留在"瞬态/压扁"的高度上（内容否则就是空的）
    expect(stats.mutedMin).toBeGreaterThan(100)
    // 已挂载的块（真实内容）高度应远大于压扁值，佐证内容确实定形了
    expect(Math.max(...stats.mountedSample)).toBeGreaterThan(200)

    await context.close()
  })
})

/** 窗口化统计（含 muted 数）—— 本守护专用。 */
async function windowStats(page: Page) {
  return page.evaluate(() => {
    const blocks = Array.from(document.querySelectorAll('.iter-block')) as HTMLElement[]
    return {
      nodes: document.querySelectorAll('*').length,
      blocks: blocks.length,
      muted: blocks.filter((b) => b.dataset.windowMuted === 'true').length,
      mountedContents: blocks.filter((b) => b.dataset.windowMuted !== 'true').length,
    }
  })
}

/**
 * ⛔ 守护（2026-09-13「stream 的时候交互也不卡」「注意上下文 iter 数量真的特别多」）：
 * **流式帧不得触碰已提交迭代的 DOM，且代价与当前 turn 的迭代数无关**。
 *
 * 实测背景（390×844 / isMobile / CPU 4×，真实 Chromium + CDP，纯测量）：
 *   - 纯流式（20Hz，6s）稳态：帧间隔 p50=16.7ms / p95=16.8ms / max=33.4ms、
 *     long task **0 次 0ms**，且 **N=20 与 N=200 完全相同**；把 live 迭代的
 *     累积内容拉到 100KB（每 tick 推累积全文）仍是 0 long task —— 即稳态流式的
 *     每帧代价与「迭代数」「内容长度」都无关。
 *   - live turn 的迭代数从 20 拉到 200：DOM 节点 315→495、挂载内容的块数 3→3
 *     （窗口化把代价钉在视口上）。
 *   - 反过来，交互（开右侧栏 / 滚动到长 turn）的代价与流式**无关**（关流式对照
 *     测得的耗时与流式期间相同）—— 那条路径不在本守护范围（见 e2e 报告）。
 *
 * 本测试断言的**因果量**（确定性，与机器速度无关）：流式期间
 *   1. `.iter-block` 节点新增 0 / 删除 0（已提交迭代既不重挂也不卸载）；
 *   2. `data-window-muted` 翻转 0（窗口化判定不被流式帧改写）；
 *   3. 真正挂载内容的块数 before == after，且 < 12（代价 = 视口，不是 N）；
 *   4. 上面三条对 N=20 与 N=200 **同一界**（代价与迭代数无关）。
 */
test.describe('streaming frames never touch committed iterations, cost independent of iteration count', () => {
  test('N=20 and N=200 share the same bounded per-frame work while streaming', async ({
    browser,
  }) => {
    const results: Array<{ n: number; muted: number; mountedBefore: number; mountedAfter: number; added: number; removed: number; mutedFlips: number }> = []

    for (const n of [20, 200]) {
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
            this.onopen = null
          }
        }
        ;(window as unknown as { EventSource: typeof MockEventSource }).EventSource = MockEventSource
      })

      await setupMock(page)
      await page.goto(`${BASE}/login`)
      await page.locator('input').first().fill('test')
      await page.locator('input[type="password"]').fill('test')
      await page.locator('button[type="submit"]').click()
      await page.waitForTimeout(2500)

      const cdp = await context.newCDPSession(page)
      await cdp.send('Emulation.setCPUThrottlingRate', { rate: 4 })

      // live turn（turn 1）：把迭代历史撑到 n
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
      for (let k = 10; k <= n; k += 10) {
        await emitSSE(page, 'progress_structured', {
          type: 'progress_structured',
          progress: {
            chat_id: 'web:chat-1',
            phase: 'tool_exec',
            turn_id: 1,
            iteration: k,
            iteration_history: historyWith(k),
          },
        })
      }
      // 等窗口化收敛（首屏块要量高度 → settle 两次采样 → 复核）
      await page.waitForFunction(
        () => document.querySelectorAll('.iter-block[data-window-muted="true"]').length > 0,
        undefined,
        { timeout: 20000 },
      )
      await page.waitForTimeout(2500)

      const before = await windowStats(page)

      // 观察器：流式窗口内 .iter-block 的增删 + 窗口化判定翻转
      await page.evaluate(() => {
        const w = window as unknown as {
          __iterEvents: { added: number; removed: number; mutedFlips: number }
        }
        w.__iterEvents = { added: 0, removed: 0, mutedFlips: 0 }
        const mo = new MutationObserver((recs) => {
          for (const r of recs) {
            for (const node of Array.from(r.addedNodes)) {
              if (node.nodeType === 1 && (node as Element).classList.contains('iter-block')) {
                w.__iterEvents.added++
              }
            }
            for (const node of Array.from(r.removedNodes)) {
              if (node.nodeType === 1 && (node as Element).classList.contains('iter-block')) {
                w.__iterEvents.removed++
              }
            }
          }
        })
        mo.observe(document.body, { childList: true, subtree: true })
        const mo2 = new MutationObserver((recs) => {
          for (const r of recs) {
            if ((r.target as Element).classList?.contains('iter-block')) w.__iterEvents.mutedFlips++
          }
        })
        mo2.observe(document.body, { subtree: true, attributeFilter: ['data-window-muted'] })
      })

      // 流式 2.5s（20Hz：reasoning 累积文本 + structured 心跳，与后端形态一致）
      await page.evaluate(() => {
        const w = window as unknown as { __n?: number }
        w.__n = 0
        window.setInterval(() => {
          const k = (w.__n = (w.__n ?? 0) + 1)
          const listeners = (window as unknown as SSEMockState).__sseListeners
          const push = (type: string, data: Record<string, unknown>) => {
            const handlers = listeners?.[type]
            if (!handlers) return
            const ev = new MessageEvent(type, { data: JSON.stringify({ ...data, seq: 9000 + k }) })
            handlers.forEach((h) => h(ev))
          }
          push('stream_content', {
            type: 'stream_content',
            progress: {
              chat_id: 'web:chat-1',
              turn_id: 1,
              iteration: 9999,
              reasoning_stream_content: `thinking chunk #${k} about the next step, weighing options. `,
            },
          })
          push('progress_structured', {
            type: 'progress_structured',
            progress: { chat_id: 'web:chat-1', phase: 'thinking', turn_id: 1, iteration: 9999 },
          })
        }, 50)
      })
      await page.waitForTimeout(2500)

      const after = await windowStats(page)
      const ev = await page.evaluate(
        () => (window as unknown as { __iterEvents: { added: number; removed: number; mutedFlips: number } }).__iterEvents,
      )
      console.log(`STREAM-INVARIANT N=${n}`, JSON.stringify({ before, after, ev }))

      results.push({
        n,
        muted: after.muted,
        mountedBefore: before.mountedContents,
        mountedAfter: after.mountedContents,
        added: ev.added,
        removed: ev.removed,
        mutedFlips: ev.mutedFlips,
      })

      await context.close()
    }

    for (const r of results) {
      // 1) 窗口化生效（结构完整 + 视口外卸载）
      expect(r.muted, `N=${r.n}: 窗口化必须生效`).toBeGreaterThan(0)
      // 2) 流式帧不得新增/卸载任何迭代块（已提交迭代零重挂、零重解析）
      expect(r.added, `N=${r.n}: 流式期间不得新增迭代块`).toBe(0)
      expect(r.removed, `N=${r.n}: 流式期间不得卸载迭代块`).toBe(0)
      // 3) 窗口化判定不被流式帧改写
      expect(r.mutedFlips, `N=${r.n}: 流式期间窗口化判定不得翻转`).toBe(0)
      // 4) 挂载内容数 before == after 且有界（代价 = 视口，不是 N）
      expect(r.mountedAfter, `N=${r.n}: 流式前后挂载内容数必须相同`).toBe(r.mountedBefore)
      expect(r.mountedAfter, `N=${r.n}: 挂载内容数必须有界`).toBeLessThan(12)
    }
    // 5) 代价与迭代数无关：N=20 与 N=200 的界相同（同一量级，非 10×）
    expect(Math.abs(results[1].mountedAfter - results[0].mountedAfter)).toBeLessThanOrEqual(2)
  })
})

/**
 * 守护（2026-09-13「加载的历史消息长了就卡」）：`/api/history` 首屏加载的长 turn
 * 也必须被窗口化。
 *
 * 回归形态：`TurnBody` 的 ref 回调（register）在 **commit 阶段**执行，而 IO/RO 在
 * 其**之后**的 useEffect 里创建 —— 首个 commit 挂载的块注册时 roRef/ioRef 还是
 * null（observe 落空），且 setRef 是 useCallback([hKey, register]) 恒定的 → React
 * 不会二次调用 → 这些块**永不被观测** → 永无高度 → 永不 settle → 永不 muted。
 * 于是「历史加载（首屏挂载）的整棵 turn」全量挂载，DOM 与每帧代价 ∝ 迭代数
 * （实测 40×400：3200 块、muted=0）；而 SSE 追加的 turn 因为在 effect 之后才挂载，
 * 窗口化正常（上面 N=15/60 两条测试走的正是这条路径 —— 回归因此漏网）。
 */
test.describe('history-loaded long turn is windowed', () => {
  test('initial /api/history render mutes off-viewport iterations', async ({ browser }) => {
    const context = await browser.newContext({
      viewport: { width: 390, height: 844 },
      isMobile: true,
      hasTouch: true,
      deviceScaleFactor: 2,
    })
    const page = await context.newPage()

    await setupMock(page, [
      { id: 1, role: 'user', content: 'u1', turn_id: 1, timestamp: new Date().toISOString(), iterations: [] },
      {
        id: 2,
        role: 'assistant',
        content: 'a1',
        turn_id: 1,
        timestamp: new Date().toISOString(),
        iterations: historyWith(120),
      },
    ])
    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()
    // 等渲染 + 高度稳定（settle 需两次同值测量，间隔 ≥200ms；复核再 +400ms）
    await page.waitForTimeout(4000)

    const stats = await page.evaluate(() => {
      const blocks = Array.from(document.querySelectorAll('.iter-block')) as HTMLElement[]
      return {
        nodes: document.querySelectorAll('*').length,
        blocks: blocks.length,
        muted: blocks.filter((b) => b.dataset.windowMuted === 'true').length,
        mounted: blocks.filter((b) => b.dataset.windowMuted !== 'true').length,
      }
    })
    console.log('HISTORY-WINDOW-GUARD', JSON.stringify(stats))

    // 全部 120 个迭代块都在（结构/滚动高度不被窗口化破坏）
    expect(stats.blocks).toBeGreaterThanOrEqual(120)
    // 视口外的块必须被窗口化卸载 —— 回归时这里是 0
    expect(stats.muted).toBeGreaterThan(0)
    // 真正挂载内容的块数由视口决定（远小于 120）
    expect(stats.mounted).toBeLessThan(60)
    // DOM 规模有界（回归时 120 个全挂载 ≈ 3000+）
    expect(stats.nodes).toBeLessThan(3000)

    await context.close()
  })
})

/**
 * ⛔ 守护（2026-09-13「手机上切换会话如果会话正在 stream 思考，则整个 stream 期间
 * 不会渲染这条思考之前的任何内容」）—— **窗口化不得把"瞬态/未定形的高度"冻结**，
 * 而且这条必须同时覆盖「会话切换 + 移动端外壳 + 正在 stream」这条路径。
 *
 * 回归机制（与上面 COLLAPSE-GUARD 同源，但走切换路径）：
 *   81bbb195 让**历史加载路径**（`/api/history` 首屏 / active_progress hydration）
 *   的迭代块第一次真的被观测 → 第一次真的拿到高度 → 第一次真的可能被冻结。手机端
 *   首次挂载时内容尚未定形（字体/异步 markdown/图片），RO 首帧量到的就是"压扁态"，
 *   `record` 的同值二次采样（+250ms ≥200ms）把它判成 settled → 冻结 → 内容卸载。
 *   此后该块盒子被钉在压扁高度上，**RO 再也不会报变化**（内容已卸载），唯一的纠错
 *   通路是 400ms 复核 —— 而复核在 `setVerifying` 之后**只等一个 rAF**：若 React 的
 *   重渲染（把内容挂回来）还没来得及 commit，它量到的就是**被冻结的占位本身**
 *   （`stillMuted === true`，实测 76/397 次），`record` 返回 `changed:false` →
 *   被当成"复核通过"→ **永久**停在压扁高度（整个 stream 期间内容都不渲染）。
 *
 * 断言（修复前红 / 修复后绿，且不牺牲窗口化收益）：
 *   1. 切换后窗口化仍然生效（muted > 0）—— 正确性与收益同时成立；
 *   2. **任何被冻结的块都不得停在瞬态高度上**（min muted height ≥ 100px）；
 *   3. 思考块之前的已提交迭代内容确实渲染（视口内的已提交块 mounted 且有文字）。
 */
test.describe('switching to a streaming session must not freeze a transient height', () => {
  /** 切换场景的两会话 mock：A=长历史，B=正在 stream 思考（active_progress + SSE）。 */
  async function setupSwitchMock(page: Page) {
    const ts = new Date().toISOString()
    /** 8 段文本（实测块高 ≈ 500px，远大于压扁态 30px 与阈值 100px）。 */
    const iterContent = (tag: string) =>
      Array.from(
        { length: 8 },
        (_, j) => `line ${j} ${tag} — lorem ipsum dolor sit amet, consectetur adipiscing elit sed do eiusmod.`,
      ).join('\n\n')
    const mkIters = (tag: string) =>
      Array.from({ length: 20 }, (_, k) => ({
        iteration: k + 1,
        thinking: `thinking ${k + 1} ${tag}`,
        content: iterContent(`${tag}-${k + 1}`),
        completed_tools: [],
      }))

    const rowsA: unknown[] = []
    let id = 1
    for (let turn = 1; turn <= 20; turn++) {
      rowsA.push({ id: id++, role: 'user', content: `A user turn ${turn}`, turn_id: turn, timestamp: ts, iterations: [] })
      rowsA.push({ id: id++, role: 'assistant', content: '', turn_id: turn, timestamp: ts, iterations: mkIters(`A-t${turn}`) })
    }
    const rowsB = [
      { id: 900, role: 'user', content: 'B user turn 1', turn_id: 1, timestamp: ts, iterations: [] },
      { id: 901, role: 'assistant', content: '', turn_id: 1, timestamp: ts, iterations: mkIters('B-t1').slice(0, 3) },
      { id: 902, role: 'user', content: 'B user turn 2', turn_id: 2, timestamp: ts, iterations: [] },
    ]

    await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
    await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
    await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
    await page.route('**/api/session-tree', (r) =>
      r.fulfill({
        json: {
          ok: true,
          data: {
            sessions: [
              { chat_id: 'chat-A', channel: 'web', label: 'SessA', last_active: ts, isCurrent: true },
              { chat_id: 'chat-B', channel: 'web', label: 'SessB', last_active: ts },
            ],
            chats: [
              { chat_id: 'chat-A', channel: 'web', label: 'SessA', last_active: ts, isCurrent: true },
              { chat_id: 'chat-B', channel: 'web', label: 'SessB', last_active: ts },
            ],
            orphan_subagents: [],
          },
        },
      }),
    )
    await page.route('**/api/history', (r) => {
      let body: { chat_id?: string } = {}
      try {
        body = JSON.parse(r.request().postData() ?? '{}')
      } catch {
        /* ignore */
      }
      if (body.chat_id === 'chat-B') {
        return r.fulfill({
          json: {
            ok: true,
            data: {
              messages: rowsB,
              chat_id: 'chat-B',
              last_seq: 0,
              // 进行中的 turn（正在 stream 思考）：已提交 20 迭代 + live 思考。
              active_progress: {
                phase: 'thinking',
                turn_id: 2,
                iteration: 21,
                seq: 5,
                stream_content: 'still thinking about the next step...',
                iteration_history: mkIters('B-t2'),
              },
            },
          },
        })
      }
      return r.fulfill({ json: { ok: true, data: { messages: rowsA, chat_id: 'chat-A', last_seq: 0, active_progress: null } } })
    })
    await page.route('**/api/chats/*/switch', (r) => r.fulfill({ json: { ok: true, data: {} } }))
    await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
    await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
    await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
  }

  /** stream 持续推流（live 思考），与"整个 stream 期间"对齐。 */
  async function startStream(page: Page) {
    await page.evaluate(() => {
      const w = window as unknown as { __n?: number }
      w.__n = 0
      window.setInterval(() => {
        const n = (w.__n = (w.__n ?? 0) + 1)
        const listeners = (window as unknown as SSEMockState).__sseListeners
        const push = (type: string, data: Record<string, unknown>) => {
          const handlers = listeners?.[type]
          if (!handlers) return
          const ev = new MessageEvent(type, { data: JSON.stringify({ ...data, seq: 500 + n }) })
          handlers.forEach((h) => h(ev))
        }
        push('stream_content', {
          type: 'stream_content',
          progress: {
            chat_id: 'web:chat-B',
            turn_id: 2,
            iteration: 21,
            reasoning_stream_content: `still thinking chunk #${n} `.repeat(3),
          },
        })
        push('progress_structured', {
          type: 'progress_structured',
          progress: { chat_id: 'web:chat-B', phase: 'thinking', turn_id: 2, iteration: 21 },
        })
      }, 150)
    })
  }

  test('mobile A(long history) → B(streaming): pre-thinking content stays rendered', async ({ browser }) => {
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

    await setupSwitchMock(page)
    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()
    await page.waitForTimeout(3000)

    // 手机端 CPU 4×（交互/渲染真实代价）
    const cdp = await context.newCDPSession(page)
    await cdp.send('Emulation.setCPUThrottlingRate', { rate: 4 })

    // 切换期间内容尚未定形（字体/异步 markdown/图片的真实形态）：
    // 先压扁到 30px，300ms 后放开（+ro 采样 250ms / 复核 400ms 的窗口内）
    await page.addStyleTag({ content: '.iter-block > * { max-height: 30px; overflow: hidden; }' })

    await page.getByRole('button', { name: /会话|sessions/i }).first().click()
    await page.waitForTimeout(400)
    await page.getByText('SessB', { exact: false }).first().click()
    await startStream(page)

    // 等目标会话（B）的块真正挂载（live 行 = B 的 turn 2）—— 压扁态必须覆盖
    // 「RO 首帧 + 250ms 二次采样」这段（settle 窗口），但要在 400ms 复核之前放开。
    await page.waitForFunction(
      () => !!document.querySelector('[data-iter-id="live"][data-turn-id="2"]'),
      undefined,
      { timeout: 20000 },
    )
    await page.waitForTimeout(180)
    const clampedSample = await page.evaluate(() =>
      Array.from(document.querySelectorAll('.iter-block'))
        .slice(0, 6)
        .map((b) => Math.round(b.getBoundingClientRect().height)),
    )
    console.log('CLAMPED-BLOCKS', JSON.stringify(clampedSample))
    await page.waitForTimeout(140)
    await page.addStyleTag({ content: '.iter-block > * { max-height: none; }' })
    // 定形后内容变高 → 窗口化必然生效；等它落地（复核是冻结的前置条件，故需等到
    // 出现 muted 之后再留一段时间让复核/重新结算收敛）。
    await page.waitForFunction(
      () => document.querySelectorAll('.iter-block[data-window-muted="true"]').length > 0,
      undefined,
      { timeout: 20000 },
    )
    await page.waitForTimeout(2500)

    const stats = await page.evaluate(() => {
      const blocks = Array.from(document.querySelectorAll('.iter-block')) as HTMLElement[]
      const muted = blocks.filter((b) => b.dataset.windowMuted === 'true')
      const mounted = blocks.filter((b) => b.dataset.windowMuted !== 'true')
      const anchor = document.querySelector('[data-message-list-content]') as HTMLElement | null
      let sc = anchor?.parentElement as HTMLElement | null
      while (sc) {
        const oy = getComputedStyle(sc).overflowY
        if (oy === 'auto' || oy === 'scroll') break
        sc = sc.parentElement
      }
      const scBox = sc?.getBoundingClientRect()
      const visible = (el: HTMLElement) => {
        const r = el.getBoundingClientRect()
        return !!scBox && r.bottom > scBox.top && r.top < scBox.bottom
      }
      const mutedHeights = muted.map((b) => Math.round(b.getBoundingClientRect().height))
      return {
        nodes: document.querySelectorAll('*').length,
        blocks: blocks.length,
        muted: muted.length,
        mutedMin: mutedHeights.length ? Math.min(...mutedHeights) : -1,
        mutedUnder100: mutedHeights.filter((h) => h < 100).length,
        mutedSample: mutedHeights.slice(0, 8),
        mounted: mounted.length,
        visibleCommitted: blocks.filter(visible).filter((b) => b.dataset.windowMuted !== 'true' && b.dataset.iterId !== 'live').length,
        visibleCommittedText: blocks
          .filter(visible)
          .filter((b) => b.dataset.iterId !== 'live')
          .map((b) => (b.innerText || '').trim().length),
        draftedIterations: blocks.filter((b) => b.dataset.iterId === 'live').length,
      }
    })
    console.log('SWITCH-TRANSIENT-GUARD', JSON.stringify(stats))

    // 1) 窗口化收益没被牺牲（正确性与收益同时成立）
    expect(stats.muted, '窗口化必须仍然生效（muted > 0）').toBeGreaterThan(0)
    // 2) ⛔ 不能有任何块停在瞬态（压扁）高度上 —— 修复前这里是一堆 ~30px
    expect(stats.mutedUnder100, `冻结块不得停在瞬态高度：${JSON.stringify(stats.mutedSample)}`).toBe(0)
    // 3) 思考块之前的已提交内容确实渲染（视口内的已提交块挂载 + 有文字）
    expect(stats.visibleCommitted).toBeGreaterThan(0)
    expect(Math.max(...stats.visibleCommittedText)).toBeGreaterThan(50)
    // 4) DOM 规模仍与迭代数解耦
    expect(stats.nodes).toBeLessThan(2000)

    await context.close()
  })
})

/** mock 历史：turns 个 turn，每个 turn 一条 user + 一条含 iters 个迭代的 assistant。 */
function multiTurnHistory(turns: number, iters: number): unknown[] {
  const ts = new Date().toISOString()
  const out: unknown[] = []
  let id = 1
  for (let turn = 1; turn <= turns; turn++) {
    out.push({ id: id++, role: 'user', content: `user turn ${turn}`, turn_id: turn, timestamp: ts, iterations: [] })
    out.push({
      id: id++,
      role: 'assistant',
      content: '',
      turn_id: turn,
      timestamp: ts,
      iterations: Array.from({ length: iters }, (_, k) => ({
        iteration: k + 1,
        thinking: `thinking ${k + 1} t${turn}`,
        content: Array.from(
          { length: 8 },
          (_, j) => `line ${j} of answer ${k + 1} turn ${turn} — lorem ipsum dolor sit amet, consectetur adipiscing elit sed do eiusmod.`,
        ).join('\n\n'),
        completed_tools: [],
      })),
    })
  }
  return out
}

/**
 * ⛔ 守护（2026-09-13「手机上 iter 多了就卡 / 开侧边栏慢 5-6 倍」的**根因**）：
 * **扰动布局的交互不得让已提交迭代的内容整体重挂载**。
 *
 * 实测根因链（真实 Chromium 390×844 + CDP CPU 4×，mock 40 turn × 40 迭代）：
 *   1. 手机端打开工具页 ⇒ `MobileAppShell` 把 AgentPanel 外壳置 `display:none`
 *      （MobileAppShell.tsx:384）；
 *   2. 消息滚动容器随之变成 0×0（实测 `scrollerH/W = 0`），TanStack virtual-core 的
 *      `observeElementRect` 把这个「没有布局的测量」当成真实几何 → `getVirtualItems()`
 *      塌成空 → **所有 virt-row 卸载**（实测一次交互 320 个 `.iter-block` 全部移除、
 *      DOM 节点 528→173）；
 *   3. 行卸载 ⇒ `CommittedTurn` 实例销毁 ⇒ **实例作用域**的实测高度缓存 / 复核裁决
 *      （TurnBody.tsx 的 `trackerRef` / `verified`）整体丢失；
 *   4. 返回时整行重挂载：没有实测高度 ⇒ 无块可冻结 ⇒ **每个迭代块的内容全部重新
 *      挂载** + markdown 全量重解析（上一轮实测 nodes 313→**5303**、muted 138→**0**、
 *      mounted 301；CDP self time：`measureElement` 875ms / react-markdown ~618ms）。
 *
 * 断言（修复前红 / 修复后绿）：
 *   1. 打开工具页后 `.iter-block` 不得归 0（行不得卸载）、muted 不得归 0；
 *   2. 关掉工具页的**瞬态**（返回后立刻，早于 250ms 结算 + 400ms 复核）muted 仍 > 0
 *      —— 证明复用缓存，而不是靠重新测量"救回来"；
 *   3. 整个交互里 `.iter-block` 的 mount/unmount 计数 ≈ 0；
 *   4. DOM 规模不得成倍爆炸。
 */
test.describe('layout-perturbing interaction must not remount committed iteration content', () => {
  test('opening/closing the mobile tools panel keeps windowing, content mounts and nodes bounded', async ({
    browser,
  }) => {
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

    await setupMock(page, multiTurnHistory(40, 40))
    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()
    await page.waitForTimeout(3000)

    // 到底部（历史首屏 + 高度稳定：settle 需两次同值测量 ≥200ms，复核再 +400ms）
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
    await page.waitForTimeout(4000)

    const before = await windowStats(page)
    console.log('LAYOUT-PERTURB-BEFORE', JSON.stringify(before))
    expect(before.muted, '前置：窗口化必须先成立').toBeGreaterThan(0)

    // 交互期间统计 `.iter-block` 的挂载/卸载次数（任何子树里被增删的都算）。
    await page.evaluate(() => {
      const w = window as unknown as { __iterChurn: { added: number; removed: number } }
      w.__iterChurn = { added: 0, removed: 0 }
      const count = (n: Node): number => {
        if (n.nodeType !== 1) return 0
        const el = n as HTMLElement
        let c = el.classList?.contains('iter-block') ? 1 : 0
        c += el.querySelectorAll?.('.iter-block').length ?? 0
        return c
      }
      new MutationObserver((records) => {
        for (const r of records) {
          for (const n of r.addedNodes) w.__iterChurn.added += count(n)
          for (const n of r.removedNodes) w.__iterChurn.removed += count(n)
        }
      }).observe(document.body, { subtree: true, childList: true })
    })

    // ── 开（手机端「工具」页 = 右侧栏的等价物） ──
    await page.getByRole('button', { name: /^tools$|工具/i }).first().click()
    await page.waitForFunction(() => !!document.querySelector('[role="tablist"]'), undefined, { timeout: 20000 })
    await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))))
    await page.waitForTimeout(300)
    const openStats = await windowStats(page)
    console.log('LAYOUT-PERTURB-OPEN', JSON.stringify(openStats))

    // ── 关（返回 agent 视图） ──
    await page.locator('header button').first().click()
    await page.waitForFunction(() => document.querySelectorAll('.iter-block').length > 0, undefined, {
      timeout: 20000,
    })
    // 瞬态读数：必须早于「250ms 结算 + 400ms 复核」，否则只证明"重新测回来了"
    const backTransient = await windowStats(page)
    console.log('LAYOUT-PERTURB-BACK-TRANSIENT', JSON.stringify(backTransient))
    await page.waitForTimeout(2500)
    const after = await windowStats(page)
    const churn = await page.evaluate(
      () => (window as unknown as { __iterChurn: { added: number; removed: number } }).__iterChurn,
    )
    console.log('LAYOUT-PERTURB-AFTER', JSON.stringify(after), 'CHURN', JSON.stringify(churn))

    // 1) 打开工具页时行不得卸载（修复前这里是 blocks=0 / muted=0）
    expect(openStats.blocks, '开工具页后迭代块外壳必须仍在（行不得卸载）').toBeGreaterThan(0)
    expect(openStats.muted, '开工具页后窗口化判定不得归 0').toBeGreaterThan(0)
    // 2) 返回后的瞬态：内容不得整体重挂载（修复前 muted = 0）
    expect(backTransient.muted, '返回瞬态里窗口化必须仍然成立（缓存复用，不是重新测回来）').toBeGreaterThan(0)
    // 3) 整个交互的迭代块增删 ≈ 0（修复前 added+removed = 640：320 卸载 + 320 重挂）
    expect(churn.added + churn.removed, `.iter-block 不得因交互重挂载：${JSON.stringify(churn)}`).toBeLessThanOrEqual(10)
    // 4) DOM 规模不得成倍爆炸（修复后现场实测 5303 → 与 before 同量级）
    expect(after.nodes, `DOM 不得因交互膨胀：before=${before.nodes} after=${after.nodes}`).toBeLessThan(before.nodes * 2)
    expect(after.muted, '交互后窗口化仍成立').toBeGreaterThan(0)

    await context.close()
  })
})
