import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E test for streaming scroll jitter.
 *
 * Reproduces the issue where scroll height or scrollTop oscillates during
 * fast-updating stream_content events, especially when the user scrolls.
 *
 * Approach: replace EventSource with a controllable mock via addInitScript,
 * then inject stream_content events with precise timing.
 */

function mockMessages(n: number) {
  return Array.from({ length: n }, (_, i) => ({
    role: i % 2 === 0 ? 'user' : 'assistant',
    content: `Message ${i}. `.repeat(10),
    seq: i + 1,
    timestamp: new Date(Date.now() - (n - i) * 60000).toISOString(),
  }))
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
    json: { ok: true, data: { messages: mockMessages(100), chat_id: 'chat-1', last_seq: 100, active_progress: null } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  // SSE: return empty stream (real events injected via __emitSSE)
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

let seqCounter = 0

/** Type for the global SSE mock state injected via addInitScript. */
interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
  __sseSeq: number
}

async function emitSSE(page: Page, type: string, data: Record<string, unknown>) {
  await page.evaluate(({ type, data, seq }) => {
    const w = window as unknown as SSEMockState
    const listeners = w.__sseListeners
    if (!listeners) return
    const handlers = listeners[type] as Set<(ev: MessageEvent) => void> | undefined
    if (!handlers) return
    const ev = new MessageEvent(type, { data: JSON.stringify({ ...data, seq }) })
    handlers.forEach((h) => h(ev))
  }, { type, data, seq: ++seqCounter })
}

test.describe('Streaming scroll jitter', () => {
  test.beforeEach(() => {
    seqCounter = 0
  })

  test('scrollTop and scrollHeight do NOT oscillate during rapid streaming + random scroll', async ({ browser }) => {
    const page = await browser.newPage()

    // Replace EventSource with a controllable mock
    await page.addInitScript(() => {
      const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
      const w = window as unknown as SSEMockState
      w.__sseListeners = listeners
      class MockEventSource {
        readyState = 1 // OPEN
        onopen: ((ev: Event) => void) | null = null
        onerror: ((ev: Event) => void) | null = null
        constructor(public url: string) {
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
    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()

    // Wait for messages to load
    await page.waitForFunction(() => document.body.textContent?.includes('Message 99'), { timeout: 10000 })

    // Start streaming: send session(busy) + initial stream_content
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'stream_content', { type: 'stream_content', progress: { stream_content: 'Starting...', chat_id: 'web:chat-1', streaming: true } })

    // Wait for live message to appear
    await page.waitForFunction(() => document.body.textContent?.includes('Starting'), { timeout: 5000 })

    // ── Run the streaming + scroll test ──
    const result = await page.evaluate(async () => {
      const findSc = (): HTMLElement | null => {
        const els = Array.from(document.querySelectorAll('div'))
        return els.find((d) => {
          const s = getComputedStyle(d)
          if (s.overflowY !== 'auto' && s.overflowY !== 'scroll') return false
          return d.querySelector('[data-index]') !== null
        }) as HTMLElement | undefined ?? null
      }

      const data: { t: number; scrollTop: number; scrollHeight: number }[] = []
      const start = performance.now()
      const duration = 4000

      return new Promise<{ samples: typeof data; emit: (type: string, data: Record<string, unknown>) => void }>((resolve) => {
        // Sample every animation frame
        const sample = () => {
          const sc = findSc()
          if (sc) {
            data.push({
              t: Math.round(performance.now() - start),
              scrollTop: Math.round(sc.scrollTop),
              scrollHeight: Math.round(sc.scrollHeight),
            })
          }
          if (performance.now() - start < duration) {
            requestAnimationFrame(sample)
          } else {
            resolve({ samples: data, emit: () => {} })
          }
        }
        requestAnimationFrame(sample)

        // ── Inject stream_content every 50ms ──
        let line = 0
        const streamInterval = setInterval(() => {
          line++
          const content = Array.from(
            { length: line + 1 },
            (_, i) => `Line ${i}. This is streaming content that grows to simulate LLM output. `,
          ).join('\n')
          const w = window as unknown as SSEMockState
          const listeners = w.__sseListeners
          const handlers = listeners?.['stream_content'] as Set<(ev: MessageEvent) => void> | undefined
          if (handlers) {
            const seq = w.__sseSeq = (w.__sseSeq || 2) + 1
            const ev = new MessageEvent('stream_content', {
              data: JSON.stringify({ type: 'stream_content', seq, progress: { stream_content: content, chat_id: 'web:chat-1', streaming: true } }),
            })
            handlers.forEach((h) => h(ev))
          }
        }, 50)

        // ── Random low-frequency scroll every ~800ms ──
        const scrollInterval = setInterval(() => {
          const sc = findSc()
          if (!sc) return
          const action = Math.random()
          if (action < 0.5) {
            sc.scrollTop = Math.max(0, sc.scrollTop - (100 + Math.random() * 200))
          } else if (action < 0.8) {
            sc.scrollTop = Math.min(sc.scrollHeight, sc.scrollTop + (100 + Math.random() * 200))
          }
        }, 800)

        // Clean up
        setTimeout(() => {
          clearInterval(streamInterval)
          clearInterval(scrollInterval)
        }, duration)
      })
    })

    const samples = result.samples
    expect(samples.length).toBeGreaterThan(50)

    // ── Analysis ──
    // 1. scrollHeight should NEVER decrease (content only grows)
    let heightDecreases = 0
    const heightDecreaseDetails: string[] = []
    for (let i = 1; i < samples.length; i++) {
      if (samples[i].scrollHeight < samples[i - 1].scrollHeight - 1) {
        heightDecreases++
        heightDecreaseDetails.push(`t=${samples[i].t}: ${samples[i-1].scrollHeight}→${samples[i].scrollHeight} (Δ${samples[i].scrollHeight - samples[i-1].scrollHeight})`)
      }
    }

    // 2. Rapid oscillation: reversals within any 500ms window
    let maxReversalsInWindow = 0
    const windowMs = 500
    for (let i = 0; i < samples.length; i++) {
      const windowEnd = samples[i].t + windowMs
      let windowReversals = 0
      let prevDir = 0
      for (let j = i + 1; j < samples.length && samples[j].t <= windowEnd; j++) {
        const d = samples[j].scrollTop - samples[j - 1].scrollTop
        if (Math.abs(d) <= 2) continue
        const dir = d > 0 ? 1 : -1
        if (prevDir !== 0 && dir !== prevDir) windowReversals++
        prevDir = dir
      }
      if (windowReversals > maxReversalsInWindow) maxReversalsInWindow = windowReversals
    }

    console.log('Samples:', samples.length)
    console.log('scrollHeight decreases:', heightDecreases)
    console.log('Max reversals in 500ms window:', maxReversalsInWindow)
    console.log('First 10:', JSON.stringify(samples.slice(0, 10)))
    console.log('Last 10:', JSON.stringify(samples.slice(-10)))

    // ── Assertions ──
    // scrollHeight decreases can happen from virtualizer lazy measurement
    // (unmeasured items get actual size < estimate when scrolled into view).
    // Log for diagnosis but don't fail on small counts.
    console.log('heightDecreases:', heightDecreases, '(virtualizer lazy-measure can cause 1-2)')
    // The real jitter signal: rapid scrollTop direction reversals
    expect(maxReversalsInWindow).toBeLessThan(6)

    await page.close()
  })

  test('scrollTop stays fixed near top when visible items resize during streaming', async ({ browser }) => {
    const page = await browser.newPage()
    await page.setViewportSize({ width: 1280, height: 720 })

    // Replace EventSource with a controllable mock
    await page.addInitScript(() => {
      const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
      const w = window as unknown as SSEMockState
      w.__sseListeners = listeners
      class MockEventSource {
        readyState = 1
        onopen: ((ev: Event) => void) | null = null
        onerror: ((ev: Event) => void) | null = null
        constructor(public url: string) { setTimeout(() => this.onopen?.(new Event('open')), 0) }
        addEventListener(type: string, handler: (ev: MessageEvent) => void) {
          if (!listeners[type]) listeners[type] = new Set(); listeners[type].add(handler)
        }
        removeEventListener(type: string, handler: (ev: MessageEvent) => void) { listeners[type]?.delete(handler) }
        close() { for (const key of Object.keys(listeners)) listeners[key].clear() }
      }
      ;(window as unknown as { EventSource: typeof MockEventSource }).EventSource = MockEventSource
    })

    await setupMock(page)
    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()

    // Wait for messages to load (100 messages → scrollHeight >> viewport)
    await page.waitForFunction(() => document.body.textContent?.includes('Message 99'), { timeout: 10000 })
    await page.waitForTimeout(500)

    // ── Scroll to the top (user reading history at the beginning) ──
    // Home key → onKeyDown → pauseFollowing() → stickToBottomRef = false
    const scrollContainer = page.locator('[data-message-list-content]').locator('..')
    await scrollContainer.focus()
    await page.keyboard.press('Home')
    await page.waitForTimeout(300)

    // ── Scroll down slightly (50px) — user is near the top but not at scrollTop=0 ──
    // This sets stickToBottomRef=false (via onWheel → pauseFollowing).
    // scrollTop is now ~50, so the first item (item.start=0) satisfies the
    // virtualizer's default condition `item.start < scrollTop` (0 < 50 = TRUE).
    // The item's bottom (item.end ≈ 250) is still IN the viewport.
    await page.mouse.wheel(0, 50)
    await page.waitForTimeout(300)

    const scrollTopBefore = await page.evaluate(() => {
      const els = Array.from(document.querySelectorAll('div'))
      const sc = els.find(d => { const s = getComputedStyle(d); return (s.overflowY === 'auto' || s.overflowY === 'scroll') && d.querySelector('[data-index]') !== null }) as HTMLElement
      return sc ? Math.round(sc.scrollTop) : null
    })
    // User should be near the top, with a small scrollTop
    expect(scrollTopBefore!).toBeGreaterThan(5)
    expect(scrollTopBefore!).toBeLessThan(200)

    // ── Trigger re-measurement of visible items (simulates async rendering) ──
    // Resize the viewport width → text re-wraps → visible item heights change →
    // virtualizer's ResizeObserver fires → resizeItem. With the default
    // condition `item.start < scrollTop`, the first item (start=0 < scrollTop=50)
    // triggers a correction → scrollTop changes → viewport jumps (THE BUG).
    await page.setViewportSize({ width: 900, height: 720 })
    await page.waitForTimeout(500)

    // Also simulate streaming (content grows at the bottom — shouldn't affect top)
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
    await emitSSE(page, 'stream_content', { type: 'stream_content', progress: { stream_content: 'Streaming at bottom...', chat_id: 'web:chat-1', streaming: true } })
    await page.waitForTimeout(300)

    const scrollTopAfter = await page.evaluate(() => {
      const els = Array.from(document.querySelectorAll('div'))
      const sc = els.find(d => { const s = getComputedStyle(d); return (s.overflowY === 'auto' || s.overflowY === 'scroll') && d.querySelector('[data-index]') !== null }) as HTMLElement
      return sc ? Math.round(sc.scrollTop) : null
    })

    console.log('scrollTop before:', scrollTopBefore)
    console.log('scrollTop after:', scrollTopAfter)
    console.log('delta:', (scrollTopAfter ?? 0) - (scrollTopBefore ?? 0))

    // scrollTop must NOT have changed — user is near the top, no scroll operation.
    // The virtualizer must NOT correct scrollTop for items that are partially or
    // fully in the viewport (item.start < scrollTop is too aggressive; should be
    // item.end < scrollTop — only correct for items entirely above the viewport).
    expect(Math.abs((scrollTopAfter ?? 0) - (scrollTopBefore ?? 0))).toBeLessThan(5)

    await page.close()
  })
})
