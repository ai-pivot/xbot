import { test, expect } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:9999'

/** Mock a session with N messages (alternating user/assistant). */
function mockMessages(n: number) {
  return Array.from({ length: n }, (_, i) => ({
    id: `msg-${i}`,
    role: i % 2 === 0 ? 'user' : 'assistant',
    content: `Message ${i}. `.repeat(10),
    timestamp: new Date(Date.now() - (n - i) * 60000).toISOString(),
    persisted: true,
    turn_id: Math.floor(i / 2),
  }))
}

/** Intercept all API calls and return mock data. No real backend needed. */
async function setupMockBackend(page: import('@playwright/test').Page) {
  const messages = mockMessages(150)

  await page.route('**/api/settings', (route) =>
    route.fulfill({ status: 200, json: { ok: true, data: {} } }),
  )
  await page.route('**/api/auth/config', (route) =>
    route.fulfill({ json: { ok: true, data: { invite_only: false } } }),
  )
  await page.route('**/api/auth/login', (route) =>
    route.fulfill({ json: { ok: true, data: { user_id: 'test-user' } } }),
  )
  await page.route('**/api/session-tree', (route) =>
    route.fulfill({
      json: {
        ok: true,
        data: {
          sessions: [
            {
              chat_id: 'chat-1',
              channel: 'web',
              label: 'Test Chat',
              last_active: new Date().toISOString(),
            },
          ],
          chats: [
            {
              chat_id: 'chat-1',
              channel: 'web',
              label: 'Test Chat',
              last_active: new Date().toISOString(),
            },
          ],
          orphan_subagents: [],
        },
      },
    }),
  )
  await page.route('**/api/history', (route) =>
    route.fulfill({
      json: {
        ok: true,
        data: {
          messages: messages.map((m, i) => ({
            role: m.role,
            content: m.content,
            seq: i + 1,
            timestamp: m.timestamp,
          })),
          chat_id: 'chat-1',
          last_seq: messages.length,
          active_progress: null,
        },
      },
    }),
  )
  await page.route('**/api/session/status', (route) =>
    route.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }),
  )
  await page.route('**/api/sse**', (route) => {
    // Return an empty SSE stream — no live events needed for scroll tests
    route.fulfill({
      status: 200,
      contentType: 'text/event-stream',
      body: '',
    })
  })
  await page.route('**/api/rpc', (route) =>
    route.fulfill({ json: { ok: true, data: null } }),
  )
}

/** Find the actual message-list scroller (has overflow-y-auto + virtualized items). */
async function getScrollInfo(page: import('@playwright/test').Page) {
  return page.evaluate(() => {
    const els = Array.from(document.querySelectorAll('div'))
    const scrollable = els.find(d => {
      const s = getComputedStyle(d)
      if (s.overflowY !== 'auto' && s.overflowY !== 'scroll') return false
      return d.querySelector('[data-index]') !== null
    }) as HTMLElement
    if (!scrollable) return null
    return {
      scrollHeight: scrollable.scrollHeight,
      clientHeight: scrollable.clientHeight,
      scrollTop: Math.round(scrollable.scrollTop),
      diff: Math.round(scrollable.scrollHeight - scrollable.clientHeight - scrollable.scrollTop),
    }
  })
}

test.describe('Scroll behavior (mock backend)', () => {
  test('page load scrolls to bottom', async ({ browser }) => {
    const page = await browser.newPage()
    await setupMockBackend(page)
    await page.goto(`${BASE}/login`)

    // Login
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()

    // Wait for messages to load and render
    await page.waitForTimeout(5000)

    const info = await getScrollInfo(page)
    console.log('After load:', JSON.stringify(info))
    expect(info).not.toBeNull()
    expect(Math.abs(info!.diff)).toBeLessThan(5)

    await page.close()
  })

  test('page reload scrolls to bottom', async ({ browser }) => {
    const page = await browser.newPage()
    await setupMockBackend(page)
    await page.goto(`${BASE}/login`)

    // Login first
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()
    await page.waitForTimeout(3000)

    // Reload
    await page.reload()
    await page.waitForTimeout(5000)

    const info = await getScrollInfo(page)
    console.log('After reload:', JSON.stringify(info))
    expect(info).not.toBeNull()
    expect(Math.abs(info!.diff)).toBeLessThan(5)

    await page.close()
  })
})
