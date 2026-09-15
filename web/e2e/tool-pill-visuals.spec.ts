import { test, expect, type Page } from '@playwright/test'
import i18n from '@/i18n'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/** 复制自 tool-pill-width.spec.ts 的 mock（同一套 /api/*）。 */
async function setupMock(page: Page, historyMessages: unknown[] = []) {
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) => r.fulfill({ json: { ok: true, data: {
    sessions: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() }],
    chats: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString(), isCurrent: true }],
    orphan_subagents: [], } } }))
  await page.route('**/api/history', (r) => r.fulfill({ json: { ok: true, data: { messages: historyMessages, chat_id: 'chat-1', last_seq: 0, active_progress: null } } }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

/**
 * 视觉语言断言（用户 2026-09-15 定稿）：
 *   ① 失败 pill = 红系 + 「失败」文字标签（成功安静、失败吵闹）；
 *   ② 假工具 pill = 虚线边框 + kind 头像 + 「系统」角标；
 *   ③ 行级失败告警 = `N 失败` chip。
 */
const ASSISTANT = {
  id: 2, role: 'assistant', content: 'done', timestamp: new Date().toISOString(), turn_id: 1,
  iterations: [
    { iteration: 1, content: 'ok', reasoning: '', tools: [
      { name: 'Shell', label: 'Shell: cargo check', status: 'done', summary: 'ok', detail: 'ok' },
      { name: 'Grep', label: 'Grep: shared_experts', status: 'error', summary: 'Error: exit 1', detail: 'Error: exit 1', exitCode: 1 },
      { name: 'background_task_result', label: '后台任务已完成', status: 'done', detail: 'build ok',
        toolHints: JSON.stringify({ kind: 'bg_task', task_id: '3f8f492a', status: 'done', elapsed_ms: 12300 }) },
    ] },
  ],
}

test('pill 视觉语言：失败吵闹 / 假工具可辨 / 行级告警', async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } })
  const page = await ctx.newPage()
  await setupMock(page, [{ id: 1, role: 'user', content: 'run check', timestamp: new Date().toISOString(), turn_id: 1 }, ASSISTANT])
  // 与同文件其它用例统一：**真登录**后才落进 agent 视图（`goto('/')` 在 mock 下不一定渲染）
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForSelector('[data-testid="tool-pill"]', { timeout: 30000 })

  // ① 失败 pill：data-tool-status=error 且带「失败」标签
  const errPill = page.locator('[data-tool-status="error"]').first()
  await expect(errPill).toBeAttached()
  await expect(errPill).toContainText(i18n.t('agent.tool.statusFailed'))

  // ② 假工具 pill：虚线边框 + 「系统」角标
  // `data-tool-name` 同时在 pill 与其外层 popover 包装上 ⇒ 取**最内层**（含「系统」角标的那个）
  const synPill = page.locator('[data-tool-name="background_task_result"]', { hasText: '系统' }).last()
  await expect(synPill).toBeAttached()
  await expect(synPill).toContainText(i18n.t('agent.tool.syntheticBadge'))
  const borderStyle = await synPill.evaluate((el) => getComputedStyle(el).borderStyle)
  expect(borderStyle, '假工具 pill 必须是虚线边框').toBe('dashed')

  // ③ 行级失败告警
  await expect(page.locator('[data-testid="tool-group-failed"]').first()).toContainText(i18n.t('agent.tool.statusFailed'))

  // 截图仅供人工/多模态复核；注意 mock 流程下 `historyReady` 不会翻转，面板仍显示 loading 遮罩
  //（移除 DOM 节点无效 —— React 会重渲染）。视觉契约以**上面的断言**为准；真会话截图待服务端重启后补。
  await page.screenshot({ path: '/tmp/pillvis/desktop.png', fullPage: true })
  await ctx.close()
})

/**
 * 用户 2026-09-15 追加的两条硬要求（必须先有几何断言，否则"一行一个"永远抓不到）：
 *   A. **同行合并**：390px 宽手机下必须**至少有两个 pill 共享同一行**（旧实现 max-w-full 是容器百分比
 *      ⇒ 长参数时每个 pill 独占一行；修复=恒定 `max-width: min(46vw, 15rem)`）。
 *   B. **左对齐**：所有 pill 的 icon 槽与名字首字符必须**同一 x**（恒定 16px 图标槽 + 恒定 3px 左色条
 *      + 「系统」角标移到名字之后）。
 * 旧断言只查"pill ≤ 行宽"，一行一个时同样成立 —— 所以那两个断言漏掉了这个 bug。
 */
test('手机端：pill 必须同行合并 + icon/首字符左对齐', async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 } })
  const page = await ctx.newPage()
  // ⚠️ 全部用**真实长参数**：短参数下"没有上限也能挤在一行" ⇒ 断言会假绿（2026-09-15 踩过：
  // 4 个 short label 的 pill 恰好同行合并，掩盖了真机长参数一行一个的 bug）。
  const msgs = [
    { id: 1, role: 'user', content: 'run', timestamp: new Date().toISOString(), turn_id: 1 },
    { id: 2, role: 'assistant', content: 'ok', timestamp: new Date().toISOString(), turn_id: 1, iterations: [
      { iteration: 1, content: 'ok', reasoning: '', tools: [
        { name: 'Shell', label: 'cd /home/smith/src/xbot && cargo check --workspace --all-targets', status: 'done', summary: 'ok', detail: 'ok' },
        { name: 'Shell', label: 'cd /home/smith/src/xbot && cargo test --workspace --release', status: 'done', summary: 'ok', detail: 'ok' },
        { name: 'Grep', label: 'Grep: shared_experts.*gate|expert_gate.*shared', status: 'done', summary: 'ok', detail: 'ok' },
        { name: 'Read', label: 'Read: /home/smith/src/xbot/docs/agent/architecture.md', status: 'done', summary: 'ok', detail: 'ok' },
        { name: 'background_task_result', label: '后台任务已完成: cargo build --release', status: 'done', detail: 'ok',
          toolHints: JSON.stringify({ kind: 'bg_task', task_id: '3f8f492a', status: 'done' }) },
      ] },
    ] },
  ]
  await setupMock(page, msgs)
  // 手机壳（MobileAppShell）默认视图不是 agent 面板 ⇒ 必须像通过的移动 spec 那样**真登录**，
  // 登录后才会落到 agent 视图（`goto('/')` + mock auth 会停在别的视图，pill 不在 DOM）。
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForSelector('[data-testid="tool-pill"]', { timeout: 30000 })

  const pills = page.locator('[data-testid="tool-pill"]')
  const n = await pills.count()
  expect(n).toBeGreaterThanOrEqual(3)

  // A. 同行合并：offsetTop 必须出现重复（即不止一个 pill 在同一行）
  const tops: number[] = []
  for (let i = 0; i < n; i++) {
    const box = await pills.nth(i).boundingBox()
    if (box) tops.push(Math.round(box.y))
  }
  const uniqueTops = new Set(tops).size
  expect(uniqueTops, `pill 必须同行合并（390px 下 ${n} 个 pill 占了 ${uniqueTops} 行）`).toBeLessThan(tops.length)

  // B. **列对齐**（用户原话「所有工具开头第一个字符以及 icon 都必须对齐」）：
  //    判据是**每个 pill 内部的相对偏移** —— `icon.x − pill.x` 与 `name.x − pill.x` 必须逐个一致。
  //    （不能比较绝对 x：同一 pill 行第 2/3 列的 pill 天然在 x+171；这正是我上一版断言的错误。）
  //    错位根因曾是状态槽不定宽：running 的点 6px vs done 的勾 14px ⇒ 内部偏移差 8px（截图②）。
  const offsets = await page.evaluate(() => {
    const round = (n: number) => Math.round(n)
    return Array.from(document.querySelectorAll('[data-testid="tool-pill"]')).map((pill) => {
      const px = pill.getBoundingClientRect().x
      const icon = pill.querySelector('[data-testid="tool-pill-icon"]')
      const name = pill.querySelector('[data-testid="tool-pill-name"]')
      return {
        icon: icon ? round(icon.getBoundingClientRect().x - px) : null,
        name: name ? round(name.getBoundingClientRect().x - px) : null,
      }
    })
  })
  expect(offsets.length).toBeGreaterThanOrEqual(3)
  for (const key of ['icon', 'name'] as const) {
    const vals = offsets.map((o) => o[key]).filter((v): v is number => v !== null)
    expect(vals.length, `${key} 样本不足`).toBeGreaterThanOrEqual(3)
    expect(Math.max(...vals) - Math.min(...vals), `${key} 列偏移必须一致（实测 ${JSON.stringify(vals)}）`).toBeLessThanOrEqual(1)
  }
  await page.screenshot({ path: '/tmp/pillvis/mobile-row.png', fullPage: true })
  await ctx.close()
})

/**
 * 用户 2026-09-15：「现在还是一个工具一行」+「连续 tool 可能跨越迭代边界，这种也要折叠」。
 * 真根因：`IterationGroup` **逐迭代**渲染 pill 行 ⇒ 连续 N 个 tool-only 迭代 = N 行。
 * 本用例 = 该现场（7 个连续 tool-only 迭代、每个 1 个工具、1 个失败）：
 *   A. **行数必须远小于工具数**（跨迭代折叠生效）——修复前 rows === 7（红）。
 *   B. **失败 chip 与 pill 同一行**（`N 失败` 不能独占一行）。
 */
test('跨迭代连续 tool：必须折叠为少数行 + 失败 chip 同行', async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } })
  const page = await ctx.newPage()
  const ts = new Date().toISOString()
  const T = (name: string, label: string, status = 'done') => ({ name, label, status, summary: 'ok', detail: 'ok' })
  await setupMock(page, [
    { id: 1, role: 'user', content: 'run', timestamp: ts, turn_id: 1 },
    { id: 2, role: 'assistant', content: '', timestamp: ts, turn_id: 1, iterations: [
      { iteration: 1, content: '', thinking: '', tools: [T('Shell', 'cd /home/smith/src/xbot && cargo check --workspace')] },
      { iteration: 2, content: '', thinking: '', tools: [T('Shell', 'cd /home/smith/src/xbot && cargo test --workspace')] },
      { iteration: 3, content: '', thinking: '', tools: [T('task_status', 'task_status: {"task_id": ["3f8f492a"]}')] },
      { iteration: 4, content: '', thinking: '', tools: [T('task_status', 'task_status: {"task_id": ["aaaaaaaa"]}')] },
      { iteration: 5, content: '', thinking: '', tools: [T('task_read', 'task_read: {"task_id": ["3f8f492a"]}', 'error')] },
      { iteration: 6, content: '', thinking: '', tools: [T('Shell', 'ls -la')] },
      { iteration: 7, content: '', thinking: '', tools: [T('Shell', 'pwd')] },
    ] },
  ])
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForSelector('[data-testid="tool-pill"]', { timeout: 30000 })
  await page.waitForTimeout(800)

  const geo = await page.evaluate(() => {
    const R = (n: number) => Math.round(n)
    const pills = Array.from(document.querySelectorAll('[data-testid="tool-pill"]'))
    const rows = Array.from(document.querySelectorAll('[data-testid="tool-pill-row"]'))
    const fail = document.querySelector('[data-testid="tool-group-failed"]')
    const pillRow = rows[0] as HTMLElement | undefined
    return {
      pills: pills.length,
      rows: rows.length,
      distinctPillY: new Set(pills.map((p) => R(p.getBoundingClientRect().y))).size,
      // 用**上下边界**判"同行"：flex `items-center` 下不同高度的元素顶端不同（截图实测
      // chip top=192 / 同行 pill top=202），"top 相等"是错的判据 —— 必须是**垂直重叠**。
      pillBands: pills.map((p) => { const b = p.getBoundingClientRect(); return [R(b.top), R(b.bottom)] }),
      chipBand: (() => { const b = fail?.getBoundingClientRect(); return b ? [R(b.top), R(b.bottom)] : null })(),
      failChipY: fail ? R(fail.getBoundingClientRect().y) : null,
      rowY: pillRow ? R(pillRow.getBoundingClientRect().y) : null,
    }
  })
  console.log('CROSS_GEO=' + JSON.stringify(geo))
  await page.screenshot({ path: '/tmp/cross-iter.png', fullPage: true })

  expect(geo.pills, '7 个工具都要有 pill').toBeGreaterThanOrEqual(7)
  // A. 跨迭代折叠：行数必须远小于工具数（修复前 = 7 行）
  expect(geo.rows, `pill 行数必须折叠（rows=${geo.rows}）`).toBeLessThanOrEqual(2)
  expect(geo.distinctPillY, `pill 的 y 值必须收敛（y 数=${geo.distinctPillY}）`).toBeLessThanOrEqual(3)
  // B. 失败 chip 与 pill 同一行（且不能独占一行）
  expect(geo.failChipY, '失败 chip 必须渲染').not.toBeNull()
  // ⚠️ 判据是「chip 与**某个 pill** 同行」而不是「chip 与行容器 y 相同」：chip 是 flex 行里
  // 换行后那一行的**首项**，其 y 与容器顶部天然相差行高（截图实测 chip=192 / row=170）。
  // 修复前 chip 在**自己的 flex 容器**里（独占一行，与任何 pill 都不同 y）。
  const chip = geo.chipBand as [number, number] | null
  const sharesRow = !!chip && (geo.pillBands as [number, number][]).some(([t, b]) => chip[0] < b - 2 && chip[1] > t + 2)
  expect(sharesRow, `失败 chip 必须与某个 pill **同一视觉行**（垂直重叠；chip=${JSON.stringify(chip)} pillBands=${JSON.stringify(geo.pillBands)}）`).toBe(true)
  await ctx.close()
})
