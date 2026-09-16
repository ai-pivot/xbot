import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { UserMessage } from './UserMessage'

// 2026-09-16 用户报告（真实手机）：
//   ① 「手机上 user msg 宽度能超出屏幕」
//   ② 「你没给手机复制 user msg 的交互」
//   ③ 「手机长按老是变出那个蓝色选中判定，位置还根本不对」
// 三条都只在**触屏**（hover: none / pointer: coarse）设备上暴露，所以这里固定 touch=true。
const device = { touch: true }
vi.mock('@/hooks/useIsMobile', () => ({
  useIsTouch: () => device.touch,
  useIsMobile: () => false,
}))

const writeText = vi.fn()

beforeEach(() => {
  device.touch = true
  writeText.mockReset()
  writeText.mockResolvedValue(undefined)
  Object.assign(navigator, { clipboard: { writeText } })
})

/** 按 lucide 图标找按钮（避免依赖当前语言下的 aria-label 文案）。 */
function buttonWithIcon(icon: string) {
  return screen.getAllByRole('button').find((b) => b.querySelector(`svg.lucide-${icon}`))
}

describe('UserMessage — 手机端（2026-09-16 用户报告）', () => {
  it('① 气泡带宽度约束：长内容不得把气泡撑出屏幕', () => {
    renderWithProviders(<UserMessage content={'x'.repeat(300)} />)
    const bubble = screen.getByTestId('user-bubble')
    // min-w-0：让 flex 子项可收缩到内容宽度以下（否则 min-content 会把整行撑宽）
    expect(bubble.className).toContain('min-w-0')
    expect(bubble.className).toContain('max-w-full')
    // 长 token（URL / token / 长英文词）必须能断行
    expect(bubble.className).toContain('break-words')
    // markdown 产出的任意宽子元素（宽表格 / 图片 / <pre>）也不许超出气泡
    expect(bubble.className).toContain('[&_*]:max-w-full')
  })

  it('② 触屏：复制按钮常显（44px 命中区），点一下复制整条消息', async () => {
    renderWithProviders(<UserMessage content="copy me please" />)
    const btn = buttonWithIcon('copy')
    expect(btn).toBeDefined()
    // 触屏没有 hover：不能靠 hover 显形，必须常显且命中区加高加宽
    expect(btn!.className).toContain('opacity-100')
    expect(btn!.className).toContain('h-9')

    fireEvent.click(btn!)
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('copy me please'))
    // 复制后要有反馈（图标切成 check）
    await waitFor(() => expect(buttonWithIcon('check')).toBeDefined())
  })

  it('③ 触屏：禁用原生文本选择与 iOS 长按 callout（长按不再弹蓝色选中控件）', () => {
    renderWithProviders(<UserMessage content="long press me" />)
    const bubble = screen.getByTestId('user-bubble')
    // 原生选择被关掉 ⇒ 长按不再出现浏览器蓝色高亮/选择控件（其位置在虚拟滚动容器里不受控）
    expect(bubble.className).toContain('select-none')
    expect(bubble.className).toContain('-webkit-touch-callout')
  })

  it('③ 桌面：不抑制原生选择（拖选文本仍然可用）', () => {
    device.touch = false
    renderWithProviders(<UserMessage content="desktop selectable" />)
    const bubble = screen.getByTestId('user-bubble')
    expect(bubble.className).not.toContain('select-none')
  })

  it('② 桌面（有 hover）：复制按钮仍在，但走半透明 + hover 显形', () => {
    device.touch = false
    renderWithProviders(<UserMessage content="desktop" />)
    const btn = buttonWithIcon('copy')
    expect(btn).toBeDefined()
    expect(btn!.className).toContain('hover:opacity-100')
    expect(btn!.className).not.toContain('h-9')
  })

  it('复制失败（无剪贴板权限）时静默，不抛错', async () => {
    writeText.mockRejectedValue(new Error('denied'))
    renderWithProviders(<UserMessage content="nope" />)
    fireEvent.click(buttonWithIcon('copy')!)
    await waitFor(() => expect(writeText).toHaveBeenCalled())
  })
})
