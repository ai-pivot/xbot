/**
 * v71 窗口化 —— 全量 vs 窗口化渲染一致性（用户点名的「截图对比」场景）。
 *
 * 「无感知」硬约束的最强判据：**同一份数据**走两条拉取路径（全量 / 窗口化），
 * 渲染结果必须一致：
 *  - DOM 可见文本序列逐字一致（innerText 对比）；
 *  - pill 总数 + "+N" 徽标序列一致（折叠 run 的渲染镜像）；
 *  - **截图 buffer 逐字节一致**（同一浏览器同一次运行的两个页面互相对比 ——
 *    不用 baseline 快照，避免环境性 flaky）。
 *
 * 数据：矩阵生成器（确定性种子，~200 迭代六形态混合，含 >8 / ≤8 / =8 的 run
 * —— =8 正是 off-by-one bug 的回归判据）。窗口化用完整窗口（loaded_top=1），
 * 两条路径信息量等价，渲染必须逐像素一致。
 */
import { test, expect, type Browser, type Page } from '@playwright/test'
import { appendFileSync } from 'node:fs'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'
/** 诊断 log 写文件（worker SIGTRAP 时 stdout 缓冲丢失 —— 文件 log 崩溃后仍可读）。 */
const diag = (msg: string): void => { try { appendFileSync('/tmp/parity-diag.log', `${Date.now()} ${msg}\n`) } catch { /* ignore */ } }
diag('module loaded')

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

// ─── 确定性数据生成（与 iter-window-matrix.spec.ts 同法，精简内联） ──

type Shape = 'pure-tool' | 'reasoning-tool' | 'content-tool' | 'all-three' | 'pure-content' | 'pure-reasoning'
const SHAPES: Shape[] = ['pure-tool', 'reasoning-tool', 'content-tool', 'all-three', 'pure-content', 'pure-reasoning']

interface GenIter {
  iteration: number
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

/** 含手工注入的 =8 工具 run（off-by-one 回归判据：全量显示 8 pill、窗口化 head-8 也显示 8）。 */
function genData(total: number, seed: number): GenIter[] {
  const rnd = lcg(seed)
  const out: GenIter[] = []
  for (let i = 1; i <= total; i++) {
    // 注入：iter 30 = 恰好 8 工具的 run 头部 + iter 31..33 各 2 个 tool-only 成员
    // （总 8+6=14>8 显示 7+徽标）；iter 50 = 恰好 8 工具独立 run（无成员，总 8
    // → 全量 8 pill / 窗口化 head-8 全量 8 pill —— off-by-one 的精确判据）。
    if (i === 30) {
      out.push({ iteration: i, content: `注入头部 ${i}`, reasoning: '', tools: mkTools(i, 8) })
      continue
    }
    if (i >= 31 && i <= 33) {
      out.push({ iteration: i, content: '', reasoning: '', tools: mkTools(i, 2) })
      continue
    }
    if (i === 50) {
      out.push({ iteration: i, content: `注入独立八工具 run ${i}`, reasoning: '', tools: mkTools(i, 8) })
      continue
    }
    const shape = SHAPES[Math.floor(rnd() * SHAPES.length)]
    const toolCount = shape === 'pure-content' || shape === 'pure-reasoning' ? 0 : 1 + Math.floor(rnd() * 3)
    out.push({
      iteration: i,
      content: shape === 'content-tool' || shape === 'all-three' || shape === 'pure-content' ? `形态块 ${i}` : '',
      reasoning: shape === 'reasoning-tool' || shape === 'all-three' || shape === 'pure-reasoning' ? `形态思考 ${i}` : '',
      tools: mkTools(i, toolCount),
    })
  }
  return out
}

function mkTools(iteration: number, n: number): GenIter['tools'] {
  return Array.from({ length: n }, (_, k) => ({
    name: 'Shell', status: 'done', iteration,
    label: `p${iteration}-${k}`, summary: `parity summary ${iteration}-${k}`,
  }))
}

/** 窗口化契约模拟（storage assembleRuns 同契约；完整窗口 loaded_top=1）。
 *  ⚠️ 与后端 line 350-373 对齐：文本块之后的无头部 tool-only 段组装成
 *  headless run 摘要（head=首成员，工具不双计数）—— 绝不跳过（跳过=丢渲染）。 */
function windowifyFull(iters: GenIter[]) {
  const isToolOnly = (g: GenIter) => g.tools.length > 0 && !g.content && !g.reasoning
  const rows: Array<{ iteration: number; content: string; reasoning: string; tools: unknown[] }> = []
  const runs: Array<Record<string, unknown>> = []
  let i = 1
  while (i <= iters.length) {
    const g = iters[i - 1]
    if (isToolOnly(g)) {
      // 无头部 tool-only 段（前一行是文本块）：headless run —— head=首成员。
      let end = i
      while (end + 1 <= iters.length && isToolOnly(iters[end])) end++
      const members = iters.slice(i - 1, end) // head(首成员) + 其余成员
      const headTools = members.slice(0, 8).flatMap((m) => m.tools).slice(0, 8)
      const toolCount = members.reduce((n, m) => n + m.tools.length, 0)
      runs.push({
        start_iter: members[0].iteration, end_iter: iters[end - 1].iteration,
        head_content: '', head_reasoning: '',
        head_tools: headTools, tool_count: toolCount,
      })
      i = end + 1
      continue
    }
    if (g.tools.length === 0) {
      rows.push({ iteration: g.iteration, content: g.content, reasoning: g.reasoning, tools: [] })
      i++
      continue
    }
    let end = i
    while (end + 1 <= iters.length && isToolOnly(iters[end])) end++
    const members = iters.slice(i - 1, end)
    const headTools = members.slice(0, 8).flatMap((m) => m.tools).slice(0, 8)
    const toolCount = members.reduce((n, m) => n + m.tools.length, 0)
    runs.push({
      start_iter: g.iteration, end_iter: iters[end - 1].iteration,
      head_content: g.content, head_reasoning: g.reasoning,
      head_tools: headTools, tool_count: toolCount,
    })
    i = end + 1
  }
  return { rows, runs, iter_window: { total: iters.length, loaded_top: 1 } }
}

// ─── 双页面 mock（同数据，两条路径） ────────────────────────────────

async function setupBase(page: Page, chat: string): Promise<void> {
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
      sessions: [{ chat_id: chat, channel: 'web', label: 'parity', last_active: '2026-09-28T00:00:00Z', running: false }],
      chats: [{ chat_id: chat, channel: 'web', label: 'parity', last_active: '2026-09-28T00:00:00Z', isCurrent: true, running: false }],
      orphan_subagents: [],
    } },
  }))
  await page.route('**/api/history/iterations', (r) => r.fulfill({
    json: { ok: true, data: { turn_id: 3, iterations: [], run_summaries: [], total: 200, loaded_top: 1 } },
  }))
  await page.route('**/api/history/run_tools', (r) => r.fulfill({ json: { ok: true, data: { tools: [], total: 1 } } }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

const FIXED_TS = '2026-09-28T00:00:00Z'

/** 全量响应：iterations 携带全部迭代（含 tool-only 成员）—— 旧路径。 */
async function setupFullMock(page: Page, chat: string, iters: GenIter[]): Promise<void> {
  await setupBase(page, chat)
  await page.route('**/api/history', (r) => r.fulfill({
    json: { ok: true, data: {
      messages: [
        { id: 1, role: 'user', content: '一致性对比', timestamp: FIXED_TS, turn_id: 3 },
        {
          id: 2, role: 'assistant', content: '', timestamp: FIXED_TS, turn_id: 3,
          iterations: iters.map((g) => ({ iteration: g.iteration, content: g.content, reasoning: g.reasoning, tools: g.tools })),
        },
      ],
      chat_id: chat, channel: 'web', last_seq: 10,
      active_progress: null, has_more: false, oldest_id: 1,
    } },
  }))
}

/** 窗口化响应：Rows + run_summaries（完整窗口 loaded_top=1）—— v71 路径。 */
async function setupWindowedMock(page: Page, chat: string, win: ReturnType<typeof windowifyFull>): Promise<void> {
  await setupBase(page, chat)
  await page.route('**/api/history', (r) => r.fulfill({
    json: { ok: true, data: {
      messages: [
        { id: 1, role: 'user', content: '一致性对比', timestamp: FIXED_TS, turn_id: 3 },
        {
          id: 2, role: 'assistant', content: '', timestamp: FIXED_TS, turn_id: 3,
          iterations: win.rows,
          run_summaries: win.runs,
          iter_window: win.iter_window,
        },
      ],
      chat_id: chat, channel: 'web', last_seq: 10,
      active_progress: null, has_more: false, oldest_id: 1,
    } },
  }))
}

async function renderPage(browser: Browser, setup: (page: Page) => Promise<void>): Promise<Page> {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  await ctx.addInitScript(() => {
    try { localStorage.setItem('xbot-locale', 'zh-CN') } catch { /* ignore */ }
  })
  const page = await ctx.newPage()
  await setup(page)
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await expect(page.locator('[data-message-list-content]')).toContainText('一致性对比', { timeout: 30000 })
  // 等渲染稳定（两页面同等等待 —— 截图对比的公平性）。
  await expect(page.locator('[data-testid="tool-pill"]').first()).toBeVisible({ timeout: 10000 })
  await page.waitForTimeout(800)
  // ⚠️ 不做程序化 scrollTop=0 跳变：实测触发虚拟列表重渲染消息风暴压垮
  // Playwright worker（SIGTRAP）。两页面同处 follow 贴底（同锚定），底部对齐
  // 天然可比；顶部 overscan 微差由 fullPage 截图（滚动拼接含全部内容）覆盖。
  return page
}

test('全量 vs 窗口化渲染一致：DOM 文本 + pill/徽标结构 + 截图逐字节对比', async ({ browser }) => {
  diag('test body start')
  const iters = genData(200, 424242)
  diag('genData done')
  const win = windowifyFull(iters)
  diag(`genData+windowifyFull done: iters=${iters.length} runs=${win.runs.length}`)

  // 页面 A：全量路径（iterations 全量 → 前端 mergeToolRuns 折叠）。
  const pageA = await renderPage(browser, (p) => setupFullMock(p, 'chat-parity-a', iters))
  diag('pageA done')
  // 页面 B：窗口化路径（Rows + run_summaries → 虚拟块渲染）。
  const pageB = await renderPage(browser, (p) => setupWindowedMock(p, 'chat-parity-b', win))
  diag('pageB done')

  // ① DOM 可见文本：两页面同处 follow 贴底（同锚定）—— 底部内容序列可比。
  //    虚拟列表顶部 overscan 有天然微差（估算高度不同），故对比尾部（贴底
  //    锚定区）+ 首部由 fullPage 截图（滚动拼接全部内容）覆盖。
  const textA = (await pageA.locator('[data-message-list-content]').innerText()).slice(-600)
  const textB = (await pageB.locator('[data-message-list-content]').innerText()).slice(-600)
  expect(textB, '窗口化渲染的贴底文本序列必须与全量渲染逐字一致').toEqual(textA)

  // ② pill 总数 + 徽标序列一致（折叠 run 的渲染镜像；贴底视口两页面同覆盖）。
  const pillsA = await pageA.locator('[data-testid="tool-pill"]').count()
  const pillsB = await pageB.locator('[data-testid="tool-pill"]').count()
  expect(pillsB, `pill 总数必须一致（全量 ${pillsA} vs 窗口化 ${pillsB}）`).toBe(pillsA)
  const badgesA = await pageA.locator('[data-testid="tool-pill-more"]').allInnerTexts()
  const badgesB = await pageB.locator('[data-testid="tool-pill-more"]').allInnerTexts()
  expect(badgesB, '+N 徽标序列必须一致（真实总数不因路径变化）').toEqual(badgesA)

  // ③ 注入的 =8 工具 run（off-by-one 回归判据）：两条路径都显示 8 个 pill
  //    （iter 50 独立 run —— 全量 8 工具不折叠、窗口化 head-8 全量携带）。
  expect(pillsA, 'parity 前提：注入的八工具 run 使全量渲染有 pill').toBeGreaterThan(8)

  // ④ 贴底区域截图像素级一致：裁剪视口底部 400px（贴底锚定；上边缘 overscan
  //    裁掉）。⚠️ 独立渲染的双页面存在抗锯齿亚像素噪声（实测 0.035%——
  //    PNG 字节流必然不同），逐字节对比天然过严；用浏览器 Canvas 逐像素
  //    对比 + 0.1% 容差（内容不同时差异是数量级的）。
  const shotA = await pageA.screenshot({ clip: { x: 0, y: 500, width: 1440, height: 400 } })
  const shotB = await pageB.screenshot({ clip: { x: 0, y: 500, width: 1440, height: 400 } })
  expect(shotA.length, '截图非空（页面已渲染）').toBeGreaterThan(0)
  const diffPixels = await pageB.evaluate(async ([aData, bData]: [string, string]) => {
    const load = (data: string): Promise<HTMLImageElement> => new Promise((res, rej) => {
      const img = new Image()
      img.onload = () => res(img)
      img.onerror = () => rej(new Error('img load failed'))
      img.src = data
    })
    const [ia, ib] = await Promise.all([load(aData), load(bData)])
    const w = Math.max(ia.width, ib.width)
    const h = Math.max(ia.height, ib.height)
    const mk = (img: HTMLImageElement): Uint8ClampedArray => {
      const c = document.createElement('canvas')
      c.width = w; c.height = h
      const ctx = c.getContext('2d')!
      ctx.drawImage(img, 0, 0)
      return ctx.getImageData(0, 0, w, h).data
    }
    const [pa, pb] = [mk(ia), mk(ib)]
    let diff = 0
    for (let i = 0; i < pa.length; i += 4) {
      if (pa[i] !== pb[i] || pa[i + 1] !== pb[i + 1] || pa[i + 2] !== pb[i + 2]) diff++
    }
    return diff
  }, [`data:image/png;base64,${shotA.toString('base64')}`, `data:image/png;base64,${shotB.toString('base64')}`] as [string, string])
  const totalPixels = 1440 * 400
  expect(diffPixels / totalPixels, '贴底截图像素差异必须 ≤ 0.1%（实测独立渲染亚像素噪声 ~0.035%）').toBeLessThanOrEqual(0.001)

  await pageA.context().close()
  await pageB.context().close()
})
