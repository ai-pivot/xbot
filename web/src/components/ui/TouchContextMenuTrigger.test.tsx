/**
 * TouchContextMenuTrigger —— 安卓长按菜单守护测试。
 *
 * 背景（2026-09-27 用户报告「只有 iPhone 能长按出菜单，安卓长按都没用」）：
 * Radix ContextMenuTrigger 的触屏实现 `onPointerMove → clearLongPress` 无容差
 * + 触发元素无 select-none 时安卓原生文本选择抢手势（pointercancel 杀计时且
 * 不派发 contextmenu）。本组件用「select-none 包裹层 + 带容差的长按 → 派发
 * synthetic contextmenu」修复，这里逐条守护：
 *   ① 触屏渲染包裹层（select-none = 断文本选择根因）
 *   ② 长按 500ms（容差内抖动不取消）→ 菜单打开
 *   ③ 长按触发后抬手的 click 被拦截（不误触「切会话」）
 *   ④ 位移超过容差 → 计时取消，菜单不弹
 *   ⑤ 桌面（hover 可用）零包裹、直接透传
 */
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, act } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'

import { renderWithProviders } from '@/test-utils'
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
} from '@/components/ui/context-menu'
import { TouchContextMenuTrigger } from './TouchContextMenuTrigger'

/** 覆写 matchMedia：touch=true 时把触屏媒体查询判为真（useIsTouch）。 */
function mockTouchCapability(touch: boolean) {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: touch && query.includes('hover: none'),
    media: query,
    onchange: null,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    addListener: vi.fn(),
    removeListener: vi.fn(),
    dispatchEvent: vi.fn(),
  }))
}

function setupMenu(onSelect: () => void = vi.fn()) {
  return renderWithProviders(
    <ContextMenu>
      <TouchContextMenuTrigger asChild>
        <div role="button" onClick={vi.fn()}>row</div>
      </TouchContextMenuTrigger>
      <ContextMenuContent>
        <ContextMenuItem onSelect={onSelect}>menu item</ContextMenuItem>
      </ContextMenuContent>
    </ContextMenu>,
  )
}

describe('TouchContextMenuTrigger — 安卓长按守护', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('触屏：渲染 select-none 包裹层（断「原生文本选择抢手势」根因）', () => {
    mockTouchCapability(true)
    setupMenu()
    const wrap = screen.getByText('row').parentElement
    expect(wrap).not.toBeNull()
    expect(wrap!.className).toContain('select-none')
    expect(wrap!.className).toContain('-webkit-touch-callout:none')
  })

  it('触屏：长按 500ms 且容差内抖动 → 菜单打开（pointermove 不取消计时）', () => {
    mockTouchCapability(true)
    setupMenu()

    const row = screen.getByText('row')
    act(() => {
      fireEvent.pointerDown(row, { pointerType: 'touch', clientX: 100, clientY: 100 })
    })
    // 安卓触摸噪声：容差内（≤12px）的 move 不取消。
    act(() => {
      fireEvent.pointerMove(row, { pointerType: 'touch', clientX: 105, clientY: 106 })
    })
    // Radix 自己的 700ms 计时之前，我们 500ms 先触发。
    act(() => {
      vi.advanceTimersByTime(500)
    })

    expect(screen.getByText('menu item')).toBeInTheDocument()
  })

  it('触屏：长按触发后抬手的 click 被拦截（不把「长按」变成「点击」）', () => {
    mockTouchCapability(true)
    const rowClick = vi.fn()
    renderWithProviders(
      <ContextMenu>
        <TouchContextMenuTrigger asChild>
          <div role="button" onClick={rowClick}>row</div>
        </TouchContextMenuTrigger>
        <ContextMenuContent>
          <ContextMenuItem>menu item</ContextMenuItem>
        </ContextMenuContent>
      </ContextMenu>,
    )

    const row = screen.getByText('row')
    act(() => {
      fireEvent.pointerDown(row, { pointerType: 'touch', clientX: 50, clientY: 50 })
    })
    act(() => {
      vi.advanceTimersByTime(500)
    })
    // 安卓长按抬手仍会派发 click —— 必须被吞掉。
    act(() => {
      fireEvent.click(row)
    })
    expect(rowClick).not.toHaveBeenCalled()
  })

  it('触屏：位移超过容差（真正在划动/滚动）→ 计时取消，菜单不弹', () => {
    mockTouchCapability(true)
    setupMenu()

    const row = screen.getByText('row')
    act(() => {
      fireEvent.pointerDown(row, { pointerType: 'touch', clientX: 100, clientY: 100 })
    })
    act(() => {
      fireEvent.pointerMove(row, { pointerType: 'touch', clientX: 130, clientY: 100 })
    })
    act(() => {
      vi.advanceTimersByTime(600)
    })

    expect(screen.queryByText('menu item')).not.toBeInTheDocument()
  })

  it('桌面（hover 可用）：零包裹层 —— select-none 不加、行为完全透传', () => {
    mockTouchCapability(false)
    setupMenu()

    // 直接透传 asChild：row 元素本身不裹 div、无 select-none。
    const row = screen.getByText('row')
    expect(row.parentElement).not.toHaveClass('select-none')
    expect(screen.queryByTestId('touch-context-trigger')).not.toBeInTheDocument()

    // 桌面右键路径不受影响：contextmenu 事件仍打开菜单。
    act(() => {
      fireEvent.contextMenu(row, { clientX: 10, clientY: 10 })
    })
    expect(screen.getByText('menu item')).toBeInTheDocument()
  })
})
