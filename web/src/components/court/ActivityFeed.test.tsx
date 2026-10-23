import '@testing-library/jest-dom/vitest'
/**
 * 活动流（默认视图）：空态会教人 + 每条事件带类型/部门/时间 + 时间轴实时过滤。
 */
import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { ActivityFeed, messageAuthor, messageSummary, relativeTime } from './ActivityFeed'
import type { GroupMessage, Lane } from './types'

const now = Date.now()

function msg(id: number, kind: string, minutesAgo: number, extra: Partial<GroupMessage> = {}): GroupMessage {
  return {
    id,
    group_id: 'g',
    kind,
    sender_type: 'agent',
    sender_id: 'agent:court.hubu/main',
    sender_name: 'court.hubu',
    role: 'court.hubu',
    content: `第 ${id} 条内容`,
    created_at: new Date(now - minutesAgo * 60e3).toISOString(),
    ...extra,
  }
}

const LANES: Lane[] = [
  { id: 'agent:court.hubu/main', name: '户部', type: 'agent', status: 'busy', task: '清理 worktree', progress: 0.4 },
  { id: 'agent:court.gongbu/main', name: '工部', type: 'agent', status: 'idle', task: '部署', progress: 0.1 },
]

const base = {
  lanes: LANES,
  selectedId: null,
  onSelect: () => {},
  laneNameOf: (id: string) => id.replace('agent:', '').split('/')[0],
  cutoff: now + 60e3,
  onRefresh: () => {},
}

describe('活动流', () => {
  it('空态：说清「这里会出现什么」，并给「发送一条测试消息 / 看示例」两个按钮', () => {
    render(<ActivityFeed {...base} messages={[]} onSendTest={() => {}} />)
    expect(screen.getByTestId('feed-empty')).toHaveTextContent('还没有结构化消息')
    expect(screen.getByTestId('feed-send-test')).toBeInTheDocument()
    expect(screen.getByTestId('feed-show-sample')).toBeInTheDocument()
  })

  it('空态只留事实 + 引导，不再自我辩解「这不代表页面坏了」', () => {
    render(<ActivityFeed {...base} messages={[]} onSendTest={() => {}} />)
    const empty = screen.getByTestId('feed-empty')
    expect(empty).toHaveTextContent('部门汇报、请旨（提问）、批红会出现在这里')
    expect(empty).not.toHaveTextContent('这不代表页面坏了')
    expect(empty.querySelectorAll('button').length).toBeGreaterThanOrEqual(3)
  })

  it('点「看示例」⇒ 出现 3 条标注「示例」的灰态数据', () => {
    render(<ActivityFeed {...base} messages={[]} />)
    fireEvent.click(screen.getByTestId('feed-show-sample'))
    const sample = screen.getByTestId('feed-sample')
    expect(sample).toHaveTextContent('示例')
    expect(sample.querySelectorAll('li')).toHaveLength(3)
  })

  it('有数据：每条带类型名、部门名、相对时间', () => {
    const rows = [
      msg(1, 'report', 30, { payload: { summary: '清理 12 个过期工作区，释放 41GB' } }),
      msg(2, 'ask_user', 5, { content: '是否批准释放 3 台空转 spot？', correlation_id: 'c1' }),
    ]
    render(<ActivityFeed {...base} messages={rows} />)
    const items = screen.getAllByTestId('feed-item')
    expect(items).toHaveLength(2)
    expect(items[0]).toHaveTextContent('汇报')
    expect(items[0]).toHaveTextContent('户部')
    expect(items[0]).toHaveTextContent('30 分钟前')
    expect(items[0]).toHaveTextContent('清理 12 个过期工作区，释放 41GB')
    expect(items[1]).toHaveTextContent('提问')
    expect(items[1]).toHaveTextContent('待回答')
  })

  it('时间轴游标（cutoff）实时过滤：拖动即少行，不是松手才刷新', () => {
    const rows = [msg(1, 'report', 60), msg(2, 'report', 10)]
    const { rerender } = render(<ActivityFeed {...base} messages={rows} />)
    expect(screen.getAllByTestId('feed-item')).toHaveLength(2)
    rerender(<ActivityFeed {...base} messages={rows} cutoff={now - 30 * 60e3} />)
    expect(screen.getAllByTestId('feed-item')).toHaveLength(1)
  })

  it('点一条 ⇒ onSelect 收到该消息（再点一次取消选中）', () => {
    const onSelect = vi.fn()
    const rows = [msg(1, 'report', 3)]
    const { rerender } = render(<ActivityFeed {...base} messages={rows} onSelect={onSelect} />)
    fireEvent.click(screen.getAllByTestId('feed-item')[0])
    expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ id: 1 }))
    rerender(<ActivityFeed {...base} messages={rows} onSelect={onSelect} selectedId={1} />)
    fireEvent.click(screen.getAllByTestId('feed-item')[0])
    expect(onSelect).toHaveBeenLastCalledWith(null)
  })

  it('窗口内没有事件（但有历史）⇒ 提示把时间轴往右拖，而不是「页面坏了」', () => {
    render(<ActivityFeed {...base} messages={[msg(1, 'report', 600)]} cutoff={now - 24 * 3600e3} />)
    expect(screen.getByTestId('feed-empty-window')).toHaveTextContent('这个时间窗口里没有事件')
  })
})

describe('活动流纯函数', () => {
  it('相对时间粒度', () => {
    const t = (min: number) => relativeTime(new Date(now - min * 60e3).toISOString(), now)
    expect(t(0)).toBe('刚刚')
    expect(t(5)).toBe('5 分钟前')
    expect(t(120)).toBe('2 小时前')
    expect(t(24 * 60)).toBe('昨天')
  })

  it('摘要优先 payload.summary / question，其次正文首行', () => {
    expect(messageSummary(msg(1, 'report', 1, { payload: { summary: 'S' }, content: 'C' }))).toBe('S')
    expect(messageSummary(msg(1, 'ask_user', 1, { payload: { question: 'Q' }, content: 'C' }))).toBe('Q')
    expect(messageSummary(msg(1, 'event', 1, { content: '第一行\n第二行' }))).toBe('第一行')
    expect(messageSummary(msg(1, 'event', 1, { content: '' }))).toBe('（无正文）')
    // 回答的机器串 q0:yes 不能原样出现在活动流里（用户原话：只有 q1:yes 这种裸文本）
    expect(messageSummary(msg(1, 'answer', 1, { content: 'q0:yes' }))).toBe('已回答：yes')
    expect(messageSummary(msg(1, 'answer', 1, { content: 'q2:later' }))).toBe('已回答：later')
  })

  it('作者名优先组织树，其次来源角色，最后 sender_name', () => {
    expect(messageAuthor(msg(1, 'report', 1), (id) => (id === 'agent:court.hubu/main' ? '户部' : ''))).toBe('户部')
    expect(messageAuthor(msg(1, 'report', 1, { sender_id: 'agent:court.li/main', sourceChannel: 'agent', sourceChatID: 'chat_li', sourceRole: 'court.li' }), () => '')).toBe('court.li')
  })

  it('完全拿不到来源 ⇒ 写「未知来源」，header 不会整行消失', () => {
    const orphan = msg(1, 'event', 1, { sender_id: '', sender_name: '' })
    expect(messageAuthor(orphan, () => '')).toBe('未知来源')
    render(<ActivityFeed {...base} messages={[orphan]} />)
    const item = screen.getByTestId('feed-item')
    expect(item).toHaveTextContent('未知来源')
    expect(item).toHaveTextContent('事件')
  })
})
