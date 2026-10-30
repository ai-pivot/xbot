/**
 * Inbox（待处理看板）—— 可见性与状态流转（N1 验收第 ①②③ 条的界面侧）。
 *
 * 断言行为：
 * - 逐条显示 kind / 摘要 / 时间；
 * - 点「准」→ POST /api/inbox/resolve（action=approve、带 message_id 与幂等键）
 *   → 该条从列表消失、角标 -1；
 * - Duplicate=true（服务端早已处理）→ 只清残留，角标不再减。
 */
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { StructuredMessage } from './types'

vi.mock('sonner', () => ({ toast: vi.fn() }))

import { Inbox } from './Inbox'
import { getInboxState, resetInboxStore } from './inboxStore'

function card(id: number, kind: 'report' | 'ask_user' = 'report', text = `摘要 ${id}`): StructuredMessage {
  return {
    message_id: id,
    kind,
    direction: 'out',
    answered: false,
    cancelled: false,
    text,
    sender: { type: 'agent', id: 'proj.xbot.dev', name: 'proj.xbot.dev' },
    created_at: new Date().toISOString(),
  }
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

interface Call {
  url: string
  body: Record<string, unknown> | null
}

function stubFetch(items: StructuredMessage[], resolveBody: (n: number) => unknown) {
  const calls: Call[] = []
  let resolveCalls = 0
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const body = init?.body ? (JSON.parse(String(init.body)) as Record<string, unknown>) : null
    calls.push({ url, body })
    if (url.startsWith('/api/inbox/resolve')) {
      resolveCalls += 1
      // 真实契约：data.result 包一层
      return json({ ok: true, data: { result: resolveBody(resolveCalls) }, error: null })
    }
    return json({
      ok: true,
      data: { items, has_more: false, next_cursor: 0, unanswered: items.length },
      error: null,
    })
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

describe('Inbox board', () => {
  it('逐条列出 kind / 摘要 / 时间', async () => {
    stubFetch([card(1, 'report', '部署完成'), card(2, 'ask_user', '要继续吗')], () => ({ message_id: 1, duplicate: false }))
    render(<Inbox />)

    const rows = await screen.findAllByTestId('inbox-item')
    expect(rows).toHaveLength(2)
    expect(within(rows[0]).getByText('汇报')).toBeTruthy()
    expect(within(rows[0]).getByText('部署完成')).toBeTruthy()
    expect(within(rows[0]).getByText('刚刚')).toBeTruthy()
    expect(within(rows[1]).getByText('提问')).toBeTruthy()
    expect(within(rows[1]).getByText('要继续吗')).toBeTruthy()
    expect(screen.getByTestId('inbox-badge').textContent).toBe('2')
  })

  it('② 点「准」→ 调 /api/inbox/resolve → 该条消失、角标 -1', async () => {
    const { calls } = stubFetch([card(1), card(2), card(3)], () => ({ message_id: 1, duplicate: false }))
    render(<Inbox />)

    const rows = await screen.findAllByTestId('inbox-item')
    expect(rows).toHaveLength(3)

    fireEvent.click(within(rows[0]).getByTestId('inbox-approve'))

    await waitFor(() => expect(screen.getAllByTestId('inbox-item')).toHaveLength(2))
    expect(screen.getByTestId('inbox-badge').textContent).toBe('2')

    const post = calls.find((c) => c.url.startsWith('/api/inbox/resolve'))
    expect(post?.body).toMatchObject({ message_id: 1, action: 'approve' })
    expect(typeof post?.body?.idempotency_key).toBe('string')
  })

  it('③ Duplicate=true（重复点）→ 条目被清掉但角标不再减', async () => {
    stubFetch([card(1), card(2)], () => ({ message_id: 1, duplicate: true }))
    render(<Inbox />)

    const rows = await screen.findAllByTestId('inbox-item')
    expect(screen.getByTestId('inbox-badge').textContent).toBe('2')

    fireEvent.click(within(rows[0]).getByTestId('inbox-approve'))

    await waitFor(() => expect(screen.getAllByTestId('inbox-item')).toHaveLength(1))
    expect(screen.getByTestId('inbox-badge').textContent).toBe('2')
    expect(getInboxState().unanswered).toBe(2)
  })

  it('ask_user 行可以填内容后「回答」（action=answer 带 comment）', async () => {
    const { calls } = stubFetch([card(7, 'ask_user', '要继续吗')], () => ({ message_id: 7, duplicate: false }))
    render(<Inbox />)

    const row = (await screen.findAllByTestId('inbox-item'))[0]
    fireEvent.change(within(row).getByTestId('inbox-answer-input'), { target: { value: '继续' } })
    fireEvent.click(within(row).getByTestId('inbox-answer'))

    await waitFor(() => expect(screen.queryAllByTestId('inbox-item')).toHaveLength(0))
    const post = calls.find((c) => c.url.startsWith('/api/inbox/resolve'))
    expect(post?.body).toMatchObject({ message_id: 7, action: 'answer', comment: '继续' })
  })
})
