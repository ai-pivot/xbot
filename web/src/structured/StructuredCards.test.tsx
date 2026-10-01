import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { AskUserCard } from './StructuredCards'
import type { StructuredMessage } from './types'

/**
 * AskUserCard 组件测试（2026-10-22 补）。
 *
 * 背景：本分支把 ask_user 的渲染从 `AskUserPanel`（删了）换成 `AskUserCard`
 * （从结构化消息 payload 渲染）。但当时**没有任何组件测试**覆盖新组件——
 * E2E 的 `expect(hasAskUser).toBe(true)` 是唯一覆盖"用户能看到问题"的断言，
 * 而它走的还是裸 SSE 路径（新架构下不再直接渲染面板），所以既红又保不住。
 *
 * 这组测试补齐四件事（E2E 里那条断言的语义等价物）：
 *   1. 问题文本与选项可见
 *   2. 单选/多选能选中
 *   3. 提交把正确 answers 交给 onAnswer
 *   4. answered / cancelled 后不允许再操作
 */

function makeMessage(overrides: Partial<StructuredMessage> = {}): StructuredMessage {
  return {
    message_id: 42,
    kind: 'ask_user',
    correlation_id: 'req-1',
    chat_id: 'web:chat-1',
    payload: {
      questions: [
        {
          id: 'q1',
          question: 'Do you want to proceed?',
          options: [
            { id: 'yes', label: 'yes' },
            { id: 'no', label: 'no' },
          ],
        },
      ],
    },
    text: '',
    answered: false,
    cancelled: false,
    ...overrides,
  } as unknown as StructuredMessage
}

describe('AskUserCard', () => {
  it('renders nothing when the payload has no questions', () => {
    // 空载荷防线（旧 AskUserPanel 的「questions 为空 ⇒ 不渲染」契约）：
    // 一条 ask_user 行但没有任何问题不得伪造一张"没内容却带提交按钮"的空卡片。
    // E2E 等价用例：e2e/askuser-empty-payload.spec.ts ①。
    const { container } = render(
      <AskUserCard
        message={makeMessage({ payload: { questions: [] } })}
        onAnswer={vi.fn()}
      />,
    )
    expect(screen.queryByTestId('ask-user-card')).toBeNull()
    expect(container.textContent).toBe('')
  })

  it('renders the question text and every option label', () => {
    render(<AskUserCard message={makeMessage()} onAnswer={vi.fn()} />)

    // E2E 断言的那句原文必须出现在页面上
    expect(screen.getByText('Do you want to proceed?')).toBeTruthy()
    expect(screen.getByText('yes')).toBeTruthy()
    expect(screen.getByText('no')).toBeTruthy()
    expect(screen.getByTestId('ask-user-card')).toBeTruthy()
  })

  it('submits the selected option for the right question', async () => {
    const onAnswer = vi.fn().mockResolvedValue(undefined)
    render(<AskUserCard message={makeMessage()} onAnswer={onAnswer} />)

    fireEvent.click(screen.getByRole('radio', { name: /yes/i }))
    fireEvent.click(screen.getByRole('button', { name: /提交|确认|submit/i }))

    await waitFor(() => expect(onAnswer).toHaveBeenCalledTimes(1))
    expect(onAnswer).toHaveBeenCalledWith(
      'req-1',
      [{ question_id: 'q1', option_ids: ['yes'] }],
      false,
    )
  })

  it('collects multiple selections when multi_select is set', async () => {
    const onAnswer = vi.fn().mockResolvedValue(undefined)
    render(
      <AskUserCard
        message={makeMessage({
          payload: {
            questions: [
              {
                id: 'q1',
                question: 'Pick any',
                multi_select: true,
                options: [
                  { id: 'a', label: 'alpha' },
                  { id: 'b', label: 'beta' },
                ],
              },
            ],
          },
        })}
        onAnswer={onAnswer}
      />,
    )

    fireEvent.click(screen.getByRole('checkbox', { name: /alpha/i }))
    fireEvent.click(screen.getByRole('checkbox', { name: /beta/i }))
    fireEvent.click(screen.getByRole('button', { name: /提交|确认|submit/i }))

    await waitFor(() => expect(onAnswer).toHaveBeenCalledTimes(1))
    const answers = onAnswer.mock.calls[0][1] as Array<{ question_id: string; option_ids: string[] }>
    expect(answers[0].question_id).toBe('q1')
    expect(new Set(answers[0].option_ids)).toEqual(new Set(['a', 'b']))
  })

  it('cancels with an empty answer set', async () => {
    const onAnswer = vi.fn().mockResolvedValue(undefined)
    render(<AskUserCard message={makeMessage()} onAnswer={onAnswer} />)

    fireEvent.click(screen.getByRole('button', { name: /取消|cancel/i }))

    await waitFor(() => expect(onAnswer).toHaveBeenCalledTimes(1))
    expect(onAnswer).toHaveBeenCalledWith('req-1', [], true)
  })

  it('locks the card after it has been answered', async () => {
    const onAnswer = vi.fn().mockResolvedValue(undefined)
    render(<AskUserCard message={makeMessage({ answered: true })} onAnswer={onAnswer} />)

    const radio = screen.getByRole('radio', { name: /yes/i }) as HTMLInputElement
    expect(radio.disabled).toBe(true)
    expect(screen.getByText('已回答')).toBeTruthy()

    fireEvent.click(radio)
    fireEvent.click(screen.getByRole('button', { name: /提交|确认|submit/i }))
    expect(onAnswer).not.toHaveBeenCalled()
  })

  it('surfaces a submit error instead of silently swallowing it', async () => {
    const onAnswer = vi.fn().mockRejectedValue(new Error('target already answered'))
    render(<AskUserCard message={makeMessage()} onAnswer={onAnswer} />)

    fireEvent.click(screen.getByRole('radio', { name: /yes/i }))
    fireEvent.click(screen.getByRole('button', { name: /提交|确认|submit/i }))

    await waitFor(() => expect(screen.getByText(/target already answered/)).toBeTruthy())
  })
})
