/**
 * v71 窗口化 —— 规模 × 形态矩阵 E2E（结构性测试：数据生成器 + 后端窗口化契约模拟）。
 *
 * 用户点名场景（2026-09-28：「我之前说的不同的 E2E 场景……这个是一个结构性的
 * 测试优化」）：**1000 / 10000 双规模** × 六种迭代形态随机组合：
 *   纯 tool（run 成员）/ reasoning+tool / content+tool / 三者都有 / 纯 content /
 *   纯 reasoning。
 *
 * 结构性原则：**不手工构造固定形态**——确定性 LCG 生成器产出可复现的形态流，
 * 测试侧用与 storage 层 assembleRuns 相同的契约（tool_only 判据、run 组装、
 * head-7 + 真实计数、Rows=无工具混合块）模拟后端窗口化，再对前端渲染断言。
 * 以后同类结构性改动都按这个标准写。
 */
import { test, expect, type Browser, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

// ─── 确定性数据生成器（LCG —— 种子固定可复现） ─────────────────────

type Shape = 'pure-tool' | 'reasoning-tool' | 'content-tool' | 'all-three' | 'pure-content' | 'pure-reasoning'
const SHAPES: Shape[] = ['pure-tool', 'reasoning-tool', 'content-tool', 'all-three', 'pure-content', 'pure-reasoning']

interface GenIter {
  iteration: number
  shape: Shape
  content: string
  reasoning: string
  tools: { name: string; status: string; iteration: number; label: string; summary: string }[]
}

function lcg(seed: number): () => number {
  let s = seed >>> 0
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0
    return s / 0x100000000
  }
}

/** 生成 total 个迭代的六形态随机流（每迭代 1~3 个工具，可复现）。 */
function genIterations(total: number, seed: number): GenIter[] {
  const rnd = lcg(seed)
  const out: GenIter[] = []
  for (let i = 1; i <= total; i++) {
    const shape = SHAPES[Math.floor(rnd() * SHAPES.length)]
    const toolCount = shape === 'pure-content' || shape === 'pure-reasoning' ? 0 : 1 + Math.floor(rnd() * 3)
    out.push({
      iteration: i,
      shape,
      content: shape === 'content-tool' || shape === 'all-three' || shape === 'pure-content'
        ? `形态块 ${i}（${shape}）` : '',
      reasoning: shape === 'reasoning-tool' || shape === 'all-three' || shape === 'pure-reasoning'
        ? `形态思考 ${i}（${shape}）` : '',
      tools: Array.from({ length: toolCount }, (_, k) => ({
        name: 'Shell', status: 'done', iteration: i,
        label: `m${i}-${k}`, summary: `matrix summary ${i}-${k}`,
      })),
    })
  }
  return out
}

// ─── 后端窗口化契约模拟（与 storage assembleRuns 同契约） ────────────

interface MockRunSummary {
  start_iter: number
  end_iter: number
  head_content: string
  head_reasoning: string
  head_tools: { name: string; status: string; iteration: number; label: string; summary: string }[]
  tool_count: number
}

interface WindowedTurn {
  iterations: Array<{ iteration: number; content: string; reasoning: string; tools: unknown[] }>
  run_summaries: MockRunSummary[]
  iter_window: { total: number; loaded_top: number }
  /** 断言用：期望 pill 总数（>8 的 run 显示 7，≤8 全量）。 */
  expectedPills: number
  /** 断言用：期望 +N 徽标文本序列（按 run 顺序）。 */
  expectedBadges: string[]
  /** 断言用：窗口内纯文本块的 content 文本（抽查可见性）。 */
  textBlocks: string[]
  /** 断言用：窗口内纯 reasoning 块（渲染为「思考 N 字」折叠行）。 */
  reasoningOnly: { iteration: number; chars: number }[]
}

/**
 * 窗口化模拟（契约与 storage/sqlite/iteration_window.go 一致）：
 *  - tool_only = 有工具且无 content 无 reasoning（前端 mergeToolRuns absorbs 同判据）
 *  - 窗口 = 尾部 mixedLimit 个非 tool-only 行
 *  - Rows = 窗口内【无工具】的混合块（纯 content / 纯 reasoning）
 *  - run = 头部（有工具的混合块）+ 后续连续 tool-only 成员；无头部直接从
 *    tool-only 开始 = headless run（头部 = 首成员）
 *  - run 摘要 = head-7 工具（头部工具 + 首批成员工具合并前 7）+ 真实总数
 */
function windowify(iters: GenIter[], mixedLimit: number): WindowedTurn {
  const isToolOnly = (g: GenIter) => g.tools.length > 0 && !g.content && !g.reasoning
  // 从尾部向前找 mixedLimit 个非 tool-only 行 → 窗口顶。
  let windowTop = iters.length + 1
  let mixedSeen = 0
  for (let i = iters.length; i >= 1 && mixedSeen < mixedLimit; i--) {
    if (!isToolOnly(iters[i - 1])) { mixedSeen++; windowTop = iters[i - 1].iteration }
  }
  // 窗口成员：windowTop..total（含触边的 headless run：windowTop 之上连续 tool-only）。
  const inWindow: GenIter[] = []
  for (const g of iters) if (g.iteration >= windowTop) inWindow.push(g)

  const rows: WindowedTurn['iterations'] = []
  const runs: MockRunSummary[] = []
  const textBlocks: string[] = []
  const reasoningOnly: { iteration: number; chars: number }[] = []
  const expectedBadges: string[] = []
  let expectedPills = 0

  // 触边 headless run：窗口顶之上连续 tool-only（后端 crossingRunSummary 同判据）。
  let above = windowTop - 1
  while (above >= 1 && isToolOnly(iters[above - 1])) above--
  const crossStart = above + 1
  if (crossStart < windowTop) {
    // 跨窗口 run：从 crossStart 到窗口内第一个非 tool-only 行之前的连续 tool-only。
    let runEnd = windowTop
    while (runEnd <= iters.length && isToolOnly(iters[runEnd - 1])) runEnd++
    const members = iters.slice(crossStart - 1, runEnd - 1)
    if (members.length > 0) {
      const head = members[0]
      // head-8（PILL_INLINE_MAX——后端契约：≤8 全显示、>8 前端取前 7+徽标）。
      const headTools = members.slice(0, 8).flatMap((m) => m.tools).slice(0, 8)
      const toolCount = members.reduce((n, m) => n + m.tools.length, 0)
      runs.push({ start_iter: crossStart, end_iter: runEnd - 1, head_content: '', head_reasoning: '', head_tools: headTools, tool_count: toolCount })
      expectedPills += toolCount > 8 ? 7 : toolCount
      if (toolCount > 8) expectedBadges.push(`+${toolCount - 7}`)
    }
  }

  // 窗口内：逐行分类（Rows vs run 头部 + 成员组装）。
  let i = crossStart < windowTop ? (runs[0] ? runs[0].end_iter + 1 : windowTop) : windowTop
  i = Math.max(i, windowTop)
  while (i <= iters.length) {
    const g = iters[i - 1]
    if (isToolOnly(g)) {
      // ⚠️ 无头部 tool-only 段（前一行是文本块）：headless run —— 与后端
      // assembleRuns（iteration_window.go line 350-373）同契约：head=首成员
      // （mergeToolRuns 允许 tool-only 迭代作头部），工具不双计数。绝不跳过
      // （跳过 = 丢渲染 —— parity spec 抓到的模拟契约偏差）。
      let end = i
      while (end + 1 <= iters.length && isToolOnly(iters[end])) end++
      const members = iters.slice(i - 1, end) // head(首成员) + 其余成员
      const headTools = members.slice(0, 8).flatMap((m) => m.tools).slice(0, 8)
      const toolCount = members.reduce((n, m) => n + m.tools.length, 0)
      runs.push({ start_iter: members[0].iteration, end_iter: iters[end - 1].iteration, head_content: '', head_reasoning: '', head_tools: headTools, tool_count: toolCount })
      expectedPills += toolCount > 8 ? 7 : toolCount
      if (toolCount > 8) expectedBadges.push(`+${toolCount - 7}`)
      i = end + 1
      continue
    }
    if (g.tools.length === 0) {
      // Rows：纯文本块。
      rows.push({ iteration: g.iteration, content: g.content, reasoning: g.reasoning, tools: [] })
      if (g.content) textBlocks.push(g.content)
      if (!g.content && g.reasoning) reasoningOnly.push({ iteration: g.iteration, chars: g.reasoning.length })
      i++
      continue
    }
    // run 头部（有工具的混合块）+ 后续连续 tool-only 成员。
    let end = i
    while (end + 1 <= iters.length && isToolOnly(iters[end])) end++
    const members = iters.slice(i - 1, end) // 头部 + 成员
    // head-8（PILL_INLINE_MAX——后端契约：≤8 全显示、>8 前端取前 7+徽标）。
    const headTools = members.slice(0, 8).flatMap((m) => m.tools).slice(0, 8)
    const toolCount = members.reduce((n, m) => n + m.tools.length, 0)
    runs.push({ start_iter: g.iteration, end_iter: iters[end - 1].iteration, head_content: g.content, head_reasoning: g.reasoning, head_tools: headTools, tool_count: toolCount })
    expectedPills += toolCount > 8 ? 7 : toolCount
    if (toolCount > 8) expectedBadges.push(`+${toolCount - 7}`)
    i = end + 1
  }

  return {
    iterations: rows,
    run_summaries: runs,
    iter_window: { total: iters.length, loaded_top: windowTop },
    expectedPills,
    expectedBadges,
    textBlocks,
    reasoningOnly,
  }
}

// ─── mock 基础设施（与 iter-window.spec.ts 同模式） ──────────────────

async function setupMock(page: Page, chat: string, win: WindowedTurn): Promise<void> {
  await page.addInitScript(() => {
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
    ;(window as unknown as SSEMockState).__sseListeners = listeners
    class MockEventSource {
      readyState = 1
      onopen: ((ev: Event) => void) | null = null
      onerror: ((ev: Event) => void) | null = null
      constructor(_url: string) { setTimeout(() => this.onopen?.(new Event('open')), 0) }
      addEventListener(type: string, handler: (ev: MessageEvent) => void) {
        if (!listeners[type]) listeners[type] = new Set()
        listeners[type].add(handler)
      }
      removeEventListener(type: string, handler: (ev: MessageEvent) => void) { listeners[type]?.delete(handler) }
      close() { for (const key of Object.keys(listeners)) listeners[key].clear() }
    }
    ;(window as unknown as { EventSource: typeof MockEventSource }).EventSource = MockEventSource
  })
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) => r.fulfill({
    json: { ok: true, data: {
      sessions: [{ chat_id: chat, channel: 'web', label: `matrix-${chat}`, last_active: new Date().toISOString(), running: false }],
      chats: [{ chat_id: chat, channel: 'web', label: `matrix-${chat}`, last_active: new Date().toISOString(), isCurrent: true, running: false }],
      orphan_subagents: [],
    } },
  }))
  await page.route('**/api/history', (r) => r.fulfill({
    json: { ok: true, data: {
      messages: [
        { id: 1, role: 'user', content: '矩阵回放', timestamp: '2026-09-28T00:00:00Z', turn_id: 5 },
        {
          id: 2, role: 'assistant', content: '', timestamp: '2026-09-28T00:01:00Z', turn_id: 5,
          iterations: win.iterations,
          run_summaries: win.run_summaries,
          iter_window: win.iter_window,
        },
      ],
      chat_id: chat, channel: 'web', last_seq: 10,
      active_progress: null, has_more: false, oldest_id: 1,
    } },
  }))
  await page.route('**/api/history/iterations', (r) => r.fulfill({
    json: { ok: true, data: { turn_id: 5, iterations: [], run_summaries: [], total: win.iter_window.total, loaded_top: 1 } },
  }))
  await page.route('**/api/history/run_tools', (r) => r.fulfill({ json: { ok: true, data: { tools: [], total: 1 } } }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

async function login(page: Page, marker: string): Promise<void> {
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await expect(page.locator('[data-message-list-content]')).toContainText(marker, { timeout: 30000 })
}

/** 矩阵断言核心：渲染契约 vs 生成器期望（规模无关 —— 双规模共用）。 */
async function assertMatrix(page: Page, win: WindowedTurn, scale: string): Promise<void> {
  const list = page.locator('[data-message-list-content]')
  // ① pill 总数（>8 的 run 显示 7 pill + +N；≤8 全量 —— 与全量渲染同构）。
  const pills = await page.locator('[data-testid="tool-pill"]').count()
  expect(pills, `${scale}：pill 总数必须 = 期望（>8 显示 7 + 徽标，≤8 全量）`).toBe(win.expectedPills)
  // ② +N 徽标序列（真实总数，绝不估算）。
  const badges = await page.locator('[data-testid="tool-pill-more"]').allInnerTexts()
  expect(badges, `${scale}：+N 徽标序列必须逐个等于真实溢出数`).toEqual(win.expectedBadges)
  // ③ 窗口内纯文本块全部可见（线性一致性 —— Rows 逐块渲染）。
  expect(win.textBlocks.length, `${scale}：生成器必须产出纯文本块（否则矩阵退化）`).toBeGreaterThan(0)
  for (const tb of win.textBlocks.slice(0, 6)) {
    await expect(list).toContainText(tb)
  }
  // ④ 纯 reasoning 块渲染为「思考 N 字」折叠行（与全量同形态）。
  if (win.reasoningOnly.length > 0) {
    await expect(list).toContainText('思考')
  }
  // ⑤ 回拉 divider 数值（loadedTop - 1）。
  const divider = page.locator('[data-testid="iteration-window-more"]')
  if (win.iter_window.loaded_top > 1) {
    await expect(divider).toBeVisible()
    await expect(divider).toContainText(`${win.iter_window.loaded_top - 1}`)
  }
}

// ─── 双规模矩阵（1000 / 10000） ─────────────────────────────────────

test.describe('规模 × 六形态矩阵（确定性生成器 + 后端窗口化契约模拟）', () => {
  for (const [scale, total, seed] of [['1000', 1000, 20260928], ['10000', 10000, 20260929]] as const) {
    test(`矩阵 ${scale} 迭代：六形态随机组合的窗口化渲染契约`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
      await ctx.addInitScript(() => {
        try { localStorage.setItem('xbot-locale', 'zh-CN') } catch { /* ignore */ }
      })
      const page = await ctx.newPage()

      // 传输体积审计（10000 规模断言阈值；1000 记录实测）。
      let historyBodyBytes = 0
      page.on('response', async (res) => {
        if (res.url().includes('/api/history') && !res.url().includes('/api/history/')) {
          try { historyBodyBytes = (await res.body()).length } catch { /* ignore */ }
        }
      })

      const win = windowify(genIterations(total, seed), 50)
      // 契约自检：生成器必须覆盖全部六形态（结构性矩阵的最低要求）。
      const shapesSeen = new Set(genIterations(Math.min(total, 600), seed).map((g) => g.shape))
      expect(shapesSeen.size, '生成器必须覆盖全部六种形态').toBe(6)

      await setupMock(page, `chat-matrix-${scale}`, win)
      await login(page, '矩阵回放')

      await assertMatrix(page, win, scale)

      // 传输体积（10000 规模的硬指标；run 内部不传输的数量级证明）。
      if (total >= 10000) {
        expect(historyBodyBytes, `矩阵 10000：窗口化体积 ≤ 256KB（实测 ${historyBodyBytes}B）`).toBeLessThanOrEqual(256 * 1024)
      }
      await ctx.close()
    })
  }
})
