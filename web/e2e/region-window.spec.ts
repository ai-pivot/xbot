import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * 折叠视图（展示区域窗口 + 工具详情按需）**端到端守护** ——
 * `docs/plan-history-fold-windowing.md` §6-T11 / §8。
 *
 * 覆盖（全部 page.route mock 后端，不起真服务）：
 *   1. 默认视图无感（G2 核心验收）：同一 turn 的「全量下发」vs「折叠窗口下发
 *      （tools_folded 轻字段 + regions_before 缺省）」两种 mock ⇒ **DOM 等价**
 *      （pill 数 / pill 工具名 / 迭代块 id / 迭代范围全等），且零 `/api/regions`、
 *      零 `/api/iteration_detail` 请求。
 *   2. `regions_before > 0` + 上滚自动加载：分隔条渲染 → 一次手势**恰好 1 次**
 *      `/api/regions`（连续滚动不风暴）→ 段插入后迭代块增长、`data-regions-before`
 *      递减、归零后分隔条消失；拼接处**迭代号连续无洞**（`data-iter-range`）。
 *   3. 浮层详情按需：`tools_folded` 迭代的 pill 点击 ⇒ 1 次 `/api/iteration_detail`
 *      → 详情（summary/args）渲染；同迭代第二个工具 ⇒ **0 新请求**（迭代级缓存）；
 *      非 `tools_folded` 迭代 ⇒ 0 请求直接渲染。
 *   4. 加载失败降级：`/api/regions` 500 ⇒ 分隔条 `data-state="error"`；连续失败
 *      达 `MAX_AUTO_REGION_FAILURES` 后新手势也不再自动发（保守停止）；点击 retry
 *      → **再发 1 次**；恢复 200 → 正常插入、分隔条消失。
 *   5. busy 恢复：`/api/history` 的 `active_progress`（折叠窗口 + iteration_regions_before）
 *      ⇒ live 行顶部渲染分隔条 + 上滚取段。**依赖 live 闭环 patch，见该用例内的说明**。
 *
 * 断言纪律（i18n）：一律走 `data-testid` / `data-*` 属性与 DOM 结构，**不断言具体语言文案**。
 *
 * mock 纪律（方案 §7-R6）：字段以**冻结形状**为准 ——
 *   · `HistoryMessage.regions_before`：omitempty ⇒ **0 不出现**（>0 才出现）；
 *   · `HistoryIteration.tools_folded`：omitempty，轻字段迭代的工具**不带**
 *     summary/args/detail（只带 name/label/status/elapsed_ms）；
 *   · `POST /api/regions` req `{channel,chat_id,turn_id,before_iteration,region_limit}` →
 *     200 `{ok:true, data:{iterations:[…], regions_before}}`（`writeJSON` 把扁平平铺进 data，
 *     见 `channel/web/web_auth.go:511`；客户端 `postAPI` 只还 data，见 `web/src/lib/api.ts:44`）；
 *   · `POST /api/iteration_detail` req `{channel,chat_id,turn_id,iteration}` →
 *     200 `{ok:true, data:{iteration:{完整字段}}}`；未命中 404。
 */

// ─────────────────────────────────────────────────────────────────────────────
// 载荷构造（冻结形状）
// ─────────────────────────────────────────────────────────────────────────────

const iso = (i: number) => new Date(1700000000000 + i * 1000).toISOString()

/** 一条日志行的正文（高度可观：保证列表可滚动 ⇒ 分隔条哨兵能离开/回到视口）。 */
function body(tag: string, lines = 8): string {
  return Array.from({ length: lines }, (_, i) => `${tag} — line ${i + 1}: 历史正文占位。`).join('\n\n')
}

interface ToolSpec {
  name: string
  /** 完整字段（全量下发时使用；轻字段形态不带）。 */
  summary?: string
  args?: string
  detail?: string
}

interface IterSpec {
  iteration: number
  content?: string
  tools?: ToolSpec[]
  /** true = 后端 `tools_folded` 轻字段形态（详情省略）。 */
  folded?: boolean
}

/** 「折叠窗口」形态的迭代：工具**只带 pill 渲染所需轻字段**，迭代级打 `tools_folded`。 */
function iterPayload(spec: IterSpec): Record<string, unknown> {
  const tools = (spec.tools ?? []).map((t) =>
    spec.folded
      ? { name: t.name, label: `${t.name} 参数摘要`, status: 'done', elapsed_ms: 12 }
      : {
          name: t.name,
          label: `${t.name} 参数摘要`,
          status: 'done',
          elapsed_ms: 12,
          summary: t.summary ?? '',
          args: t.args ?? '',
          detail: t.detail ?? '',
        },
  )
  return {
    iteration: spec.iteration,
    content: spec.content ?? '',
    ...(tools.length > 0 ? { tools } : {}),
    // omitempty：缺省 = 完整（normalize 归一成 false，见 normalize.ts:56）
    ...(spec.folded ? { tools_folded: true } : {}),
  }
}

interface MsgSpec {
  id: number
  turnID: number
  iters: IterSpec[]
  /** >0 才出现（后端 omitempty；0/缺省不出现）。 */
  regionsBefore?: number
  content?: string
}

function historyMessages(specs: MsgSpec[]): unknown[] {
  const out: unknown[] = []
  for (const s of specs) {
    out.push({
      id: s.id - 1,
      role: 'user',
      content: `提问 turn ${s.turnID}`,
      turn_id: s.turnID,
      timestamp: iso(s.id - 1),
    })
    out.push({
      id: s.id,
      role: 'assistant',
      content: s.content ?? '',
      turn_id: s.turnID,
      timestamp: iso(s.id),
      iterations: s.iters.map(iterPayload),
      ...(s.regionsBefore !== undefined && s.regionsBefore > 0
        ? { regions_before: s.regionsBefore }
        : {}),
    })
  }
  return out
}

/** 一段区域段（`/api/regions` 响应的 `iterations`）—— 轻字段形态。 */
function segment(from: number, to: number): unknown[] {
  const out: unknown[] = []
  for (let n = from; n <= to; n++) {
    out.push(iterPayload({ iteration: n, content: body(`iter ${n}`, 4), tools: [{ name: `tool${n}` }], folded: true }))
  }
  return out
}

// ─────────────────────────────────────────────────────────────────────────────
// 请求计数 + mock 装配
// ─────────────────────────────────────────────────────────────────────────────

interface RegionReq {
  turnID: number
  beforeIteration: number
  regionLimit: number | undefined
  body: Record<string, unknown>
}

interface DetailReq {
  turnID: number
  iteration: number
  body: Record<string, unknown>
}

interface Counters {
  regions: RegionReq[]
  details: DetailReq[]
  history: number
}

interface RegionsReply {
  status?: number
  iterations?: unknown[]
  regions_before?: number
}

interface MockOpts {
  messages: unknown[]
  /** `active_progress` 快照（默认 null）。 */
  activeProgress?: unknown
  /** `/api/regions` 应答（缺省 = 500 之外的 200 空段）。 */
  regions?: (req: RegionReq) => RegionsReply
  /** `/api/iteration_detail` 应答；返回 null = 404（未命中）。 */
  detail?: (req: DetailReq) => unknown | null
}

async function setupMock(page: Page, opts: MockOpts): Promise<Counters> {
  const counters: Counters = { regions: [], details: [], history: 0 }

  await page.addInitScript(() => {
    try {
      localStorage.setItem('xbot-locale', 'zh-CN')
    } catch {
      /* ignore */
    }
    // SSE：本 spec 不推流事件（历史/区域/详情全部走 REST mock）。
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
    ;(window as unknown as { __sseListeners: typeof listeners }).__sseListeners = listeners
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
    counters.history += 1
    await route.fulfill({
      json: {
        ok: true,
        data: {
          messages: opts.messages,
          chat_id: 'chat-1',
          channel: 'web',
          last_seq: 0,
          active_progress: opts.activeProgress ?? null,
          // 外层消息行分页在断（本 spec 只测内层区域分页）
          has_more: false,
          oldest_id: 1,
        },
      },
    })
  })
  await page.route('**/api/regions', async (route) => {
    const b = (route.request().postDataJSON() ?? {}) as Record<string, unknown>
    const req: RegionReq = {
      turnID: Number(b.turn_id ?? 0),
      beforeIteration: Number(b.before_iteration ?? 0),
      regionLimit: typeof b.region_limit === 'number' ? (b.region_limit as number) : undefined,
      body: b,
    }
    counters.regions.push(req)
    const reply = opts.regions?.(req) ?? { iterations: [], regions_before: 0 }
    const status = reply.status ?? 200
    if (status >= 400) {
      await route.fulfill({
        status,
        json: { ok: false, data: null, error: { code: 'internal', message: 'mock regions failure' } },
      })
      return
    }
    await route.fulfill({
      json: {
        ok: true,
        data: { iterations: reply.iterations ?? [], regions_before: reply.regions_before ?? 0 },
      },
    })
  })
  await page.route('**/api/iteration_detail', async (route) => {
    const b = (route.request().postDataJSON() ?? {}) as Record<string, unknown>
    const req: DetailReq = {
      turnID: Number(b.turn_id ?? 0),
      iteration: Number(b.iteration ?? 0),
      body: b,
    }
    counters.details.push(req)
    const it = opts.detail ? opts.detail(req) : null
    if (it === null) {
      await route.fulfill({
        status: 404,
        json: { ok: false, data: null, error: { code: 'not_found', message: 'iteration not found' } },
      })
      return
    }
    await route.fulfill({ json: { ok: true, data: { iteration: it } } })
  })
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
  await page.route('**/api/queue/list', (r) => r.fulfill({ json: { ok: true, data: { items: [] } } }))

  return counters
}

// ─────────────────────────────────────────────────────────────────────────────
// DOM / 滚动 helpers
// ─────────────────────────────────────────────────────────────────────────────

/** 折叠视图相关的 DOM 签名（G2「无感」的判据面）。 */
async function domSignature(page: Page) {
  return page.evaluate(() => {
    const pills = Array.from(document.querySelectorAll('[data-testid="tool-pill"]'))
    const iters = Array.from(document.querySelectorAll('[data-iter-id]'))
    const blocks = document.querySelector('[data-iter-range]') as HTMLElement | null
    return {
      pillCount: pills.length,
      pillToolNames: pills.map((p) => p.getAttribute('data-tool-name') ?? ''),
      iterIDs: iters.map((e) => e.getAttribute('data-iter-id') ?? ''),
      iterRange: blocks?.getAttribute('data-iter-range') ?? null,
      iterTotal: blocks?.getAttribute('data-iter-total') ?? null,
      dividerCount: document.querySelectorAll('[data-testid="regions-divider"]').length,
    }
  })
}

/** 已渲染迭代块的迭代号（数字，升序）。 */
async function renderedIterNumbers(page: Page): Promise<number[]> {
  const raw = await page.evaluate(() =>
    Array.from(document.querySelectorAll('[data-iter-id]'))
      .map((e) => Number(e.getAttribute('data-iter-id')))
      .filter((n) => Number.isFinite(n)),
  )
  return raw.sort((a, b) => a - b)
}

/** 迭代号序列不得有洞（相邻必须 +1）—— 「拼接处无洞」的守护。 */
function expectContiguous(ids: number[], label: string) {
  expect(ids.length, `${label}：应至少有一个迭代块`).toBeGreaterThan(0)
  for (let i = 1; i < ids.length; i++) {
    expect(ids[i], `${label}：迭代号出现洞（${ids[i - 1]} → ${ids[i]}）`).toBe(ids[i - 1] + 1)
  }
}

async function iterRange(page: Page): Promise<string | null> {
  return page.evaluate(
    () => (document.querySelector('[data-iter-range]') as HTMLElement | null)?.getAttribute('data-iter-range') ?? null,
  )
}

async function iterTotal(page: Page): Promise<number> {
  const v = await page.evaluate(
    () => (document.querySelector('[data-iter-total]') as HTMLElement | null)?.getAttribute('data-iter-total') ?? null,
  )
  return Number(v ?? -1)
}

async function dividerCount(page: Page): Promise<number> {
  return page.locator('[data-testid="regions-divider"]').count()
}

/** 列表滚动容器（与 loadmore-pagination.spec.ts 同一取法）。 */
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

async function forceScrollTop(page: Page, top: number) {
  const deadline = Date.now() + 3000
  for (;;) {
    const ok = await page.evaluate((t) => {
      const anchor = document.querySelector('[data-message-list-content]') as HTMLElement | null
      let sc = anchor?.parentElement as HTMLElement | null
      while (sc) {
        const oy = getComputedStyle(sc).overflowY
        if (oy === 'auto' || oy === 'scroll') break
        sc = sc.parentElement
      }
      if (!sc) return false
      sc.scrollTop = t
      sc.dispatchEvent(new Event('scroll'))
      return true
    }, top)
    if (ok) return
    if (Date.now() > deadline) throw new Error('scroller not found')
    await page.waitForTimeout(100)
  }
}

/** **一次**「向上翻页」手势：先离开顶部（手势真实起点），再滑到顶并贴住。
 *  两个阶段缺一不可 —— 哨兵「不可见 → 可见」是 re-arm 的唯一路径
 *  （`useRegionWindow.ts:115-121`）。 */
async function scrollGesture(page: Page) {
  await page.mouse.move(400, 400)
  await page.mouse.wheel(0, 800)
  await page.waitForTimeout(200)
  await page.mouse.wheel(0, -800)
  await page.waitForTimeout(150)
  await page.mouse.wheel(0, -800)
  await page.waitForTimeout(250)
  await forceScrollTop(page, 0)
}

/**
 * 一次手势 + 「最多 1 次 `/api/regions`」断言。
 *  @param expected 期望本次手势触发的请求数（0 = 断言**不得**发请求）。
 *  @returns 本次手势新增的请求
 */
async function gestureExpectRegions(page: Page, c: Counters, expected: number, label: string): Promise<RegionReq[]> {
  const before = c.regions.length
  await scrollGesture(page)
  if (expected > 0) {
    await expect
      .poll(() => c.regions.length, { message: `${label}：未观测到 /api/regions 请求`, timeout: 8000 })
      .toBe(before + expected)
  }
  // 静默窗口：多发的请求（风暴复辟）会在这里现形。
  await page.waitForTimeout(1200)
  expect(
    c.regions.length - before,
    `${label}：一次手势只允许 ${expected} 次 /api/regions（实测 ${c.regions.length - before}）`,
  ).toBe(expected)
  return c.regions.slice(before)
}

async function openSession(page: Page) {
  await page.goto(`${BASE}/`)
  await expect(page.locator('[data-message-list-content]')).toBeAttached({ timeout: 15_000 })
  await page.waitForTimeout(1200)
}

// ─────────────────────────────────────────────────────────────────────────────
// 1. 默认视图无感（G2 核心验收）
// ─────────────────────────────────────────────────────────────────────────────

/** 同一 turn 的两种下发形态：全量 vs 折叠窗口（tools_folded 轻字段）。 */
const EQUIV_ITERS: IterSpec[] = [
  {
    iteration: 1,
    content: '第一段正文。',
    tools: [{ name: 'Alpha', summary: 'alpha summary', args: '{"a":1}', detail: 'alpha detail' }],
  },
  { iteration: 2, tools: [{ name: 'Beta', summary: 'beta summary', args: '{"b":2}' }, { name: 'Gamma' }] },
  { iteration: 3, content: '第三段正文。', tools: [{ name: 'Delta', summary: 'delta summary' }] },
]

test.describe('区域窗口 — 默认视图无感（G2）', () => {
  test('同一 turn 全量 vs 折叠窗口：pills/DOM 等价 + 零区域/详情请求', async ({ browser }) => {
    const ctxA = await browser.newContext({ viewport: { width: 900, height: 800 } })
    const ctxB = await browser.newContext({ viewport: { width: 900, height: 800 } })
    const ctxC = await browser.newContext({ viewport: { width: 900, height: 800 } })
    try {
      // A：全量下发（详情齐全，无 tools_folded / 无 regions_before）
      const pageA = await ctxA.newPage()
      const cA = await setupMock(pageA, {
        messages: historyMessages([{ id: 2, turnID: 7, iters: EQUIV_ITERS }]),
      })
      await openSession(pageA)
      const sigA = await domSignature(pageA)

      // B：折叠窗口下发（tools_folded 轻字段；regions_before 缺省 = 该 turn 已完整下发）
      const pageB = await ctxB.newPage()
      const cB = await setupMock(pageB, {
        messages: historyMessages([{ id: 2, turnID: 7, iters: EQUIV_ITERS.map((s) => ({ ...s, folded: true })) }]),
      })
      await openSession(pageB)
      const sigB = await domSignature(pageB)

      // 判据①：pill 数与工具身份完全一致（「折叠对象 = 工具组详情载荷」不改变默认视图）
      expect(sigA.pillCount, '两种形态的 pill 数必须一致').toBeGreaterThan(0)
      expect(sigB.pillCount, '折叠窗口的 pill 数不得少于/多于全量视图').toBe(sigA.pillCount)
      expect(sigB.pillToolNames).toEqual(sigA.pillToolNames)

      // 判据②：迭代块 id 序列与迭代范围一致（迭代号/content 结构零变化）
      expect(sigB.iterIDs).toEqual(sigA.iterIDs)
      expect(sigA.iterRange, '全量视图迭代范围').toBe('1-3')
      expect(sigB.iterRange, '折叠窗口的迭代范围必须与全量一致').toBe(sigA.iterRange)
      expect(sigB.iterTotal).toBe(sigA.iterTotal)

      // 判据③：regions_before 缺省 ⇒ 无分隔条（唯一新增可见物不出现）
      expect(sigA.dividerCount, '全量视图不得有分隔条').toBe(0)
      expect(sigB.dividerCount, 'regions_before 缺省时不得有分隔条').toBe(0)

      // 判据④：**零**区域段 / 详情请求（route 计数）
      expect(cA.regions.length, '全量视图不得请求 /api/regions').toBe(0)
      expect(cA.details.length, '全量视图不得请求 /api/iteration_detail').toBe(0)
      expect(cB.regions.length, 'regions_before 缺省时不得请求 /api/regions').toBe(0)
      expect(
        cB.details.length,
        '默认视图（未打开浮层）不得请求 /api/iteration_detail —— 详情必须按需',
      ).toBe(0)

      console.log(`[G2] A(全量)=${JSON.stringify(sigA)}`)
      console.log(`[G2] B(折叠窗口)=${JSON.stringify(sigB)}`)

      // ── 变体 C：**判别力自证（mutation proof）** ─────────────────────────────
      // 仅把「同一 turn 的 regions_before 声明从缺省改成 1」，其余载荷逐字节相同：
      //  - 分隔条必须**出现**（否则判据③ 的「不存在」是空洞通过 —— 恒 0 的断言
      //    在分隔条根本渲染不出来的缺陷下也会绿；本变体把它变成有判别力的判据）；
      //  - 「无感」判据必须仍然成立：除**新增的分隔条**外，pill 数/工具身份/迭代
      //    块 id/迭代范围/迭代总数与 A/B 全等（唯一新增可见物 = 分隔条）。
      const pageC = await ctxC.newPage()
      await setupMock(pageC, {
        messages: historyMessages([{ id: 2, turnID: 7, iters: EQUIV_ITERS, regionsBefore: 1 }]),
      })
      await openSession(pageC)
      const sigC = await domSignature(pageC)
      expect(
        sigC.dividerCount,
        '变体 C：regions_before>0 必须渲染出分隔条（否则判据③ 无判别力）',
      ).toBe(1)
      await expect(pageC.locator('[data-testid="regions-divider"]')).toHaveAttribute('data-regions-before', '1')
      expect(sigC.pillCount, '变体 C：pill 数必须与全量视图一致').toBe(sigA.pillCount)
      expect(sigC.pillToolNames).toEqual(sigA.pillToolNames)
      expect(sigC.iterIDs).toEqual(sigA.iterIDs)
      expect(sigC.iterRange).toBe(sigA.iterRange)
      expect(sigC.iterTotal).toBe(sigA.iterTotal)
      console.log(`[G2] C(regions_before=1，判别力自证)=${JSON.stringify(sigC)}`)
    } finally {
      await ctxA.close()
      await ctxB.close()
      await ctxC.close()
    }
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 2. regions_before > 0 + 上滚自动加载
// ─────────────────────────────────────────────────────────────────────────────

/** 窗口 = 迭代 21..30；更早还有 2 个区域段（15..17 / 18..20），共 regions_before=3。 */
const WINDOW_FROM = 21
const WINDOW_TO = 30

function windowIters(): IterSpec[] {
  const out: IterSpec[] = []
  for (let n = WINDOW_FROM; n <= WINDOW_TO; n++) {
    out.push({ iteration: n, content: body(`iter ${n}`), tools: [{ name: `tool${n}` }], folded: true })
  }
  return out
}

test.describe('区域窗口 — regions_before>0 + 上滚自动加载', () => {
  test('一次手势恰好 1 次 /api/regions；段插入后迭代号连续、计数递减、归零消失', async ({ page }) => {
    const counters = await setupMock(page, {
      messages: historyMessages([{ id: 2, turnID: 7, iters: windowIters(), regionsBefore: 3 }]),
      regions: (req) => {
        if (req.beforeIteration === WINDOW_FROM) return { iterations: segment(18, 20), regions_before: 1 }
        if (req.beforeIteration === 18) return { iterations: segment(15, 17), regions_before: 0 }
        return { iterations: [], regions_before: 0 }
      },
    })
    await openSession(page)

    // ── 初始：分隔条在，计数 = 3；窗口 = 21..30；**未经手势不得自动发请求** ──
    const divider = page.locator('[data-testid="regions-divider"]')
    await expect(divider).toHaveCount(1)
    await expect(divider).toHaveAttribute('data-regions-before', '3')
    await expect(divider).toHaveAttribute('data-state', 'idle')
    expect(await iterRange(page), '初始窗口必须是尾部连续区间').toBe(`${WINDOW_FROM}-${WINDOW_TO}`)
    expect(counters.regions.length, '挂载即发请求 = 自动风暴（未经手势）').toBe(0)

    // ── 用户规则（2026-10-01 回归修复）：regions_before>0 ⇒ user 输入不可见 ──
    // 「如果用户看到了一个用户输入，那么这个用户输入之后的所有消息就必须是完整的，
    //  不能是接下来动态加载的。所以这种情况如果需要动态加载，你不能渲染那个用户的输入。」
    const userRow = page.getByText('提问 turn 7')
    await expect(userRow, '折叠 turn 的 user 输入必须不可见（不能悬在待加载内容上方）').toHaveCount(0)

    // ── 手势 #1：恰好 1 次，游标 = 当前窗口最早迭代号 ──
    const first = await gestureExpectRegions(page, counters, 1, '上滚手势#1')
    expect(first[0].turnID, '/api/regions 必须带 turn_id').toBe(7)
    expect(first[0].beforeIteration, 'before_iteration 必须是当前窗口最早迭代号').toBe(WINDOW_FROM)
    expect(first[0].body.channel, '/api/regions 必须带 channel').toBe('web')
    expect(first[0].body.chat_id, '/api/regions 必须带 chat_id').toBe('chat-1')

    // 段插入：DOM 增长 + 拼接处无洞 + 计数递减为 1（分隔条仍在）
    await expect.poll(() => iterTotal(page), { timeout: 8000 }).toBe(WINDOW_TO - 18 + 1)
    expect(await iterRange(page), '拼接后必须是连续区间 18-30').toBe(`18-${WINDOW_TO}`)
    expectContiguous(await renderedIterNumbers(page), '段#1 拼接后')
    await expect(divider).toHaveCount(1)
    await expect(divider).toHaveAttribute('data-regions-before', '1')
    await expect(userRow, 'regions_before=1（仍未到顶）⇒ user 输入保持不可见').toHaveCount(0)

    // ── 手势 #2：恰好 1 次（连续滚动不风暴），游标推进到新的最早迭代号 ──
    const second = await gestureExpectRegions(page, counters, 1, '上滚手势#2')
    expect(second[0].beforeIteration, '第二次手势的游标必须续上段#1 的最早迭代号').toBe(18)
    expect(counters.regions.length, '两次手势 ⇒ 恰好 2 次请求').toBe(2)

    // 到顶：regions_before = 0 ⇒ 分隔条消失；窗口 15..30 连续
    await expect.poll(() => iterTotal(page), { timeout: 8000 }).toBe(WINDOW_TO - 15 + 1)
    expect(await iterRange(page), '拼接后必须是连续区间 15-30').toBe(`15-${WINDOW_TO}`)
    expectContiguous(await renderedIterNumbers(page), '段#2 拼接后')
    await expect(divider, 'regions_before 归零 ⇒ 分隔条必须消失').toHaveCount(0)
    await expect(userRow, 'regions_before 归零 ⇒ user 输入随完整内容一起出现（时间线还原）').toBeVisible()

    console.log(
      `[上滚] regions_requests=${counters.regions
        .map((r) => `(turn=${r.turnID},before=${r.beforeIteration})`)
        .join(' ')}`,
    )
  })

  test('连续滚动风暴守护：一次手势内多次滚动事件 ⇒ 仍恰好 1 次请求', async ({ page }) => {
    const counters = await setupMock(page, {
      messages: historyMessages([{ id: 2, turnID: 7, iters: windowIters(), regionsBefore: 3 }]),
      regions: () => ({ iterations: segment(18, 20), regions_before: 2 }),
    })
    await openSession(page)
    await expect(page.locator('[data-testid="regions-divider"]')).toHaveCount(1)

    const before = counters.regions.length
    // 一次手势内**大量**滚动事件（模拟手指快速来回拖动）
    await page.mouse.move(400, 400)
    await page.mouse.wheel(0, 600)
    for (let i = 0; i < 12; i++) {
      await page.mouse.wheel(0, -120)
      await page.waitForTimeout(20)
    }
    await forceScrollTop(page, 0)
    await page.waitForTimeout(1500)
    expect(
      counters.regions.length - before,
      `一次手势内多次滚动事件只允许 1 次 /api/regions（实测 ${counters.regions.length - before}）`,
    ).toBe(1)
    void getScroller(page)
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 3. 浮层详情按需
// ─────────────────────────────────────────────────────────────────────────────

/** 迭代 21/22/23：21 带两个工具且 tools_folded；22 完整；23 无工具。 */
const DETAIL_ITERS: IterSpec[] = [
  {
    iteration: 21,
    content: '第 21 段正文。',
    folded: true,
    tools: [
      { name: 'Alpha21', summary: 'sum-21-alpha', args: '{"cmd":"alpha21"}' },
      { name: 'Beta21', summary: 'sum-21-beta', args: '{"cmd":"beta21"}' },
    ],
  },
  { iteration: 22, content: '第 22 段正文。', tools: [{ name: 'Gamma22', summary: 'sum-22-gamma' }] },
  { iteration: 23, content: '第 23 段正文。' },
]

/** 完整形态（`/api/iteration_detail` 的应答）。 */
function fullIteration(n: number): Record<string, unknown> {
  const spec = DETAIL_ITERS.find((s) => s.iteration === n)
  if (!spec) return iterPayload({ iteration: n })
  return iterPayload({ ...spec, folded: false })
}

test.describe('区域窗口 — 浮层详情按需', () => {
  test('tools_folded pill 点击 ⇒ 1 次详情请求；同迭代第二个工具 0 请求；完整迭代 0 请求', async ({ page }) => {
    const counters = await setupMock(page, {
      messages: historyMessages([{ id: 2, turnID: 7, iters: DETAIL_ITERS }]),
      detail: (req) => {
        const it = fullIteration(req.iteration)
        return it
      },
    })
    await openSession(page)

    // 默认视图：无分隔条、零详情请求
    expect(await dividerCount(page)).toBe(0)
    expect(counters.details.length, '未打开浮层不得发详情请求').toBe(0)

    // ① 点 tools_folded 迭代（21）的第一个工具 → 恰好 1 次 /api/iteration_detail
    const pillA = page.locator('[data-testid="tool-pill"][data-tool-name="Alpha21"]')
    await expect(pillA).toBeAttached()
    await pillA.click()
    const pop = page.locator('[data-slot="popover-content"]')
    await expect(pop).toBeVisible({ timeout: 5000 })
    await expect.poll(() => counters.details.length, { timeout: 8000 }).toBe(1)
    expect(counters.details[0].turnID).toBe(7)
    expect(counters.details[0].iteration, '详情必须按 (turn_id, iteration) 寻址').toBe(21)
    expect(counters.details[0].body.channel).toBe('web')
    expect(counters.details[0].body.chat_id).toBe('chat-1')
    // 详情 hydrate 后渲染 summary / args（同号覆盖，迭代号不变）
    await expect(pop).toContainText('sum-21-alpha')
    await expect(pop).toContainText('alpha21')
    await expect(page.locator('[data-testid="tool-detail-skeleton"]')).toHaveCount(0)

    // 迭代号不得因 hydrate 改变
    expectContiguous(await renderedIterNumbers(page), 'detail hydrate 后')
    expect(await iterRange(page)).toBe('21-23')

    // ② 同迭代的**另一个**工具 → 0 新请求（迭代级缓存：一次拉全、迭代内复用）
    await page.keyboard.press('Escape')
    await expect(pop).toHaveCount(0)
    const beforeSecond = counters.details.length
    const pillB = page.locator('[data-testid="tool-pill"][data-tool-name="Beta21"]')
    await expect(pillB).toBeAttached()
    await pillB.click()
    await expect(page.locator('[data-slot="popover-content"]')).toContainText('sum-21-beta')
    expect(
      counters.details.length - beforeSecond,
      '同迭代第二个工具的浮层不得再发详情请求（迭代级缓存）',
    ).toBe(0)

    // ③ 非 tools_folded 迭代的 pill → 0 请求，直接渲染详情
    await page.keyboard.press('Escape')
    await expect(page.locator('[data-slot="popover-content"]')).toHaveCount(0)
    const beforeWhole = counters.details.length
    const pillC = page.locator('[data-testid="tool-pill"][data-tool-name="Gamma22"]')
    await expect(pillC).toBeAttached()
    await pillC.click()
    const popC = page.locator('[data-slot="popover-content"]')
    await expect(popC).toContainText('sum-22-gamma', { timeout: 5000 })
    expect(page.locator('[data-testid="tool-detail-skeleton"]')).toHaveCount(0)
    expect(counters.details.length - beforeWhole, '完整迭代的浮层不得发详情请求').toBe(0)

    console.log(`[浮层] detail_requests=${JSON.stringify(counters.details.map((d) => [d.turnID, d.iteration]))}`)
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 4. 加载失败降级
// ─────────────────────────────────────────────────────────────────────────────

test.describe('区域窗口 — 失败降级与重试', () => {
  test('500 ⇒ error 态；连续失败保守停止自动触发；retry 手动再发 1 次；恢复后正常插入', async ({ page }) => {
    // 前 2 次失败（`MAX_AUTO_REGION_FAILURES = 2`，useRegionWindow.ts:29）；第 3 次（手动 retry）成功。
    let calls = 0
    const counters = await setupMock(page, {
      messages: historyMessages([{ id: 2, turnID: 7, iters: windowIters(), regionsBefore: 3 }]),
      regions: () => {
        calls += 1
        if (calls <= 2) return { status: 500 }
        return { iterations: segment(15, 20), regions_before: 0 }
      },
    })
    await openSession(page)

    const divider = page.locator('[data-testid="regions-divider"]')
    await expect(divider).toHaveCount(1)
    await expect(divider).toHaveAttribute('data-regions-before', '3')

    // ── 手势 #1：1 次请求（500）⇒ error 态 + retry 按钮 ──
    await gestureExpectRegions(page, counters, 1, '失败手势#1')
    await expect(divider).toHaveAttribute('data-state', 'error', { timeout: 8000 })
    await expect(page.locator('[data-testid="regions-divider-retry"]')).toBeVisible()

    // ── 手势 #2：仍是新手势 ⇒ 自动再试 1 次（失败计数 1 < 2）──
    await gestureExpectRegions(page, counters, 1, '失败手势#2')
    await expect(divider).toHaveAttribute('data-state', 'error', { timeout: 8000 })
    expect(counters.regions.length, '连续两次失败后请求数为 2').toBe(2)

    // ── 手势 #3：连续失败达上限 ⇒ **保守停止**（新手势也不再自动发）──
    await gestureExpectRegions(page, counters, 0, '失败手势#3（保守停止）')
    await expect(divider).toHaveAttribute('data-state', 'error')

    // ── 手动 retry：绕过保守停止，恰好再发 1 次；恢复 200 ⇒ 段插入、分隔条消失 ──
    const before = counters.regions.length
    await page.locator('[data-testid="regions-divider-retry"]').click()
    await expect.poll(() => counters.regions.length, { timeout: 8000 }).toBe(before + 1)
    expect(counters.regions[before].beforeIteration, 'retry 的 before_iteration 必须是当前窗口最早迭代号').toBe(WINDOW_FROM)
    await expect.poll(() => iterTotal(page), { timeout: 8000 }).toBe(WINDOW_TO - 15 + 1)
    expect(await iterRange(page), '恢复后拼接必须连续').toBe(`15-${WINDOW_TO}`)
    expectContiguous(await renderedIterNumbers(page), '失败恢复后')
    await expect(divider, 'regions_before 归零 ⇒ 分隔条消失').toHaveCount(0)

    console.log(`[失败降级] regions_requests=${counters.regions.length}（前 2 次 500，第 3 次 retry 成功）`)
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 5. busy 恢复（active_progress 折叠窗口）
// ─────────────────────────────────────────────────────────────────────────────

/**
 * busy 恢复：`active_progress` 快照携带折叠窗口（`iteration_history` 轻字段 +
 * `iteration_regions_before`）⇒ live 行顶部渲染分隔条 + 上滚取段。
 *
 * 透传链路（前端已闭环，本用例即其守护）：
 *   `normalize.ts:197`（`iteration_regions_before` → `ProgressSnapshot.iterationRegionsBefore`）
 *   → `integrate.ts:216`（`snapshotToLive` → `LiveSnapshot.regionsBefore`）
 *   → `derive.ts:241`（live 行）→ `integrate.ts:311`（`rowToChatMessage` live 分支）
 *   → `AssistantMessage.tsx:99`（`useRegionWindow`）→ `RegionsDivider`。
 */
test.describe('区域窗口 — busy 恢复（active_progress 折叠窗口）', () => {
  test('live turn 顶部渲染分隔条 + 上滚正常取段', async ({ page }) => {
    const counters = await setupMock(page, {
      messages: [
        { id: 1, role: 'user', content: '提问 turn 7', turn_id: 7, timestamp: iso(1) },
      ],
      activeProgress: {
        phase: 'tool_exec',
        seq: 50,
        turn_id: 7,
        iteration: 30,
        content: '',
        stream_content: '',
        active_tools: [{ name: 'Running30', label: 'Running30 运行中', status: 'running' }],
        completed_tools: [],
        // 折叠窗口 + 区域计数（P1 后端投影）
        iteration_regions_before: 3,
        iteration_history: windowIters().map(iterPayload),
      },
      regions: () => ({ iterations: segment(18, 20), regions_before: 1 }),
    })
    await openSession(page)

    const divider = page.locator('[data-testid="regions-divider"]')
    await expect(divider, 'live 行顶部必须渲染分隔条').toHaveCount(1)
    await expect(divider).toHaveAttribute('data-regions-before', '3')

    // ── 用户规则（2026-10-02 生产截图第二次点名，三态统一）：live turn 折叠窗口
    // （iteration_regions_before>0）⇒ user 行也不可渲染 ——「加载更多前面不能渲染
    // 任何东西」。此前 live 豁免让 user「提问 turn 7」悬在分隔条上方（生产形态：
    // 「继续」悬在「⌃ 更早的 166 个区域」上方）。
    await expect(
      page.getByText('提问 turn 7'),
      'live 折叠 turn 的 user 输入必须不可见（分隔条前不能渲染任何东西）',
    ).toHaveCount(0)

    const made = await gestureExpectRegions(page, counters, 1, 'busy 恢复上滚')
    expect(made[0].turnID).toBe(7)
    expect(made[0].beforeIteration, 'before_iteration 必须是 live 窗口最早迭代号').toBe(WINDOW_FROM)
  })
})
