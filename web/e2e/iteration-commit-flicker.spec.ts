import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E test: iteration completion must not flicker.
 *
 * User report: "每次新的 iter 完成都要闪烁一下" — at the exact moment an
 * iteration is appended to iterationHistory (live → committed transfer), the
 * rendered content visually jumps/blinks. Also "iter padding 有几种，有时
 * 高一点有时低一点" — inconsistent vertical rhythm between iterations.
 *
 * Detection: an rAF loop snapshots the agent row height, the live area
 * height, and EVERY [data-iter-id] block height on each frame, across the
 * iteration-completion event. A flicker manifests as
 *   (a) a dip frame: row height drops below BOTH neighbours, then recovers
 *       (content disappears for one frame and comes back), or
 *   (b) a blank frame: the iteration's content is in NEITHER the committed
 *       blocks NOR the live area while it was visible before.
 */

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

let seqCounter = 0

async function emitSSE(page: Page, type: string, data: Record<string, unknown>) {
  await page.evaluate(({ type, data, seq }) => {
    const w = window as unknown as SSEMockState
    const handlers = w.__sseListeners?.[type] as Set<(ev: MessageEvent) => void> | undefined
    if (!handlers) return
    const ev = new MessageEvent(type, { data: JSON.stringify({ ...data, seq }) })
    handlers.forEach((h) => h(ev))
  }, { type, data, seq: ++seqCounter })
}

async function setupMock(page: Page) {
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) => r.fulfill({
    json: { ok: true, data: {
      sessions: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() }],
      chats: [], orphan_subagents: [],
    } },
  }))
  await page.route('**/api/history', (r) => r.fulfill({
    json: { ok: true, data: { messages: [], chat_id: 'chat-1', last_seq: 0, active_progress: null } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

/** Install the per-frame layout recorder (idempotent; reads the frames array
 *  from window every frame so resetting __frames works). */
async function startFrameRecorder(page: Page) {
  await page.evaluate(() => {
    if ((window as unknown as { __framesRec?: boolean }).__framesRec) return
    ;(window as unknown as { __framesRec?: boolean }).__framesRec = true
    const rec = () => {
      const w = window as unknown as { __frames?: Frame[] }
      if (!w.__frames) w.__frames = []
      const live = document.querySelector('[data-iter-id="live"]')
      const committed: Record<string, number> = {}
      document.querySelectorAll('[data-iter-id]').forEach((el) => {
        const id = el.getAttribute('data-iter-id') || ''
        if (id === 'live') return
        committed[id] = Math.round(el.getBoundingClientRect().height)
      })
      const rows = document.querySelectorAll('[data-message-id]')
      const row = rows[rows.length - 1]
      w.__frames.push({
        t: Math.round(performance.now()),
        live: live ? Math.round(live.getBoundingClientRect().height) : -1,
        committed,
        rowH: row ? Math.round(row.getBoundingClientRect().height) : -1,
      })
      requestAnimationFrame(rec)
    }
    requestAnimationFrame(rec)
  })
}

interface Frame {
  t: number
  live: number
  committed: Record<string, number>
  rowH: number
}

async function resetFrames(page: Page) {
  await page.evaluate(() => { (window as unknown as { __frames?: Frame[] }).__frames = [] })
}

async function collectFrames(page: Page, ms = 600): Promise<Frame[]> {
  await page.waitForTimeout(ms)
  return page.evaluate(() => (window as unknown as { __frames: Frame[] }).__frames.slice())
}

test.describe('Iteration completion flicker', () => {
  test.beforeEach(() => { seqCounter = 0 })

  test('iter completion must not dip or blank content (reasoning + tools scenario)', async ({ browser }) => {
    const page = await browser.newPage()

    await page.addInitScript(() => {
      const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
      ;(window as unknown as { __sseListeners: typeof listeners }).__sseListeners = listeners
      class M { readyState=1; onopen:((e:Event)=>void)|null=null; onerror:((e:Event)=>void)|null=null; constructor(public url:string){setTimeout(()=>this.onopen?.(new Event('open')),0)} addEventListener(t:string,h:(ev:MessageEvent)=>void){if(!listeners[t])listeners[t]=new Set();listeners[t].add(h)} removeEventListener(){} close(){} }
      ;(window as unknown as { EventSource: typeof M }).EventSource = M
    })

    await setupMock(page)
    await page.goto(`${BASE}/login`)
    await page.locator('input').first().fill('test')
    await page.locator('input[type="password"]').fill('test')
    await page.locator('button[type="submit"]').click()
    await page.waitForTimeout(2000)

    const structured = (p: Record<string, unknown>) =>
      emitSSE(page, 'progress_structured', { type: 'progress_structured', progress: { chat_id: 'web:chat-1', ...p } })
    const stream = (p: Record<string, unknown>) =>
      emitSSE(page, 'stream_content', { type: 'stream_content', progress: { chat_id: 'web:chat-1', ...p } })

    // ── Turn: iteration 1 with reasoning + tool, iteration 2 same ──
    await structured({ phase: 'turn_started', turn_id: 1, turn_start: { trigger: 'notification', content: 'run tests' } })
    await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })

    // Iteration 1: reasoning streams, then a tool runs
    await stream({ reasoning_stream_content: 'Analyzing the repository structure to find the main entry point of the application.', streaming: true })
    await page.waitForTimeout(200)
    await structured({ phase: 'tool_exec', iteration: 1, seq: 2, turn_id: 1,
      active_tools: [{ name: 'Read', status: 'running', iteration: 1 }] })
    await page.waitForTimeout(300)

    // ── FLICKER MOMENT 1: iteration 1 completes — REAL backend sequence ──
    // The backend is TWO events, not one atomic update:
    //   Event A (snapshotCompletedIteration → notify): iteration STAYS at N,
    //   iteration_history appends iter N, completed_tools carries the tools.
    //   The live area's reasoningStreamContent is NOT cleared yet (the clear
    //   only happens when the iteration ADVANCES) → iter 1's reasoning renders
    //   BOTH in the committed fold AND in the live fold (double render).
    //   Event B (beginIteration N+1): iteration advances → live clears.
    await startFrameRecorder(page)
    await structured({ phase: 'tool_exec', iteration: 1, seq: 3, turn_id: 1,
      completed_tools: [{ name: 'Read', status: 'done', iteration: 1, summary: 'main.go' }],
      iteration_history: [{ iteration: 1,
        reasoning: 'Analyzing the repository structure to find the main entry point of the application.',
        completed_tools: [{ name: 'Read', status: 'done', iteration: 1, summary: 'main.go' }] }],
    })
    await page.waitForTimeout(250)
    // Double-render check: after the history-append event, the reasoning must
    // appear in the COMMITTED fold only — NOT again in the live area.
    const diag = await page.evaluate(() => ({
      liveHasReasoning: document.querySelector('[data-iter-id="live"]')?.textContent?.includes('Analyzing the repository') ?? false,
      bodyHasReasoning: (document.body.textContent || '').includes('Analyzing the repository'),
      iterIds: Array.from(document.querySelectorAll('[data-iter-id]')).map((e) => e.getAttribute('data-iter-id')),
      msgRows: document.querySelectorAll('[data-message-id]').length,
      bodyLen: (document.body.textContent || '').length,
    }))
    console.log('DIAG:', JSON.stringify(diag))
    test.info().annotations.push({ type: 'diagn', description: JSON.stringify(diag) })
    // Event B: next iteration begins — the live area finally clears.
    await structured({ phase: 'thinking', iteration: 2, seq: 4, turn_id: 1 })
    const frames1 = await collectFrames(page)

    // Iteration 2: reasoning streams, then a tool runs
    await stream({ reasoning_stream_content: 'Now searching for configuration loading logic in the codebase.', streaming: true })
    await page.waitForTimeout(200)
    await structured({ phase: 'tool_exec', iteration: 2, seq: 4, turn_id: 1,
      active_tools: [{ name: 'Grep', status: 'running', iteration: 2 }] })
    await page.waitForTimeout(300)

    // ── FLICKER MOMENT 2: iteration 2 completes — same two-event sequence ──
    await resetFrames(page)
    await structured({ phase: 'tool_exec', iteration: 2, seq: 5, turn_id: 1,
      completed_tools: [{ name: 'Grep', status: 'done', iteration: 2, summary: 'x' }],
      iteration_history: [
        { iteration: 1,
          reasoning: 'Analyzing the repository structure to find the main entry point of the application.',
          completed_tools: [{ name: 'Read', status: 'done', iteration: 1, summary: 'main.go' }] },
        { iteration: 2,
          reasoning: 'Now searching for configuration loading logic in the codebase.',
          completed_tools: [{ name: 'Grep', status: 'done', iteration: 2, summary: 'x' }] },
      ],
    })
    await page.waitForTimeout(250)
    await structured({ phase: 'thinking', iteration: 3, seq: 6, turn_id: 1 })
    const frames2 = await collectFrames(page)

    // ── Analysis ──
    const dips = (frames: Frame[]) => {
      const out: string[] = []
      const h = frames.map((f) => f.rowH)
      for (let i = 2; i < h.length - 2; i++) {
        const before = Math.max(h[i - 2], h[i - 1])
        const after = Math.max(h[i + 1], h[i + 2])
        if (h[i] <= before - 6 && h[i] <= after - 6) {
          out.push(`frame ${i}: ${h[i]} (before ${before}, after ${after})`)
        }
      }
      return out
    }
    /** Double-render spike: the committed fold AND the live fold render the
     *  SAME iteration content simultaneously (event A) until the live area
     *  clears (event B) — the row briefly grows then shrinks back. A spike
     *  frame is ≥8px ABOVE both neighbours. */
    const spikes = (frames: Frame[]) => {
      const out: string[] = []
      const h = frames.map((f) => f.rowH)
      for (let i = 3; i < h.length - 3; i++) {
        const before = Math.max(h[i - 3], h[i - 2], h[i - 1])
        const after = Math.max(h[i + 1], h[i + 2], h[i + 3])
        // Compare against the SMALLER plateau (the steady state after the
        // spike collapses) — spike must exceed the post-collapse level.
        const plateau = Math.min(before, after)
        if (h[i] >= plateau + 12) {
          out.push(`frame ${i}: ${h[i]} (plateau ${plateau})`)
        }
      }
      return out
    }
    /** Iteration content blank frames: committed height for the iter is 0
     *  after having been non-zero, OR live collapsed while committed hasn't
     *  appeared yet (transfer gap). */
    const blanks = (frames: Frame[], iterId: string) => {
      const out: string[] = []
      let seen = false
      for (let i = 0; i < frames.length; i++) {
        const f = frames[i]
        const h = Object.entries(f.committed)
          .filter(([id]) => id.split(',').includes(iterId))
          .reduce((s, [, v]) => s + v, 0)
        if (h > 0) seen = true
        if (seen && h === 0) out.push(`frame ${i} t=${f.t}: iter${iterId} committed=0 rowH=${f.rowH} live=${f.live}`)
      }
      return out
    }

    const report = [
      `M1 dips: ${dips(frames1).join(' | ') || 'none'}`,
      `M1 spikes: ${spikes(frames1).join(' | ') || 'none'}`,
      `M1 blanks(1): ${blanks(frames1, '1').join(' | ') || 'none'}`,
      `M2 dips: ${dips(frames2).join(' | ') || 'none'}`,
      `M2 spikes: ${spikes(frames2).join(' | ') || 'none'}`,
      `M2 blanks(2): ${blanks(frames2, '2').join(' | ') || 'none'}`,
      `M1 rowH: ${frames1.map((f) => f.rowH).join(',')}`,
      `M1 live: ${frames1.map((f) => f.live).join(',')}`,
      `M1 committed: ${frames1.map((f) => JSON.stringify(f.committed)).join(' ; ')}`,
      `M2 rowH: ${frames2.map((f) => f.rowH).join(',')}`,
      `M2 live: ${frames2.map((f) => f.live).join(',')}`,
      `M2 committed: ${frames2.map((f) => JSON.stringify(f.committed)).join(' ; ')}`,
    ].join('\n')
    console.log(report)
    test.info().annotations.push({ type: 'report', description: report })

    expect(dips(frames1), `moment-1 height dips:\n${report}`).toHaveLength(0)
    expect(spikes(frames1), `moment-1 double-render spikes:\n${report}`).toHaveLength(0)
    expect(dips(frames2), `moment-2 height dips:\n${report}`).toHaveLength(0)
    expect(spikes(frames2), `moment-2 double-render spikes:\n${report}`).toHaveLength(0)
    expect(blanks(frames1, '1'), `moment-1 iter-1 blank frames:\n${report}`).toHaveLength(0)
    expect(blanks(frames2, '2'), `moment-2 iter-2 blank frames:\n${report}`).toHaveLength(0)
  })
})
