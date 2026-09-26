import { describe, expect, it } from 'vitest'

import { deriveRows } from './derive'
import { historyToReplaced, rowsToChatMessages } from './integrate'
import { reduce } from './reduce'
import { initialChatState } from './types'
import { bindTurnIDs, orderMessageRows } from '@/components/agent/messageOrder'
import type { ChatMessage } from '@/types/shared'

/**
 * 端到端管线 REPRO（P0 2026-09-26 用户报告：「我发了个继续然后没有压缩，但是前端
 * 显示我这里压缩了。我感觉是前面的压缩渲染错了位置」）。
 *
 * 线上根因：`deriveTurnIDs`（后端）把 turn_id=0 的压缩标记 [Compacted context]
 * 绑到了**它之后最近的 user 行所属的 turn**（用户当前 turn），于是前端
 * `historyToReplaced` 的「每 turn 只取第一条 user」让标记抢占了该 turn 的 user
 * 槽位，**顶掉了用户真实消息**。
 *
 * 正确契约：压缩标记是「无 turn 的独立展示行」——standalone + 锚点（它所在的
 * turn），既不占用任何 turn 的 user 槽位，又按锚点插回它发生的位置。
 */
function histMsg(over: Partial<ChatMessage> & Pick<ChatMessage, 'id' | 'role' | 'content' | 'turnID' | 'dbID'>): ChatMessage {
  return {
    iterations: [],
    timestamp: '2026-09-26T05:41:00Z',
    isPartial: false,
    ...over,
  } as ChatMessage
}

describe('压缩标记（[Compacted context]）渲染链', () => {
  it('绝不顶掉用户消息，且插回它发生的位置（turn 3 之后、turn 4 之前）', () => {
    const msgs: ChatMessage[] = [
      histMsg({ id: 'u3', role: 'user', content: 'turn3 用户消息', turnID: 3, dbID: 100 }),
      histMsg({ id: 'a3', role: 'assistant', content: 'turn3 回复', turnID: 3, dbID: 101 }),
      // 压缩标记（后端修复后的形态：standalone + anchor=3）
      histMsg({
        id: 'marker', role: 'user', content: '[Compacted context]\n\nsummary', turnID: 0, dbID: 150,
        standalone: true, anchorTurnID: 3,
      }),
      histMsg({ id: 'u4', role: 'user', content: '继续优化到 7ms，你的上下文无限', turnID: 4, dbID: 160 }),
      histMsg({ id: 'a4', role: 'assistant', content: 'turn4 回复', turnID: 4, dbID: 161 }),
    ]

    let s = initialChatState('chat-1')
    s = reduce(s, historyToReplaced(msgs, null))
    const rows = orderMessageRows(bindTurnIDs(rowsToChatMessages(deriveRows(s))))

    // ① turn 4 的 user 行必须是用户真实消息 —— 绝不是压缩标记（本 bug 的核心症状）。
    const turn4User = rows.find((r) => r.turnID === 4 && r.role === 'user')
    expect(turn4User, 'turn 4 的 user 行必须存在').toBeTruthy()
    expect(turn4User!.content, '用户消息绝不能被压缩标记顶掉').toBe('继续优化到 7ms，你的上下文无限')

    // ② 压缩标记必须存在，且保持「无 turn」（不占用任何 turn 的槽位）。
    const marker = rows.find((r) => String(r.content).startsWith('[Compacted context]'))
    expect(marker, '压缩标记必须渲染（信息不能丢）').toBeTruthy()
    expect(marker!.turnID, '压缩标记必须保持 turnID=0').toBe(0)

    // ③ 位置：turn 3 → 标记 → turn 4 user（marker 按 anchor=3 插在 turn 3 之后）。
    const idxA3 = rows.findIndex((r) => r.turnID === 3 && r.role === 'assistant')
    const idxMarker = rows.indexOf(marker!)
    const idxU4 = rows.indexOf(turn4User!)
    expect(idxA3, 'turn 3 行必须存在').toBeGreaterThanOrEqual(0)
    expect(idxA3, '标记必须排在 turn 3 之后').toBeLessThan(idxMarker)
    expect(idxMarker, '标记必须排在 turn 4 用户消息之前').toBeLessThan(idxU4)
  })

  it('压缩标记（standalone）不被 bindTurnIDs 重新绑到后续 turn', () => {
    // 防御性回归：即使标记进入渲染层时是 turnID=0 且 non-standalone 之外的形态，
    // bindTurnIDs 也绝不能把它绑到「后面的 user 行所属 turn」——那会让它顶掉用户消息。
    const msgs: ChatMessage[] = [
      histMsg({ id: 'u3', role: 'user', content: 'turn3 用户消息', turnID: 3, dbID: 100 }),
      histMsg({
        id: 'marker', role: 'user', content: '[Compacted context]\n\nsummary', turnID: 0, dbID: 150,
        standalone: true, anchorTurnID: 3,
      }),
      histMsg({ id: 'u4', role: 'user', content: '继续', turnID: 4, dbID: 160 }),
      histMsg({ id: 'a4', role: 'assistant', content: 'turn4 回复', turnID: 4, dbID: 161 }),
    ]
    let s = initialChatState('chat-1')
    s = reduce(s, historyToReplaced(msgs, null))
    const base = rowsToChatMessages(deriveRows(s))
    const bound = bindTurnIDs(base)
    const marker = bound.find((r) => r && String(r.content).startsWith('[Compacted context]'))
    expect(marker, '标记必须在').toBeTruthy()
    expect(marker!.turnID, '标记必须保持 turnID=0（standalone 跳过绑定）').toBe(0)
  })
})
