import '@testing-library/jest-dom/vitest'
import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { ThreadPanel } from './ThreadPanel'
import {
  UNKNOWN_SOURCE, messageSource, shortSessionID, sourceLabel,
  type GroupMessage,
} from './types'

/**
 * 2026-10-22：通信视图的每条事件必须能看出"是哪个会话/部门发的"。
 * 有来源 → 渠道 + 会话短 id（+ 角色）；没有来源 → 仍然渲染，但标「未知来源」。
 */

const BASE: GroupMessage = {
  id: 1,
  group_id: 'g',
  kind: 'report',
  correlation_id: 'corr-1',
  sender_type: 'system',
  sender_id: 'session:web:chat_1',
  sender_name: '朝廷议事',
  role: 'assistant',
  content: '工部回报：CI 全绿',
  created_at: new Date().toISOString(),
}

describe('会话来源标识', () => {
  it('shortSessionID 截断成尾 4 位，短 id 原样返回', () => {
    expect(shortSessionID('chat_9f3a8f3a1b2c')).toBe('…1b2c')
    expect(shortSessionID('web-1')).toBe('web-1')
    expect(shortSessionID('')).toBe('')
  })

  it('messageSource：有来源给 channel + 短 id + 角色 + 完整 key', () => {
    const src = messageSource({ ...BASE, sourceChannel: 'agent', sourceChatID: 'web:web-4/court.hubu:main', sourceRole: 'court.hubu' })
    expect(src).toMatchObject({ channel: 'agent', short: '…main', role: 'court.hubu' })
    expect(src?.key).toBe('agent:web:web-4/court.hubu:main')
    expect(sourceLabel({ ...BASE, sourceChannel: 'web', sourceChatID: 'chat_9f3a1b2c' })).toBe('web·…1b2c')
  })

  it('没有来源信息 ⇒ 标签是「未知来源」', () => {
    expect(messageSource(BASE)).toBeNull()
    expect(sourceLabel(BASE)).toBe(UNKNOWN_SOURCE)
  })
})

describe('ThreadPanel 渲染来源', () => {
  it('线程头显示部门名（组织树名），不是技术来源串', () => {
    render(
      <ThreadPanel
        messages={[{ ...BASE, laneID: 'session:web:chat_9f3a8f3a1b2c', sourceChannel: 'web', sourceChatID: 'chat_9f3a8f3a1b2c' }]}
        selected={null}
        onSelect={vi.fn()}
        onClose={vi.fn()}
        laneNameOf={(id) => (id === 'session:web:chat_9f3a8f3a1b2c' ? '工部' : id)}
      />,
    )
    // 2026-10-22 用户报障：右栏原来只显示技术来源（示例数据没填就成「未知来源」）；
    // 现在线程头按**会话归属**显示部门名，与左栏活动流一致。
    expect(screen.getByTestId('thread-author')).toHaveTextContent('工部')
  })

  it('消息行仍带来源 chip：渠道 + 会话短 id', () => {
    const msg = { ...BASE, laneID: 'session:web:chat_9f3a8f3a1b2c', sourceChannel: 'web', sourceChatID: 'chat_9f3a8f3a1b2c' }
    render(<ThreadPanel messages={[msg]} selected={msg} onSelect={vi.fn()} onClose={vi.fn()} />)
    const chip = screen.getByTestId('msg-source')
    expect(chip).toHaveTextContent('web·…1b2c')
    expect(chip).toHaveAttribute('data-source', 'web:chat_9f3a8f3a1b2c')
    expect(chip.getAttribute('title')).toContain('web:chat_9f3a8f3a1b2c')
  })

  it('缺来源的消息仍然渲染，并在消息行标「未知来源」', () => {
    const msg = { ...BASE }
    render(<ThreadPanel messages={[msg]} selected={msg} onSelect={vi.fn()} onClose={vi.fn()} />)
    expect(screen.getByTestId('msg-source')).toHaveTextContent(UNKNOWN_SOURCE)
    expect(screen.getAllByText('工部回报：CI 全绿').length).toBeGreaterThan(0)
  })
})
