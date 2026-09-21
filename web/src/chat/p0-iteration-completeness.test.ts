/**
 * ⛔ P0 不变量（用户 2026-09-21 定稿）：「**不能有任何 gap，任何 gap 都是破坏线性一致性**」。
 *
 * 根因与修复（本轮）：服务端曾把每个 turn 的迭代截到最近 60 个（`BoundHistoryIterations`，
 * 2026-09-15 为压 payload 体积），客户端又截一次（`boundIterationTail`）。两侧各截一次 ⇒
 * 本地手里的窗口与之后下发的窗口**不相邻** ⇒ 合并出 gap ⇒ 渲染层只能在 gap 处截断 ⇒
 * 用户看到「历史停在旧位置 / 中间很多迭代不见 / 新迭代出现即消失」，而且**取不回来**。
 *
 * 现在：**任何地方都不许截断**（两侧的截断实现已删除）⇒ 权威数据永远从 iteration 1 开始且
 * 连续 ⇒ 本地 `[1..93]` ∪ 权威 `[1..435]` = `[1..435]`：连续、无损、最新可见。
 * 若权威侧**仍然不完整**（旧二进制 / 缓存 / 任何遗留截断），则不拼接、不截断，而是
 * **整会话重载**（`resyncToken` ⇒ AgentPanel reload + loading 屏）。
 *
 * 判别力：① 恢复任何截断 ⇒ 第 3 例红；② 去掉 resyncToken 自增 ⇒ 第 1 例红；
 *        ③ 去掉"同一形状只触发一次"⇒ 第 2 例红（重载循环）。
 */

import { describe, expect, it } from 'vitest'
import { reduce } from './reduce'
import { commitViaFold, initialChatState, turnID, type ChatState, type DomainEvent, type Turn } from './types'
import { continuousIterations } from '@/components/agent/progressStore'
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

function iterationsOf(s: ChatState): readonly WebIteration[] {
  const t = s.turns.get(T5)
  if (!t) return []
  return t.phase.kind === 'committed' ? t.phase.payload.iterations : t.phase.data.iterations
}

describe('P0（2026-09-21）迭代完整性：不许有 gap，不完整即整会话重载', () => {
  it('权威迭代不从 1 开始（旧二进制仍截断）⇒ resyncToken 自增（触发重载）', () => {
    const s = reduce(initialChatState('chat-1'), hist(win(355, 434)))
    expect(s.resyncToken).toBe(1)
    expect(s.incompleteSig).toBe('5:from355')
  })

  it('同一缺口形状重复下发 ⇒ 不再自增（无重载循环）', () => {
    let s = reduce(initialChatState('chat-1'), hist(win(355, 434)))
    expect(s.resyncToken).toBe(1)
    s = reduce(s, hist(win(355, 434)))
    expect(s.resyncToken).toBe(1) // 形状未变 ⇒ 不重复触发
  })

  it('权威迭代完整（1..435 连续）⇒ 永不自增；且与本地旧窗口 [1..93] union = 连续、无损、最新可见', () => {
    let s = reduce(initialChatState('chat-1'), hist(win(1, 93)))
    expect(s.resyncToken).toBe(0)
    expect(s.incompleteSig).toBe('')
    s = reduce(s, hist(win(1, 435)))
    // 权威完整 ⇒ 不触发重载（没有 gap 可言）。
    expect(s.resyncToken).toBe(0)
    const its = iterationsOf(s)
    expect(its.length).toBe(435)
    // 渲染层看到的必须是**完整连续**序列（这才是"上一条下一条接得上"）。
    expect(continuousIterations([...its]).length).toBe(435)
    expect(its[its.length - 1].iteration).toBe(435)
  })

  it('权威内部有洞 ⇒ 同样视为不完整 ⇒ 自增', () => {
    const holed = [...win(1, 10), ...win(12, 20)]
    const s = reduce(initialChatState('chat-1'), hist(holed))
    expect(s.resyncToken).toBe(1)
    expect(s.incompleteSig).toBe('5:gap@12')
  })
})
