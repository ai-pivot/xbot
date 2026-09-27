/**
 * useKeyboardInset —— 软键盘补偿量守护测试。
 *
 * 背景（2026-09-27 用户报告「安卓浏览器键盘盖住输入框」）：
 *   ① index.html 加了 `interactive-widget=resizes-content`（新 Chrome/Firefox
 *      直接缩小布局视口）—— 此模式下 vv.height ≈ innerHeight ⇒ 补偿量必须
 *      算出 0（**不得双重补偿**）。
 *   ② 不认识该参数的浏览器（老安卓 / 国产 WebView / iOS）：布局视口不变、
 *      只有 visualViewport 缩小 ⇒ innerHeight - vv.height - vv.offsetTop
 *      = 键盘盖住布局视口底部的像素数，AppShell/MobileAppShell 用它垫高内容。
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'

import { useKeyboardInset } from './useKeyboardInset'

type FakeVV = {
  height: number
  offsetTop: number
  listeners: Record<string, Array<() => void>>
  addEventListener: (t: string, fn: () => void) => void
  removeEventListener: (t: string, fn: () => void) => void
}

function stubVisualViewport(height: number, offsetTop: number, innerHeight: number): FakeVV {
  const vv: FakeVV = {
    height,
    offsetTop,
    listeners: {},
    addEventListener(t, fn) {
      vv.listeners[t] = vv.listeners[t] ?? []
      vv.listeners[t].push(fn)
    },
    removeEventListener(t, fn) {
      vv.listeners[t] = (vv.listeners[t] ?? []).filter((f) => f !== fn)
    },
  }
  vi.stubGlobal('visualViewport', vv)
  vi.stubGlobal('innerHeight', innerHeight)
  return vv
}

describe('useKeyboardInset — 安卓键盘补偿', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('键盘关闭：补偿量 0（不改变布局）', () => {
    stubVisualViewport(800, 0, 800)
    const { result } = renderHook(() => useKeyboardInset())
    expect(result.current).toBe(0)
  })

  it('resizes-visual 模式（默认/老安卓 WebView）：键盘弹出 ⇒ 补偿 = 键盘高度', () => {
    // 布局视口 800 不变，键盘 300 ⇒ visual viewport 只剩 500。
    const vv = stubVisualViewport(500, 0, 800)
    const { result } = renderHook(() => useKeyboardInset())
    act(() => {
      vv.listeners['resize'].forEach((fn) => fn())
    })
    expect(result.current).toBe(300)
  })

  it('resizes-content 模式（新 Chrome，interactive-widget 生效）：布局视口已缩小 ⇒ 补偿必须为 0（不得双重补偿）', () => {
    // innerHeight 与 vv.height 同步缩到 500 ⇒ 公式算出 0。
    const vv = stubVisualViewport(500, 0, 500)
    const { result } = renderHook(() => useKeyboardInset())
    act(() => {
      vv.listeners['resize'].forEach((fn) => fn())
    })
    expect(result.current).toBe(0)
  })

  it('iOS Safari：页面被自动滚动（offsetTop>0）时按可见部分折算（fixed 底栏已部分可见 ⇒ 少垫）', () => {
    // 键盘 300，Safari 已把页面往上滚 120 ⇒ 视觉视口底边距布局视口底边只剩 180。
    const vv = stubVisualViewport(500, 120, 800)
    const { result } = renderHook(() => useKeyboardInset())
    act(() => {
      vv.listeners['resize'].forEach((fn) => fn())
    })
    expect(result.current).toBe(180)
  })

  it('小幅 resize（<80px 阈值）不算键盘（地址栏收展不触发补偿）', () => {
    const vv = stubVisualViewport(760, 0, 800)
    const { result } = renderHook(() => useKeyboardInset())
    act(() => {
      vv.listeners['resize'].forEach((fn) => fn())
    })
    expect(result.current).toBe(0)
  })

  it('卸载时解绑监听（无泄漏）', () => {
    const vv = stubVisualViewport(500, 0, 800)
    const { result, unmount } = renderHook(() => useKeyboardInset())
    act(() => {
      vv.listeners['resize'].forEach((fn) => fn())
    })
    expect(result.current).toBe(300)
    unmount()
    expect(vv.listeners['resize']).toHaveLength(0)
    expect(vv.listeners['scroll']).toHaveLength(0)
  })
})
