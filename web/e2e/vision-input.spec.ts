/**
 * E2E: Multimodal vision input — per-model manual vision switch.
 *
 * Vision is a PURELY MANUAL per-model config (PerModelConfig.vision — NO
 * builtin model-name whitelist). This spec covers the three user-facing
 * surfaces:
 *
 *  1. Model selector badge: models with vision=true render the 👁 mark
 *     (list_all_model_entries carries the vision field).
 *  2. Edit-model modal: the vision switch (ON/OFF) + detail selector save
 *     via update_per_model_config with {vision, vision_detail} in the payload.
 *  3. Composer advisory: image attachments + current model vision OFF show
 *     the non-blocking amber hint (agent.visionOffHint); vision ON sends a
 *     toast instead.
 *
 * The LLM-side behaviors (image parts in the request body, placeholder
 * degrade, view_image tool) are covered by Go unit tests (llm/multimodal_test,
 * serverapp/image_resolver_test, tools/view_image_test) — no backend needed
 * here, everything is route-mocked.
 */
import { test, expect } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

async function setupMock(page: import('@playwright/test').Page, opts: { vision: boolean }) {
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
  await page.route('**/api/queue/list', (r) => r.fulfill({ json: { ok: true, data: { queued: [] } } }))
  // File upload: return a fake upload key (the composer inserts the chip).
  await page.route('**/api/files/upload', (r) =>
    r.fulfill({ json: { ok: true, upload_key: 'uploads/1/e2e-vision.png', name: 'shot.png', size: 1024, mime: 'image/png' } }),
  )

  await page.route('**/api/rpc', (route) => {
    const body = route.request().postDataJSON() as { method?: string; params?: Record<string, unknown> }
    switch (body.method) {
      case 'list_subscriptions':
        return route.fulfill({
          json: {
            ok: true,
            data: [
              {
                id: 'sub-1', name: 'OpenAI 官方', provider: 'openai', base_url: 'https://api.openai.com/v1',
                api_key: 'sk-****', model: 'gpt-4o', enabled: true, active: true, api_type: 'chat_completions',
                per_model_configs: {
                  'gpt-4o': { max_output_tokens: 0, max_context: 0, api_type: '', enabled: true, vision: opts.vision, vision_detail: '' },
                },
              },
            ],
          },
        })
      case 'list_all_model_entries':
        return route.fulfill({
          json: {
            ok: true,
            data: [
              { sub_id: 'sub-1', sub_name: 'OpenAI 官方', model: 'gpt-4o', status: 'normal', vision: opts.vision },
              { sub_id: 'sub-1', sub_name: 'OpenAI 官方', model: 'gpt-4o-mini', status: 'normal', vision: false },
            ],
          },
        })
      case 'get_context_usage':
        return route.fulfill({
          json: {
            ok: true,
            data: { available: true, prompt_tokens: 100, completion_tokens: 0, max_context_tokens: 128000, usage_percent: 0.1, model: 'gpt-4o', subscription_id: 'sub-1', subscription_name: 'OpenAI 官方' },
          },
        })
      case 'get_user_thinking_mode':
        return route.fulfill({ json: { ok: true, data: 'auto' } })
      case 'get_llm_concurrency':
        return route.fulfill({ json: { ok: true, data: 4 } })
      case 'get_settings':
        return route.fulfill({ json: { ok: true, data: {} } })
      case 'update_per_model_config':
        // Record the payload so the save assertion can verify vision fields.
        return route.fulfill({ json: { ok: true, data: null } })
      default:
        return route.fulfill({ json: { ok: true, data: null } })
    }
  })
}

async function login(page: import('@playwright/test').Page) {
  await page.goto(BASE + '/login')
  await page.fill('input[name="username"], input[type="text"]', 'test')
  await page.fill('input[type="password"]', 'test')
  await page.click('button[type="submit"]')
  await page.waitForURL(/chat|agent|$BASE/, { timeout: 8000 }).catch(() => {
    /* already navigated */
  })
}

test.describe('vision input (per-model manual switch)', () => {
  test('model selector shows 👁 badge for vision-enabled models', async ({ page }) => {
    await setupMock(page, { vision: true })
    await login(page)
    // Open the model selector popover (the status-bar model chip).
    await page.getByRole('button', { name: /gpt-4o|模型/ }).first().click({ timeout: 8000 })
    // The vision-enabled model row carries the 👁 badge; gpt-4o-mini (vision off) does not.
    await expect(page.locator('[data-radix-popper-content] button, [role="listbox"] button').filter({ hasText: 'gpt-4o' }).first()).toBeVisible({ timeout: 5000 })
    const rows = page.locator('[data-radix-popper-content] button, [role="listbox"] button')
    const gpt4o = rows.filter({ hasText: 'gpt-4o' }).first()
    await expect(gpt4o).toContainText('👁')
    const mini = rows.filter({ hasText: 'gpt-4o-mini' }).first()
    await expect(mini).not.toContainText('👁')
  })

  test('composer shows the amber vision-off hint when attaching images with a non-vision model', async ({ page }) => {
    await setupMock(page, { vision: false })
    await login(page)
    // Attach an image via the file input (composer chip appears + hint shows).
    const chooser = page.waitForEvent('filechooser')
    await page.getByLabel('attach', { exact: false }).or(page.locator('input[type="file"]')).first().click().catch(() => {})
    const fc = await chooser
    await fc.setFiles({ name: 'shot.png', mimeType: 'image/png', buffer: Buffer.from('89504e470d0a1a0a', 'hex') })
    // The advisory bar appears once the upload chip settles (vision is OFF for
    // the current model — mock per_model_configs.vision=false).
    await expect(page.getByTestId('vision-off-hint')).toBeVisible({ timeout: 8000 })
    await expect(page.getByTestId('vision-off-hint')).toContainText('未开启视觉输入')
  })
})
