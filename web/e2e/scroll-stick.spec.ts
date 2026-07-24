import { test, expect } from '@playwright/test'
import { Readable } from 'stream'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

function mockMessages(n: number) {
  return Array.from({ length: n }, (_, i) => ({
    role: i % 2 === 0 ? 'user' : 'assistant',
    content: `Message ${i}. `.repeat(10),
    seq: i + 1,
    timestamp: new Date(Date.now() - (n - i) * 60000).toISOString(),
  }))
}

/** Create a continuous SSE stream that sends stream_content events over time */
function createSSEStream(): Readable {
  let seq = 1
  let line = 0
  let sent = 0
  return new Readable({
    read() {
      if (sent === 0) {
        // First: session(busy)
        this.push(`data: ${JSON.stringify({ type: 'session', seq: seq++, session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })}\n\n`)
        // Initial stream_content
        this.push(`data: ${JSON.stringify({ type: 'stream_content', seq: seq++, progress: { stream_content: 'Starting reasoning...', chat_id: 'web:chat-1' } })}\n\n`)
        sent = 1
        return
      }
      if (sent > 15) {
        this.push(null) // end stream
        return
      }
      // Grow content by adding a new line every 200ms
      setTimeout(() => {
        line++
        const content = Array.from({ length: line + 1 }, (_, i) => `Reasoning line ${i}. This is a longer line to simulate real reasoning content. `).join('\n')
        this.push(`data: ${JSON.stringify({ type: 'stream_content', seq: seq++, progress: { stream_content: content, chat_id: 'web:chat-1' } })}\n\n`)
        sent++
      }, 200)
    },
  })
}

async function setupMock(page: import('@playwright/test').Page) {
  await page.route('**/api/settings', r => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', r => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', r => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', r => r.fulfill({ json: { ok: true, data: { sessions: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() }], chats: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() }], orphan_subagents: [] } } }))
  await page.route('**/api/history', r => r.fulfill({ json: { ok: true, data: { messages: mockMessages(150), chat_id: 'chat-1', last_seq: 150, active_progress: null } } }))
  await page.route('**/api/session/status', r => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', r => r.fulfill({ status: 200, contentType: 'text/event-stream', body: createSSEStream() }))
  await page.route('**/api/rpc', r => r.fulfill({ json: { ok: true, data: null } }))
}

async function getScrollTop(page: import('@playwright/test').Page) {
  return page.evaluate(() => {
    const els = Array.from(document.querySelectorAll('div'))
    const sc = els.find(d => {
      const s = getComputedStyle(d)
      if (s.overflowY !== 'auto' && s.overflowY !== 'scroll') return false
      return d.querySelector('[data-index]') !== null
    }) as HTMLElement
    return sc ? Math.round(sc.scrollTop) : null
  })
}

test('stick=false: continuous reasoning stream does NOT scroll viewport', async ({ browser }) => {
  const page = await browser.newPage()
  await setupMock(page)
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForTimeout(5000)

  // Wait for liveMessage to appear (SSE stream started)
  await page.waitForFunction(() => {
    return document.body.textContent?.includes('Starting reasoning')
  }, { timeout: 5000 })

  // Scroll up to simulate stick=false (user reading earlier content)
  await page.evaluate(() => {
    const els = Array.from(document.querySelectorAll('div'))
    const sc = els.find(d => {
      const s = getComputedStyle(d)
      if (s.overflowY !== 'auto' && s.overflowY !== 'scroll') return false
      return d.querySelector('[data-index]') !== null
    }) as HTMLElement
    sc.scrollTop = Math.max(0, sc.scrollTop - 400)
  })
  await page.waitForTimeout(300)
  const before = await getScrollTop(page)
  console.log('Before streaming:', before)

  // Wait for multiple stream_content events (content grows ~3 lines)
  await page.waitForTimeout(1500)
  const after = await getScrollTop(page)
  console.log('After streaming:', after)
  console.log('Delta:', after! - before!)

  // scrollTop should NOT have changed — stick=false, content grew at bottom
  expect(Math.abs(after! - before!)).toBeLessThan(10)

  await page.close()
})
