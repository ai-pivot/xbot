/**
 * P0 复现/回归（2026-09-16 用户报告 #2）：
 *   「切换会话后，两个 turn 之间的 notification（以 **user 形式**注入的通知，例如
 *     `⏰ [定时任务触发] …` / `[System Notification] …`）不渲染，导致两个 turn 的
 *     assistant 消息在视觉上连接在一起。」
 *
 * 现场证据（DB 实证，线上库）：
 *   id=1793020  role=user  display_only=0  turn=19391  "⏰ [定时任务触发] 背诵一次出师表（全文，不要省略）"
 *   id=1792261  role=user  display_only=0  turn=39     "[System Notification] Background subagent explore …"
 *   ⇒ 通知行**不是** display_only（服务端转换只 skip role=tool 与 display_only，
 *     见 channel/subscription.go:1042/1049）⇒ 它理应在 history 载荷里。
 *   ⇒ 因此丢失点在前端「历史 → 行」的归属/渲染（historyToReplaced → deriveRows）。
 *
 * 契约（本文件钉死）：通知 turn 的 user 行必须作为一行渲染出来（它把两侧的
 * assistant 消息分隔开）；不得因为「该 turn 没有 assistant 产出」「内容以通知前缀
 * 开头」等原因被丢弃。
 */

import { describe, expect, it } from 'vitest'
import { deriveRows } from './derive'
import { historyToReplaced } from './integrate'
import { reduce } from './reduce'
import { initialChatState, type DomainEvent } from './types'
import type { ChatMessage } from '@/types/shared'

const NOTIF = '[System Notification] Background subagent explore (instance=switch-finalize-trace) completed.'

const row = (o: Partial<ChatMessage>): ChatMessage =>
  ({
    id: 'db-x',
    role: 'user',
    content: '',
    turnID: 0,
    iterations: [],
    timestamp: '2026-09-16T00:00:00Z',
    isPartial: false,
    persisted: true,
    ...o,
  }) as ChatMessage

/** 现场还原：turn 100（问+答）→ turn 101（**只有通知行**，无 assistant 产出）→ turn 102（问+答）。 */
const HISTORY: ChatMessage[] = [
  row({ id: 'db-1', role: 'user', content: '先跑一遍', turnID: 100, dbID: 1 }),
  row({ id: 'db-2', role: 'assistant', content: 'turn100 的回答', turnID: 100, dbID: 2 }),
  row({ id: 'db-3', role: 'user', content: NOTIF, turnID: 101, dbID: 3 }),
  row({ id: 'db-4', role: 'user', content: '再跑一遍', turnID: 102, dbID: 4 }),
  row({ id: 'db-5', role: 'assistant', content: 'turn102 的回答', turnID: 102, dbID: 5 }),
]

function renderedRows(history: ChatMessage[]) {
  const state = reduce(initialChatState('chat-1'), historyToReplaced(history, null))
  return deriveRows(state)
}

describe('P0#2: 两个 turn 之间的 notification（user 形式）必须渲染', () => {
  it('通知行渲染为独立一行（不得被丢，否则两侧 assistant 会粘连）', () => {
    const rows = renderedRows(HISTORY)
    const texts = rows.map((r) => r.content ?? '')
    expect(texts.some((t) => t.includes('[System Notification]'))).toBe(true)
  })

  it('行序：turn100 回答 → 通知 → turn102 回答（通知必须夹在中间）', () => {
    const rows = renderedRows(HISTORY)
    const idx = (needle: string) => rows.findIndex((r) => (r.content ?? '').includes(needle))
    const i100 = idx('turn100 的回答')
    const iNotif = idx('[System Notification]')
    const i102 = idx('turn102 的回答')
    expect(i100).toBeGreaterThanOrEqual(0)
    expect(iNotif).toBeGreaterThan(i100)
    expect(i102).toBeGreaterThan(iNotif)
  })

  it('通知 turn 只有 user 行时也必须产出该行（无 assistant 不能吞掉 user）', () => {
    const rows = renderedRows(HISTORY)
    const notifUserRows = rows.filter(
      (r) => r.kind === 'user' && (r.content ?? '').includes('[System Notification]'),
    )
    expect(notifUserRows).toHaveLength(1)
  })

  // ── 真实时序复现（P0#2 根因）：通知 turn 在 live 里跑 —— `turn_started(notification)`
  //    只注入 user 行、agent 无 assistant 产出 ⇒ `session(idle)` 到达时**必须保留该
  //    user 行**。旧实现 `turns.delete(t.id)` 把通知行一起删掉 ⇒ 两侧 assistant 粘连。 ──
  it('live：无产出的通知 turn 收到 session(idle) 后，通知行必须仍然渲染（不得删槽）', () => {
    let s = reduce(initialChatState('chat-1'), {
      type: 'turn_started',
      turnID: 101 as never,
      requestID: null,
      trigger: 'notification',
      content: NOTIF,
      senderName: undefined,
    } as unknown as DomainEvent)
    expect(s.turns.get(101 as never)?.user?.isNotification).toBe(true) // 前置：通知 user 行已建

    s = reduce(s, {
      type: 'session',
      session: { channel: 'web', chat_id: 'chat-1', action: 'idle' },
      busy: false,
    } as unknown as DomainEvent)

    // 关键断言：turn 槽位必须保留（冻成空壳），user 行照常渲染
    const notifRows = deriveRows(s).filter(
      (r) => r.kind === 'user' && (r.content ?? '').includes('[System Notification]'),
    )
    expect(notifRows).toHaveLength(1) // 修前 = 0（turns.delete 把通知行删了）
  })

  it('live→切会话：冻结成空壳的通知 turn，history_replaced（重拉）后通知行仍在且只一行', () => {
    let s = reduce(initialChatState('chat-1'), {
      type: 'turn_started',
      turnID: 101 as never,
      requestID: null,
      trigger: 'notification',
      content: NOTIF,
      senderName: undefined,
    } as unknown as DomainEvent)
    s = reduce(s, {
      type: 'session',
      session: { channel: 'web', chat_id: 'chat-1', action: 'idle' },
      busy: false,
    } as unknown as DomainEvent)
    s = reduce(s, historyToReplaced(HISTORY, null))

    const notifRows = deriveRows(s).filter(
      (r) => r.kind === 'user' && (r.content ?? '').includes('[System Notification]'),
    )
    expect(notifRows).toHaveLength(1)
  })
})
