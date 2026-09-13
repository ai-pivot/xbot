/**
 * GoalBanner 编辑交互守护（用户 2026-09-13：goal 常超过一行，单行 input 不方便）。
 * 契约：显示不截断（2 行 clamp + 展开/收起）；编辑 = 自适应 textarea；
 * Enter 保存 / Shift+Enter 换行 / Esc 取消 / IME 组合态不提交；失焦不自动保存；
 * 触屏走底部 Sheet。
 */
import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'

const state = vi.hoisted(() => ({ touch: false }))
vi.mock('@/hooks/useIsMobile', () => ({ useIsTouch: () => state.touch, useIsMobile: () => false }))

import { GoalBanner } from '@/components/agent/GoalBanner'
import { I18nProvider } from '@/providers/i18n'
import '@/i18n'

const goal = (objective: string) => ({ objective, status: 'in_progress' }) as never

function setup(objective = '短目标', onEdit = vi.fn(), onClear = vi.fn()) {
  render(
    <I18nProvider>
      <GoalBanner goal={goal(objective)} onEdit={onEdit} onClear={onClear} />
    </I18nProvider>,
  )
  return { onEdit, onClear }
}

describe('GoalBanner 编辑交互', () => {
  it('显示不截断：文本可换行、2 行 clamp，超长给展开/收起', () => {
    setup('这是一段很长的目标描述，用来验证它不会被省略号截断而是可以折行显示并且提供展开收起按钮，并且长度一定要超过折叠阈值以保证展开按钮出现')
    const text = screen.getByTestId('goal-text')
    expect(text.className).toContain('whitespace-pre-wrap')
    expect(text.className).toContain('line-clamp-2')
    expect(screen.getByTestId('goal-expand')).toBeInTheDocument()
  })

  it('点击进入编辑：textarea 自适应（多行），Enter 保存、Shift+Enter 换行不保存、Esc 取消', () => {
    const { onEdit } = setup('旧目标')
    fireEvent.click(screen.getByTestId('goal-text'))
    const ta = screen.getByTestId('goal-edit-input') as HTMLTextAreaElement
    expect(ta.tagName).toBe('TEXTAREA')

    fireEvent.change(ta, { target: { value: '第一行' } })
    fireEvent.keyDown(ta, { key: 'Enter', shiftKey: true })
    expect(onEdit).not.toHaveBeenCalled()

    fireEvent.change(ta, { target: { value: '新目标' } })
    fireEvent.keyDown(ta, { key: 'Enter' })
    expect(onEdit).toHaveBeenCalledWith('新目标')

    fireEvent.click(screen.getByTestId('goal-text'))
    fireEvent.change(screen.getByTestId('goal-edit-input'), { target: { value: '不要保存' } })
    fireEvent.keyDown(screen.getByTestId('goal-edit-input'), { key: 'Escape' })
    expect(onEdit).toHaveBeenCalledTimes(1)
    expect(screen.getByTestId('goal-text')).toHaveTextContent('旧目标')
  })

  it('IME 组合态下的 Enter 不提交（中文输入法选词）', () => {
    const { onEdit } = setup('旧目标')
    fireEvent.click(screen.getByTestId('goal-text'))
    const ta = screen.getByTestId('goal-edit-input')
    fireEvent.change(ta, { target: { value: '组合中' } })
    fireEvent.keyDown(ta, { key: 'Enter', isComposing: true })
    expect(onEdit).not.toHaveBeenCalled()
  })

  it('显式保存按钮生效；失焦不自动保存（避免误改）', () => {
    const { onEdit } = setup('旧目标')
    fireEvent.click(screen.getByTestId('goal-text'))
    fireEvent.change(screen.getByTestId('goal-edit-input'), { target: { value: '按钮保存' } })
    fireEvent.blur(screen.getByTestId('goal-edit-input'))
    expect(onEdit).not.toHaveBeenCalled()
    fireEvent.click(screen.getByTestId('goal-save'))
    expect(onEdit).toHaveBeenCalledWith('按钮保存')
  })

  it('触屏：编辑器放在底部 Sheet 里（键盘不遮挡）', () => {
    state.touch = true
    setup('手机上编辑目标')
    fireEvent.click(screen.getByTestId('goal-text'))
    expect(screen.getByTestId('goal-sheet')).toBeInTheDocument()
    expect(screen.getByTestId('goal-sheet').className).toContain('fixed')
    expect(screen.getByTestId('goal-sheet').className).toContain('bottom-0')
    state.touch = false
  })

  it('已完成目标不可编辑', () => {
    render(
      <I18nProvider>
        <GoalBanner goal={{ objective: '已完成', status: 'completed' } as never} onEdit={vi.fn()} onClear={vi.fn()} />
      </I18nProvider>,
    )
    fireEvent.click(screen.getByTestId('goal-text'))
    expect(screen.queryByTestId('goal-edit-input')).toBeNull()
  })
})
