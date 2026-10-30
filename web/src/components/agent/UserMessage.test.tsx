import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, screen } from '@testing-library/react'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { I18nProvider } from '@/providers/i18n'
import { UserMessage } from './UserMessage'
import { formatAnnotatedMessage } from '@/lib/messageAnnotations'

// Use the i18n key directly — the test environment may be zh-CN or en,
// so we query by title attribute instead of localized label text.

describe('UserMessage — inline edit mode (Spec C §2)', () => {
  it('keeps the full XML prompt available when editing a displayed annotation message', () => {
    const content = formatAnnotatedMessage('正文', [{ id: 'one', quote: '原文', comment: '评论', source: { turnID: 7, iteration: 1 } }], (key) => key)
    const { container } = renderWithProviders(<UserMessage content={content} isEditing onRewind={vi.fn()} />)
    expect(screen.getByRole('textbox')).toHaveValue(content)
    expect(container.querySelector('textarea')!.value).toContain('<response-annotations>')
  })

  it('copies the actual submitted XML prompt rather than only the visible quote/comment', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.assign(navigator, { clipboard: { writeText } })
    const content = formatAnnotatedMessage('正文', [{ id: 'one', quote: '原文', comment: '评论', source: { turnID: 7, iteration: 1 } }], (key) => key)
    renderWithProviders(<UserMessage content={content} />)
    const button = screen.getAllByRole('button').find((item) => item.querySelector('svg.lucide-copy') && !item.closest('pre, .group\\/code'))!
    fireEvent.click(button)
    expect(writeText).toHaveBeenCalledWith(content)
  })
  it('renders plain content with markdown', () => {
    renderWithProviders(
      <UserMessage content="Hello world" />,
    )
    expect(screen.getByText('Hello world')).toBeInTheDocument()
  })

  it('shows pencil edit button when onRewind and onStartEdit are provided', () => {
    const onRewind = vi.fn()
    const onStartEdit = vi.fn()

    renderWithProviders(
      <UserMessage
        content="Hello"
        onRewind={onRewind}
        onStartEdit={onStartEdit}
      />,
    )

    // The edit button has a title attribute from i18n; find by button role
    const buttons = screen.getAllByRole('button')
    const editBtn = buttons.find((b) => b.querySelector('svg.lucide-pencil'))
    expect(editBtn).toBeDefined()
  })

  it('does not show edit button when onRewind is not provided', () => {
    renderWithProviders(
      <UserMessage content="Hello" />,
    )

    const buttons = screen.queryAllByRole('button')
    const editBtn = buttons.find((b) => b.querySelector('svg.lucide-pencil'))
    expect(editBtn).toBeUndefined()
  })

  it('disables edit button when editDisabled is true', () => {
    const onStartEdit = vi.fn()

    renderWithProviders(
      <UserMessage
        content="Hello"
        onRewind={vi.fn()}
        onStartEdit={onStartEdit}
        editDisabled={true}
      />,
    )

    const buttons = screen.getAllByRole('button')
    const editBtn = buttons.find((b) => b.querySelector('svg.lucide-pencil')) as HTMLButtonElement
    expect(editBtn).toBeDefined()
    expect(editBtn.disabled).toBe(true)
  })

  it('calls onStartEdit when edit button is clicked', () => {
    const onStartEdit = vi.fn()

    renderWithProviders(
      <UserMessage
        content="Hello"
        onRewind={vi.fn()}
        onStartEdit={onStartEdit}
      />,
    )

    const buttons = screen.getAllByRole('button')
    const editBtn = buttons.find((b) => b.querySelector('svg.lucide-pencil'))!
    fireEvent.click(editBtn)
    expect(onStartEdit).toHaveBeenCalledTimes(1)
  })

  it('clamps the edit box minHeight to the textarea cap (a tall message must not leave a huge empty box)', () => {
    // Regression: handleStartEdit captured the FULL rendered display height
    // (1500px for long pasted content) as the edit container's minHeight,
    // while the edit textarea itself caps at 300px — the box was extremely
    // tall with the editable text squeezed into the top strip.
    const heightDesc = Object.getOwnPropertyDescriptor(window.HTMLElement.prototype, 'offsetHeight')
    Object.defineProperty(window.HTMLElement.prototype, 'offsetHeight', {
      configurable: true,
      get() { return 1500 },
    })
    try {
      const { rerender } = renderWithProviders(
        <UserMessage content="long content" onRewind={vi.fn()} onStartEdit={vi.fn()} />,
      )
      const editBtn = screen.getAllByRole('button').find((b) => b.querySelector('svg.lucide-pencil'))!
      fireEvent.click(editBtn)

      // Parent flips isEditing (controlled) — same component instance keeps
      // the captured editMinHeight state. rerender needs the Provider again.
      rerender(
        <I18nProvider>
          <UserMessage content="long content" onRewind={vi.fn()} onStartEdit={vi.fn()} onEndEdit={vi.fn()} isEditing />
        </I18nProvider>,
      )
      const box = screen.getByRole('textbox').closest('.rounded-lg') as HTMLElement
      expect(box).not.toBeNull()
      // 300 (textarea cap) + py-2×2 + border×2 = 318 — NOT 1500
      expect(box.style.minHeight).toBe('318px')
    } finally {
      if (heightDesc) Object.defineProperty(window.HTMLElement.prototype, 'offsetHeight', heightDesc)
      else delete (window.HTMLElement.prototype as { offsetHeight?: number }).offsetHeight
    }
  })

  it('shows textarea and confirm/cancel buttons when in edit mode', () => {
    renderWithProviders(
      <UserMessage
        content="Original text"
        onRewind={vi.fn()}
        isEditing={true}
        onStartEdit={vi.fn()}
        onEndEdit={vi.fn()}
      />,
    )

    // Should have a textarea with the original content
    const textarea = screen.getByRole('textbox') as HTMLTextAreaElement
    expect(textarea.value).toBe('Original text')

    // Should have confirm (check) and cancel (x) buttons
    const buttons = screen.getAllByRole('button')
    const checkBtn = buttons.find((b) => b.querySelector('svg.lucide-check'))
    const xBtn = buttons.find((b) => b.querySelector('svg.lucide-x'))
    expect(checkBtn).toBeDefined()
    expect(xBtn).toBeDefined()
  })

  it('calls onRewind with edited content when confirm is clicked', () => {
    const onRewind = vi.fn()
    const onEndEdit = vi.fn()

    renderWithProviders(
      <UserMessage
        content="Original text"
        onRewind={onRewind}
        isEditing={true}
        onStartEdit={vi.fn()}
        onEndEdit={onEndEdit}
      />,
    )

    const textarea = screen.getByRole('textbox') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'Edited text' } })

    const buttons = screen.getAllByRole('button')
    const checkBtn = buttons.find((b) => b.querySelector('svg.lucide-check'))!
    fireEvent.click(checkBtn)

    expect(onRewind).toHaveBeenCalledWith('Edited text')
  })

  it('calls onRewind with original content when confirm is clicked without changes', () => {
    const onRewind = vi.fn()
    const onEndEdit = vi.fn()

    renderWithProviders(
      <UserMessage
        content="Same text"
        onRewind={onRewind}
        isEditing={true}
        onStartEdit={vi.fn()}
        onEndEdit={onEndEdit}
      />,
    )

    const buttons = screen.getAllByRole('button')
    const checkBtn = buttons.find((b) => b.querySelector('svg.lucide-check'))!
    fireEvent.click(checkBtn)

    // Rewind should be called even when content is unchanged — the user
    // confirmed the rewind action by clicking Check, not Cancel (X).
    expect(onRewind).toHaveBeenCalledWith('Same text')
  })

  it('calls onEndEdit and restores original when cancel is clicked', () => {
    const onRewind = vi.fn()
    const onEndEdit = vi.fn()

    renderWithProviders(
      <UserMessage
        content="Original"
        onRewind={onRewind}
        isEditing={true}
        onStartEdit={vi.fn()}
        onEndEdit={onEndEdit}
      />,
    )

    const textarea = screen.getByRole('textbox') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'Changed' } })

    const buttons = screen.getAllByRole('button')
    const xBtn = buttons.find((b) => b.querySelector('svg.lucide-x'))!
    fireEvent.click(xBtn)

    expect(onRewind).not.toHaveBeenCalled()
    expect(onEndEdit).toHaveBeenCalledTimes(1)
  })
})

describe('UserMessage — compact-marker folding', () => {
  it('renders a compact marker as a collapsed placeholder (summary hidden)', () => {
    const { container, getByText } = renderWithProviders(
      <UserMessage content={'[Compacted context]\nLONG SUMMARY BODY'} />,
    )
    // The title line is visible in the collapsed <summary>.
    expect(getByText('[Compacted context]')).toBeInTheDocument()
    // The body is inside a closed <details> — not visible until expanded.
    const details = container.querySelector('details')
    expect(details).not.toBeNull()
    expect(details!.open).toBe(false)
  })

  it('reveals the summary body when the placeholder is expanded', () => {
    const { container, getByText } = renderWithProviders(
      <UserMessage content={'[Compacted context]\nLONG SUMMARY BODY'} />,
    )
    const details = container.querySelector('details')!
    fireEvent.click(getByText('[Compacted context]'))
    expect(details.open).toBe(true)
    expect(getByText('LONG SUMMARY BODY')).toBeInTheDocument()
  })

  it('does not fold a normal user message', () => {
    const { container, queryByText } = renderWithProviders(
      <UserMessage content="hello world" />,
    )
    expect(container.querySelector('details')).toBeNull()
    expect(queryByText('hello world')).toBeInTheDocument()
  })
})

/**
 * 2026-10-30 P0 回归 —— "已复制" 复位定时器泄漏。
 *
 * 故障现场：`handleCopy` 里裸调 `window.setTimeout(() => setCopied(false), 1200)`，
 * 组件在 1200ms 内卸载后回调仍在跑；测试环境 teardown 后 jsdom 的 window 已销毁，
 * 回调路径抛 `ReferenceError: window is not defined` 的**未捕获异常** ——
 * `Test Files 204 passed / Tests 2243 passed / Errors 1 error`，退出码非 0，
 * 把所有人的 pre-commit 一起拦下。
 *
 * 三条用例分别锁：卸载必须清理 / 连点不叠加 / 正常回落行为不变。
 */
describe('UserMessage — copy-confirmation reset timer cleanup (2026-10-30 regression)', () => {
  function mockClipboard() {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.assign(navigator, { clipboard: { writeText } })
    return writeText
  }

  // user 气泡上的复制按钮：第一个带 copy 图标、且不在代码块复制区里的 button。
  function copyButton(): HTMLElement {
    const btn = screen.getAllByRole('button').find(
      (item) => item.querySelector('svg.lucide-copy') && !item.closest('pre, .group\\/code'),
    )
    if (!btn) throw new Error('copy button not found')
    return btn
  }

  // 剪贴板 .then 回调、定时器回调都会触发 setState：必须包在 act 里，
  // 否则会出现 act(...) 告警，令 ① 的 "console.error 零调用" 断言失去意义。
  async function flushClipboard() {
    await act(async () => {
      await Promise.resolve()
      await Promise.resolve()
    })
  }

  const tick = async (ms: number) => {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ms)
    })
  }

  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  it('① unmount 早于 1200ms 时清掉复位定时器（定时器回调不再执行、无错误输出）', async () => {
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    mockClipboard()

    // 直接盯"那个 1200ms 回调到底有没有被执行"：泄漏的实现在卸载后依然会触发它
    // （真实环境里就是 teardown 之后访问已销毁的 window 那条未捕获异常）。
    const fakeSetTimeout = globalThis.setTimeout
    let resetRuns = 0
    vi.spyOn(globalThis, 'setTimeout').mockImplementation(((
      fn: TimerHandler,
      ms?: number,
      ...args: unknown[]
    ) => fakeSetTimeout(((...passed: unknown[]) => {
      if (ms === 1200) resetRuns += 1
      return (fn as (...x: unknown[]) => void)(...passed)
    }) as TimerHandler, ms, ...args)) as unknown as typeof setTimeout)

    const { unmount } = renderWithProviders(<UserMessage content="hello" />)

    fireEvent.click(copyButton())
    await flushClipboard()
    expect(resetRuns).toBe(0) // 还没到点

    unmount() // 1200ms 之前卸载
    await tick(2000) // 推进到 2000ms：被清理掉的定时器不该再执行
    expect(resetRuns).toBe(0)
    expect(errorSpy).not.toHaveBeenCalled()
  })

  it('② 连点两次只有一个待触发复位定时器，且最终回到未复制态', async () => {
    mockClipboard()

    renderWithProviders(<UserMessage content="hello" />)
    const btn = copyButton()
    const baseline = vi.getTimerCount()

    fireEvent.click(btn)
    await flushClipboard()
    expect(vi.getTimerCount()).toBe(baseline + 1)
    expect(btn.querySelector('svg.lucide-check')).not.toBeNull()

    fireEvent.click(btn) // 第二次点击：旧 timer 必须先被清掉
    await flushClipboard()
    expect(vi.getTimerCount()).toBe(baseline + 1) // 而不是 baseline + 2

    await tick(1200)
    expect(vi.getTimerCount()).toBe(0)
    expect(btn.querySelector('svg.lucide-check')).toBeNull()
    expect(btn.querySelector('svg.lucide-copy')).not.toBeNull()
  })

  it('③ 正常流程：复制 → 1200ms 内显示已复制 → 到点回落到未复制', async () => {
    const writeText = mockClipboard()

    renderWithProviders(<UserMessage content="hello" />)
    const btn = copyButton()

    fireEvent.click(btn)
    await flushClipboard()
    expect(writeText).toHaveBeenCalledWith('hello')
    expect(btn.querySelector('svg.lucide-check')).not.toBeNull()

    await tick(1199)
    expect(btn.querySelector('svg.lucide-check')).not.toBeNull() // 未到点，仍显示已复制

    await tick(1)
    expect(btn.querySelector('svg.lucide-check')).toBeNull()
    expect(btn.querySelector('svg.lucide-copy')).not.toBeNull()
  })

  it('④ 卸载后剪贴板 promise 才 resolve：不排复位定时器、不报错', async () => {
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    // writeText 挂起，等卸载之后才 resolve —— 输入框/消息被回收时真实会发生。
    let release: () => void = () => {}
    const pending = new Promise<void>((resolve) => { release = resolve })
    Object.assign(navigator, { clipboard: { writeText: vi.fn().mockReturnValue(pending) } })

    const fakeSetTimeout = globalThis.setTimeout
    let resetScheduled = 0
    vi.spyOn(globalThis, 'setTimeout').mockImplementation(((
      fn: TimerHandler,
      ms?: number,
      ...args: unknown[]
    ) => {
      if (ms === 1200) resetScheduled += 1
      return fakeSetTimeout(fn, ms, ...args)
    }) as unknown as typeof setTimeout)

    const { unmount } = renderWithProviders(<UserMessage content="hello" />)
    fireEvent.click(copyButton())
    unmount()

    await act(async () => { release() })
    await flushClipboard()

    expect(resetScheduled).toBe(0) // 卸载后不再起定时器
    expect(errorSpy).not.toHaveBeenCalled()
  })
})
