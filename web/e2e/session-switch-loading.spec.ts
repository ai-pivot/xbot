/**
 * 切换会话的**逐帧**渲染契约（2026-09-18 用户报告）：
 *   「还是会有一瞬间的渲染错误，原因是切换会话后不会立刻渲染loading。
 *     会话只要开始切换就应该渲染loading了，这才是修复」
 *
 * 判据（帧级、与实现无关）：从「切换开始」到「目标会话内容出现」之间，**不允许**
 * 出现「可见的消息列表（非 loading）却不是目标会话」的帧 —— 也就是不能在切换途中
 * 先渲染上一会话的残留内容（那正是用户看到的一瞬间渲染错误）。
 *
 * 本 spec 同时把每一帧的样本打进 console，便于定位是哪一帧违规。
 */

import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

const HISTORY: Record<string, unknown[]> = {
  'chat-1': [
    { id: 1, role: 'user', content: 'hello from S1', seq: 1, turn_id: 101, timestamp: '2026-09-18T10:00:00Z' },
    { id: 2, role: 'assistant', content: 'answer one', seq: 2, turn_id: 101, timestamp: '2026-09-18T10:00:01Z' },
  ],
  'chat-2': [
    { id: 3, role: 'user', content: 'hello from S2', seq: 3, turn_id: 202, timestamp: '2026-09-18T10:01:00Z' },
    { id: 4, role: 'assistant', content: 'answer two', seq: 4, turn_id: 202, timestamp: '2026-09-18T10:01:01Z' },
  ],
}

type Sample = {
  frame: number
  t: number
  clickT: number
  loading: boolean
  visibleLists: number
  rows: string
  text: string
  /** 所有 agent 面板：[chatID|visible|text] */
  panels: string
}

async function newClient(browser: import('@playwright/test').Browser): Promise<{ page: Page }> {
  const page = await browser.newPage()
  let activeChat = 'chat-1'

  await page.addInitScript(() => {
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
    ;(window as unknown as { __sseListeners: typeof listeners }).__sseListeners = listeners
    class MockEventSource {
      readyState = 1
      onopen: ((ev: Event) => void) | null = null
      onerror: ((ev: Event) => void) | null = null
      constructor(public url: string) {
        setTimeout(() => this.onopen?.(new Event('open')), 0)
      }
      addEventListener(type: string, h: (ev: MessageEvent) => void) {
        ;(listeners[type] ||= new Set()).add(h)
      }
      removeEventListener(type: string, h: (ev: MessageEvent) => void) {
        listeners[type]?.delete(h)
      }
      close() {
        for (const k of Object.keys(listeners)) listeners[k].clear()
      }
    }
    ;(window as unknown as { EventSource: typeof MockEventSource }).EventSource = MockEventSource
  })

  const now = new Date().toISOString()
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: {
          sessions: [
            { chat_id: 'chat-1', channel: 'web', label: 'S1', last_active: now },
            { chat_id: 'chat-2', channel: 'web', label: 'S2', last_active: now },
          ],
          chats: [
            { chat_id: 'chat-1', channel: 'web', label: 'S1', last_active: now },
            { chat_id: 'chat-2', channel: 'web', label: 'S2', last_active: now },
          ],
          orphan_subagents: [],
        },
      },
    }),
  )
  await page.route('**/api/chats/*/switch**', async (r) => {
    const m = r.request().url().match(/\/api\/chats\/([^/]+)\/switch/)
    if (m?.[1]) activeChat = decodeURIComponent(m[1])
    await r.fulfill({ json: { ok: true, data: {} } })
  })
  const histCount: Record<string, number> = {}
  await page.route('**/api/history**', async (r) => {
    const url = new URL(r.request().url())
    const cid = url.searchParams.get('chat_id') || url.searchParams.get('id') || activeChat
    histCount[cid] = (histCount[cid] ?? 0) + 1
    // 目标会话刻意延迟，让「切换途中」的帧可观测（真实网络亦非 0 延迟）。
    // chat-1 的**第二次**请求（切回已有 tab 时的重新对账）同样延迟 —— 这正是
    // 「切回已缓存 tab 时不渲染 loading」的观测窗口。
    if (cid === 'chat-2' || (cid === 'chat-1' && histCount[cid] > 1)) {
      await new Promise((res) => setTimeout(res, 400))
    }
    const rows = HISTORY[cid] ?? []
    return r.fulfill({
      json: { ok: true, data: { messages: rows, chat_id: cid, last_seq: rows.length * 2, active_progress: null } },
    })
  })
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
  await page.route('**/api/queue/list', (r) => r.fulfill({ json: { ok: true, data: { items: [] } } }))

  // 页内统一时钟：真实点击时刻（capture 阶段）+ 每次 fetch 的发出/返回时刻。
  await page.addInitScript(() => {
    const w = window as unknown as { __reqs: { url: string; t: number; respT?: number }[] }
    w.__reqs = []
    const origFetch = window.fetch.bind(window)
    window.fetch = (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
      const rec = { url, t: performance.now() }
      if (/\/api\/(history|chats\/[^/]+\/switch|session-tree)/.test(url)) w.__reqs.push(rec)
      return origFetch(input, init).then((res) => {
        rec.respT = performance.now()
        return res
      })
    }
    document.addEventListener(
      'click',
      () => {
        const c = window as unknown as { __clickT?: number }
        if (c.__clickT === undefined) c.__clickT = performance.now()
      },
      true,
    )
  })

  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForFunction(() => document.body.textContent?.includes('answer one'), { timeout: 15_000 })
  return { page }
}

/** 逐帧采样：可见消息列表的行 id / 文本 + loading 屏是否可见。 */
async function startSampler(page: Page): Promise<void> {
  await page.evaluate(() => {
    const samples: Sample[] = []
    ;(window as unknown as { __samples: Sample[] }).__samples = samples
    // 重置点击时钟 —— 同一个页面里可能观测多次切换，每次采样只认紧随其后的那次点击
    //（capture 监听只在 __clickT === undefined 时写入）。
    ;(window as unknown as { __clickT: number | undefined }).__clickT = undefined
    let frame = 0
    const tick = () => {
      const clickT = (window as unknown as { __clickT?: number }).__clickT ?? 0
      const loadingEl = document.querySelector('[data-testid="session-loading-screen"]')
      const loading = !!loadingEl && (loadingEl as HTMLElement).getBoundingClientRect().height > 0
      const lists = Array.from(document.querySelectorAll('[data-message-list-content]')).filter((el) => {
        const rect = (el as HTMLElement).getBoundingClientRect()
        return rect.height > 0 && rect.width > 0
      })
      const rows = lists.flatMap((el) =>
        Array.from(el.querySelectorAll('[data-message-id]')).map((r) => r.getAttribute('data-message-id') ?? ''),
      )
      const panels = Array.from(document.querySelectorAll('[data-agent-chat-id]'))
        .map((el) => {
          const rect = (el as HTMLElement).getBoundingClientRect()
          const visible = rect.height > 0 && rect.width > 0
          const hasList = !!el.querySelector('[data-message-list-content]')
          const listEl = el.querySelector('[data-message-list-content]')
          const listRect = listEl?.getBoundingClientRect()
          const listVisible = !!listRect && listRect.height > 0 && listRect.width > 0
          const listText = (listEl?.textContent ?? '').replace(/\s+/g, ' ').slice(0, 24)
          const loadingEl = el.querySelector('[data-testid="session-loading-screen"]')
          const isLoading = !!loadingEl && (loadingEl as HTMLElement).getBoundingClientRect().height > 0
          const r = Math.round
          return `${el.getAttribute('data-agent-chat-id') || '(seed)'}|vis=${visible ? 1 : 0}|rect=${r(rect.x)},${r(rect.y)} ${r(rect.width)}x${r(rect.height)}|list=${hasList ? 1 : 0}${listVisible ? 'v' : 'h'}|load=${isLoading ? 1 : 0}|"${listText}"`
        })
        .join('  ~  ')
      samples.push({
        frame: frame++,
        t: performance.now(),
        clickT,
        loading,
        visibleLists: lists.length,
        rows: rows.join(','),
        text: (lists[0]?.textContent ?? '').replace(/\s+/g, ' ').slice(0, 70),
        panels,
      })
      if (samples.length < 300) requestAnimationFrame(tick)
    }
    requestAnimationFrame(tick)
  })
}

declare global {
  interface Window {
    __samples: Sample[]
  }
}

test.describe('切换会话：切换开始即 loading，不得先渲染上一会话残留', () => {
  test('切到 S2 的过程中，可见列表要么是 loading 要么是 S2（不出现 S1 残留帧）', async ({ browser }) => {
    const { page } = await newClient(browser)

    await expect(page.getByText('hello from S1')).toBeVisible({ timeout: 10_000 })

    await startSampler(page)

    await page.getByText('S2', { exact: true }).first().click()
    await expect(page.getByText('hello from S2')).toBeVisible({ timeout: 10_000 })
    await page.waitForTimeout(200)

    const { samples, clickT, reqs } = await page.evaluate(() => {
      const w = window as unknown as {
        __samples: Sample[]
        __clickT?: number
        __reqs: { url: string; t: number; respT?: number }[]
      }
      return { samples: w.__samples.slice(), clickT: w.__clickT ?? 0, reqs: w.__reqs.slice() }
    })
    // 页内统一时钟：真实点击时刻（capture 监听）对齐 fetch 打点与逐帧采样。
    const afterClick = samples.filter((s) => s.t >= clickT)
    const label = (u: string) => u.replace(/^https?:\/\/[^/]+/, '').split('?')[0]

    // eslint-disable-next-line no-console
    console.log(
      'REQUEST TIMELINE (relative to real in-page click):\n' +
        reqs
          .map(
            (r) =>
              `+${Math.round(r.t - clickT)}ms REQ  ${label(r.url)}${r.url.includes('chat-2') ? ' (chat-2)' : ''}` +
              (r.respT ? `\n+${Math.round(r.respT - clickT)}ms RESP ${label(r.url)}` : ''),
          )
          .join('\n') +
        '\n\nFRAMES after click:\n' +
        afterClick
          .map(
            (s) =>
              `+${Math.round(s.t - clickT)}ms #${s.frame} loading=${s.loading} lists=${s.visibleLists} rows=[${s.rows}]\n    panels: ${s.panels}`,
          )
          .join('\n'),
    )

    const s2SeenAt = afterClick.findIndex((s) => s.text.includes('hello from S2'))
    expect(s2SeenAt, 'S2 内容最终必须出现').toBeGreaterThanOrEqual(0)

    // 违规帧 = 目标内容出现之前，可见列表既不是 loading，渲染的也不是 S2 —— 即
    // 「切换途中仍然渲染上一会话（S1）的残留内容」。
    const violations = afterClick
      .slice(0, s2SeenAt)
      .filter((s) => !s.loading && s.visibleLists > 0 && !s.text.includes('hello from S2'))
      .filter((s) => s.rows !== '')

    expect(
      violations,
      `切换途中出现了非 loading 的残留帧：\n${violations.map((v) => `#${v.frame} rows=[${v.rows}] text="${v.text}"`).join('\n')}`,
    ).toEqual([])

    await page.close()
  })

  test('切回已存在的 tab：点击后第一帧就必须是 loading（不得渲染保留内容等待后台对账）', async ({ browser }) => {
    const { page } = await newClient(browser)

    await expect(page.getByText('hello from S1')).toBeVisible({ timeout: 10_000 })
    // 先切到 S2（新 tab → loading → 内容），让 S1 的 tab 变成"已存在但已隐藏"。
    await page.getByText('S2', { exact: true }).first().click()
    await expect(page.getByText('hello from S2')).toBeVisible({ timeout: 10_000 })

    // 重置采样/时钟，只观测"切回 S1"这一次切换（startSampler 会清空 __samples 与
    // __clickT；capture 阶段的 click 监听随后记下这次真实点击时刻）。
    await startSampler(page)

    await page.getByText('S1', { exact: true }).first().click()
    // 让"重新对账"窗口跑完（路由对 chat-1 的第二次请求延迟 400ms）
    await page.waitForTimeout(800)

    const { samples, clickT } = await page.evaluate(() => {
      const w = window as unknown as { __samples: Sample[]; __clickT?: number }
      return { samples: w.__samples.slice(), clickT: w.__clickT ?? 0 }
    })
    const after = samples.filter((s) => s.t >= clickT)
    const first = after[0]

    // eslint-disable-next-line no-console
    console.log(
      'SWITCH-BACK frames:\n' +
        after
          .slice(0, 12)
          .map((s) => `+${Math.round(s.t - clickT)}ms loading=${s.loading} | ${s.panels}`)
          .join('\n'),
    )

    expect(first, '点击后必须有采样帧').toBeTruthy()
    // 用户判据：会话只要开始切换就应该渲染 loading。切回已有 tab 时，面板不得先渲染
    // 保留内容（那是上一时刻的快照，稍后会被后台对账改写 ⇒ 肉眼可见的"渲染错误"）。
    expect(
      first.loading,
      `切回已有 tab 的第一帧必须已是 loading，实际 panels=${first.panels}`,
    ).toBe(true)

    // ⛔ 幽灵面板守卫：占位 tab（无 sessionId）在别的面板承载会话时**不渲染任何 UI**
    //（用户截图：消息区上方浮着一排输入框控件 = 幽灵面板的 MessageInput 漏出）。
    const ghosts = await page.evaluate(
      () => document.querySelectorAll('[data-agent-chat-id=""]').length,
    )
    expect(ghosts, '不得存在无归属会话的幽灵 agent 面板（会漏出输入框控件）').toBe(0)

    await page.close()
  })
})
