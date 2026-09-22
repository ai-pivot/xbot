/**
 * 用户要求（2026-09-21）：「出现**无法追赶的 gap** 就重新加载 session」。
 *
 * 判据（`reduce.ts` 的 `unreachableGapSig`）：本地迭代窗口 ∪ 权威窗口之后**仍有洞**，
 * 且洞**有一部分落在权威窗口之外** —— 服务端历史按 turn 尾部有界（最近 60 个迭代/ turn），
 * 那段不在响应里、也**再取不回来** ⇒ 本地视图与权威永久断裂。
 *
 * 此时**不拼合、不遮掩**：`gapReloadToken` 自增 ⇒ `AgentPanel` 重新加载该会话
 * （`markHistoryStale` + `reset` + `reload` + loading 屏）。
 *
 * 判别力：① 去掉自增 ⇒ 第 1 例红；② 去掉"同一形状只触发一次" ⇒ 第 2 例红（重载循环）；
 *        ③ 把"可追赶的洞"也当无法追赶 ⇒ 第 3 例红。
 */

import { describe, expect, it } from 'vitest'
import { reduce } from './reduce'
import { commitViaFold, initialChatState, turnID, type ChatState, type DomainEvent, type Turn } from './types'
import type { WebIteration } from '@/types/shared'

const T5 = turnID(5)

function win(from: number, to: number): WebIteration[] {
  const out: WebIteration[] = []
  for (let i = from; i <= to; i++) {
    out.push({ iteration: i, content: `iter-${i}`, reasoning: '', tools: [], toolCount: 0 })
  }
  return out
}

function committedTurn(its: WebIteration[]): Turn {
  return {
    id: T5,
    user: null,
    phase: { kind: 'committed', payload: commitViaFold(its as never, '', 0) },
    requestID: null,
  }
}

const hist = (its: WebIteration[]): DomainEvent => ({
  type: 'history_replaced',
  legacy: [],
  turns: [committedTurn(its)],
  active: null,
  lastSeq: null,
  todos: [],
})

describe('无法追赶的 gap ⇒ 重新加载会话', () => {
  it('本地 [1..93] + 权威尾部 [451..510]（洞 94..450 在权威窗口之外）⇒ gapReloadToken 自增', () => {
    let s: ChatState = reduce(initialChatState('chat-1'), hist(win(1, 93)))
    expect(s.gapReloadToken).toBe(0)
    s = reduce(s, hist(win(451, 510)))
    expect(s.gapReloadToken).toBe(1)
    expect(s.unreachableGapSig).toBe('5:gap94-450')
  })

  it('同一缺口形状重复下发 ⇒ 不再自增（无重载循环）', () => {
    let s: ChatState = reduce(initialChatState('chat-1'), hist(win(1, 93)))
    s = reduce(s, hist(win(451, 510)))
    expect(s.gapReloadToken).toBe(1)
    s = reduce(s, hist(win(451, 510)))
    expect(s.gapReloadToken).toBe(1)
  })

  it('可追赶的洞（洞在权威窗口内，权威窗口连续）⇒ 不触发重载（交给 union/下一次 reload 补）', () => {
    // 本地 1,2,4..93（丢了 delta 3）；权威窗口覆盖 34..93（含 3 吗？不含 —— 见下例）
    let s: ChatState = reduce(initialChatState('chat-1'), hist([...win(1, 2), ...win(4, 93)]))
    expect(s.gapReloadToken).toBe(0)
    // 权威窗口 1..93（连续、覆盖洞 3）⇒ 合并后无洞 ⇒ 不触发重载。
    s = reduce(s, hist(win(1, 93)))
    expect(s.gapReloadToken).toBe(0)
    expect(s.unreachableGapSig).toBe('')
  })

  it('洞有一部分在权威窗口之外（更早）⇒ 无法追赶 ⇒ 触发重载', () => {
    // 本地 [1..40]；权威窗口是尾部 [60..100]（服务端按 turn 尾部有界）⇒ 洞 41..59
    // 在权威窗口之外 ⇒ 再也取不回来。
    let s: ChatState = reduce(initialChatState('chat-1'), hist(win(1, 40)))
    expect(s.gapReloadToken).toBe(0)
    s = reduce(s, hist(win(60, 100)))
    expect(s.gapReloadToken).toBe(1)
  })
})
