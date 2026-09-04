/**
 * 手机端「设置 → LLM 配置」tab 宽度/布局回归。
 *
 * 设置面板 = 居中 Dialog（w-[min(92vw,56rem)] h-[70vh]，fix/theme-system
 * 从右侧 Sheet w-[720px] 重构为居中弹窗）+ 手机 nav 横滚条（<640px：
 * w-full flex-row overflow-x-auto；≥sm：竖直侧栏 w-36 仅桌面）。
 *
 * 断言（E2E 语义）：
 *   - 页面无横向溢出（scrollWidth <= innerWidth）
 *   - Dialog 居中且边界在视口内（92vw 自适应手机）
 *   - LLM 控制台内容区无不可达溢出
 */
import { test, expect } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

async function setupMock(page: import('@playwright/test').Page) {
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: {
          sessions: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() }],
          chats: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() }],
          orphan_subagents: [],
        },
      },
    }),
  )
  await page.route('**/api/history', (r) =>
    r.fulfill({ json: { ok: true, data: { messages: [], chat_id: 'chat-1', last_seq: 0, active_progress: null } } }),
  )
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))

  // LLM 设置数据：2 订阅 + 多模型（触发 header / 卡片 / chips 的完整渲染路径）
  await page.route('**/api/rpc', (route) => {
    const body = route.request().postDataJSON() as { method?: string }
    switch (body.method) {
      case 'list_subscriptions':
        return route.fulfill({
          json: {
            ok: true,
            data: [
              { id: 'sub-1', name: 'OpenAI 官方', provider: 'openai', base_url: 'https://api.openai.com/v1', api_key: 'sk-****', model: 'gpt-4o', enabled: true, is_system: false, active: true, api_type: 'chat_completions' },
              { id: 'sub-2', name: 'Anthropic', provider: 'anthropic', base_url: 'https://api.anthropic.com', api_key: 'sk-ant-****', model: 'claude-sonnet-4', enabled: true, is_system: false, active: false, api_type: '' },
            ],
          },
        })
      case 'list_all_model_entries':
        return route.fulfill({
          json: {
            ok: true,
            data: [
              { sub_id: 'sub-1', sub_name: 'OpenAI 官方', model: 'gpt-4o', status: 'normal' },
              { sub_id: 'sub-1', sub_name: 'OpenAI 官方', model: 'gpt-4o-mini', status: 'normal' },
              { sub_id: 'sub-2', sub_name: 'Anthropic', model: 'claude-sonnet-4', status: 'normal' },
              { sub_id: 'sub-2', sub_name: 'Anthropic', model: 'claude-opus-4', status: 'offline' },
            ],
          },
        })
      case 'get_user_thinking_mode':
        return route.fulfill({ json: { ok: true, data: 'auto' } })
      case 'get_llm_concurrency':
        return route.fulfill({ json: { ok: true, data: 4 } })
      case 'get_settings':
        return route.fulfill({ json: { ok: true, data: {} } })
      default:
        return route.fulfill({ json: { ok: true, data: null } })
    }
  })
}

async function openMobileSettingsLLM(page: import('@playwright/test').Page) {
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForTimeout(3000)

  // 打开设置：手机 = MobileAppShell 顶栏「设置」（aria-label="设置"）；
  // 桌面 = AppShell header「打开设置」（aria-label="打开设置"）
  await page.locator('button[aria-label="设置"], button[aria-label="打开设置"], button[aria-label="Settings"]').first().click()
  // 设置 Dialog 左侧/顶部 nav → LLM 配置 tab
  await page.locator('nav button', { hasText: 'LLM' }).first().click()
  await page.waitForTimeout(500)
}

test('mobile: settings LLM tab fits viewport — no horizontal overflow', async ({ browser }) => {
  const page = await browser.newPage({ viewport: { width: 375, height: 812 } })
  await page.addInitScript(() => {
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
    ;(window as unknown as SSEMockState).__sseListeners = listeners
    class MockEventSource {
      readyState = 1
      onopen: ((ev: Event) => void) | null = null
      onerror: ((ev: Event) => void) | null = null
      constructor(_url: string) {
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
  await openMobileSettingsLLM(page)

  // 诊断 + 断言数据一次性收集
  const layout = await page.evaluate(() => {
    const vw = window.innerWidth
    const doc = document.documentElement
    const dialog = document.querySelector('[data-slot="dialog-content"]') as HTMLElement | null
    const dialogRect = dialog?.getBoundingClientRect()
    const nav = dialog?.querySelector('nav') as HTMLElement | null
    const navRect = nav?.getBoundingClientRect()
    const contentArea = dialog?.querySelector('nav + div') as HTMLElement | null
    const contentRect = contentArea?.getBoundingClientRect()
    // 可见溢出探测：Dialog 内 rect.right > 视口的可见元素。
    // 排除三类合法情况：
    //  1) transform/visibility/opacity 隐藏态（detail drawer translateX(100%) 滑出、modal 关闭态）
    //  2) 祖先有 overflow-x: auto/scroll 的合法横向滚动容器（nav tab 条——tab 超宽可滚是特性）
    //  3) 内容区自身 overflow-x: hidden（显式裁剪，不可滚——drawer 平移溢出被裁剪不可见）
    const overflowing: string[] = []
    document.querySelectorAll('[data-slot="dialog-content"] *').forEach((el) => {
      const r = el.getBoundingClientRect()
      if (r.width > 0 && r.right > vw + 1) {
        const st = getComputedStyle(el)
        if (st.opacity === '0' || st.visibility === 'hidden' || st.transform !== 'none') return
        let p = el.parentElement
        let clippedAnc = false
        while (p && p !== document.body) {
          const ps = getComputedStyle(p)
          if (ps.overflowX === 'auto' || ps.overflowX === 'scroll' || ps.overflowX === 'hidden') { clippedAnc = true; break }
          p = p.parentElement
        }
        if (clippedAnc) return
        const cls = (el as HTMLElement).className
        const tag = el.tagName.toLowerCase()
        overflowing.push(`${tag}.${String(cls).split(' ')[0]} right=${Math.round(r.right)} w=${Math.round(r.width)}`)
      }
    })
    // 内容区横向裁剪模式（显式 hidden = 不可横拖）
    const contentOverflowX = contentArea ? getComputedStyle(contentArea).overflowX : ''
    // nav tab 条可达性：最后一个 tab（overflow-x-auto 可滚）
    const navTabs = nav ? Array.from(nav.querySelectorAll('button')) : []
    const lastTab = navTabs[navTabs.length - 1] as HTMLElement | undefined
    return {
      viewportWidth: vw,
      docScrollWidth: doc.scrollWidth,
      dialogRight: dialogRect ? Math.round(dialogRect.right) : null,
      dialogLeft: dialogRect ? Math.round(dialogRect.left) : null,
      dialogWidth: dialogRect ? Math.round(dialogRect.width) : null,
      navRect: navRect ? { l: Math.round(navRect.left), r: Math.round(navRect.right), w: Math.round(navRect.width) } : null,
      navOverflowX: nav ? getComputedStyle(nav).overflowX : null,
      contentRect: contentRect ? { l: Math.round(contentRect.left), r: Math.round(contentRect.right), w: Math.round(contentRect.width) } : null,
      contentOverflowX,
      overflowing,
      lastTabText: lastTab?.textContent?.trim() ?? null,
    }
  })

  console.log('MOBILE LLM TAB LAYOUT:', JSON.stringify(layout, null, 2))

  // 1) 页面整体无横向溢出
  expect(layout.docScrollWidth).toBeLessThanOrEqual(layout.viewportWidth + 1)
  // 2) Dialog 边界在视口内（92vw 自适应手机：375px 视口 → ~345px 宽）
  expect(layout.dialogRight ?? 0).toBeLessThanOrEqual(layout.viewportWidth + 1)
  expect(layout.dialogLeft ?? 0).toBeGreaterThanOrEqual(-1)
  // 居中弹窗：左右边距对称（±2px 容差防亚像素取整）
  const leftMargin = layout.dialogLeft ?? 0
  const rightMargin = (layout.viewportWidth ?? 0) - (layout.dialogRight ?? 0)
  expect(Math.abs(leftMargin - rightMargin)).toBeLessThanOrEqual(2)
  // 3) 内容区拿回全宽（手机 nav 横滚条替代 144px 侧栏，不再挤压内容区；
  //    Dialog 92vw - 2px border ≈ 91.5% 视口宽，阈值 0.9 防回归挤压）
  expect(layout.contentRect?.w ?? 0).toBeGreaterThanOrEqual(Math.round((layout.viewportWidth ?? 0) * 0.9))
  // 4) 内容区显式禁用横向滚动（溢出内容被裁剪，手机上不可拖动出空白）
  expect(layout.contentOverflowX).toBe('hidden')
  // 5) 无可见的、不可达的元素溢出（排除隐藏态与合法横滚容器内的元素）
  expect(layout.overflowing).toEqual([])
  // 6) LLM tab 在 nav 中可选中（最后一个 tab 存在——nav 横滚可达）
  expect(layout.lastTabText).toBeTruthy()
  await page.close()
})

test('desktop: settings LLM tab keeps sidebar layout (no mobile regression)', async ({ browser }) => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 } })
  await page.addInitScript(() => {
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
    ;(window as unknown as SSEMockState).__sseListeners = listeners
    class MockEventSource {
      readyState = 1
      onopen: ((ev: Event) => void) | null = null
      onerror: ((ev: Event) => void) | null = null
      constructor(_url: string) {
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
  await openMobileSettingsLLM(page)

  const layout = await page.evaluate(() => {
    const dialog = document.querySelector('[data-slot="dialog-content"]') as HTMLElement | null
    const dialogRect = dialog?.getBoundingClientRect()
    const nav = dialog?.querySelector('nav') as HTMLElement | null
    const navRect = nav?.getBoundingClientRect()
    const contentArea = dialog?.querySelector('nav + div') as HTMLElement | null
    const contentRect = contentArea?.getBoundingClientRect()
    const navStyle = nav ? getComputedStyle(nav) : null
    return {
      dialogWidth: dialogRect ? Math.round(dialogRect.width) : null,
      dialogLeft: dialogRect ? Math.round(dialogRect.left) : null,
      navW: navRect ? Math.round(navRect.width) : null,
      navLeft: navRect ? Math.round(navRect.left) : null,
      navFlexDir: navStyle ? navStyle.flexDirection : null,
      contentLeft: contentRect ? Math.round(contentRect.left) : null,
      contentW: contentRect ? Math.round(contentRect.width) : null,
    }
  })
  console.log('DESKTOP LLM TAB LAYOUT:', JSON.stringify(layout, null, 2))

  // 桌面（1280px 视口，≥sm 640px）：居中 Dialog 896px（min(92vw,56rem) 取 56rem）
  // + nav 竖直侧栏 144px + 内容区在其右侧（布局回归保护）
  expect(layout.dialogWidth).toBe(896)
  expect(layout.navW).toBe(144)
  expect(layout.navFlexDir).toBe('column')
  expect((layout.contentLeft ?? 0)).toBeGreaterThan((layout.navLeft ?? 0))
  // 896 - 144 nav - 2px Dialog 左右 border（border-box）= 750（±1 容差防计法差）
  expect(Math.abs((layout.contentW ?? 0) - (896 - 144 - 2))).toBeLessThanOrEqual(1)
  await page.close()
})
