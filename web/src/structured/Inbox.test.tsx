/**
 * Inbox（待处理看板）—— 可见性与状态流转（N1 验收第 ①②③ 条的界面侧）。
 *
 * 断言行为：
 * - 逐条显示 kind / 摘要 / 时间；
 * - 展开后点「准」→ POST /api/inbox/resolve（action=approve、带 message_id 与幂等键）
 *   → 该条从列表消失、角标 -1；
 * - Duplicate=true（服务端早已处理）→ 只清残留，角标不再减；
 * - 取数必须带当前会话 chat_id（服务端按会话解析 tenant，不带就读到默认会话那份）。
 */
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { StructuredMessage } from './types'

vi.mock('sonner', () => ({ toast: vi.fn() }))

import { Inbox } from './Inbox'
import { getInboxState, resetInboxStore } from './inboxStore'

const SCOPE = { channel: 'web', chat_id: 'chat_test' }

function card(id: number, kind: 'report' | 'ask_user' = 'report', text = `摘要 ${id}`): StructuredMessage {
  return {
    message_id: id,
    kind,
    direction: 'out',
    answered: false,
    cancelled: false,
    text,
    // ask_user 卡片必须带 payload.questions，否则 StructuredCard 按契约渲染 null
    // （见 StructuredCards.tsx：没有 questions 就不是一张提问卡）。
    ...(kind === 'ask_user' ? { payload: { questions: [{ id: 'q1', question: text }] } } : {}),
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
      // 真实契约：结果在 data.result（见 channel/web/web_structured.go handleInboxResolve）
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

function renderInbox() {
  return render(
    <Inbox
      scope={SCOPE}
      onAnswer={async () => {}}
      onDecide={async () => {}}
    />,
  )
}

/** 点条目的摘要按钮展开（动作按钮只出现在展开区，保持「点条目=展开」的语义）。 */
async function expandRow(row: HTMLElement) {
  fireEvent.click(within(row).getAllByRole('button')[0])
  await waitFor(() => expect(within(row).getByTestId('inbox-approve')).toBeTruthy())
}

beforeEach(() => {
  resetInboxStore()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('Inbox board', () => {
  it('逐条列出 kind / 摘要 / 时间，且请求带上当前会话 chat_id', async () => {
    const { calls } = stubFetch(
      [card(1, 'report', '部署完成'), card(2, 'ask_user', '要继续吗')],
      () => ({ message_id: 1, duplicate: false }),
    )
    renderInbox()

    const rows = await screen.findAllByTestId('inbox-item')
    expect(rows).toHaveLength(2)
    expect(within(rows[0]).getByText('汇报')).toBeTruthy()
    expect(within(rows[0]).getByText('部署完成')).toBeTruthy()
    expect(within(rows[0]).getByText('刚刚')).toBeTruthy()
    expect(within(rows[1]).getByText('提问')).toBeTruthy()
    expect(within(rows[1]).getByText('要继续吗')).toBeTruthy()
    expect(screen.getByTestId('inbox-badge').textContent).toBe('2')
    // 只读判据：取数路径必须带 chat_id（不带会读到默认会话那份，徽标恒 0）。
    const get = calls.find((c) => c.url.startsWith('/api/inbox?'))
    expect(get?.url).toContain('chat_id=chat_test')
  })

  it('② 展开后点「准」→ 调 /api/inbox/resolve → 该条消失、角标 -1', async () => {
    const { calls } = stubFetch([card(1), card(2), card(3)], () => ({ message_id: 1, duplicate: false }))
    renderInbox()

    const rows = await screen.findAllByTestId('inbox-item')
    expect(rows).toHaveLength(3)
    await expandRow(rows[0])
    fireEvent.click(within(rows[0]).getByTestId('inbox-approve'))

    await waitFor(() => expect(screen.getAllByTestId('inbox-item')).toHaveLength(2))
    expect(screen.getByTestId('inbox-badge').textContent).toBe('2')

    const post = calls.find((c) => c.url.startsWith('/api/inbox/resolve'))
    expect(post?.body).toMatchObject({ message_id: 1, action: 'approve', chat_id: 'chat_test' })
    expect(typeof post?.body?.idempotency_key).toBe('string')
  })

  it('③ Duplicate=true（重复点）→ 条目被清掉但角标不再减', async () => {
    stubFetch([card(1), card(2)], () => ({ message_id: 1, duplicate: true }))
    renderInbox()

    const rows = await screen.findAllByTestId('inbox-item')
    expect(screen.getByTestId('inbox-badge').textContent).toBe('2')
    await expandRow(rows[0])
    fireEvent.click(within(rows[0]).getByTestId('inbox-approve'))

    await waitFor(() => expect(screen.getAllByTestId('inbox-item')).toHaveLength(1))
    expect(screen.getByTestId('inbox-badge').textContent).toBe('2')
    expect(getInboxState().unanswered).toBe(2)
  })

  it('ask_user 行可以填内容后「回答」（action=answer 带 comment）', async () => {
    const { calls } = stubFetch([card(7, 'ask_user', '要继续吗')], () => ({ message_id: 7, duplicate: false }))
    renderInbox()

    const row = (await screen.findAllByTestId('inbox-item'))[0]
    await expandRow(row)
    fireEvent.change(within(row).getByTestId('inbox-answer-input'), { target: { value: '继续' } })
    fireEvent.click(within(row).getByTestId('inbox-answer'))

    await waitFor(() => expect(screen.queryAllByTestId('inbox-item')).toHaveLength(0))
    const post = calls.find((c) => c.url.startsWith('/api/inbox/resolve'))
    expect(post?.body).toMatchObject({ message_id: 7, action: 'answer', comment: '继续' })
  })

  it('展开后保留原卡片（ask-user-card），不吞掉既有详情交互', async () => {
    stubFetch([card(9, 'ask_user', '要继续吗')], () => ({ message_id: 9, duplicate: false }))
    renderInbox()

    const row = (await screen.findAllByTestId('inbox-item'))[0]
    await expandRow(row)
    expect(within(row).getByTestId('ask-user-card')).toBeTruthy()
  })
})
