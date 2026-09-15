import { describe, expect, it, vi } from 'vitest'
import { fireEvent, screen } from '@testing-library/react'
import '@testing-library/jest-dom'

import i18n from '@/i18n'
import { renderWithProviders } from '@/test-utils'
import { ChannelPicker, availableChannels } from './ChannelPicker'
import type { SessionStore } from '@/hooks/useSessionStore'
import type { SessionInfo } from '@/types/shared'

// 需求（用户 2026-09-15）：「sessions 侧边栏哪有渠道选择功能？」—— 会话面板里必须有
// **渠道下拉**（9d7e99fe 把左栏换成 PanelDock 后，下拉只留在 SessionSidebar、而它只被
// 手机抽屉渲染 ⇒ 桌面丢了入口）。这组测试锁死：下拉渲染、渠道集合由会话推导、点击切换。

const mocks = vi.hoisted(() => ({ setActiveChannel: vi.fn() }))

const sessions = [
  { chatID: 'feishu:1', channel: 'feishu', label: 'fs' },
  { chatID: 'web:1', channel: 'web', label: 'web' },
  { chatID: 'agent:a/1', channel: 'agent', label: 'sub' },
  { chatID: 'web:2', channel: 'web', label: 'web2', parentChannel: 'qq' },
  { chatID: 'web:3', channel: 'web', label: 'web3' },
] as unknown as SessionInfo[]

vi.mock('@/hooks/useSessionStore', () => ({
  useSessionStore: (): SessionStore =>
    ({
      sessions,
      activeChannel: null,
      setActiveChannel: mocks.setActiveChannel,
    }) as unknown as SessionStore,
}))

describe('ChannelPicker（会话面板/抽屉共用下拉）', () => {
  it('availableChannels：去重、排除内部 agent、按预设顺序（web→cli→feishu→qq→napcat）', () => {
    expect(availableChannels(sessions)).toEqual(['web', 'feishu', 'qq'])
  })

  it('渲染当前渠道标签（无筛选 = 全部渠道）', () => {
    renderWithProviders(<ChannelPicker />)
    const trigger = screen.getByTestId('channel-picker')
    expect(trigger).toBeTruthy()
    expect(trigger.textContent).toContain(i18n.t('channel.all'))
  })

  it('展开后列出「全部」+ 每个渠道；点击渠道调用 setActiveChannel(渠道)', () => {
    mocks.setActiveChannel.mockClear()
    renderWithProviders(<ChannelPicker />)
    fireEvent.click(screen.getByTestId('channel-picker'))

    const options = screen.getAllByTestId('channel-option')
    expect(options.map((o) => o.getAttribute('data-channel'))).toEqual(['__all__', 'web', 'feishu', 'qq'])

    fireEvent.click(options.find((o) => o.getAttribute('data-channel') === 'feishu')!)
    expect(mocks.setActiveChannel).toHaveBeenCalledWith('feishu')
  })

  it('「全部」= setActiveChannel(null)（清除渠道筛选）', () => {
    mocks.setActiveChannel.mockClear()
    renderWithProviders(<ChannelPicker />)
    fireEvent.click(screen.getByTestId('channel-picker'))
    fireEvent.click(screen.getAllByTestId('channel-option')[0])
    expect(mocks.setActiveChannel).toHaveBeenCalledWith(null)
  })
})
