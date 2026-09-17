import { describe, expect, it } from 'vitest'

import { deriveRows } from './derive'
import { rowsToChatMessages } from './integrate'
import { normalizeEvent } from './normalize'
import { reduce } from './reduce'
import { initialChatState } from './types'
import type { DomainEvent } from './types'

/**
 * 端到端管线 REPRO（用户报告："!pwd 发出去之后消息直接消失了 / 还是不显示"）。
 *
 * 断言的是**整条渲染链**（与线上同一条链路）：
 *   原始 SSE 事件 → normalizeEvent → reduce → deriveRows → rowsToChatMessages
 *   （= AgentPanel 传给 MessageList 的 `messages`）。
 *
 * 命令回复（`!cmd`）的 text 事件**没有 turn_id**（后端命令分发不分配 turn），
 * 因此链路里每一步都必须保留它：reduce → standalone 段；derive → 排在 turns
 * 之后；integrate → kind='committed' 映射成 assistant 消息。
 */
describe('命令回复（!cmd）整条渲染链', () => {
  it('无 turn_id 的 text 事件必须最终出现在 messages 里（user 行也不消失）', () => {
    let s = initialChatState('chat-1')
    // 用户敲 `!pwd`：乐观行
    s = reduce(s, {
      type: 'user_sent',
      row: {
        id: 'u-cmd', content: '!pwd' as never, timestamp: 't0', isNotification: false,
        queued: false, sending: true, requestID: 'r-cmd', turnHint: undefined, dbID: undefined,
      },
    })
    // REST ack：命令没有 turn_id / message_id
    s = reduce(s, { type: 'user_ack', requestID: 'r-cmd', dbID: 0, turnHint: 0, queued: false })

    // 原始 SSE 事件（与探针抓到的线上事件同形：无 turn_id 字段）
    const evs = normalizeEvent(
      { type: 'text', content: '```\n/root\n```', chat_id: 'chat-1', channel: 'web' } as never,
      'chat-1',
    )
    expect(evs, 'normalizeEvent 必须产出事件（否则前端根本没处理这条 text）').not.toBeNull()
    for (const e of evs as DomainEvent[]) s = reduce(s, e)

    const rows = deriveRows(s)
    const msgs = rowsToChatMessages(rows)

    expect(
      msgs.filter(Boolean).some((mm) => String(mm.content ?? '').includes('/root')),
      `命令输出必须出现在最终 messages 里；rows=${rows.map((r) => r.kind).join(',')}`,
    ).toBe(true)
    expect(
      msgs.some((mm) => mm.role === 'user' && String(mm.content) === '!pwd'),
      '用户消息（乐观行）不得消失',
    ).toBe(true)
  })
})
