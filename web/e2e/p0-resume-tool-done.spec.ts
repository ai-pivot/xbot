/**
 * P0 复现（2026-10-02 03:00 用户报告）：「历史迭代完全不更新，新迭代一 commit 就消失。
 * 我用的就是最新前端。」
 *
 * 用户场景（chat_D3D036023DB7，turn 18 live，50 迭代在跑）：
 *   1. 切走/熄屏一段时间 → 切回来 → fetchHistory → history_replaced 水合 live turn
 *   2. SSE 推送新 tool（activeTools 渲染 = 用户看到）
 *   3. tool 执行完毕 → iteration 事件（delta 携带完成迭代）→ **应进历史渲染**
 *   4. 期望：完成的迭代（含 done tool pill）出现在历史中
 *   现象：完成的迭代消失（activeTools 清空后 completed 不渲染）——历史冻结
 *
 * 本 spec 复刻完整链路（REST mock + SSE emit），在真实浏览器中断点定位。
 */
import { test, expect, type Page } from '@playwright/test'

const BASE = 'http://localhost:5199'

/** SSE mock listeners（askuser-empty-payload.spec.ts 的同款模式）。 */
async function emitSSE(page: Page, type: string, data: Record<string, unknown>): Promise<void> {
  await page.evaluate(
    ({ type, data }) => {
      const w = window as unknown as { __sseListeners?: Record<string, Set<(ev: MessageEvent) => void>> }
      const handlers = w.__sseListeners?.[type]
      if (!handlers) return
      const ev = new MessageEvent(type, { data: JSON.stringify(data) })
      handlers.forEach((h) => h(ev))
    },
    { type, data },
  )
}

/** 轻字段迭代（折叠视图形态——REST 历史下发的 tools_folded=true 版本）。 */
function foldedIter(n: number, toolNames: string[]): Record<string, unknown> {
  return {
    iteration: n,
    content: '',
    reasoning: '',
    tools: toolNames.map((t) => ({ name: t, label: t, status: 'done', elapsed_ms: 100, iteration: n })),
    folded: true,
    tool_count: toolNames.length,
  }
}

/** 完整迭代（SSE 事件携带的形态——tools_folded 缺省 false）。 */
function fullIter(n: number, toolNames: string[], content = ''): Record<string, unknown> {
  return {
    iteration: n,
    content,
    reasoning: '',
    tools: toolNames.map((t) => ({
      name: t, label: t, status: 'done', elapsed_ms: 100, iteration: n, summary: 's', args: '{}', detail: 'd',
    })),
    tool_count: toolNames.length,
  }
}

/** 滚到列表底部（消息列表的虚拟滚动容器）。 */
async function scrollListToBottom(page: Page): Promise<void> {
  await page.evaluate(() => {
    const el = document.querySelector('[data-message-list-content]')
    const scrollRoot = el?.closest('.overflow-y-auto') ?? el?.parentElement
    if (scrollRoot) {
      scrollRoot.scrollTop = scrollRoot.scrollHeight
    }
    window.scrollTo(0, document.body.scrollHeight)
  })
  await page.waitForTimeout(500)
}

test('熄屏恢复 × 双 turn（758 迭代折叠窗口 + live）× tool done —— completed tool 不消失', async ({ browser }) => {
  const page = await (await browser.newContext({ viewport: { width: 900, height: 800 } })).newPage()

  // ── Mock 基建 ──
  await page.addInitScript(() => {
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
    ;(window as unknown as { __sseListeners: typeof listeners }).__sseListeners = listeners
    class MockEventSource {
      readyState = 1
      onopen: ((ev: Event) => void) | null = null
      onerror: ((ev: Event) => void) | null = null
      constructor(_url: string) {
        setTimeout(() => this.onopen?.(new Event('open')), 0)
      }
      addEventListener(t: string, h: (ev: MessageEvent) => void) {
        if (!listeners[t]) listeners[t] = new Set()
        listeners[t].add(h)
      }
      removeEventListener() {}
      close() {}
    }
    ;(window as unknown as { EventSource: typeof MockEventSource }).EventSource = MockEventSource
  })
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: {
          sessions: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() }],
          chats: [], orphan_subagents: [],
        },
      },
    }),
  )
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))

  // ── 双 turn：turn 17（758 迭代 → 折叠窗口 [659..758] + regions_before=658）+ turn 18（live 50 迭代）──
  const turn17Window = Array.from({ length: 100 }, (_, i) => foldedIter(659 + i, [`t17_${659 + i}`]))
  const turn18Iters = Array.from({ length: 49 }, (_, i) => foldedIter(i + 1, [`t18_${i + 1}`]))

  await page.route('**/api/history', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: {
          messages: [
            { id: 50, role: 'user', content: '继续修复', turn_id: 17, timestamp: '2026-10-02T02:00:00Z', dbID: 50 },
            {
              id: 51, role: 'assistant', content: 'turn 17 完成回复', turn_id: 17, dbID: 51,
              iterations: turn17Window,
              regions_before: 658,
            },
            { id: 100, role: 'user', content: '跑个大任务', turn_id: 18, timestamp: '2026-10-02T02:52:00Z', dbID: 100 },
            {
              id: 101, role: 'assistant', content: '', turn_id: 18, dbID: 101,
              iterations: turn18Iters,
              regions_before: 0,
            },
          ],
          chat_id: 'chat-1',
          channel: 'web',
          last_seq: 0,
          active_progress: {
            phase: 'tool_exec',
            iteration: 50,
            turn_id: 18,
            iteration_history: turn18Iters,
            active_tools: [{ name: 'Shell', label: 'Shell: npm test', status: 'running', iteration: 50 }],
            streaming_tools: [],
            todos: [],
          },
          has_more: false,
          oldest_id: 1,
        },
      },
    }),
  )
  await page.route('**/api/regions', (r) => r.fulfill({ json: { ok: true, data: { iterations: [], regions_before: 0 } } }))

  // ── 打开会话 → 滚到底部 ──
  await page.goto(`${BASE}/`)
  await expect(page.locator('[data-message-list-content]')).toBeAttached({ timeout: 15_000 })
  await page.waitForTimeout(1500)
  await scrollListToBottom(page)

  // ── 初始诊断 ──
  const diag = (_name: string) =>
    page.evaluate(() => {
      const live = document.querySelector('[data-iter-id="live"]')
      const all = document.querySelectorAll('[data-iter-range]')
      const ranges: string[] = []
      all.forEach((el) => ranges.push(el.getAttribute('data-iter-range') ?? ''))
      const pills = document.querySelectorAll('[data-testid="tool-pill"]').length
      let shell = 0
      document.querySelectorAll('[data-testid="tool-pill"]').forEach((p) => {
        if ((p as HTMLElement).textContent?.includes('Shell')) shell++
      })
      return { live: !!live, ranges, pills, shell }
    })
  const d0 = await diag('initial')
  console.log(`[initial] pills=${d0.pills} ranges=${d0.ranges.slice(-3).join('|')} live=${d0.live}`)

  // ── SSE：tool done → iteration 事件（iter=51 前进，delta=[迭代50 完整版 Shell]）──
  await emitSSE(page, 'progress_structured', {
    type: 'progress_structured',
    chat_id: 'chat-1',
    channel: 'web',
    seq: 60,
    progress: {
      phase: 'thinking',
      iteration: 51,
      turn_id: 18,
      seq: 60,
      iteration_history: [fullIter(50, ['Shell'])],
      active_tools: [],
      streaming_tools: [],
      todos: [],
    },
  })
  await page.waitForTimeout(1000)
  await scrollListToBottom(page)

  const d1 = await diag('after')
  console.log(`[after] pills=${d1.pills} ranges=${d1.ranges.slice(-3).join('|')} live=${d1.live}`)

  // ── dump 全部已挂载块的 id + pill 数 ──
  const allBlocks = await page.evaluate(() => {
    const out: { id: string; pills: number; texts: string[] }[] = []
    document.querySelectorAll('[data-iter-id]').forEach((el) => {
      const pills = el.querySelectorAll('[data-testid="tool-pill"]')
      const texts: string[] = []
      pills.forEach((p) => texts.push((p as HTMLElement).textContent?.trim() ?? ''))
      out.push({ id: el.getAttribute('data-iter-id') ?? '', pills: pills.length, texts })
    })
    return out
  })
  console.log('[after] all mounted iter blocks:', JSON.stringify(allBlocks.map((b) => `${b.id}(${b.pills})`).join(' ')))
  console.log('[after] block detail:', JSON.stringify(allBlocks, null, 2))

  // ── 断言：Shell pill（迭代 50 完成版）必须在 DOM 中 ──
  const shellPills = await page.locator('[data-testid="tool-pill"]', { hasText: 'Shell' }).count()
  console.log(`[after] Shell pills = ${shellPills}`)
  expect(shellPills, '完成的迭代 50 的 Shell pill 必须在历史中（不消失）').toBeGreaterThan(0)

  // ── 断言：turn 18 的 contiguous 扩展到 50 ──
  const turn18Range = d1.ranges.find((r) => r.startsWith('1-'))
  expect(turn18Range, 'turn 18 的 contiguous 范围').toBeTruthy()
})
