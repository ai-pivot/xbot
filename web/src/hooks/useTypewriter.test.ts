import { act, renderHook } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { useTypewriter } from './useTypewriter'

describe('useTypewriter — exponential catch-up', () => {
  it('advances gap/3 per tick (fast catch-up for large gaps, no uniform cap)', () => {
    vi.useFakeTimers()
    try {
      const { result } = renderHook(() => useTypewriter('x'.repeat(1000)))

      expect(result.current.visibleChars).toBe(0)

      // 第一个 tick：advance = gap/3 = 333（指数追赶，gap 大时大步追平，
      // 不是被上限卡成匀速导致追不上）
      act(() => { vi.advanceTimersByTime(50) })
      const first = result.current.visibleChars
      expect(first).toBeGreaterThan(100)

      // 第二个 tick：gap 缩小，advance 也随之缩小（自然减速）
      act(() => { vi.advanceTimersByTime(50) })
      const second = result.current.visibleChars
      expect(second).toBeGreaterThan(first)
      expect(second - first).toBeLessThan(first)
    } finally {
      vi.useRealTimers()
    }
  })

  it('still converges to the full text', () => {
    vi.useFakeTimers()
    try {
      const { result } = renderHook(() => useTypewriter('hello world'))

      act(() => { vi.advanceTimersByTime(5000) })
      expect(result.current.visibleChars).toBe('hello world'.length)
      expect(result.current.isTyping).toBe(false)
    } finally {
      vi.useRealTimers()
    }
  })
})
