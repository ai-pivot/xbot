import { fireEvent, screen } from '@testing-library/react'
import '@testing-library/jest-dom'
import { useRef } from 'react'
import { beforeAll, describe, expect, it, vi } from 'vitest'

import i18n from '@/i18n'
import { renderWithProviders } from '@/test-utils'
import { SessionSearch } from './SessionSearch'

// 断言文案与组件同语言（jsdom 的 navigator.language=en-US ⇒ 不钉死会拿 en 文案）。
beforeAll(async () => {
  await i18n.changeLanguage('zh-CN')
})

/**
 * 回归（2026-09-15 用户：「session 面板那个搜索，不要 disable 弹出键盘，这导致
 * 手机端都不会弹出键盘」）：输入框曾用 readOnly-until-focus 反 Chrome 自动填充
 * —— readOnly 的输入框在手机上**永远不弹软键盘**。断言必须锁死"不是 readOnly"
 * 这个语义（并覆盖移动端键盘类型 / 输入可用 / 手势内同步 focus）。
 */
describe('SessionSearch（手机端必须能弹软键盘）', () => {
  it('输入框不是 readOnly/disabled，带移动端 search 键盘，且可正常输入', () => {
    const onChange = vi.fn()
    renderWithProviders(<SessionSearch value="" onChange={onChange} open />)
    const input = screen.getByLabelText('搜索') as HTMLInputElement

    expect(input.readOnly).toBe(false)
    expect(input.disabled).toBe(false)
    // 反自动填充（Chrome 曾把登录名 "adm" 填进这个框）不许再靠 readOnly。
    expect(input.getAttribute('autocomplete')).toBe('off')
    expect(input.getAttribute('name')).toBe('xbot-session-search')
    // 移动端键盘类型 + 回车键语义（enterKeyHint 只在真机可见，这里守 DOM 契约）。
    expect(input.getAttribute('inputmode')).toBe('search')
    expect(input.getAttribute('enterkeyhint')).toBe('search')

    fireEvent.change(input, { target: { value: 'abc' } })
    expect(onChange).toHaveBeenCalledWith('abc')
  })

  it('外部 inputRef：父组件能在点击手势内同步 focus（手机弹键盘的前提）', () => {
    function Harness() {
      const ref = useRef<HTMLInputElement | null>(null)
      return (
        <>
          {/* 模拟 SessionSearchToggle：焦点必须在 click 的同一次任务里落下。 */}
          <button type="button" onClick={() => ref.current?.focus()}>
            toggle
          </button>
          <SessionSearch value="" onChange={() => {}} open={false} inputRef={ref} />
        </>
      )
    }
    renderWithProviders(<Harness />)
    const input = screen.getByLabelText('搜索')
    expect(document.activeElement).not.toBe(input)
    fireEvent.click(screen.getByText('toggle'))
    expect(document.activeElement).toBe(input)
  })
})
