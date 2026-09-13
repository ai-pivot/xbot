import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E (mobile): tool pills must never exceed their row / the viewport.
 *
 * Bug report: "subagent 工具 pill 手机上有时候超宽" — a pill's rounded background
 * spilled past the message column and off-screen on phones.
 *
 * Root cause (flexbox): the pill and both of its wrappers (`LazyPillPopover` span
 * inside the `flex-wrap` pill list) were flex items with `min-width: auto` and
 * `overflow: visible` → they refuse to shrink below their content's min-content
 * width, which for `white-space: nowrap` text is the FULL text width. `max-w-full`
 * on the pill cannot help: a percentage max-width is indefinite while the ancestor
 * is being size-computed. Result: the pill rendered at content width (measured:
 * 592px / 648px pills in a 350px row at 390x844) and the text was never ellipsized.
 *
 * This spec drives the REAL app in mobile viewports with a mocked assistant message
 * carrying extreme tool names / params and asserts, geometrically:
 *   1. every pill (and the `+N` badge) sits inside its row; pill width <= row width
 *   2. the pill box clips its own content; direct flex children fit inside the box;
 *      deeper nodes (SweepText per-character spans) are clipped INSIDE the pill
 *   3. the long text IS ellipsized (scrollWidth > clientWidth = truncation happened)
 *   4. no ancestor of the row overflows horizontally, and neither does the page
 *   5. semantics preserved: >8 tools still render 7 pills + `+N`, clicking a pill
 *      still opens its detail popover
 *
 * NOTE on `scrollWidth` semantics (why (3) is `scrollWidth > clientWidth`, not `<=`):
 * for a *truncated* nowrap text node the content box is narrower than the text, so
 * `scrollWidth > clientWidth` IS the signature of ellipsis. `scrollWidth <=
 * clientWidth` would assert the opposite (no truncation at all — i.e. the bug, where
 * the text box was as wide as its text). "Nothing overflows" is therefore asserted on
 * the pill BOX (`box.scrollWidth <= box.clientWidth + 1`) plus the geometry of the
 * direct flex children, and (3) asserts the positive truncation evidence.
 */

async function setupMock(page: Page, historyMessages: unknown[] = []) {
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) => r.fulfill({
    json: { ok: true, data: {
      sessions: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() }],
      chats: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString(), isCurrent: true }],
      orphan_subagents: [],
    } },
  }))
  await page.route('**/api/history', (r) => r.fulfill({
    json: { ok: true, data: { messages: historyMessages, chat_id: 'chat-1', last_seq: 0, active_progress: null } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

// ── extreme payloads ──────────────────────────────────────────────────
/** 320-char ASCII param, no spaces, so it can only break by ellipsis. */
const LONG_ASCII_PARAM = 'x'.repeat(320)
/** 300+ char CJK param, no spaces (CJK text does not break on spaces). */
const LONG_CJK_PARAM = '这是一个非常长的中文参数'.repeat(25)

/** Long ASCII tool name (MCP style, no spaces). */
const LONG_ASCII_NAME = 'mcp__some__extremely_long_tool_name_v2_for_mobile_overflow'
/** Long mixed CJK/ASCII tool name (no spaces). */
const LONG_CJK_NAME = '工具名称超级长带中文与英文混合mcp__another__long_one_v3'

const LONG_SUBAGENT_ROLE = 'extremely-long-subagent-role-name-for-overflow-check'
const LONG_SUBAGENT_INSTANCE = 'instance-with-an-extremely-long-name-for-overflow-check'

/** >8 tools → 7 pills + "+N" badge (PILL_INLINE_MAX = 8 / HEAD = 7). */
function extremeTools(): unknown[] {
  const tools: unknown[] = [
    // 1. long ASCII tool name + 320-char ASCII param
    { name: LONG_ASCII_NAME, label: `${LONG_ASCII_NAME}: ${LONG_ASCII_PARAM}`, status: 'done', elapsed_ms: 1200, iteration: 1 },
    // 2. long CJK/mixed tool name + 300+ char CJK param (no spaces at all)
    { name: LONG_CJK_NAME, label: `${LONG_CJK_NAME}: ${LONG_CJK_PARAM}`, status: 'error', elapsed_ms: 800, iteration: 1 },
    // 3. running tool → SweepText path (per-character spans)
    { name: `${LONG_ASCII_NAME}_running`, label: `${LONG_ASCII_NAME}_running: ${LONG_ASCII_PARAM}`, status: 'running', elapsed_ms: 0, iteration: 1 },
    // 4. subagent (synthetic) — the tool the user actually reported
    {
      name: 'bg_subagent_completed',
      label: `bgsub:${LONG_SUBAGENT_ROLE}/${LONG_SUBAGENT_INSTANCE}`,
      status: 'done',
      summary: '子代理已完成',
      tool_hints: JSON.stringify({
        kind: 'subagent', role: LONG_SUBAGENT_ROLE, instance: LONG_SUBAGENT_INSTANCE,
        task: '验证移动端 pill 宽度', status: 'done', elapsed_ms: 42000, output: 'ok',
      }),
      iteration: 1,
    },
  ]
  // 5.-9. ordinary tools: still must render 7 pills + "+2" badge.
  for (let i = 5; i <= 9; i++) {
    tools.push({ name: `Short${i}`, label: `Short${i}: ok`, status: 'done', elapsed_ms: 10 * i, iteration: 1 })
  }
  return tools
}

/** Upstream viewports the app is checked at. */
const MOBILE = { width: 390, height: 844 }
const NARROW = { width: 320, height: 568 }

interface BoxGeo {
  left: number
  right: number
  width: number
  scrollWidth: number
  clientWidth: number
}

interface PillTextGeo {
  text: string
  /** Nesting depth relative to the pill BOX: 1 = a direct flex child of the box. */
  depth: number
  right: number
  width: number
  scrollWidth: number
  clientWidth: number
  /** Some PROPER ancestor strictly inside the box clips this element (overflow != visible). */
  clippedInside: boolean
}

interface PillGeo {
  name: string | null
  /** The `[data-testid="tool-pill"]` wrapper span (flex item of the pill list). */
  wrapper: BoxGeo
  /** The rounded pill box inside the wrapper (the flex container of the pill). */
  box: BoxGeo
  texts: PillTextGeo[]
}

interface AncestorGeo {
  tag: string
  className: string
  scrollWidth: number
  clientWidth: number
  right: number
  width: number
}

interface GeoDump {
  innerWidth: number
  docScrollWidth: number
  row: BoxGeo
  pills: PillGeo[]
  moreBadge: BoxGeo | null
  ancestors: AncestorGeo[]
}

/** Collect all geometry this spec asserts on (runs inside the page — helpers must be
 *  defined INSIDE the function: page.evaluate only serializes the function itself). */
function collectGeo(): GeoDump {
  const box = (el: Element): BoxGeo => {
    const h = el as HTMLElement
    const r = h.getBoundingClientRect()
    return {
      left: r.left, right: r.right, width: r.width,
      scrollWidth: h.scrollWidth, clientWidth: h.clientWidth,
    }
  }
  const row = document.querySelector('[data-testid="tool-pill-row"]') as HTMLElement | null
  if (!row) throw new Error('tool-pill-row not found')
  const wrappers = Array.from(document.querySelectorAll('[data-testid="tool-pill"]')) as HTMLElement[]
  const more = document.querySelector('[data-testid="tool-pill-more"]')

  const pillGeo: PillGeo[] = wrappers.map((wrapper) => {
    // The rounded pill box is the wrapper's only element child.
    const pillBox = wrapper.firstElementChild as HTMLElement
    const texts: PillTextGeo[] = []
    pillBox.querySelectorAll('*').forEach((el) => {
      const h = el as HTMLElement
      const hasDirectText = Array.from(h.childNodes).some(
        (n) => n.nodeType === Node.TEXT_NODE && (n.textContent || '').trim().length > 0,
      )
      let depth = 0
      let clippedInside = false
      for (let up: HTMLElement | null = h; up && up !== pillBox; up = up.parentElement) {
        depth++
        if (depth > 1) {
          const st = getComputedStyle(up)
          if (st.overflowX !== 'visible' || st.overflowY !== 'visible') clippedInside = true
        }
      }
      // Direct flex children always count (their text may live in nested spans, e.g.
      // SweepText); deeper nodes only when they hold text themselves.
      if (!hasDirectText && depth !== 1) return
      if (!(h.textContent || '').trim()) return
      const b = h.getBoundingClientRect()
      texts.push({
        text: (h.textContent || '').trim().slice(0, 40),
        depth,
        right: b.right, width: b.width,
        scrollWidth: h.scrollWidth, clientWidth: h.clientWidth,
        clippedInside,
      })
    })
    return { name: wrapper.getAttribute('data-tool-name'), wrapper: box(wrapper), box: box(pillBox), texts }
  })

  const ancestors: AncestorGeo[] = []
  let cur: HTMLElement | null = row
  while (cur && cur !== document.documentElement) {
    const b = cur.getBoundingClientRect()
    ancestors.push({
      tag: cur.tagName.toLowerCase(),
      className: cur.className.toString().slice(0, 90),
      scrollWidth: cur.scrollWidth,
      clientWidth: cur.clientWidth,
      right: b.right,
      width: b.width,
    })
    cur = cur.parentElement
  }

  return {
    innerWidth: window.innerWidth,
    docScrollWidth: document.scrollingElement ? document.scrollingElement.scrollWidth : -1,
    row: box(row),
    pills: pillGeo,
    moreBadge: more ? box(more) : null,
    ancestors,
  }
}

/** Drive the app to a rendered assistant message carrying the extreme tool pills. */
async function renderExtremePills(page: Page) {
  await page.addInitScript(() => {
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
    ;(window as unknown as { __sseListeners: typeof listeners }).__sseListeners = listeners
    class M {
      readyState = 1
      onopen: ((e: Event) => void) | null = null
      onerror: ((e: Event) => void) | null = null
      constructor(public url: string) { setTimeout(() => this.onopen?.(new Event('open')), 0) }
      addEventListener(t: string, h: (ev: MessageEvent) => void) {
        if (!listeners[t]) listeners[t] = new Set()
        listeners[t].add(h)
      }
      removeEventListener() {}
      close() {}
    }
    ;(window as unknown as { EventSource: typeof M }).EventSource = M
  })

  const ts = new Date().toISOString()
  await setupMock(page, [
    { id: 1, role: 'user', content: '这条消息带极端长的工具名', turn_id: 1, timestamp: ts, iterations: [] },
    {
      id: 2, role: 'assistant', content: '', turn_id: 1, timestamp: ts,
      iterations: [{ iteration: 1, thinking: 'thinking', content: '', completed_tools: extremeTools() }],
    },
  ])
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  // Wait for the pills to be rendered (a fixed sleep races the post-login
  // navigation, and evaluating during it destroys the JS context).
  await page.waitForSelector('[data-testid="tool-pill"]', { timeout: 30_000 })
  await page.waitForSelector('[data-testid="tool-pill-more"]', { timeout: 30_000 })
  await page.waitForTimeout(800)
}

async function assertPillsDoNotOverflow(page: Page, label: string) {
  const geo: GeoDump = await page.evaluate(collectGeo)
  // Raw dump — kept as evidence (the pre-fix run shows the overflowing numbers).
  console.log(`[${label}] geo=${JSON.stringify(
    { ...geo, pills: geo.pills.map((p) => ({ ...p, texts: p.texts.filter((t) => t.depth === 1) })) },
  )}`)

  // Semantics preserved: >8 tools → 7 pills + "+N" badge.
  expect(geo.pills.length, `${label}: 7 inline pills + overflow badge`).toBe(7)
  expect(geo.moreBadge, `${label}: "+N" badge rendered`).not.toBeNull()

  for (const p of geo.pills) {
    // (1) wrapper and box live inside the row
    expect(p.wrapper.right, `${label}: pill ${p.name} wrapper right inside row`).toBeLessThanOrEqual(geo.row.right + 1)
    expect(p.wrapper.width, `${label}: pill ${p.name} wrapper no wider than row`).toBeLessThanOrEqual(geo.row.width)
    expect(p.box.right, `${label}: pill ${p.name} box right inside row`).toBeLessThanOrEqual(geo.row.right + 1)
    expect(p.box.width, `${label}: pill ${p.name} box no wider than row`).toBeLessThanOrEqual(geo.row.width)

    // (2) the rounded box never scrolls its own content (nothing escapes it), and
    //     every direct flex child fits inside it
    expect(p.box.scrollWidth, `${label}: pill ${p.name} box has no overflowing content`).toBeLessThanOrEqual(p.box.clientWidth + 1)
    for (const tx of p.texts) {
      if (tx.depth === 1) {
        expect(tx.right, `${label}: text "${tx.text}" inside pill ${p.name}`).toBeLessThanOrEqual(p.box.right + 1)
        expect(tx.right, `${label}: text "${tx.text}" inside row`).toBeLessThanOrEqual(geo.row.right + 1)
        expect(tx.width, `${label}: text "${tx.text}" no wider than pill ${p.name}`).toBeLessThanOrEqual(p.box.width)
      } else {
        expect(tx.clippedInside, `${label}: nested text "${tx.text}" must be clipped inside pill ${p.name}`).toBe(true)
      }
    }
  }

  // (3) the overly long text IS ellipsized (truncation, not silent overflow)
  const truncated = geo.pills
    .flatMap((p) => p.texts)
    .filter((tx) => tx.depth === 1 && tx.scrollWidth > tx.clientWidth + 1)
  expect(truncated.length, `${label}: at least one pill text is ellipsized`).toBeGreaterThan(0)

  // (4) the "+N" badge stays inside the row as well
  expect(geo.moreBadge!.right, `${label}: "+N" badge right inside row`).toBeLessThanOrEqual(geo.row.right + 1)
  expect(geo.moreBadge!.width, `${label}: "+N" badge no wider than row`).toBeLessThanOrEqual(geo.row.width)

  // (5) no ancestor of the row overflows horizontally, and neither does the page
  for (const a of geo.ancestors) {
    expect(a.scrollWidth, `${label}: ancestor <${a.tag} class="${a.className}"> has no horizontal overflow`)
      .toBeLessThanOrEqual(a.clientWidth + 1)
  }
  expect(geo.docScrollWidth, `${label}: document has no horizontal overflow`).toBeLessThanOrEqual(geo.innerWidth + 1)
}

test.describe('tool pill width on mobile', () => {
  test('390x844: extreme tool names/params stay inside the pill row', async ({ browser }) => {
    const page = await browser.newPage({ viewport: MOBILE })
    await renderExtremePills(page)
    await assertPillsDoNotOverflow(page, '390x844')
    // Clean (popover closed) shot for the visual self-check.
    await page.screenshot({ path: 'test-results/tool-pill-width-390.png', fullPage: false })

    // ── semantics preserved: a pill still opens its detail popover ──
    const pill = page.locator(`[data-testid="tool-pill"]`).first()
    await pill.click()
    await expect(page.locator('[data-slot="popover-content"]')).toBeVisible()
    // the pill text stays ellipsized with the popover open too
    await assertPillsDoNotOverflow(page, '390x844+popover')

    await page.screenshot({ path: 'test-results/tool-pill-width-390-popover.png', fullPage: false })
    await page.close()
  })

  test('320x568: extreme tool names/params stay inside the pill row', async ({ browser }) => {
    const page = await browser.newPage({ viewport: NARROW })
    await renderExtremePills(page)
    await assertPillsDoNotOverflow(page, '320x568')
    await page.screenshot({ path: 'test-results/tool-pill-width-320.png', fullPage: false })
    await page.close()
  })
})
