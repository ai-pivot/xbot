import '@testing-library/jest-dom/vitest'
/**
 * 2026-10-22 用户报障：右栏「对话线程」14 条全部显示「未知来源」，而左栏活动流
 * 同一批消息都能显示部门名；且每条都写「已闭环 6 条」，像写死的。
 *
 * 根因：右栏只读技术来源（sourceChannel/sourceRole），示例数据没有这些字段 →
 * 全部落到「未知来源」；线程归属其实是**会话**（laneID），不是 source。
 */
import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { ThreadList, threadAuthor, threadGroups, threadLane } from './ThreadPanel'
import { buildDemoMessages } from './seed'
import type { GroupMessage } from './types'

const LANES: Record<string, string> = {
  'web-1': '首辅',
  'agent:court.gongbu/main': '工部',
  'agent:court.hubu/main': '户部',
  'agent:court.hanlin/main': '翰林院',
  'agent:court.bingbu/main': '兵部',
  'agent:court.ducha/main': '都察院',
  'agent:court.sili/main': '司礼监',
  'agent:court.menxia/main': '门下省',
}
const laneNameOf = (id: string) => LANES[id] ?? id

function m(id: number, corr: string, extra: Partial<GroupMessage> = {}): GroupMessage {
  return {
    id,
    group_id: 'g',
    kind: 'report',
    correlation_id: corr,
    sender_type: 'user',
    sender_id: 'web-1',
    sender_name: 'web-1',
    role: 'user',
    content: `第 ${id} 条`,
    created_at: new Date(Date.now() - id * 60e3).toISOString(),
    ...extra,
  }
}

const base = { selected: null, onSelect: () => {} }

describe('线程归属（右栏部门名）', () => {
  it('同一会话的线程 ⇒ 显示组织树里的部门名', () => {
    const [t] = threadGroups([
      m(1, 'c1', { laneID: 'agent:court.gongbu/main' }),
      m(2, 'c1', { laneID: 'agent:court.gongbu/main' }),
    ])
    expect(threadLane(t)).toBe('agent:court.gongbu/main')
    expect(threadAuthor(t, laneNameOf)).toBe('工部')
  })

  it('跨会话的线程 ⇒ 写「多部门」，不挑多数派冒充', () => {
    const [t] = threadGroups([
      m(1, 'c1', { laneID: 'agent:court.gongbu/main' }),
      m(2, 'c1', { laneID: 'web-1' }),
    ])
    expect(threadAuthor(t, laneNameOf)).toBe('多部门')
  })

  it('既没有会话也没有来源 ⇒ 才写「未知来源」', () => {
    const [t] = threadGroups([m(1, 'c1', { sender_id: '', sender_name: '' })])
    expect(threadAuthor(t, () => '')).toBe('未知来源')
  })

  it('右栏渲染：部门名 + 真实条数（不是写死的 6）', () => {
    const msgs = [
      m(1, 'c1', { laneID: 'agent:court.gongbu/main' }),
      m(2, 'c1', { laneID: 'agent:court.gongbu/main' }),
      m(3, 'c1', { laneID: 'agent:court.gongbu/main' }),
    ]
    const { container } = render(<ThreadList {...base} messages={msgs} laneNameOf={laneNameOf} />)
    expect(container.querySelector('[data-testid="thread-author"]')?.textContent).toBe('工部')
    expect(container.textContent).toContain('3 条')
    expect(container.textContent).not.toContain('6 条')
  })
})

describe('示例数据（seed）', () => {
  it('每条消息都带 laneID / 来源，且线程条数不再一律相同', () => {
    const msgs = buildDemoMessages()
    expect(msgs.every((x) => x.laneID && x.sourceChannel && x.sourceChatID)).toBe(true)
    const threads = threadGroups(msgs).filter((t) => t.key !== 'singles')
    const sizes = new Set(threads.map((t) => t.list.length))
    expect(sizes.size).toBeGreaterThan(1)
    // 同一次对话的消息落在同一个会话（线程归属唯一）
    expect(threads.every((t) => new Set(t.list.map((x) => x.laneID)).size === 1)).toBe(true)
    // 部门名全部落在真实编制里
    const real = new Set(['首辅', '工部', '户部', '翰林院', '兵部', '都察院', '司礼监', '门下省'])
    for (const t of threads) expect(real.has(threadAuthor(t, laneNameOf))).toBe(true)
  })
})
