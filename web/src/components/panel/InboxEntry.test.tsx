/**
 * InboxEntry —— 侧栏「收件箱」入口的可见性（N1 验收第 ① 条）。
 *
 * 断言行为而非快照：
 * - unanswered=0 → **不渲染**角标（0 是常态，常驻一个「0」是噪音）；
 * - unanswered=3 → 渲染数字 3。
 */
import { render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { getInboxState, resetInboxStore } from '@/structured/inboxStore'
import { InboxEntry } from './InboxEntry'

vi.mock('sonner', () => ({ toast: vi.fn() }))

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })
}

function stubInbox(unanswered: number) {
  const items = Array.from({ length: unanswered }, (_, i) => ({
    message_id: i + 1,
    kind: 'report',
    direction: 'out',
    answered: false,
    cancelled: false,
    text: `card ${i + 1}`,
    sender: { type: 'agent', id: 'proj.xbot.dev' },
    created_at: '2026-10-30T00:00:00Z',
  }))
  const fetchMock = vi.fn(async () =>
    json({ ok: true, data: { items, has_more: false, next_cursor: 0, unanswered }, error: null }),
  )
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

beforeEach(() => {
  resetInboxStore()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('InboxEntry badge', () => {
  it('unanswered=0 时不渲染角标', async () => {
    const fetchMock = stubInbox(0)
    render(<InboxEntry />)

    await waitFor(() => expect(fetchMock).toHaveBeenCalled())
    // 等状态真的落下来（loading=false）再判定「没有角标」，
    // 否则断言可能只是「还没拉到数据」。
    await waitFor(() => expect(getInboxState().loading).toBe(false))
    expect(getInboxState().unanswered).toBe(0)
    expect(screen.queryByTestId('inbox-entry-badge')).toBeNull()
  })

  it('unanswered=3 时角标渲染 3', async () => {
    stubInbox(3)
    render(<InboxEntry />)

    const badge = await screen.findByTestId('inbox-entry-badge')
    expect(badge.textContent).toBe('3')
  })
})
