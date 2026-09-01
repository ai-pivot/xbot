import { test, expect, type Page } from '@playwright/test'

/**
 * E2E: 玻璃模式层色审计（诊断 spec）。
 *
 * 背景：用户报告 tab 栏与聊天区同色（1aa89293 修复后 ✓）但与会话列表
 * 透明度完全不同——理论层叠分析全同构（app-bg alpha + card-bg alpha
 * 双层），停止理论推演，Playwright 实测逐层 computed backgroundColor
 * 找出非预期层。
 *
 * 玻璃模拟 = AmbienceRoot 同款：.ambience-glass class + root inline
 * setProperty --app-bg/--panel-bg = color-mix(#1e1e1e 60%, transparent)。
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

test.describe('玻璃模式层色审计', () => {
  test('逐层 backgroundColor 审计（tab 栏 / 会话列表 / 聊天区 / 全层链）', async ({ browser }) => {
    const page = await browser.newPage({ viewport: { width: 1400, height: 900 } })
    await setupMock(page)
    await page.goto('/')
    await expect(page.locator('.dv-groupview')).toHaveCount(2, { timeout: 15_000 })

    // 模拟玻璃（AmbienceRoot 同款：root inline setProperty + .ambience-glass + dark）
    await page.evaluate(() => {
      const root = document.documentElement
      root.classList.add('dark')
      root.classList.add('ambience-glass')
      root.style.setProperty('--app-bg', 'color-mix(in srgb, #1e1e1e 60%, transparent)')
      root.style.setProperty('--panel-bg', 'color-mix(in srgb, #1e1e1e 60%, transparent)')
    })
    await page.waitForTimeout(300)

    const audit = await page.evaluate(() => {
      const bg = (el: Element | null) => (el ? getComputedStyle(el).backgroundColor : 'N/A')
      const groups = Array.from(document.querySelectorAll('.dv-groupview'))
      const audit: Record<string, string> = {}
      audit.appShellRoot = bg(document.querySelector('.fixed.inset-0'))
      audit.host = bg(document.querySelector('.min-h-0.w-full.flex-1'))
      audit.dvView = bg(document.querySelector('.dv-view'))
      audit.groupview0 = groups[0] ? bg(groups[0]) : 'N/A'
      audit.groupview1 = groups[1] ? bg(groups[1]) : 'N/A'
      audit.contentContainer = bg(document.querySelector('.dv-content-container'))
      audit.tabsBar = bg(document.querySelector('.dv-tabs-and-actions-container'))
      audit.overlayHost = bg(document.querySelector('.dv-render-overlay > div'))
      audit.overlayFC = bg(document.querySelector('.dv-render-overlay'))
      // 会话卡（x 最小 = 左侧）内容根（.bg-card-bg）
      const sessionGroup = groups.find((g) => {
        const r = g.getBoundingClientRect()
        return r.x === Math.min(...groups.map((x) => x.getBoundingClientRect().x))
      })
      audit.sessionGroup = sessionGroup ? bg(sessionGroup) : 'N/A'
      audit.sessionContentRoot = sessionGroup ? bg(sessionGroup.querySelector('.bg-card-bg')) : 'N/A'
      audit.sessionGroupViewChildren = sessionGroup
        ? Array.from(sessionGroup.children).map((c) => `${(c as HTMLElement).className.split(' ')[0]}:${bg(c)}`).slice(0, 5).join(' | ')
        : 'N/A'
      // 所有 .bg-card-bg 元素（找涂 card-bg 的全部层）
      audit.allCardBgLayers = Array.from(document.querySelectorAll('.bg-card-bg'))
        .map((el) => `${el.tagName}.${(el as HTMLElement).className.split(' ').slice(0, 2).join('.')}:${bg(el)}`)
        .slice(0, 8)
        .join(' | ')
      return audit
    })
    console.log('[glass-audit]', JSON.stringify(audit, null, 2))
    expect(true).toBe(true) // 诊断 spec：断言仅确保执行完成
    await page.close()
  })
})

test.describe('XPath 元素定位', () => {
  test('用户报告的 DOM 路径元素背景审计', async ({ browser }) => {
    const page = await browser.newPage({ viewport: { width: 1400, height: 900 } })
    await setupMock(page)
    await page.goto('/')
    await expect(page.locator('.dv-groupview')).toHaveCount(2, { timeout: 15_000 })
    await page.evaluate(() => {
      const root = document.documentElement
      root.classList.add('dark')
      root.classList.add('ambience-glass')
      root.style.setProperty('--app-bg', 'color-mix(in srgb, #1e1e1e 60%, transparent)')
      root.style.setProperty('--panel-bg', 'color-mix(in srgb, #1e1e1e 60%, transparent)')
    })
    await page.waitForTimeout(300)
    const el = await page.evaluate(() => {
      const el = document.evaluate(
        '//*[@id="root"]/div/main/div[1]/div/div[4]/div/div/div[2]',
        document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null,
      ).singleNodeValue as HTMLElement | null
      if (!el) return { found: false }
      const path: string[] = []
      let cur: HTMLElement | null = el
      for (let i = 0; i < 10 && cur; i++) {
        path.unshift(cur.className ? `${cur.tagName}.${cur.className.split(' ').slice(0, 3).join('.')}` : cur.tagName)
        cur = cur.parentElement
      }
      return {
        found: true,
        cls: el.className,
        bg: getComputedStyle(el).backgroundColor,
        path,
        parentCls: el.parentElement?.className ?? '',
        parentBg: el.parentElement ? getComputedStyle(el.parentElement).backgroundColor : '',
      }
    })
    console.log('[xpath-audit]', JSON.stringify(el, null, 2))
    expect(true).toBe(true)
    await page.close()
  })
})
