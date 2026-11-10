/**
 * P0（2026-10-15 用户实录，web）：**切回 iter 很多的会话 ⇒ 历史与最新迭代脱节**。
 *
 * 用户原话：「有时候切回一个 iter 很多的会话，历史会显示，例如迭代 1-1000，但是实际上
 * 最新迭代是 2000，然后此时最新迭代 2000 在迭代 1000 后渲染出来，然后迭代 2000 结束后
 * 变回只有迭代 1-1000 能看到。」
 *
 * 根因（与 `p0-lost-mid-run-iterations.test.ts` 是**同一缺陷家族的另一条形态**）：
 *   那条判据（`lostIterGapSig`）只在**事件带着跳跃的迭代号到达**时才可能判出
 *   （`evNumber > maxHeld + 1`）。而本例的脱节是在**一次 hydration/合并内部**形成的：
 *   权威快照与本地窗口各带一段，union 之后 turn 的迭代计数器已到 2000、`iterations`
 *   只到 1000 —— **没有任何事件会带出跳跃** ⇒ 旧判据恒不触发 ⇒ 界面永久落后：
 *     · 渲染层用 `lastIter` 合成在飞块（`derive.ts:foldToolsIntoIterations` /
 *       live 行读 `lastIter`）⇒「2000 渲染在 1000 之后」；
 *     · turn 结束（定格/提交）后合成块消失 ⇒「又只剩 1-1000」。
 *
 * 修复契约（用户定向："如果 live 迭代很明显的脱节，要么 catch up gap 要么直接清除 memo
 * 重新拉 api 加载会话"）：`liveAheadGapSig` = 「`live.iter > maxHeldIteration(iterations)
 * + 容差`」⇒ 走**既有**的 `gapReloadToken` 出口（面板 markHistoryStale + reset + 权威
 * reload ⇒ DB 窗口 union 补洞）。判据挂在三处：① hydration 合并（用户实测触发点）；
 * ② 定格（session idle / text_final fold）；③ 提交（committed）。
 *
 * 判别力（mutation 自证）：
 *   ① `liveAheadGapSig` 恒返回 ''（永不报）⇒ T1/T3/T4/T6 红；
 *   ② 去掉同形状去重（直接返回 sig）⇒ T2 红（重载风暴）；
 *   ③ 容差改成 0（把"在飞迭代尚未落进数组"的正常语义也当脱节）⇒ T5 红（假阳性）。
 */

import { describe, expect, it } from 'vitest'
import { reduce } from './reduce'
import {
  EMPTY_LIVE,
  initialChatState,
  iterNum,
  turnID,
  type ChatState,
  type DomainEvent,
  type LiveSnapshot,
  type Turn,
} from './types'
import type { WebIteration } from '@/types/shared'

const T445 = turnID(445)
const CHAT = 'chat-AE90'

function win(from: number, to: number): WebIteration[] {
  const out: WebIteration[] = []
  for (let i = from; i <= to; i++) {
    out.push({ iteration: i, content: `iter-${i}`, reasoning: '', tools: [], toolCount: 0 })
  }
  return out
}

/** 用户实测形态：turn 的迭代计数器已到 `counter`，但**已持有窗口**只到 `windowMax`。 */
function skew(counter: number, windowMax: number): LiveSnapshot {
  return { ...EMPTY_LIVE, iterations: win(1, windowMax) as never, iter: iterNum(counter), streaming: true }
}

function turnOf(d: LiveSnapshot): Turn {
  return { id: T445, user: null, requestID: null, phase: { kind: 'live', data: d } }
}

/** 建档：把该 turn 经 history_replaced（含 active 快照）灌进状态机 —— 即"切回会话"路径。 */
function hydrateEv(d: LiveSnapshot): DomainEvent {
  return {
    type: 'history_replaced',
    legacy: [],
    turns: [turnOf(d)],
    active: { turnID: T445, snapshot: d },
    lastSeq: null,
    todos: [],
  } as unknown as DomainEvent
}
const hydrate = (d: LiveSnapshot): ChatState => reduce(initialChatState(CHAT), hydrateEv(d))

/** 手工建一个"正在跑"的状态（用于走定格/提交路径）。 */
function running(d: LiveSnapshot): ChatState {
  return { ...initialChatState(CHAT), turns: new Map([[T445, turnOf(d)]]), activeTurn: T445, busy: true }
}

describe('P0：切回多迭代会话后历史与最新迭代脱节（hydration 内部形成的 gap）', () => {
  it('★ T1 核心复现：窗口到 1000 而计数器已到 2000 ⇒ 必须触发权威重载', () => {
    const s = hydrate(skew(2000, 1000))
    // 修复前：gapReloadToken 恒 0 ⇒ 界面永久停在 1-1000（用户实录）。
    expect(s.gapReloadToken).toBe(1)
    expect(s.liveAheadGapSig).toBe(`${T445}:liveAheadAt1001`)
  })

  it('★ T2 同形状只报一次（不造成重载风暴）', () => {
    const s1 = hydrate(skew(2000, 1000))
    expect(s1.gapReloadToken).toBe(1)
    const s2 = reduce(s1, hydrateEv(skew(2000, 1000)))
    expect(s2.gapReloadToken).toBe(1) // 同形状 ⇒ 不再自增
    expect(s2.liveAheadGapSig).toBe(`${T445}:liveAheadAt1001`)
  })

  it('★ T3 定格路径同样触发（防"turn 结束后又只剩旧窗口"）', () => {
    const s1 = reduce(running(skew(2000, 1000)), { type: 'session', busy: false } as unknown as DomainEvent)
    expect(s1.gapReloadToken).toBe(1)
  })

  it('★ T4 提交路径同样触发（committed 的 iterations 就是这份短窗口）', () => {
    const s1 = reduce(running(skew(2000, 1000)), {
      type: 'text_final',
      turnID: T445,
      seq: 900,
      content: '最终回复',
      progressHistory: win(1, 1000),
      cancelled: false,
    } as unknown as DomainEvent)
    expect(s1.gapReloadToken).toBe(1)
  })

  it('★ T5 无假阳性：正常在飞语义（iter = maxHeld + 1）与容差内不得触发', () => {
    // 迭代 41 正在跑：40 已落进数组，41 还没（这是**正常**形态，不是脱节）
    expect(hydrate(skew(41, 40)).gapReloadToken).toBe(0)
    expect(hydrate(skew(41, 40)).liveAheadGapSig).toBe('')
    // 容差内（maxHeld+2）也不报 —— 边界抖动不得触发重载
    expect(hydrate(skew(42, 40)).gapReloadToken).toBe(0)
  })

  it('★ T6 洞被补上后再脱节 ⇒ 新下界 ⇒ 重新触发', () => {
    const s1 = hydrate(skew(2000, 1000))
    expect(s1.gapReloadToken).toBe(1)
    // 权威 reload 后窗口补到 1500（仍脱节，但洞下界前移到 1501）⇒ 必须再报一次
    const s2 = reduce(s1, hydrateEv(skew(2000, 1500)))
    expect(s2.gapReloadToken).toBe(2)
    expect(s2.liveAheadGapSig).toBe(`${T445}:liveAheadAt1501`)
  })
})
