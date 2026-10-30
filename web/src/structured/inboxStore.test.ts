/**
 * inboxStore —— 用户看板的状态流转与幂等边界（N1 验收的三条行为）。
 *
 * 这些用例断言的是**行为**（角标数字、列表条数），不是快照：
 * ① unanswered=0 → 角标 0（组件层见 InboxEntry.test.tsx）；
 * ② resolve 成功 → 本地列表少一条、未处理数 -1；
 * ③ 同一 message_id 二次 resolve 返回 Duplicate=true → 未处理数不变。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { StructuredKind, StructuredMessage } from './types'

const { toastMock } = vi.hoisted(() => ({ toastMock: vi.fn() }))
vi.mock('sonner', () => ({ toast: toastMock }))

import { getInboxState, refreshInbox, resetInboxStore, resolveInboxCard } from './inboxStore'

function card(id: number, kind: StructuredKind = 'report'): StructuredMessage {
  return {
    message_id: id,
    kind,
    direction: 'out',
    answered: false,
    cancelled: false,
    text: `card ${id}`,
    sender: { type: 'agent', id: 'proj.xbot.dev' },
    created_at: '2026-10-30T00:00:00Z',
  }
}

type Route = (url: string, init?: RequestInit) => Response

/** 统一装一个按 URL 分派的 fetch（GET /api/inbox + POST /api/inbox/resolve）。 */
function stubFetch(route: Route) {
  const mock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => route(String(input), init))
  vi.stubGlobal('fetch', mock)
  return mock
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

function inboxResponse(items: StructuredMessage[], unanswered: number): Response {
  return json({ ok: true, data: { items, has_more: false, next_cursor: 0, unanswered }, error: null })
}

beforeEach(() => {
  resetInboxStore()
  toastMock.mockClear()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('refreshInbox', () => {
  it('把服务端 unanswered 直接当作角标数字（3 条待处理 → 3）', async () => {
    stubFetch(() => inboxResponse([card(1), card(2, 'ask_user'), card(3)], 3))

    await refreshInbox()

    expect(getInboxState().unanswered).toBe(3)
    expect(getInboxState().items.map((m) => m.message_id)).toEqual([1, 2, 3])
  })

  it('空收件箱 → 角标 0、列表空', async () => {
    stubFetch(() => inboxResponse([], 0))

    await refreshInbox()

    expect(getInboxState().unanswered).toBe(0)
    expect(getInboxState().items).toHaveLength(0)
  })

  it('首次刷新不弹提醒，之后新到的待处理项弹一次「去处理」toast', async () => {
    let items = [card(1)]
    stubFetch(() => inboxResponse(items, items.length))

    await refreshInbox()
    expect(toastMock).not.toHaveBeenCalled()

    items = [card(1), card(2)]
    await refreshInbox()

    expect(toastMock).toHaveBeenCalledTimes(1)
    const [message, opts] = toastMock.mock.calls[0] as [string, { action?: { label: string } }]
    expect(message).toContain('1 条新的待处理')
    expect(opts.action?.label).toBe('去处理')
  })
})

describe('resolveInboxCard', () => {
  it('② resolve 成功：本地列表少一条、未处理数 -1', async () => {
    stubFetch((url) => {
      if (url.startsWith('/api/inbox/resolve')) {
        // 真实契约：结果在 data.result（见 channel/web/web_structured.go handleInboxResolve）
        return json({ ok: true, data: { result: { message_id: 1, duplicate: false } }, error: null })
      }
      return inboxResponse([card(1), card(2), card(3)], 3)
    })
    await refreshInbox()
    expect(getInboxState().unanswered).toBe(3)

    const outcome = await resolveInboxCard(1, 'approve')

    expect(outcome).toEqual({ duplicate: false, removed: true })
    expect(getInboxState().items.map((m) => m.message_id)).toEqual([2, 3])
    expect(getInboxState().unanswered).toBe(2)
  })

  it('③ 同一 message_id 二次 resolve 返回 Duplicate → 未处理数不变', async () => {
    let resolveCalls = 0
    stubFetch((url) => {
      if (url.startsWith('/api/inbox/resolve')) {
        resolveCalls += 1
        // 服务端语义：首次 Created=true → duplicate=false；重放命中幂等 → duplicate=true。
        return json({ ok: true, data: { result: { message_id: 1, duplicate: resolveCalls > 1 } }, error: null })
      }
      return inboxResponse([card(1), card(2), card(3)], 3)
    })
    await refreshInbox()

    await resolveInboxCard(1, 'approve')
    expect(getInboxState().unanswered).toBe(2)

    const second = await resolveInboxCard(1, 'approve')

    expect(second.duplicate).toBe(true)
    // 关键：重复提交不能把角标再减一次（否则刷新/双击会把数字减成负数）。
    expect(getInboxState().unanswered).toBe(2)
    expect(getInboxState().items.map((m) => m.message_id)).toEqual([2, 3])
  })

  it('409（已处理但换了幂等键）→ 本地收敛但不扣数字', async () => {
    stubFetch((url) => {
      if (url.startsWith('/api/inbox/resolve')) {
        return json({ ok: false, data: null, error: { code: 'conflict', message: 'already answered' } }, 409)
      }
      return inboxResponse([card(1), card(2)], 2)
    })
    await refreshInbox()

    const outcome = await resolveInboxCard(1, 'reject')

    expect(outcome.duplicate).toBe(true)
    expect(getInboxState().unanswered).toBe(2)
    expect(getInboxState().items.map((m) => m.message_id)).toEqual([2])
  })

  it('resolve 失败（500）→ 条目留在看板上、未处理数不变', async () => {
    stubFetch((url) => {
      if (url.startsWith('/api/inbox/resolve')) {
        return json({ ok: false, data: null, error: { code: 'internal_error', message: 'boom' } }, 500)
      }
      return inboxResponse([card(1)], 1)
    })
    await refreshInbox()

    await expect(resolveInboxCard(1, 'approve')).rejects.toThrow()

    expect(getInboxState().unanswered).toBe(1)
    expect(getInboxState().items.map((m) => m.message_id)).toEqual([1])
  })
})
