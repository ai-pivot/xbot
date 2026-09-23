import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { fireEvent } from '@testing-library/react'
import { rafCoalescedObserveElementOffset } from './MessageList'

/**
 * 回归：`rafCoalescedObserveElementOffset` 必须在 cleanup 之后**彻底静默**。
 *
 * 症状（CI 真实失败，2026-09-23）：
 *   Test Files  160 passed (160)
 *   Tests       1693 passed (1693)
 *   Errors      1 error
 *   ##[error]ReferenceError: window is not defined
 *    ❯ resolveUpdatePriority  react-dom-client.development.js:1308
 *    ❯ dispatchReducerAction  react-dom-client.development.js:9102
 *    ❯ Virtualizer.notify     @tanstack/virtual-core/dist/esm/index.js:263
 *    ❯ wrappedCb              src/components/agent/MessageList.tsx:208
 *    ❯ @tanstack/virtual-core/dist/esm/index.js:84
 *
 * 根因（虚测源码实证，不是概率性问题）：virtual-core 的默认 observer 排了一个
 * `isScrollingResetDelay` 的 debounce 定时器（`() => cb(offset, false)`），
 * 但它返回的 cleanup **只移除事件监听、从不取消这个定时器**：
 *
 *   const fallback = debounce(targetWindow, () => { cb(offset, false) }, isScrollingResetDelay)
 *   ...
 *   return () => { element.removeEventListener('scroll', handler); ... }   // ← 没有 cancel
 *
 * 于是「滚动 → 卸载/测试环境销毁 → 定时器仍触发 → cb → virtualizer.notify →
 * React setState → 读 window（已销毁）→ ReferenceError」⇒ 1693 用例全绿，
 * 进程 exit 1，CI 红。
 *
 * 契约：observer 一旦被 cleanup，就**不得再通知**（第三方泄漏的定时器也一样）——
 * 这是 disposed 语义，不是防御性编程。
 */
describe('rafCoalescedObserveElementOffset disposal contract', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  function makeInstance(delay: number) {
    const scrollElement = document.createElement('div')
    Object.defineProperty(scrollElement, 'scrollTop', { value: 500, writable: true })
    return {
      scrollElement,
      instance: {
        targetWindow: window,
        scrollElement,
        options: {
          horizontal: false,
          isRtl: false,
          isScrollingResetDelay: delay,
          useScrollendEvent: false,
        },
      },
    }
  }

  /** observer 必须返回 cleanup（类型上可能为 undefined —— 这里把它钉死）。 */
  function observe(instance: unknown, cb: (offset: number, isScrolling: boolean) => void) {
    const cleanup = rafCoalescedObserveElementOffset(instance as never, cb)
    if (!cleanup) throw new Error('observer 必须返回 cleanup 函数（否则卸载后无法静默）')
    return cleanup
  }

  it('cleanup 之后，virtual-core 泄漏的 debounce 通知不得再回调（CI 失败现场）', () => {
    const { scrollElement, instance } = makeInstance(100)
    const cb = vi.fn()

    const cleanup = observe(instance, cb)

    // 用户滚动一次：virtual-core 的 scroll handler 会排下它的 debounce 定时器，
    // 并同步通知 isScrolling=true（我们合帧）；随后组件卸载。
    fireEvent.scroll(scrollElement)
    // cleanup 之前的调用是合法的（含 virtual-core observe 时的初始 offset 同步）
    const beforeCleanup = cb.mock.calls.length
    cleanup()

    // 卸载之后，那个 virtual-core 从不取消的 debounce 定时器仍然会触发。
    vi.advanceTimersByTime(1_000)

    const leaked = cb.mock.calls.slice(beforeCleanup)
    if (leaked.length > 0) {
      throw new Error(
        `disposed observer still notified ${leaked.length}× after cleanup ` +
          `(calls: ${JSON.stringify(leaked)}) — 这正是 CI 上 ` +
          `「1693 passed + ReferenceError: window is not defined」的根因`,
      )
    }
  })

  it('cleanup 之后滚动的 scroll 事件（监听已移除前已排队的工作）不得再回调', () => {
    const { scrollElement, instance } = makeInstance(100)
    const cb = vi.fn()

    const cleanup = observe(instance, cb)
    // 合帧期间（rAF 还没跑）卸载 —— 挂起的 flush 必须被取消。
    fireEvent.scroll(scrollElement)
    const beforeCleanup = cb.mock.calls.length
    cleanup()
    vi.advanceTimersByTime(1_000)

    expect(cb.mock.calls.slice(beforeCleanup)).toEqual([])
  })

  it('未 cleanup 时行为不变：scroll 产生 isScrolling=true 的合帧通知', () => {
    const { scrollElement, instance } = makeInstance(100)
    const cb = vi.fn()

    const cleanup = observe(instance, cb)
    // virtual-core 的默认 observer 在 observe 时会同步回调一次 (offset, false)
    // —— 那是初始同步，不是滚动通知。
    const before = cb.mock.calls.length
    fireEvent.scroll(scrollElement)
    vi.advanceTimersByTime(20) // rAF 合帧落地

    const scrollCalls = cb.mock.calls.slice(before)
    expect(scrollCalls.length).toBeGreaterThan(0)
    expect(scrollCalls.some(([, isScrolling]) => isScrolling === true)).toBe(true)
    cleanup()
  })
})
