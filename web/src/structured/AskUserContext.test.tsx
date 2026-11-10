import '@testing-library/jest-dom/vitest'
import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { AskUserCard } from './StructuredCards'
import type { StructuredMessage } from './types'

/**
 * 2026-10-22：ask_user 必须自带完整背景（`message.text`），卡片要把背景显示在
 * **问题上方**；缺背景时不能静默 —— 给一条警示。
 */

function makeMessage(text: string): StructuredMessage {
  return {
    message_id: 1,
    kind: 'ask_user',
    correlation_id: 'corr-1',
    direction: 'in',
    answered: false,
    cancelled: false,
    text,
    payload: {
      questions: [{
        id: 'q1',
        question: '是否现在执行镜像同步？',
        options: [{ id: 'now', label: '现在执行' }, { id: 'later', label: '稍后' }],
      }],
    },
    sender: { type: 'user', id: 'web-1' },
    created_at: new Date().toISOString(),
  }
}

describe('AskUserCard 背景区', () => {
  it('有背景 ⇒ 背景块在问题上方可见，并带「背景」标签', () => {
    const text = '原因：新节点缺镜像，业务起不来。事实：当前 3 台 spot 空转，磁盘 82%。'
    render(<AskUserCard message={makeMessage(text)} onAnswer={vi.fn()} />)

    const ctx = screen.getByTestId('ask-user-context')
    expect(ctx).toBeInTheDocument()
    expect(ctx).toHaveTextContent('背景')
    expect(screen.getByTestId('ask-user-context-text')).toHaveTextContent('新节点缺镜像')
    // 背景在问题之前（DOM 顺序）
    const question = screen.getByText('是否现在执行镜像同步？')
    expect(ctx.compareDocumentPosition(question) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(screen.queryByTestId('ask-user-missing-context')).toBeNull()
  })

  it('没有背景 ⇒ 出现「缺少上下文」警示（不静默）', () => {
    render(<AskUserCard message={makeMessage('')} onAnswer={vi.fn()} />)
    const warn = screen.getByTestId('ask-user-missing-context')
    expect(warn).toHaveTextContent('缺少上下文')
    expect(warn).toHaveTextContent('可直接向它追问')
    expect(screen.queryByTestId('ask-user-context')).toBeNull()
  })

  it('空白背景（只有空格/换行）也按"没有背景"处理', () => {
    render(<AskUserCard message={makeMessage('  \n  ')} onAnswer={vi.fn()} />)
    expect(screen.getByTestId('ask-user-missing-context')).toBeInTheDocument()
  })

  it('服务端把问题原文回落成 message.text ⇒ 同样算"没有背景"（不假装有）', () => {
    render(<AskUserCard message={makeMessage('是否现在执行镜像同步？')} onAnswer={vi.fn()} />)
    expect(screen.getByTestId('ask-user-missing-context')).toBeInTheDocument()
    expect(screen.queryByTestId('ask-user-context')).toBeNull()
  })
})
