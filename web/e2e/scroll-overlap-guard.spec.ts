import { test, expect, type Page } from '@playwright/test'

/**
 * 回归守护（用户报告 2026-09-16）：**快速滚动 + 流式 turn 时内容重叠**。
 *
 * 根因：虚拟行（`.virt-row`）靠**内联 `transform: translateY(item.start)`** 定位，
 * 而同一元素上还挂了入场动画 `animate-msg-in`（`@keyframes msgIn` 里 from/to 都写
 * 了 `transform`）—— **CSS 动画会覆盖内联 transform** ⇒ 动画期间该行被画到容器原点
 * （压在上一行上）⇒ 视觉重叠；快速滚动时行不断卸载/重挂 ⇒ 动画反复重放 ⇒ 抖动重叠。
 *
 * 契约：**加在虚拟行上的动画/过渡绝不允许改 `transform`**（它归 virtualizer 所有）；
 * 需要位移入场效果就挂到行内层元素上。
 *
 * 本用例：mock 一个流式长 turn（边推迭代边快速上下滚动），断言**同级兄弟行/块的矩形
 * 互不相交**（允许 2px 容差）。
 */
const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

let seq = 0
async function emitSSE(page: Page, type: string, data: Record<string, unknown>) {
  await page.evaluate(
    ({ type, data, seq }) => {
      const w = window as unknown as SSEMockState
      const handlers = w.__sseListeners?.[type]
      if (!handlers) return
      const ev = new MessageEvent(type, { data: JSON.stringify({ ...data, seq }) })
      handlers.forEach((h) => h(ev))
    },
    { type, data, seq: ++seq },
  )
}

async function setupMock(page: Page) {
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
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

async function login(page: Page) {
  await page.addInitScript(() => {
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
    ;(window as unknown as { __sseListeners: typeof listeners }).__sseListeners = listeners
    class MockEventSource {
      readyState = 1
      onopen: ((ev: Event) => void) | null = null
      onerror: ((ev: Event) => void) | null = null
      constructor(public url: string) { setTimeout(() => this.onopen?.(new Event('open')), 0) }
      addEventListener(t: string, h: (ev: MessageEvent) => void) {
        if (!listeners[t]) listeners[t] = new Set()
        listeners[t].add(h)
      }
      removeEventListener() {}
      close() {}
    }
    ;(window as unknown as { EventSource: unknown }).EventSource = MockEventSource
  })
  await setupMock(page)
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForTimeout(1500)
}

/** 页内快速滚动 + 逐帧检查【同级】元素重叠（排除父子嵌套误报）。 */
async function burstScrollAndSample(page: Page, rounds: number) {
  return page.evaluate(async (rounds) => {
    const findScroller = () => {
      let el = document.querySelector('[data-message-list-content]') as HTMLElement | null
      el = el?.parentElement ?? null
      while (el) {
        const oy = getComputedStyle(el).overflowY
        if (oy === 'auto' || oy === 'scroll') return el
        el = el.parentElement
      }
      return null
    }
    const sc = findScroller()
    if (!sc) return { findings: [] as string[], frames: 0, visibleFindings: [] as string[] }
    const out: string[] = []
    const visible: string[] = []
    let frames = 0
    for (let r = 0; r < rounds; r++) {
      const total = sc.scrollHeight - sc.clientHeight
      for (let i = 0; i < 40; i++) {
        sc.scrollTop = Math.round(total * (r % 2 === 0 ? i / 40 : 1 - i / 40))
        await new Promise((res) => requestAnimationFrame(() => res(null)))
        frames++
        const groups: HTMLElement[][] = [Array.from(document.querySelectorAll('.virt-row')) as HTMLElement[]]
        for (const cont of Array.from(document.querySelectorAll('.iter-blocks')) as HTMLElement[]) {
          groups.push(Array.from(cont.children).filter((c) => (c as HTMLElement).classList.contains('iter-block')) as HTMLElement[])
        }
        const rects: Array<{ cls: string; top: number; bottom: number; h: number; group: number; text: string }> = []
        groups.forEach((g, gi) => {
          for (const el of g) {
            const rc = el.getBoundingClientRect()
            if (rc.height <= 0) continue
            rects.push({
              cls: el.className.split(' ')[0],
              top: Math.round(rc.top),
              bottom: Math.round(rc.bottom),
              h: Math.round(rc.height),
              group: gi,
              text: (el.textContent ?? '').slice(0, 18),
            })
          }
        })
        rects.sort((a, b) => a.group - b.group || a.top - b.top)
        const viewTop = sc.getBoundingClientRect().top
        for (let k = 1; k < rects.length; k++) {
          const a = rects[k - 1]
          const b = rects[k]
          if (a.group !== b.group) continue
          if (b.top < a.bottom - 2) {
            const msg = `${a.cls}(h=${a.h},b=${a.bottom},"${a.text}") ~ ${b.cls}(h=${b.h},t=${b.top},"${b.text}")`
            out.push(msg)
            // 只统计**视口内可见**的重叠（用户能看到的那种）
            if (a.bottom > viewTop && b.top < viewTop + sc.clientHeight) visible.push(msg)
            if (out.length > 40) return { findings: out, frames, visibleFindings: visible }
          }
        }
      }
    }
    return { findings: out, frames, visibleFindings: visible }
  }, rounds)
}

test('快速滚动 + 流式 turn：同级行/块不得重叠', async ({ browser }) => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  await login(page)

  await emitSSE(page, 'session', { type: 'session', session: { action: 'busy', chat_id: 'chat-1', channel: 'web' } })
  await emitSSE(page, 'progress_structured', {
    type: 'progress_structured',
    progress: {
      phase: 'turn_started',
      turn_id: 1,
      chat_id: 'web:chat-1',
      turn_start: { trigger: 'user', request_id: 'r1', content: 'long task' },
    },
  })

  const visibleFindings: string[] = []
  for (let i = 1; i <= 60 && visibleFindings.length === 0; i++) {
    const it = {
      iteration: i,
      thinking: `thought ${i}`,
      content: Array.from({ length: 18 }, (_, k) => `line ${k} of iter ${i} — lorem ipsum dolor sit amet, consectetur adipiscing elit.`).join('\n\n'),
      completed_tools: [{ name: 'Shell', status: 'done', summary: `cmd ${i}`, label: `cmd ${i}` }],
    }
    await emitSSE(page, 'progress_structured', {
      type: 'progress_structured',
      progress: {
        phase: 'tool_exec',
        turn_id: 1,
        iteration: i,
        chat_id: 'web:chat-1',
        active_tools: [],
        completed_tools: it.completed_tools,
        content: it.content,
        reasoning: it.thinking,
        iteration_history: i > 1 ? [{ ...it, iteration: i - 1 }] : [],
      },
    })
    if (i % 4 === 0) {
      const res = await burstScrollAndSample(page, 2)
      visibleFindings.push(...res.visibleFindings)
    }
  }

  expect(visibleFindings, `视口内出现重叠：\n${visibleFindings.slice(0, 5).join('\n')}`).toEqual([])
  await page.close()
})
