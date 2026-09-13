/**
 * GoalBanner 显示/编辑守护（用户 2026-09-13 回退要求）。
 *
 * 契约：
 * - **显示层 = master 单行样式**：truncate 截断、单行行容器、无展开/收起、
 *   无手机端 Sheet、无保存/取消按钮（卡片永不换行完整渲染）；
 *   只有 master 原有的「编辑铅笔 + 清除 ×」两个小按钮。
 * - **编辑交互 = 点击文本就地编辑**（单行 input）：Enter 保存 / Esc 取消 /
 *   **失焦回退不保存** / IME 组合态不提交。
 *
 * 任何"多行完整渲染 / 展开收起 / 底部 Sheet / 保存取消按钮"的重引入都会被本文件红。
 */
import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'

const state = vi.hoisted(() => ({ touch: false }))
vi.mock('@/hooks/useIsMobile', () => ({ useIsTouch: () => state.touch, useIsMobile: () => false }))

import { GoalBanner } from '@/components/agent/GoalBanner'
import { I18nProvider } from '@/providers/i18n'
import i18n from '@/i18n'

const goal = (objective: string, status = 'in_progress') => ({ objective, status }) as never
/** 断言用的期望文案：跟随当前 locale（jsdom 下通常是 en），避免与语言耦合。 */
const t = (key: string) => i18n.t(key)

function setup(objective = '短目标', status = 'in_progress') {
  const onEdit = vi.fn()
  const onClear = vi.fn()
  const view = render(
    <I18nProvider>
      <GoalBanner goal={goal(objective, status)} onEdit={onEdit} onClear={onClear} />
    </I18nProvider>,
  )
  return { onEdit, onClear, ...view }
}

afterEach(() => {
  state.touch = false
})

describe('GoalBanner 显示层 = master 单行样式', () => {
  it('单行截断：truncate、行容器 items-center、无多行编辑器', () => {
    const long =
      '这是一段很长的目标描述，用来验证它保持 master 的单行截断样式，而不是折行完整渲染，长度足够超过一行'
    const { container } = setup(long)
    const text = screen.getByTestId('goal-text')

    expect(text).toHaveTextContent(long)
    expect(text.className).toContain('truncate')
    expect(text.className).not.toContain('whitespace-pre-wrap')
    expect(text.className).not.toContain('line-clamp')

    const row = text.parentElement as HTMLElement
    expect(row.className).toContain('items-center')
    expect(row.className).not.toContain('items-start')

    expect(container.querySelector('textarea')).toBeNull()
  })

  it('不引入多余按钮：无展开/收起、无保存/取消、无手机端 Sheet', () => {
    setup('目标')
    for (const id of ['goal-expand', 'goal-save', 'goal-cancel', 'goal-sheet']) {
      expect(screen.queryByTestId(id)).toBeNull()
    }
    // master 原有的两个小按钮仍在：行内总共只有「文本 + 编辑铅笔 + 清除 ×」三个按钮
    const row = screen.getByTestId('goal-text').parentElement as HTMLElement
    expect(row.querySelectorAll('button')).toHaveLength(3)
    expect(screen.getByTitle(t('agent.goal.edit'))).toBeInTheDocument()
    expect(screen.getByTestId('goal-clear')).toBeInTheDocument()
  })

  it('状态徽标与清除回调（master 结构保留）', () => {
    const { onClear } = setup('目标')
    expect(screen.getByText(t('agent.goal.inProgress'))).toBeInTheDocument()
    fireEvent.click(screen.getByTestId('goal-clear'))
    expect(onClear).toHaveBeenCalledTimes(1)
  })
})

describe('GoalBanner 编辑交互（唯一改动点）', () => {
  it('点击文本进入就地编辑（单行 input），Enter 保存并退出', () => {
    const { onEdit } = setup('旧目标')
    fireEvent.click(screen.getByTestId('goal-text'))
    const input = screen.getByTestId('goal-edit-input')

    expect(input.tagName).toBe('TEXTAREA')
    expect(input.className).toContain('text-xs')

    fireEvent.change(input, { target: { value: '新目标' } })
    fireEvent.keyDown(input, { key: 'Enter' })

    expect(onEdit).toHaveBeenCalledWith('新目标')
    expect(screen.queryByTestId('goal-edit-input')).toBeNull()
    expect(screen.getByTestId('goal-text')).toBeInTheDocument()
  })

  it('Esc 取消：不保存、显示回原值、退出编辑', () => {
    const { onEdit } = setup('旧目标')
    fireEvent.click(screen.getByTestId('goal-text'))
    const input = screen.getByTestId('goal-edit-input')

    fireEvent.change(input, { target: { value: '不要保存' } })
    fireEvent.keyDown(input, { key: 'Escape' })

    expect(onEdit).not.toHaveBeenCalled()
    expect(screen.queryByTestId('goal-edit-input')).toBeNull()
    expect(screen.getByTestId('goal-text')).toHaveTextContent('旧目标')
  })

  it('失焦保存：写入草稿并退出编辑（与 todo 编辑同一契约）', () => {
    const { onEdit } = setup('旧目标')
    fireEvent.click(screen.getByTestId('goal-text'))
    const input = screen.getByTestId('goal-edit-input')

    fireEvent.change(input, { target: { value: '新的目标' } })
    fireEvent.blur(input)

    expect(onEdit).toHaveBeenCalledWith('新的目标')
    expect(screen.queryByTestId('goal-edit-input')).toBeNull()
    expect(screen.getByTestId('goal-text')).toHaveTextContent('旧目标')
  })

  it('IME 组合态下的 Enter 不提交（中日文输入法选词）', () => {
    const { onEdit } = setup('旧目标')
    fireEvent.click(screen.getByTestId('goal-text'))
    const input = screen.getByTestId('goal-edit-input')

    fireEvent.change(input, { target: { value: '组合中' } })
    fireEvent.keyDown(input, { key: 'Enter', isComposing: true })

    expect(onEdit).not.toHaveBeenCalled()
    expect(screen.getByTestId('goal-edit-input')).toBeInTheDocument()
  })

  it('内容未变或为空时 Enter 只退出、不触发 onEdit', () => {
    const { onEdit } = setup('旧目标')
    fireEvent.click(screen.getByTestId('goal-text'))

    fireEvent.keyDown(screen.getByTestId('goal-edit-input'), { key: 'Enter' })
    expect(onEdit).not.toHaveBeenCalled()

    fireEvent.click(screen.getByTestId('goal-text'))
    const input = screen.getByTestId('goal-edit-input')
    fireEvent.change(input, { target: { value: '   ' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onEdit).not.toHaveBeenCalled()
  })

  it('已完成目标不可编辑（点击文本不进编辑）', () => {
    setup('已完成的目标', 'completed')
    fireEvent.click(screen.getByTestId('goal-text'))
    expect(screen.queryByTestId('goal-edit-input')).toBeNull()
  })

  it('触屏：底部弹出 textarea（与 todo-edit-sheet 同一形态），取消/保存按钮可用', () => {
    state.touch = true
    const { onEdit } = setup('手机上编辑目标')

    fireEvent.click(screen.getByTestId('goal-text'))
    const sheet = screen.getByTestId('goal-edit-sheet')
    expect(sheet.className).toContain('fixed')
    expect(sheet.className).toContain('bottom-0')

    const input = screen.getByTestId('goal-edit-input')
    expect(input.tagName).toBe('TEXTAREA')

    // 取消：不保存
    fireEvent.change(input, { target: { value: '不要保存' } })
    fireEvent.click(screen.getByTestId('goal-edit-cancel'))
    expect(onEdit).not.toHaveBeenCalled()
    expect(screen.queryByTestId('goal-edit-sheet')).toBeNull()

    // 保存：写入草稿
    fireEvent.click(screen.getByTestId('goal-text'))
    const input2 = screen.getByTestId('goal-edit-input')
    fireEvent.change(input2, { target: { value: '手机新目标' } })
    fireEvent.click(screen.getByTestId('goal-edit-save'))
    expect(onEdit).toHaveBeenCalledWith('手机新目标')
  })
})
