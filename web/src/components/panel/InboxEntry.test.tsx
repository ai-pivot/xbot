/**
 * InboxEntry —— 侧栏「收件箱」入口的可见性与取数 scope（N1 验收第 ① 条）。
 *
 * 断言行为而非快照：
 * - unanswered=0 → **不渲染**角标（0 是常态，常驻一个「0」是噪音）；
 * - unanswered=3 → 渲染数字 3；
 * - 只读判据（口径锁死）：本会话有 pending → 徽标出现；只有**别的会话**有 pending
 *   → 徽标不出现。防止有人把取数改回全账号/默认会话（那样徽标会恒 0 或串会话）。
 */
import { render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { getInboxState, resetInboxStore } from '@/structured/inboxStore'
import { InboxEntry } from './InboxEntry'

vi.mock('sonner', () => ({ toast: vi.fn() }))
// 当前会话 = chat_A（真实环境里来自 useSessionStore().activeSession）。
vi.mock('@/hooks/useSessionStore', () => ({
  useSessionStore: () => ({ activeSession: { channel: 'web', chatID: 'chat_A' } }),
}))

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })
}

function cards(n: number) {
  return Array.from({ length: n }, (_, i) => ({
    message_id: i + 1,
    kind: 'report',
    direction: 'out',
    answered: false,
    cancelled: false,
    text: `card ${i + 1}`,
    sender: { type: 'agent', id: 'proj.xbot.dev' },
    created_at: '2026-10-30T00:00:00Z',
  }))
}

/** 服务端按 chat_id 分账：perChat 决定"每个会话各有几条 pending"。 */
function stubInbox(perChat: Record<string, number>) {
  const calls: string[] = []
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    calls.push(url)
    const chat = new URL(url, 'http://localhost').searchParams.get('chat_id') ?? ''
    const n = perChat[chat] ?? 0
    return json({ ok: true, data: { items: cards(n), has_more: false, next_cursor: 0, unanswered: n }, error: null })
  })
  vi.stubGlobal('fetch', fetchMock)
  return { fetchMock, calls }
}

beforeEach(() => {
  resetInboxStore()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('InboxEntry badge', () => {
  it('unanswered=0 时不渲染角标', async () => {
    const { fetchMock } = stubInbox({ chat_A: 0 })
    render(<InboxEntry />)

    await waitFor(() => expect(fetchMock).toHaveBeenCalled())
    // 等状态真的落下来（loading=false）再判定「没有角标」，
    // 否则断言可能只是「还没拉到数据」。
    await waitFor(() => expect(getInboxState().loading).toBe(false))
    expect(getInboxState().unanswered).toBe(0)
    expect(screen.queryByTestId('inbox-entry-badge')).toBeNull()
  })

  it('unanswered=3 时角标渲染 3', async () => {
    stubInbox({ chat_A: 3 })
    render(<InboxEntry />)

    const badge = await screen.findByTestId('inbox-entry-badge')
    expect(badge.textContent).toBe('3')
  })

  it('只读判据：只有别的会话有 pending 时，本会话徽标不出现', async () => {
    // chat_A（当前会话）没有待处理；chat_B 有 2 条。
    const { calls } = stubInbox({ chat_A: 0, chat_B: 2 })
    render(<InboxEntry />)

    await waitFor(() => expect(getInboxState().loading).toBe(false))
    expect(screen.queryByTestId('inbox-entry-badge')).toBeNull()
    // 并且请求确实带着当前会话（而不是退化成「全账号汇总」）。
    expect(calls.some((u) => u.includes('chat_id=chat_A'))).toBe(true)
    expect(calls.some((u) => u.includes('chat_id=chat_B'))).toBe(false)
  })
})
