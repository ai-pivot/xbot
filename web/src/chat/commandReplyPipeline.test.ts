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
      { type: 'text', content: '```\n/root\n```', chat_id: 'chat-1', channel: 'web', metadata: { command_reply: 'true' } } as never,
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

  it('standalone 行必须标记为「无 turn」（否则 bindTurnIDs 绑到 live turn → 虚拟列表键撞车）', () => {
    // CI 真实 Chromium 实证的尺寸缓存串味根因：standalone 行是 assistant 且 turnID=0
    // → `bindTurnIDs` 把它绑到**最近的前一个 turn**（= 正在跑的那个）→ 与 live 行
    // 的虚拟列表 key 完全相同（`turn-N-assistant`）→ `itemSizeCache`/heightMemory 被
    // 两行共用 ⇒ 总高翻倍（实测 wrapperHeight=17320＝8660×2）、命令输出被推到可视区
    // 之上（用户看到"没有输出"）。
    let s = initialChatState('chat-1')
    // live turn（正在跑）
    s = reduce(s, {
      type: 'turn_started',
      turnID: 1 as never,
      trigger: 'user' as never,
      requestID: null,
      content: null,
    })
    // 命令回复（无 turn_id）
    const evs = normalizeEvent(
      { type: 'text', content: '```\n/root\n```', chat_id: 'chat-1', channel: 'web', metadata: { command_reply: 'true' } } as never,
      'chat-1',
    )
    for (const e of evs as DomainEvent[]) s = reduce(s, e)

    const standalone = s.standalone[0]
    expect(standalone, 'standalone 行必须存在').toBeDefined()

    // ① 显式「无 turn」标记：`bindTurnIDs` 见到它就跳过绑定（否则会绑到 live turn、
    //    与 live 行撞虚拟列表 key → 尺寸缓存串味 → 总高翻倍 → 输出被推到可视区之上）
    expect(
      standalone.standalone,
      'standalone 行必须带「无 turn」标记（否则 bindTurnIDs 会把它绑到 live turn）',
    ).toBe(true)

    // ② 由此推出的虚拟列表键不再可能与任何 turn 行相同（getItemKey/rowMemoryKey 对
    //    turnID=0 回落 `row.id`；turn 行走 `turn-<id>-<role>`）
    const turnRowKey = 'turn-1-assistant'
    const standaloneKey = standalone.id
    expect(standaloneKey).not.toBe(turnRowKey)
    expect(standaloneKey.startsWith('cmd-')).toBe(true)
  })
})
