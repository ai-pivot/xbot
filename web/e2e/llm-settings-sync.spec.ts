import { test, expect } from '@playwright/test'

/**
 * LLM 配置的「两边数据统一」回归（用户报告：设置里添加/更新 LLM 后，当前会话的
 * LLM 选择栏不更新，必须刷新页面）。
 *
 * 架构：`useLLMSettings()` 有两个独立实例 —— AgentPanel（喂会话 LLM 选择栏
 * ModelSelector 的 subscriptions / modelEntries）与 SettingsDialog（做增删改）。
 * 修复前 mutation 只 `await load()` 自己那份 → 选择栏陈旧。
 * 修复：模块级 LLM 配置变更总线（`subscribeLLMConfigChanged`），任一实例改完，
 * 所有实例（含会话栏）立即重拉；AgentPanel 另外刷新会话级上下文（当前模型/上限）。
 *
 * 本用例断言：在设置里新增订阅后，**不刷新页面**的前提下，会话 LLM 选择栏
 * 立刻出现新订阅与新模型。
 */
const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

interface Sub {
  id: string
  name: string
  provider: string
  base_url: string
  api_key: string
  model: string
  enabled: boolean
  is_system: boolean
  active: boolean
  api_type: string
}

async function setupMock(page: import('@playwright/test').Page) {
  const subs: Sub[] = []

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

  await page.route('**/api/rpc', (route) => {
    const body = route.request().postDataJSON() as { method?: string; params?: Record<string, unknown> }
    const method = body?.method ?? ''
    const entries = subs.map((s) => ({
      sub_id: s.id,
      sub_name: s.name,
      model: s.model,
      status: 'normal',
      max_context: 200000,
      max_output: 8192,
    }))
    switch (method) {
      case 'list_subscriptions':
        return route.fulfill({ json: { ok: true, data: subs } })
      case 'list_all_model_entries':
      case 'refresh_model_entries':
        return route.fulfill({ json: { ok: true, data: entries } })
      case 'get_user_thinking_mode':
        return route.fulfill({ json: { ok: true, data: '' } })
      case 'get_llm_concurrency':
        return route.fulfill({ json: { ok: true, data: 0 } })
      case 'get_settings':
        return route.fulfill({ json: { ok: true, data: {} } })
      case 'get_context_usage': {
        const sub = subs[0]
        return route.fulfill({
          json: {
            ok: true,
            data: {
              available: Boolean(sub),
              prompt_tokens: sub ? 1000 : 0,
              completion_tokens: 10,
              max_context_tokens: 200000,
              usage_percent: sub ? 0.5 : 0,
              model: sub?.model ?? '',
              subscription_id: sub?.id ?? '',
              subscription_name: sub?.name ?? '',
            },
          },
        })
      }
      case 'add_subscription': {
        const sub = (body.params?.sub ?? {}) as Record<string, string>
        subs.push({
          id: `sub-${subs.length + 1}`,
          name: sub.name ?? '',
          provider: sub.provider ?? 'openai',
          base_url: sub.base_url ?? '',
          api_key: sub.api_key ?? '',
          model: sub.model ?? '',
          enabled: true,
          is_system: false,
          active: true,
          api_type: sub.api_type ?? 'chat_completions',
        })
        return route.fulfill({ json: { ok: true, data: null } })
      }
      default:
        return route.fulfill({ json: { ok: true, data: null } })
    }
  })
  return { subs }
}

/** 注入 mock EventSource 并保持"已连接"：useLLMSettings.load() 在未连接时直接跳过拉取。 */
async function installSSE(page: import('@playwright/test').Page) {
  await page.addInitScript(() => {
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
    ;(window as unknown as { __sseListeners: typeof listeners }).__sseListeners = listeners
    class MockEventSource {
      readyState = 1
      onopen: ((e: Event) => void) | null = null
      onerror: ((e: Event) => void) | null = null
      onmessage: ((e: MessageEvent) => void) | null = null
      constructor(public url: string) {
        setTimeout(() => this.onopen?.(new Event('open')), 0)
      }
      addEventListener(t: string, h: (e: MessageEvent) => void) {
        if (!listeners[t]) listeners[t] = new Set()
        listeners[t].add(h)
      }
      removeEventListener() {}
      close() {}
    }
    ;(window as unknown as { EventSource: unknown }).EventSource = MockEventSource
  })
}

test('设置里添加 LLM 后，会话 LLM 选择栏立刻更新（不刷新页面）', async ({ browser }) => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  await installSSE(page)
  const state = await setupMock(page)

  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForTimeout(1500)

  // 设置 → LLM 配置 → 添加订阅（选择器语言无关：E2E 环境可能是 en）
  await page.locator('button[aria-label="设置"], button[aria-label="Settings"]').first().click()
  await page.locator('nav button', { hasText: 'LLM' }).first().click()
  const addBtn = page.locator('button', { hasText: /Add Subscription|添加订阅/ })
  await addBtn.first().click()

  await page.locator('input[placeholder*="OpenAI"]').fill('Brand New Sub')
  await page.locator('input[placeholder="https://…"]').fill('https://example.test/v1')
  await page.locator('input[type="password"]').last().fill('sk-test')
  await page.locator('input[placeholder="model-name"]').fill('brand-new-model')
  await addBtn.last().click()
  await page.waitForTimeout(800)

  // 关闭设置弹窗 —— 不刷新页面
  await page.keyboard.press('Escape')
  await page.waitForTimeout(500)

  const selector = page.getByRole('button', { name: /Choose model and thinking mode|选择模型和思考模式/ }).first()
  await expect(selector).toBeVisible()
  await selector.click()

  // 断言限定在弹层内（避免匹配到 "Added …" toast 造成假阳性）
  const popover = page.locator('[data-radix-popper-content-wrapper]').last()
  await expect(popover.getByText('Brand New Sub')).toBeVisible()
  await expect(popover.getByText('brand-new-model')).toBeVisible()
  expect(state.subs.map((s) => s.name)).toEqual(['Brand New Sub'])
})
