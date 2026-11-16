/**
 * 2026-10-30 P0 回归 —— mermaid 代码块「已复制」复位定时器泄漏。
 *
 * `MermaidDiagram.tsx` 里的 `CopyButton` 是从 MarkdownRenderer 复制过来的同一段
 * 实现：裸调 `setTimeout(() => setCopied(false), 1200)`，没有卸载清理。图表所在
 * 的消息 / 全屏浮层在 1200ms 内卸载（虚拟滚动回收、切会话、关浮层）后回调仍在
 * 跑；测试环境 teardown 之后 jsdom 的 `window` 已销毁 ⇒ 回调路径抛
 * `ReferenceError: window is not defined` 的**未捕获异常**：用例全过但 vitest
 * 退出码非 0，拦掉所有人的 pre-commit。
 *
 * 用 `MermaidSourceBlock`（导出、且用的就是同一个 `CopyButton`）来测，避免把
 * mermaid 本体（浏览器 API 依赖重）拉进单测。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, screen } from '@testing-library/react'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { MermaidSourceBlock } from './MermaidDiagram'

describe('MermaidDiagram — copy-confirmation reset timer cleanup (2026-10-30 regression)', () => {
  const SOURCE = 'graph TD\n  A-->B'

  function mockClipboard() {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.assign(navigator, { clipboard: { writeText } })
    return writeText
  }

  function copyButton(): HTMLElement {
    return screen.getByLabelText('Copy code')
  }

  // 剪贴板 .then 回调、定时器回调都会触发 setState：必须包在 act 里。
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

    const { unmount } = renderWithProviders(<MermaidSourceBlock source={SOURCE} />)

    fireEvent.click(copyButton())
    await flushClipboard()
    expect(resetRuns).toBe(0) // 还没到点

    unmount() // 1200ms 之前卸载
    await tick(2000)
    expect(resetRuns).toBe(0)
    expect(errorSpy).not.toHaveBeenCalled()
  })

  it('② 卸载后剪贴板 promise 才 resolve：不排复位定时器、不报错', async () => {
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
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

    const { unmount } = renderWithProviders(<MermaidSourceBlock source={SOURCE} />)
    fireEvent.click(copyButton())
    unmount()

    await act(async () => { release() })
    await flushClipboard()

    expect(resetScheduled).toBe(0) // 卸载后不再起定时器
    expect(errorSpy).not.toHaveBeenCalled()
  })
})
