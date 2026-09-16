import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import '@testing-library/jest-dom'

import { CopyTarget } from './MessageActions'

// 2026-09-16 用户报告：
//   ② 「你没给手机复制 user msg 的交互」
//   ③ 「手机长按老是变出那个蓝色选中判定，位置还根本不对」
// ② 根因：长按判定 onPointerMove **任何位移都取消计时**（无容差），触屏手指必然抖动
//    一两像素 ⇒ 480ms 计时几乎永远被清掉 ⇒ 手机上长按不出复制菜单。
// ③ 根因：消息内容是可选文本，长按触发**浏览器原生**选择（蓝色高亮 + 原生气泡），
//    其位置在虚拟滚动 + transform 容器里不受我们控制；触屏上必须抑制，把长按让给复制菜单。
const device = { touch: true }
vi.mock('@/hooks/useIsMobile', () => ({
  useIsTouch: () => device.touch,
  useIsMobile: () => false,
}))

const child = <span>target</span>

function menuOpen() {
  return document.querySelector('[data-testid="copy-sheet"], [data-testid="copy-menu"]')
}

function renderTarget() {
  const { container } = render(
    <CopyTarget kind="message" message={{ role: 'user', content: 'hi' } as never}>
      {child}
    </CopyTarget>,
  )
  return container.querySelector('[data-copy-target="message"]') as HTMLElement
}

/** 假定时器下必须用 act() 包住时间推进，否则 React 状态更新不会被 flush。 */
function advance(ms: number) {
  act(() => {
    vi.advanceTimersByTime(ms)
  })
}

describe('CopyTarget 长按（触屏抖动容差）', () => {
  beforeEach(() => {
    device.touch = true
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('手指轻微抖动（4px）后长按仍能弹出复制菜单', () => {
    const node = renderTarget()
    fireEvent.pointerDown(node, { pointerType: 'touch', clientX: 100, clientY: 100 })
    // 抖动：旧实现在这里就 clear() 了，菜单永远不会出现
    fireEvent.pointerMove(node, { pointerType: 'touch', clientX: 104, clientY: 103 })
    advance(520)
    expect(menuOpen()).not.toBeNull()
  })

  it('真正划动（>10px）时取消长按，不弹菜单', () => {
    const node = renderTarget()
    fireEvent.pointerDown(node, { pointerType: 'touch', clientX: 100, clientY: 100 })
    fireEvent.pointerMove(node, { pointerType: 'touch', clientX: 140, clientY: 100 })
    advance(520)
    expect(menuOpen()).toBeNull()
  })

  it('鼠标按下不触发长按，右键才弹菜单', () => {
    const node = renderTarget()
    fireEvent.pointerDown(node, { pointerType: 'mouse', clientX: 10, clientY: 10 })
    advance(600)
    expect(menuOpen()).toBeNull()

    // 右键是桌面入口（菜单项里必然有"复制…"，且不止一项）
    fireEvent.contextMenu(node, { clientX: 10, clientY: 10 })
    expect(menuOpen()).not.toBeNull()
    expect(screen.getAllByText(/复制/).length).toBeGreaterThan(0)
  })

  it('③ 触屏：复制面禁用原生选择与 callout（长按不再弹蓝色选中控件）', () => {
    const node = renderTarget()
    expect(node.className).toContain('select-none')
    expect(node.className).toContain('-webkit-touch-callout')
    // min-w-0 不能被丢掉（2026-09-15 的教训）
    expect(node.className).toContain('min-w-0')
  })

  it('③ 桌面：不抑制原生选择（拖选文本仍可用）', () => {
    device.touch = false
    const node = renderTarget()
    expect(node.className).not.toContain('select-none')
  })
})
