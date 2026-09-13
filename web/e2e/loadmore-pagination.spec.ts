import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * 向上翻页（loadMore）分页 **断言** harness（原为纯测量；红灯 → 断言 → 绿）。
 *
 * 断言（每条都对应一次真实故障）：
 *   1. 一次「向上翻页」手势 ⇒ **恰好 1 次** loadMore 请求（修复前长 turn 实测 10 次）；
 *   2. 连续 3 次手势 ⇒ 恰好 3 次，且每次推进游标；
 *   3. 游标严格前进：首个 loadMore 续上初始窗口 oldest_id，之后每页续上上一页
 *      oldest_id 并严格递减；**同一游标不得出现两次**（覆盖"初始 reload 重复拉
 *      同一页"—— 长 turn 首屏稳定复现 #2 == #1）；
 *   4. 哨兵 IntersectionObserver 不得随 loading 翻转/渲染重建（修复前一次手势
 *      +19 次 observe；整个会话只允许 1 次）。
 *
 * mock `/api/history` 按真实服务端语义实现：
 *  - 游标：`id < before_id`（排他），升序返回，`oldest_id` = 本页最老 DB 行 id，
 *    `has_more = total > len(page)`（total = before_id 之前的 display 行数）。
 *  - **payload 转换**：镜像 `channel/subscription.go` 的
 *    ConvertMessagesToHistoryWithIterations —— 一个 turn 的多个中间 assistant 行
 *    被吸收，只 emit 一个（`flushPending`，id = 窗口内最后一条 assistant 行 id，
 *    iterations = 该 turn 的**完整** iteration_history）。所以「一页 DB 行」与
 *    「一页渲染行」不是一回事：长 turn 的一页 DB 行可能 emit 出**零个新渲染行**。
 */

interface DbRow {
  id: number
  role: 'user' | 'assistant' | 'tool'
  turn_id: number
  content: string
  ts: string
  tool_calls?: { id: string; name: string }[]
}

interface PayloadRow {
  id: number
  role: 'user' | 'assistant'
  content: string
  turn_id: number
  timestamp: string
  iterations?: { iteration: number; content?: string }[]
}

const iso = (i: number) => new Date(1700000000000 + i * 1000).toISOString()

function makeTurnIters(turnIters: Map<number, number>) {
  const map = new Map<number, { iteration: number; content: string }[]>()
  for (const [turnID, n] of turnIters) {
    map.set(
      turnID,
      Array.from({ length: n }, (_, k) => ({
        iteration: k + 1,
        content: Array.from(
          { length: 6 },
          (_, j) => `turn ${turnID} iter ${k + 1} line ${j + 1} — lorem ipsum dolor sit amet.`,
        ).join('\n\n'),
      })),
    )
  }
  return map
}

/**
 * 镜像 ConvertMessagesToHistoryWithIterations：把窗口内的 DB 行折叠成 payload 行。
 * 只保留本 harness 需要的分支：user / 中间 assistant（累积 → flushPending）/
 * 最终 assistant（结构化 iteration_history 权威）。
 */
function emitPayload(
  window: DbRow[],
  turnIterMap: Map<number, { iteration: number; content: string }[]>,
): PayloadRow[] {
  const out: PayloadRow[] = []
  let pendingIters: { iteration: number }[] = []
  let curIterTools: string[] = []
  let curIterIdx = 0
  let lastAssistantID = 0
  let pendingTurnID = 0
  const finishCurIter = () => {
    if (curIterTools.length > 0) {
      pendingIters.push({ iteration: curIterIdx })
      curIterTools = []
    }
  }
  const flushPending = () => {
    finishCurIter()
    curIterIdx = 0
    if (pendingIters.length > 0) {
      const iters = turnIterMap.get(pendingTurnID) ?? pendingIters
      out.push({
        id: lastAssistantID,
        role: 'assistant',
        content: '',
        turn_id: pendingTurnID,
        timestamp: iso(lastAssistantID),
        iterations: iters,
      })
      pendingIters = []
    }
  }
  for (const m of window) {
    if (m.role === 'tool') continue
    if (m.role === 'assistant') {
      lastAssistantID = m.id
      const isIntermediate = (m.tool_calls?.length ?? 0) > 0
      if (!isIntermediate && m.turn_id > 0 && turnIterMap.has(m.turn_id)) {
        if (pendingTurnID > 0 && m.turn_id !== pendingTurnID && pendingIters.length > 0) {
          flushPending()
        }
        finishCurIter()
        pendingIters = []
        out.push({
          id: m.id,
          role: 'assistant',
          content: m.content,
          turn_id: m.turn_id,
          timestamp: m.ts,
          iterations: turnIterMap.get(m.turn_id)!,
        })
        continue
      }
      if (isIntermediate) {
        pendingTurnID = m.turn_id
        for (const tc of m.tool_calls ?? []) curIterTools.push(tc.name)
        curIterIdx += 1
        continue
      }
      flushPending()
      if (m.content) {
        out.push({
          id: m.id,
          role: 'assistant',
          content: m.content,
          turn_id: m.turn_id,
          timestamp: m.ts,
        })
      }
      continue
    }
    flushPending()
    out.push({
      id: m.id,
      role: 'user',
      content: m.content,
      turn_id: m.turn_id,
      timestamp: m.ts,
    })
  }
  flushPending()
  return out
}

/** 场景 L：尾部有一个超长 turn（1000 个中间 iteration = 1000 个 DB 行）。 */
function scenarioLongTurn() {
  const rows: DbRow[] = []
  const turnIters = new Map<number, number>()
  let id = 1
  for (let t = 1; t <= 20; t++) {
    rows.push({ id: id++, role: 'user', turn_id: t, content: `turn ${t} question`, ts: iso(id) })
    rows.push({ id: id++, role: 'assistant', turn_id: t, content: `turn ${t} answer`, ts: iso(id) })
    turnIters.set(t, 1)
  }
  const T = 21
  rows.push({ id: id++, role: 'user', turn_id: T, content: 'huge question', ts: iso(id) })
  for (let k = 0; k < 1000; k++) {
    rows.push({
      id: id++,
      role: 'assistant',
      turn_id: T,
      content: '',
      ts: iso(id),
      tool_calls: [{ id: `call-${k}`, name: 'shell' }],
    })
  }
  rows.push({ id: id++, role: 'assistant', turn_id: T, content: 'huge final answer', ts: iso(id) })
  turnIters.set(T, 1000)
  return { rows, turnIterMap: makeTurnIters(turnIters) }
}

/** 场景 N：1200 个普通 turn（各 2 行，1 个 iteration）。 */
function scenarioNormalTurns() {
  const rows: DbRow[] = []
  const turnIters = new Map<number, number>()
  let id = 1
  for (let t = 1; t <= 1200; t++) {
    rows.push({
      id: id++,
      role: 'user',
      turn_id: t,
      content: `turn ${t} question\n\nline2\n\nline3`,
      ts: iso(id),
    })
    rows.push({
      id: id++,
      role: 'assistant',
      turn_id: t,
      content: `turn ${t} answer\n\nline2\n\nline3`,
      ts: iso(id),
    })
    turnIters.set(t, 1)
  }
  return { rows, turnIterMap: makeTurnIters(turnIters) }
}

interface HistReq {
  seq: number
  t: number
  beforeId: number
  limit: number
  dbRows: number
  payloadRows: number
  oldestId: number
  hasMore: boolean
  newPayloadRows: number
  newTurns: number
}

interface Harness {
  log: HistReq[]
  deliveredIds: Set<number>
  deliveredTurns: Set<number>
  io: {
    observes: number
    callbacks: number
    intersecting: number
    /** 只统计**哨兵**（`[data-loadmore-sentinel]`）的 observe 次数 —— loadMore 的
     *  IntersectionObserver 是否在随渲染重建（修复前 ≈ 每次请求 +100，实测 2037）。
     *  另算一个全量计数是为了对照：TurnBody 的迭代窗口化 observer 是按**已挂载
     *  迭代块**建的（与 loadMore 无关），会把全量计数带偏。 */
    sentinelObserves: number
    sentinelCallbacks: number
    /** 文档实例数（每个整页重载 +1）：用于识别"测量期间发生整页重载"（dev HMR /
     *  导航）—— 这一轮测量作废，harness 必须重置后重做。 */
    docs: number
  }
  scroll: { t: number; scrollTop: number; scrollHeight: number }[]
  /** 已同步到的文档实例数（见 io.docs）。 */
  docs: number
}

function newHarness(): Harness {
  return {
    log: [],
    deliveredIds: new Set(),
    deliveredTurns: new Set(),
    io: { observes: 0, callbacks: 0, intersecting: 0, sentinelObserves: 0, sentinelCallbacks: 0, docs: 0 },
    scroll: [],
    docs: 0,
  }
}

async function setupMock(
  page: Page,
  h: Harness,
  scenario: { rows: DbRow[]; turnIterMap: Map<number, { iteration: number; content: string }[]> },
  delayMs = 0,
) {
  await page.addInitScript(() => {
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
    const w = window as unknown as { __sseListeners: typeof listeners }
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

    // 计数器**跨整页重载存活**（sessionStorage）：dev server 的 HMR / 任何导航都会
    // 新建 window 与新的 stats 对象；若只用新对象，一次手势前后的增量会算成负数
    //（实测 -1，其实是重载把 app 的 observer 全清掉了）。docs 记录文档实例数，
    // 用于识别"测量期间发生了整页重载"（该轮测量作废，harness 重置后重做）。
    const KEY = '__loadmoreIoStats'
    let stats: {
      observes: number
      callbacks: number
      intersecting: number
      sentinelObserves: number
      sentinelCallbacks: number
      docs: number
    } = {
      observes: 0,
      callbacks: 0,
      intersecting: 0,
      sentinelObserves: 0,
      sentinelCallbacks: 0,
      docs: 0,
    }
    try {
      const saved = sessionStorage.getItem(KEY)
      if (saved) stats = { ...stats, ...(JSON.parse(saved) as typeof stats) }
    } catch {
      /* sessionStorage 不可用 → 退化为单文档计数 */
    }
    stats.docs += 1
    const persist = () => {
      try {
        sessionStorage.setItem(KEY, JSON.stringify(stats))
      } catch {
        /* ignore */
      }
    }
    persist()
    ;(window as unknown as { __ioStats: typeof stats }).__ioStats = stats
    const isSentinel = (t: Element | null | undefined) =>
      !!t && (t as HTMLElement).hasAttribute?.('data-loadmore-sentinel') === true
    const Orig = window.IntersectionObserver
    class WrappedIO {
      private inner: IntersectionObserver
      private targets = new Set<Element>()
      constructor(cb: IntersectionObserverCallback, opts?: IntersectionObserverInit) {
        this.inner = new Orig((entries, obs) => {
          stats.callbacks++
          if (entries[0]?.isIntersecting) stats.intersecting++
          if (Array.from(this.targets).some(isSentinel)) stats.sentinelCallbacks++
          persist()
          cb(entries, obs)
        }, opts)
      }
      observe(target: Element) {
        stats.observes++
        this.targets.add(target)
        if (isSentinel(target)) stats.sentinelObserves++
        persist()
        this.inner.observe(target)
      }
      unobserve(target: Element) {
        this.inner.unobserve(target)
      }
      disconnect() {
        this.inner.disconnect()
      }
      takeRecords() {
        return this.inner.takeRecords()
      }
      get root() {
        return this.inner.root
      }
      get rootMargin() {
        return this.inner.rootMargin
      }
      get thresholds() {
        return this.inner.thresholds
      }
    }
    ;(window as unknown as { IntersectionObserver: unknown }).IntersectionObserver = WrappedIO
  })

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
  await page.route('**/api/history', async (route) => {
    const body = (route.request().postDataJSON() ?? {}) as { limit?: number; before_id?: number }
    const limit = body.limit && body.limit > 0 ? body.limit : 30
    const beforeId = body.before_id && body.before_id > 0 ? body.before_id : Number.MAX_SAFE_INTEGER
    const before = scenario.rows.filter((r) => r.id < beforeId)
    const total = before.length
    const dbPage = before.slice(Math.max(0, total - limit))
    const oldestId = dbPage.length > 0 ? dbPage[0].id : 0
    const hasMore = total > dbPage.length
    const payload = emitPayload(dbPage, scenario.turnIterMap)
    let newPayloadRows = 0
    const newTurnsThisPage = new Set<number>()
    for (const p of payload) {
      if (!h.deliveredIds.has(p.id)) newPayloadRows++
      h.deliveredIds.add(p.id)
      if (p.turn_id > 0 && !h.deliveredTurns.has(p.turn_id)) newTurnsThisPage.add(p.turn_id)
    }
    for (const t of newTurnsThisPage) h.deliveredTurns.add(t)
    h.log.push({
      seq: h.log.length + 1,
      t: Date.now(),
      beforeId,
      limit,
      dbRows: dbPage.length,
      payloadRows: payload.length,
      oldestId,
      hasMore,
      newPayloadRows,
      newTurns: newTurnsThisPage.size,
    })
    if (delayMs > 0) await new Promise((r) => setTimeout(r, delayMs))
    await route.fulfill({
      json: {
        ok: true,
        data: {
          messages: payload,
          chat_id: 'chat-1',
          channel: 'web',
          last_seq: 0,
          active_progress: null,
          has_more: hasMore,
          oldest_id: oldestId,
        },
      },
    })
  })
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

async function login(page: Page) {
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForTimeout(2500)
}

async function getScroller(page: Page) {
  return page.evaluate(() => {
    const anchor = document.querySelector('[data-message-list-content]') as HTMLElement | null
    let sc = anchor?.parentElement as HTMLElement | null
    while (sc) {
      const oy = getComputedStyle(sc).overflowY
      if (oy === 'auto' || oy === 'scroll') break
      sc = sc.parentElement
    }
    if (!sc) return null
    return { scrollTop: sc.scrollTop, scrollHeight: sc.scrollHeight, clientHeight: sc.clientHeight }
  })
}

/** 把视口直接写到指定 scrollTop（真实用户"滑到顶并贴住"的等价终态）。
 *  列表可能被 app 短暂替换成 loading/welcome 视图 —— 先等元素回来（最多 3s），
 *  超时把当时的 DOM 状态打出来，避免只丢一句 "scroller not found"。 */
async function forceScrollTop(page: Page, top: number) {
  const deadline = Date.now() + 3000
  for (;;) {
    const res = await page.evaluate((t) => {
      const anchor = document.querySelector('[data-message-list-content]') as HTMLElement | null
      let sc = anchor?.parentElement as HTMLElement | null
      while (sc) {
        const oy = getComputedStyle(sc).overflowY
        if (oy === 'auto' || oy === 'scroll') break
        sc = sc.parentElement
      }
      if (!sc) {
        return {
          ok: false as const,
          hasAnchor: !!anchor,
          text: document.body.innerText.slice(0, 160).replace(/\s+/g, ' '),
        }
      }
      sc.scrollTop = t
      sc.dispatchEvent(new Event('scroll'))
      return { ok: true as const }
    }, top)
    if (res.ok) return
    if (Date.now() > deadline) {
      console.log(`SCROLLER-MISSING hasAnchor=${res.hasAnchor} text=${JSON.stringify(res.text)}`)
      throw new Error('scroller not found')
    }
    await page.waitForTimeout(100)
  }
}

/** **一次**「向上翻页」手势（真实用户：把视口从当前内容滑到最顶并停住）。
 *
 *  两个阶段都必须有，缺一个就不是"一次手势"：
 *   1. 先 `wheel(+300)` 把视口移离顶部 —— 这是**手势的真实起点**（用户上一次
 *      翻页后视口停在中间，手指再次向下滑之前，哨兵本就在视口外）；
 *   2. 再 `wheel(-400)` ×2 + 直接写到 `scrollTop=0`，形成一次完整的"到达顶部"。
 *
 *  wheel 事件同时承担 pauseFollowing 的触发（否则 app 的 stick-to-bottom 会把
 *  我们写的 scrollTop 立刻拽回底部 —— harness 假象）。 */
async function onePageUpGesture(page: Page) {
  await page.mouse.move(195, 500)
  await page.mouse.wheel(0, 300)
  await page.waitForTimeout(200)
  await page.mouse.wheel(0, -400)
  await page.waitForTimeout(150)
  await page.mouse.wheel(0, -400)
  await page.waitForTimeout(250)
  await forceScrollTop(page, 0)
}

/** 等所有在手势里被触发的请求落地并静默下来（异步请求 + 渲染需要时间）。 */
async function settle(page: Page, h: Harness, quietMs = 1500, maxMs = 15000) {
  const t0 = Date.now()
  let last = h.log.length
  let lastChange = Date.now()
  while (Date.now() - t0 < maxMs) {
    await page.waitForTimeout(200)
    if (h.log.length !== last) {
      last = h.log.length
      lastChange = Date.now()
    } else if (Date.now() - lastChange >= quietMs) return
  }
}

function logLine(e: Harness['log'][number], t0?: number) {
  const rel = t0 === undefined ? '' : ` +${Math.round(e.t - t0)}ms`
  return `#${e.seq}${rel} before_id=${e.beforeId}${e.beforeId === Number.MAX_SAFE_INTEGER ? '(RELOAD)' : ''} -> dbRows=${e.dbRows} payload=${e.payloadRows} oldest_id=${e.oldestId} has_more=${e.hasMore} newPayloadRows=${e.newPayloadRows} newTurns=${e.newTurns}`
}

function reqLog(h: Harness) {
  const t0 = h.log[0]?.t
  return h.log.map((e) => logLine(e, t0)).join('\n  ')
}

/**
 * 游标契约：**严格前进**（loadMore 侧）
 *   - 第一条请求必须是"无界游标"（首屏 reload 拉最新 100 条）；
 *   - 首个 loadMore 必须续上**初始窗口的 oldest_id**；
 *   - 之后每页的 `before_id` 必须等于**上一页响应的 `oldest_id`**
 *     （服务端游标语义：`id < before_id`，响应回传本页最老行 id）；
 *   - 因此 before_id 序列严格单调递减，且**同一游标不得出现两次**（见
 *     expectNoDuplicateCursor —— 它覆盖"初始 reload 重复拉同一页"）。
 *
 * loadMore = 带游标的请求；`before_id === MAX_SAFE_INTEGER` 的是**全量 reload**
 * （首屏 / SSE 重连 resync / 整页重载后的启动），不属于"向上翻页"契约。
 */
const isLoadMore = (e: Harness['log'][number]) => e.beforeId !== Number.MAX_SAFE_INTEGER
const loadMores = (h: Harness, from = 0) => h.log.slice(from).filter(isLoadMore)

/** loadMore 的游标契约：首条必须续上**初始窗口的最老行**，之后每条严格续上
 *  上一条响应的 `oldest_id`（游标语义 `id < before_id`）并严格递减。 */
function expectLoadMoreCursorChain(h: Harness) {
  const pages = loadMores(h)
  expect(pages.length, `必须有 loadMore 请求：\n  ${reqLog(h)}`).toBeGreaterThan(0)
  expect(
    pages[0].beforeId,
    `首个 loadMore 的 before_id 必须是初始窗口的 oldest_id：\n  ${reqLog(h)}`,
  ).toBe(h.log[0].oldestId)
  for (let i = 1; i < pages.length; i++) {
    expect(
      pages[i].beforeId,
      `第 ${i + 1} 页的 before_id 必须等于第 ${i} 页响应的 oldest_id：\n  ${reqLog(h)}`,
    ).toBe(pages[i - 1].oldestId)
    expect(
      pages[i].beforeId,
      `第 ${i + 1} 页的 before_id 必须严格小于第 ${i} 页：\n  ${reqLog(h)}`,
    ).toBeLessThan(pages[i - 1].beforeId)
  }
}

/** 同一游标不得被请求两次（同一页不得被拉两遍）—— 覆盖"初始 reload 重复拉同一页"。 */
function expectNoDuplicateCursor(h: Harness) {
  const seen = new Set<number>()
  for (const e of h.log) {
    expect(
      seen.has(e.beforeId),
      `同一游标被请求了两次（#${e.seq} before_id=${e.beforeId}）—— 重复拉同一页：\n  ${reqLog(h)}`,
    ).toBe(false)
    seen.add(e.beforeId)
  }
}

async function readIo(page: Page): Promise<Harness['io']> {
  const stats = (await page.evaluate(() => (window as unknown as { __ioStats?: unknown }).__ioStats)) as
    | Harness['io']
    | undefined
  return stats ?? { observes: 0, callbacks: 0, intersecting: 0, sentinelObserves: 0, sentinelCallbacks: 0, docs: 0 }
}

/**
 * 整页重载（dev server HMR / 任何导航）会把 app 状态、MessageList 实例、observer
 * 全部重置。它会往日志里混进"重载后的启动 reload"（before_id=MAX），表现为
 * **同一游标被拉两次**的假红。判定到 docs 递增时把 harness 重置为"新的一致会话"，
 * 由调用方决定重做这一轮手势。
 * @returns true = 检测到整页重载（本轮测量作废）
 */
async function resetIfPageReloaded(page: Page, h: Harness, label: string) {
  const io = await readIo(page)
  if (io.docs === h.docs) return false
  console.log(`[${label}] 整页重载（docs ${h.docs} → ${io.docs}）—— 重置 harness，本轮测量重做`)
  h.log = []
  h.deliveredIds = new Set()
  h.deliveredTurns = new Set()
  h.scroll = []
  h.docs = io.docs
  h.io = io
  return true
}

/**
 * **一次**「向上翻页」手势 ⇒ 断言恰好 1 次 loadMore。
 * 若这一轮被整页重载（dev HMR）打断，则重新建立基线后重做（最多 3 次）——
 * 这是对"测量作废"的处理，不是对断言的放宽：有效测量窗口内仍然是恰好 1 次。
 * @returns 该手势的 loadMore 请求
 */
async function gestureExpectOneLoadMore(page: Page, h: Harness, label: string) {
  for (let attempt = 1; attempt <= 3; attempt++) {
    await resetIfPageReloaded(page, h, label)
    const before = h.log.length
    await onePageUpGesture(page)
    await settle(page, h)
    if (await resetIfPageReloaded(page, h, label)) continue
    const made = h.log.length - before
    console.log(`[${label}] 手势${attempt > 1 ? `重做#${attempt}` : ''}: requests=${made} 末条=${logLine(h.log[h.log.length - 1], h.log[0].t)}`)
    expect(made, `${label}：一次手势只允许 1 次 loadMore（实测 ${made} 次）：\n  ${reqLog(h)}`).toBe(1)
    return h.log[h.log.length - 1]
  }
  throw new Error(`${label}：连续 3 次手势都被整页重载打断（dev server HMR / 导航），测量无法进行`)
}

/** 客户端可见状态：渲染行数 / 行 id / 内容总高（判断"这一页有没有真的加内容"）。 */
async function clientRows(page: Page) {
  return page.evaluate(() => {
    const anchor = document.querySelector('[data-message-list-content]') as HTMLElement | null
    const ids = Array.from(document.querySelectorAll('[data-message-id]')).map(
      (e) => (e as HTMLElement).dataset.messageId ?? '',
    )
    return {
      renderedRows: ids.length,
      contentHeight: anchor ? Math.round(anchor.getBoundingClientRect().height) : 0,
      firstIds: ids.slice(0, 3),
      lastIds: ids.slice(-3),
    }
  })
}

async function sampleScroll(page: Page, h: Harness, times = 24, gapMs = 250) {
  for (let i = 0; i < times; i++) {
    const m = await getScroller(page)
    if (m) h.scroll.push({ t: Date.now(), ...m })
    await page.waitForTimeout(gapMs)
  }
}

function dump(h: Harness, label: string) {
  console.log(`\n===== ${label} =====`)
  console.log(`requests=${h.log.length}`)
  for (const e of h.log) {
    console.log(
      `  #${e.seq} before_id=${e.beforeId} -> dbRows=${e.dbRows} payload=${e.payloadRows} oldest_id=${e.oldestId} has_more=${e.hasMore} newPayloadRows=${e.newPayloadRows} newTurns=${e.newTurns}`,
    )
  }
  const pages = h.log.slice(1)
  console.log(`  loadMore_requests=${pages.length}`)
  console.log(`  distinct_cursors=${new Set(h.log.map((e) => e.beforeId)).size}`)
  console.log(`  pages_with_newRows=${pages.filter((e) => e.newPayloadRows > 0).length}`)
  console.log(`  pages_with_newTurns(会新增 slot)=${pages.filter((e) => e.newTurns > 0).length}`)
  console.log(`  io=${JSON.stringify(h.io)}`)
  console.log(`  scrollTop_trajectory=${h.scroll.map((s) => Math.round(s.scrollTop)).join(',')}`)
}

const IO_OBSERVE_BUDGET = 100

for (const [name, build] of [
  ['LONG-TURN(尾部超长 turn)', scenarioLongTurn],
  ['NORMAL-TURNS(1200 普通 turn)', scenarioNormalTurns],
] as const) {
  test.describe(`loadMore 断言 — ${name}`, () => {
    test('一次「向上翻页」手势 ⇒ 恰好 1 次请求 + 游标严格前进 + IO 观察器不膨胀', async ({
      browser,
    }) => {
      const scenario = build()
      const h = newHarness()
      const page = await browser.newPage({ viewport: { width: 390, height: 844 } })
      await setupMock(page, h, scenario, 80)
      await login(page)
      await settle(page, h, 1200)

      const io0 = await readIo(page)
      h.docs = io0.docs
      console.log(`[${name}] 初始 reload requests=${h.log.length} io=${JSON.stringify(io0)}`)
      const m0 = await getScroller(page)
      const c0 = await clientRows(page)
      console.log(`[${name}] 手势前 scroller=${JSON.stringify(m0)} client=${JSON.stringify(c0)}`)

      await gestureExpectOneLoadMore(page, h, `${name} 单手势`)
      await sampleScroll(page, h, 4, 150)

      const io1 = await readIo(page)
      h.io = io1
      dump(h, `ONE GESTURE TO TOP — ${name}`)
      console.log(
        `[${name}] 手势后 scroller=${JSON.stringify(await getScroller(page))} client=${JSON.stringify(await clientRows(page))} observes_delta=${io1.observes - io0.observes}`,
      )

      // 游标严格前进（loadMore 契约）+ 同一游标不得重复请求（覆盖"初始 reload
      // 重复拉同一页"—— 长 turn 首屏稳定复现 #2 == #1 的同 cursor 重复）
      expectLoadMoreCursorChain(h)
      expectNoDuplicateCursor(h)
      // 哨兵 observer 不得随渲染次数/每次 loading 翻转重建。
      // 注意：harness 的**全量** observes 计数会被 TurnBody 的迭代窗口化 observer
      // 带偏（它按已挂载的迭代块逐个 observe，1000 iter 的长 turn ≈ 1000+ 次，
      // 与 loadMore 无关），所以这里用哨兵作用域的计数：
      //   修复前：每次 loading 翻转都重建哨兵 observer（一次手势 +19）
      //   修复后：整个会话只 observe 1 次
      expect(
        h.io.sentinelObserves,
        `哨兵 IntersectionObserver 的 observe 次数必须 < ${IO_OBSERVE_BUDGET}（实测 ${h.io.sentinelObserves}，初始 ${io0.sentinelObserves}）`,
      ).toBeLessThan(IO_OBSERVE_BUDGET)
      expect(
        h.io.sentinelObserves,
        `一次手势期间不得重建/重挂哨兵 observer（实测 ${io0.sentinelObserves} → ${h.io.sentinelObserves}）`,
      ).toBe(io0.sentinelObserves)
      // 真的加载到了更老的数据（不是"少发几次"而是"每页都推进"）
      const last = h.log[h.log.length - 1]
      expect(last.newPayloadRows + last.newTurns, '这一页不得是空页').toBeGreaterThan(0)
    })

    test('连续 3 次手势 ⇒ 恰好 3 次请求，每次推进游标', async ({ browser }) => {
      const scenario = build()
      const h = newHarness()
      const page = await browser.newPage({ viewport: { width: 390, height: 844 } })
      await setupMock(page, h, scenario, 80)
      await login(page)
      await settle(page, h, 1200)

      const io0 = await readIo(page)
      h.docs = io0.docs
      const cursors: number[] = []
      for (let g = 1; g <= 3; g++) {
        const page1 = await gestureExpectOneLoadMore(page, h, `${name} 手势#${g}`)
        cursors.push(page1.oldestId)
      }
      const io1 = await readIo(page)
      h.io = io1
      dump(h, `THREE GESTURES TO TOP — ${name}`)
      console.log(`[${name}] 3 次手势 observes_delta=${io1.observes - io0.observes}`)

      expectLoadMoreCursorChain(h)
      expectNoDuplicateCursor(h)
      expect(cursors[0], '第 1 次手势必须推进游标').toBeLessThan(h.log[0].oldestId)
      expect(cursors[1], '第 2 次手势必须继续推进游标').toBeLessThan(cursors[0])
      expect(cursors[2], '第 3 次手势必须继续推进游标').toBeLessThan(cursors[1])
      expect(
        h.io.sentinelObserves,
        `哨兵 observe 次数必须 < ${IO_OBSERVE_BUDGET}（实测 ${h.io.sentinelObserves}）`,
      ).toBeLessThan(IO_OBSERVE_BUDGET)
      expect(
        h.io.sentinelObserves,
        `3 次手势期间不得重建/重挂哨兵 observer（实测 ${io0.sentinelObserves} → ${h.io.sentinelObserves}）`,
      ).toBe(io0.sentinelObserves)
    })
  })
}
