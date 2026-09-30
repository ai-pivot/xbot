import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen } from '@testing-library/react'
import '@testing-library/jest-dom'

import i18n from '@/i18n'
import { renderWithProviders } from '@/test-utils'
import type { ModelEntry, Subscription } from '@/types/shared'
import { ModelSelector } from './ModelSelector'

// 菜单/按钮标签走 i18n ⇒ 断言语言两侧钉死（jsdom 的 navigator.language 是 en-US）。
beforeAll(async () => {
  await i18n.changeLanguage('zh-CN')
})

// 触屏/桌面开关（既定模式：CopyTarget.longpress.test.tsx 同款）。
const device = { touch: true }
vi.mock('@/hooks/useIsMobile', () => ({
  useIsTouch: () => device.touch,
  useIsMobile: () => false,
}))
// WS 连接与模型选择 RPC：本测试只测「打开选择器时的自动聚焦」，不真正选模型。
vi.mock('@/hooks/useWSConnection', () => ({
  useWSConnection: () => ({ send: vi.fn() }),
}))
vi.mock('@/components/agent/api', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  selectModel: vi.fn(async () => {}),
}))
// 思考模式控件与本测试无关 —— mock 掉避免其内部依赖（Slider 等）干扰聚焦断言。
vi.mock('./ThinkingModeControl', () => ({
  ThinkingModeControl: () => null,
  thinkingModeLabelI18n: () => '',
}))

const subscriptions: Subscription[] = [
  { id: 'sub-1', name: 'Sub One' } as unknown as Subscription,
]
const modelEntries: ModelEntry[] = [
  { sub_id: 'sub-1', sub_name: 'Sub One', model: 'gpt-x', status: 'normal' },
]

function renderSelector() {
  return renderWithProviders(
    <ModelSelector
      channel="web"
      chatID="chat-1"
      currentSubID="sub-1"
      currentModel="gpt-x"
      subscriptions={subscriptions}
      modelEntries={modelEntries}
      thinkingMode="default"
      busy={false}
      onModelSelected={vi.fn()}
      onThinkingModeChange={vi.fn(async () => true)}
    />,
  )
}

// ── 触屏自动聚焦守护（2026-09-30 用户报告「手机端现在切换模型就会弹出键盘」根因）──
//
// 根因（Radix 源码实证）：PopoverContent 包在 FocusScope 里，默认
// onMountAutoFocus = focusFirst(getTabbableCandidates(content)) —— 聚焦内容里
// 第一个可聚焦元素 = ModelSelector 的**搜索输入框** ⇒ 触屏上打开选择器即弹软键盘，
// 键盘盖住半个模型列表。用户要的是「点开 → 直接点模型」；搜索是显式点击搜索框
// 才该发生的事。
//
// 修复 = 触屏时 onOpenAutoFocus preventDefault（不聚焦任何元素）；桌面保持默认
//（键入即筛选的既有 UX）。下面两条分别守护两个平台的行为。
describe('ModelSelector 触屏自动聚焦（手机端打开选择器不得弹软键盘）', () => {
  beforeEach(() => {
    device.touch = true
  })

  it('触屏：打开选择器不得聚焦搜索框（聚焦可编辑元素 = 弹软键盘）', async () => {
    renderSelector()
    fireEvent.click(screen.getByRole('button', { name: '选择模型和思考模式' }))
    // popover 打开后搜索框出现。
    const search = await screen.findByPlaceholderText('搜索模型…')
    expect(search).toBeInTheDocument()
    // ⛔ 判别：搜索框绝不能被自动聚焦。
    expect(search).not.toBe(document.activeElement)
  })

  it('桌面（反向守护）：打开选择器保持自动聚焦搜索框（键入即筛选的既有 UX 不回退）', async () => {
    device.touch = false
    renderSelector()
    fireEvent.click(screen.getByRole('button', { name: '选择模型和思考模式' }))
    const search = await screen.findByPlaceholderText('搜索模型…')
    expect(search).toBeInTheDocument()
    expect(search).toBe(document.activeElement)
  })
})
