/**
 * 结构化消息行渲染的纯函数测试（P3 视觉规范的可验证部分）：
 *   - 时间戳必须是 zh-CN 24 小时制 HH:mm:ss，不是美式 AM/PM；
 *   - kind 徽标区色互不相同（ask_user 琥珀 / report 紫 / decision 绿 / answer 绿描边 /
 *     event 灰 / text 蓝）；
 *   - event/text/command 走紧凑行（32–36px 高度），卡片不压缩；
 *   - 收件箱相对时间（「3 分钟前」）。
 */
import { describe, expect, it } from 'vitest'

import { relativeTime } from './Inbox'
import {
  KIND_META,
  formatClock,
  formatSender,
  isCompactKind,
  messageSummary,
} from './StructuredCards'
import type { StructuredKind, StructuredMessage } from './types'

function msg(partial: Partial<StructuredMessage>): StructuredMessage {
  return {
    message_id: 1,
    kind: 'event',
    direction: 'in',
    answered: false,
    cancelled: false,
    text: '',
    sender: { type: 'user', id: 'web-1' },
    created_at: '2026-10-20T02:41:44Z',
    ...partial,
  }
}

describe('formatClock', () => {
  it('renders HH:mm:ss in zh-CN 24h form (never AM/PM)', () => {
    const out = formatClock('2026-10-20T02:41:44Z')
    expect(out).toMatch(/^\d{2}:\d{2}:\d{2}$/)
    expect(out).not.toMatch(/AM|PM|上午|下午/)
  })

  it('falls back to a placeholder for invalid timestamps', () => {
    expect(formatClock('not-a-date')).toBe('--:--:--')
  })
})

describe('kind badges', () => {
  it('gives every kind a distinct label and colour class', () => {
    const kinds: StructuredKind[] = [
      'ask_user',
      'report',
      'decision',
      'answer',
      'event',
      'text',
      'command',
    ]
    const badges = kinds.map((k) => KIND_META[k].badge)
    expect(new Set(badges).size).toBe(kinds.length)
    expect(KIND_META.ask_user.badge).toContain('amber')
    expect(KIND_META.report.badge).toContain('violet')
    expect(KIND_META.decision.badge).toContain('emerald')
    expect(KIND_META.answer.badge).toContain('emerald')
    // answer 是绿描边（透明底），decision 是绿实心。
    expect(KIND_META.answer.badge).toContain('bg-transparent')
    expect(KIND_META.decision.badge).not.toContain('bg-transparent')
    expect(KIND_META.event.badge).toContain('slate')
    expect(KIND_META.text.badge).toContain('sky')
  })

  it('only event/text/command use the compact 32–36px row', () => {
    expect(isCompactKind('event')).toBe(true)
    expect(isCompactKind('text')).toBe(true)
    expect(isCompactKind('command')).toBe(true)
    for (const k of ['ask_user', 'report', 'decision', 'answer'] as StructuredKind[]) {
      expect(isCompactKind(k)).toBe(false)
    }
  })
})

describe('row summary', () => {
  it('prefers text and derives from payload otherwise', () => {
    expect(messageSummary(msg({ kind: 'event', text: '状态变更' }))).toBe('状态变更')
    expect(
      messageSummary(
        msg({ kind: 'report', text: '', payload: { summary: '扩容完成 8 节点' } }),
      ),
    ).toBe('扩容完成 8 节点')
    expect(
      messageSummary(
        msg({
          kind: 'ask_user',
          text: '',
          payload: { questions: [{ id: 'q1', question: '是否扩容？' }] },
        }),
      ),
    ).toBe('是否扩容？')
  })

  it('falls back to sender id when name is absent', () => {
    expect(formatSender(msg({ sender: { type: 'agent', id: 'hubu/main' } }))).toBe('hubu/main')
    expect(formatSender(msg({ sender: { type: 'user', id: 'web-1', name: '皇帝' } }))).toBe('皇帝')
  })
})

describe('relativeTime', () => {
  const now = Date.parse('2026-10-20T03:00:00Z')

  it('renders 刚刚 / N 分钟前 / N 小时前', () => {
    expect(relativeTime('2026-10-20T02:59:40Z', now)).toBe('刚刚')
    expect(relativeTime('2026-10-20T02:57:00Z', now)).toBe('3 分钟前')
    expect(relativeTime('2026-10-20T01:00:00Z', now)).toBe('2 小时前')
    expect(relativeTime('2026-10-18T03:00:00Z', now)).toBe('2 天前')
  })

  it('falls back to M月D日 HH:mm for old items and empty for invalid', () => {
    expect(relativeTime('2026-09-01T02:41:44Z', now)).toMatch(/\d+月\d+日/)
    expect(relativeTime('bad', now)).toBe('')
  })
})
