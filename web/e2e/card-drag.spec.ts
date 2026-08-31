import { test, expect, type Page } from '@playwright/test'

/**
 * E2E: 卡片 Ctrl 拖动（真实浏览器复现 — jsdom 集成测试全过但用户环境
 * 「主卡片拖动不了」，用真实 Chromium + 真实 PointerEvent 定位差异）。
 *
 * 复现路径：Ctrl + mouse down 主卡片内容区 → move（超 6px 阈值）→
 * 期望 overlay 出现（拖动激活）→ up 落子 → 主卡片位置变化。
 */

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

async function setupMock(page: Page) {
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) => r.fulfill({
    json: { ok: true, data: {
      sessions: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() }],
      chats: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() }],
      orphan_subagents: [],
    } },
  }))
  await page.route('**/api/history', (r) => r.fulfill({
    json: { ok: true, data: { messages: [], chat_id: 'chat-1', last_seq: 0, active_progress: null } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
  await page.addInitScript(() => {
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
    ;(window as unknown as SSEMockState).__sseListeners = listeners
    class MockEventSource {
      readyState = 1
      onopen: ((ev: Event) => void) | null = null
      onerror: ((ev: Event) => void) | null = null
      constructor(_url: string) { setTimeout(() => this.onopen?.(new Event('open')), 0) }
      addEventListener(type: string, handler: (ev: MessageEvent) => void) {
        if (!listeners[type]) listeners[type] = new Set(); listeners[type].add(handler)
      }
      removeEventListener(type: string, handler: (ev: MessageEvent) => void) { listeners[type]?.delete(handler) }
      close() { for (const key of Object.keys(listeners)) listeners[key].clear() }
    }
    ;(window as unknown as { EventSource: typeof MockEventSource }).EventSource = MockEventSource
  })
}

test.describe('卡片 Ctrl 拖动（真实浏览器）', () => {
  test('Ctrl+拖动主卡片：手势激活（overlay）+ 落子移动', async ({ browser }) => {
    const page = await browser.newPage({ viewport: { width: 1400, height: 900 } })
    const consoleLogs: string[] = []
    const pageErrors: string[] = []
    page.on('console', (m) => { if (m.type() === 'error' || m.type() === 'warning') consoleLogs.push(`[${m.type()}] ${m.text()}`) })
    page.on('pageerror', (e) => pageErrors.push(String(e)))
    await setupMock(page)
    await page.goto('/')

    // dockview 播种完成：会话卡片 + Agent 主卡片
    await expect(page.locator('.dv-groupview')).toHaveCount(2, { timeout: 15_000 })

    // 主卡片 = x 最大（最右侧）的 group（master 播种在 sidebar 右侧——与
    // 布局比例无关，50/50 或 80/20 下都成立）
    const groups = page.locator('.dv-groupview')
    const groupCount = await groups.count()
    let mainCard = groups.first()
    let maxX = -1
    for (let i = 0; i < groupCount; i++) {
      const b = await groups.nth(i).boundingBox()
      if (b && b.x > maxX) {
        maxX = b.x
        mainCard = groups.nth(i)
      }
    }
    const boxBefore = await mainCard.boundingBox()
    expect(boxBefore).not.toBeNull()

    // 按住 Ctrl → 光标提示（ctrl-drag-armed class on host）
    await page.keyboard.down('Control')
    await page.waitForTimeout(100)

    // 注入 pointerdown 探针（window + host capture 记录事件是否到达/ctrlKey/
    // target 是否在 .dv-groupview 内 + 完整祖先链 —— 定位拦截层）
    await page.evaluate(() => {
      const w = window as unknown as { __pdLog: unknown[] }
      w.__pdLog = []
      const log = (e: PointerEvent, layer: string) => {
        const t = e.target as Element | null
        const chain: string[] = []
        let el: Element | null = t
        while (el && chain.length < 10) {
          chain.push(`${el.tagName}.${(el.className || '').toString().slice(0, 28)}`)
          el = el.parentElement
        }
        w.__pdLog.push({
          layer,
          ctrl: e.ctrlKey,
          btn: e.button,
          inGroup: !!t?.closest?.('.dv-groupview'),
          chain,
        })
      }
      window.addEventListener('pointerdown', (e) => log(e, 'window'), true)
      const host = document.querySelector('.dv-resize-container')?.parentElement
      host?.addEventListener('pointerdown', (e) => log(e, 'host'), true)
    })

    // 手势：主卡片中心按下 → 拖向左（源左半 → quadrantZone 'left' → 换边）
    const cx = boxBefore!.x + boxBefore!.width * 0.6
    const cy = boxBefore!.y + boxBefore!.height * 0.5
    await page.mouse.move(cx, cy)
    await page.mouse.down()
    // 超过 6px 阈值：拖动激活（body grabbing + overlay 期待）
    await page.mouse.move(cx - 200, cy, { steps: 10 })
    await page.waitForTimeout(200)

    // 拖动前标记主卡 element（locator nth 索引在拖动后会漂移——groups
    // 顺序变化，用 DOM 标记稳定追踪同一张卡）
    await page.evaluate((el) => el.setAttribute('data-drag-main', '1'), await mainCard.elementHandle())

    // 拖动中诊断：overlay 是否出现（拖动激活的标志）
    const overlayCount = await page.locator('body > div[style*="z-index: 9999"]').count()
    const bodyCursor = await page.evaluate(() => document.body.style.cursor)
    const grabbingActive = bodyCursor === 'grabbing'
    const pdLog = await page.evaluate(() => (window as unknown as { __pdLog?: unknown[] }).__pdLog)
    console.log('[e2e-diag] pointerdown log:', JSON.stringify(pdLog))
    console.log('[e2e-diag] overlay count:', overlayCount, 'body cursor:', bodyCursor, 'mainCard x before:', boxBefore!.x)

    await page.mouse.up()
    await page.keyboard.up('Control')
    await page.waitForTimeout(300)

    // 用 DOM 标记拿主卡真实位置（不随 groups 顺序漂移）
    const boxAfter = await page.evaluate(() => {
      const el = document.querySelector('[data-drag-main]')
      if (!el) return null
      const r = el.getBoundingClientRect()
      return { x: r.x, y: r.y, width: r.width, height: r.height }
    })
    console.log('[e2e-diag] mainCard x after:', boxAfter?.x, 'console errors:', consoleLogs, 'pageErrors:', pageErrors)

    // 断言：拖动激活（grabbing 或 overlay 二者至少其一）
    expect(overlayCount > 0 || grabbingActive, `拖动未激活: cursor=${bodyCursor} overlay=${overlayCount}`).toBeTruthy()
    // 断言：换边生效（quadrantZone 'left' → 主卡片移到目标左侧，
    // x 显著变小 — 拖动落子不再是原位 no-op）
    expect(boxAfter, '主卡片 boundingBox 拖动后仍存在').not.toBeNull()
    const deltaX = boxBefore!.x - boxAfter!.x
    console.log('[e2e-diag] deltaX (向左换边):', deltaX)
    expect(deltaX, `主卡片未换边: before=${boxBefore!.x} after=${boxAfter!.x}`).toBeGreaterThan(100)

    // 颜色一致性断言：主卡（active，overlay 层渲染）与会话卡（group 内渲染）
    // 背景一致——所有 .dv-groupview 同 card-bg；主卡内容宿主（dv-render-overlay
    // 下 ReactContentRenderer div，bg-card-bg）与 group 背景同变量。
    const bgCheck = await page.evaluate(() => {
      const groups = Array.from(document.querySelectorAll('.dv-groupview'))
      const groupBgs = groups.map((g) => getComputedStyle(g).backgroundColor)
      const overlayHost = document.querySelector('.dv-render-overlay > div')
      return {
        groupBgs,
        overlayHostBg: overlayHost ? getComputedStyle(overlayHost).backgroundColor : null,
      }
    })
    console.log('[e2e-diag] bg check:', JSON.stringify(bgCheck))
    const uniqueGroupBgs = new Set(bgCheck.groupBgs)
    expect(uniqueGroupBgs.size, `group 背景不一致: ${[...uniqueGroupBgs].join(' vs ')}`).toBe(1)
    expect(bgCheck.overlayHostBg, '主卡 overlay 宿主背景应与 group card-bg 一致').toBe(bgCheck.groupBgs[0])
    expect(pageErrors, `page errors: ${pageErrors.join('; ')}`).toEqual([])
  })
})
