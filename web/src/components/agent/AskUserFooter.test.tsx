import '@testing-library/jest-dom/vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { AskUserFooter, belongsToSession, matchInboxRow, promptToStructuredMessage } from './AskUserFooter'
import type { AskUserPrompt } from '@/types/agent'
import type { InboxResult, StructuredMessage } from '@/structured/types'

/**
 * 2026-10-22 用户实测的两个问题：
 *   ① 换到别的会话还会弹上一个会话的 ask_user；
 *   ② 主视图**不准自动展开**面板 —— 默认只能是一条横幅，展开必须用户点击。
 *
 * 本测试锁住：会话隔离、默认收起、点击展开、收起回退、提交后收起。
 */

const PROMPT: AskUserPrompt = {
  requestId: 'req-live-1',
  questions: [{ question: '继续跑压测吗？', options: ['继续', '停'] }],
}

function makeStore(prompts: Map<string, AskUserPrompt>) {
  return {
    askUserPrompts: prompts,
    clearAskUserPrompt: vi.fn(),
    markAskUserAnswered: vi.fn(),
    hydrateAskUserPrompt: vi.fn(),
  }
}

const EMPTY_INBOX = async (): Promise<InboxResult> => ({
  items: [], has_more: false, next_cursor: 0, unanswered: 0,
})

function inboxWith(rows: StructuredMessage[]): typeof EMPTY_INBOX {
  return async () => ({ items: rows, has_more: false, next_cursor: 0, unanswered: rows.length })
}

function rowFor(correlationID: string, session = 'web:chat-1'): StructuredMessage {
  return {
    message_id: 7,
    kind: 'ask_user',
    correlation_id: correlationID,
    direction: 'in',
    answered: false,
    cancelled: false,
    text: '继续跑压测吗？',
    payload: {
      questions: [{
        id: 'q1',
        question: '继续跑压测吗？',
        options: [{ id: 'yes', label: '继续' }, { id: 'no', label: '停' }],
      }],
    },
    sender: { type: 'user', id: 'web-1' },
    created_at: new Date().toISOString(),
    envelope: { from: { type: 'user', id: 'web-1', session } },
  }
}

const ROW = rowFor('corr-7')

describe('AskUserFooter 会话隔离（①）', () => {
  it('store 里只有**别的会话**的 prompt ⇒ footer 与横幅都不渲染', async () => {
    const store = makeStore(new Map([['web:chat-A', PROMPT]]))
    render(<AskUserFooter store={store} channel="web" chatID="chat-B"
      loadInbox={EMPTY_INBOX} submit={vi.fn()} />)
    await waitFor(() => expect(store.clearAskUserPrompt).not.toHaveBeenCalled())
    expect(screen.queryByTestId('ask-user-banner')).toBeNull()
    expect(screen.queryByTestId('ask-user-card')).toBeNull()
    expect(screen.queryByTestId('ask-user-expanded')).toBeNull()
  })

  it('切回有 prompt 的会话 ⇒ 横幅出现', async () => {
    const store = makeStore(new Map([['web:chat-A', PROMPT]]))
    const { rerender } = render(<AskUserFooter store={store} channel="web" chatID="chat-B"
      loadInbox={EMPTY_INBOX} submit={vi.fn()} />)
    expect(screen.queryByTestId('ask-user-banner')).toBeNull()
    rerender(<AskUserFooter store={store} channel="web" chatID="chat-A"
      loadInbox={EMPTY_INBOX} submit={vi.fn()} />)
    expect(await screen.findByTestId('ask-user-banner')).toBeInTheDocument()
  })

  it('收件箱里属于**别的会话**的未答行被过滤掉（envelope.from.session 校验）', async () => {
    const store = makeStore(new Map())
    render(<AskUserFooter store={store} channel="web" chatID="chat-B"
      loadInbox={inboxWith([rowFor('corr-x', 'web:chat-A')])} submit={vi.fn()} />)
    await waitFor(() => expect(screen.queryByTestId('ask-user-banner')).toBeNull())
  })
})

describe('AskUserFooter 默认收起（②）', () => {
  it('当前会话有 prompt ⇒ 只渲染横幅，ask-user-card 不存在', async () => {
    const store = makeStore(new Map([['web:chat-1', PROMPT]]))
    render(<AskUserFooter store={store} channel="web" chatID="chat-1"
      loadInbox={inboxWith([ROW])} submit={vi.fn()} />)

    expect(await screen.findByTestId('ask-user-banner')).toBeInTheDocument()
    // 单条提问：横幅必须直接说出「谁、问什么」（2026-10-22 用户要求）
    expect(screen.getByTestId('ask-user-banner')).toHaveTextContent('询问：继续跑压测吗？')
    // 自动流程（收件箱返回）不得把卡片展开
    await waitFor(() => expect(screen.queryByTestId('ask-user-card')).toBeNull())
    expect(screen.queryByTestId('ask-user-expanded')).toBeNull()
  })

  it('点击横幅 ⇒ 展开出 ask-user-card；点「收起」⇒ 回到横幅态', async () => {
    const store = makeStore(new Map([['web:chat-1', PROMPT]]))
    render(<AskUserFooter store={store} channel="web" chatID="chat-1"
      loadInbox={inboxWith([ROW])} submit={vi.fn()} />)

    fireEvent.click(await screen.findByTestId('ask-user-banner'))
    expect(await screen.findByTestId('ask-user-card')).toBeInTheDocument()
    expect(screen.queryByTestId('ask-user-banner')).toBeNull()

    fireEvent.click(screen.getByTestId('ask-user-collapse'))
    expect(await screen.findByTestId('ask-user-banner')).toBeInTheDocument()
    expect(screen.queryByTestId('ask-user-card')).toBeNull()
  })

  it('没有 live prompt、只有收件箱未答行 ⇒ 也先给横幅（不直接摊开卡片）', async () => {
    const store = makeStore(new Map())
    render(<AskUserFooter store={store} channel="web" chatID="chat-1"
      loadInbox={inboxWith([ROW])} submit={vi.fn()} />)
    expect(await screen.findByTestId('ask-user-banner')).toBeInTheDocument()
    expect(screen.queryByTestId('ask-user-card')).toBeNull()
  })

  it('提交成功 ⇒ 回到收起态（卡片消失）', async () => {
    const store = makeStore(new Map([['web:chat-1', PROMPT]]))
    const submit = vi.fn().mockResolvedValue({ message_id: 8, duplicate: false, message: ROW })
    render(<AskUserFooter store={store} channel="web" chatID="chat-1"
      loadInbox={inboxWith([ROW])} submit={submit} />)

    fireEvent.click(await screen.findByTestId('ask-user-banner'))
    fireEvent.click(await screen.findByText('继续'))
    fireEvent.click(screen.getByTestId('ask-user-submit'))

    await waitFor(() => expect(submit).toHaveBeenCalledTimes(1))
    expect(submit.mock.calls[0][0]).toMatchObject({ correlation_id: 'corr-7', channel: 'web', chat_id: 'chat-1' })
    expect(store.clearAskUserPrompt).toHaveBeenCalledWith('web', 'chat-1')
    await waitFor(() => expect(screen.queryByTestId('ask-user-card')).toBeNull())
  })

  it('提交失败 ⇒ 卡片保留并显示错误，问题被放回 store', async () => {
    const store = makeStore(new Map([['web:chat-1', PROMPT]]))
    const submit = vi.fn().mockRejectedValue(new Error('ask_user not found'))
    render(<AskUserFooter store={store} channel="web" chatID="chat-1"
      loadInbox={inboxWith([ROW])} submit={submit} />)

    fireEvent.click(await screen.findByTestId('ask-user-banner'))
    fireEvent.click(await screen.findByTestId('ask-user-submit'))
    await waitFor(() => expect(screen.getByText('ask_user not found')).toBeInTheDocument())
    expect(store.markAskUserAnswered).toHaveBeenLastCalledWith('web', 'chat-1', '')
    expect(store.hydrateAskUserPrompt).toHaveBeenCalledWith('web', 'chat-1', PROMPT)
  })
})

describe('AskUserFooter 纯函数', () => {
  it('prompt → StructuredMessage：correlation_id 取 requestId，选项转成 {id,label}', () => {
    const msg = promptToStructuredMessage(PROMPT, 'web', 'chat-1')
    expect(msg.correlation_id).toBe('req-live-1')
    expect(msg.kind).toBe('ask_user')
    const q = (msg.payload as { questions: { id: string; options: { id: string }[] }[] }).questions[0]
    expect(q.id).toBe('q0')
    expect(q.options.map((o) => o.id)).toEqual(['继续', '停'])
  })

  it('matchInboxRow：优先精确 correlation_id，其次收件箱唯一未答行，再否则 null', () => {
    expect(matchInboxRow(PROMPT, [ROW])?.message_id).toBe(7)
    const other = { ...ROW, correlation_id: 'req-live-1', message_id: 9 }
    expect(matchInboxRow(PROMPT, [ROW, other])?.message_id).toBe(9)
    expect(matchInboxRow(PROMPT, [])).toBeNull()
  })

  it('belongsToSession：信封里 session 不匹配即排除，缺字段则信任请求作用域', () => {
    expect(belongsToSession(ROW, 'web', 'chat-1')).toBe(true)
    expect(belongsToSession(ROW, 'web', 'chat-2')).toBe(false)
    const noEnvelope = { ...ROW, envelope: undefined }
    expect(belongsToSession(noEnvelope, 'web', 'chat-2')).toBe(true)
  })
})
