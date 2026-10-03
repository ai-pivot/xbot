/**
 * P0（2026-10-06 用户实测，chat_AE903161C55A turn 445）：「历史 view 停在迭代 1-40，
 * 后端已跑到 80+；迭代 81 live 时能看到，81 tool done 后 view 变回 1-40」。
 *
 * DB 实证（84 迭代在 13 分钟内**连续**落库，无断连长窗口）：迭代 41..80 的完成
 * delta（iterationHistory 增量）在 SSE 链路上丢失——iterationHistory 是**增量
 * feed**，后续快照只带「新」迭代，没有任何事件会回头补 41..80（progressStore.ts
 * 的 canonical 注释早已写明："a reload is required to restore the missing
 * iterations"）。但**无人执行该 reload**：
 *
 *   - 传输层防护（seq gap / resync_required / ring 回放）尽力而为，本场景全未命中；
 *   - 状态机层：`iteration` case 盲目把 81 append 进 [1..40] ⇒ [1..40, 81] ⇒
 *     `continuousIterations` 在洞 41 处截断 ⇒ 渲染 [1..40]，81 的 commit
 *     「消失」（正是用户观察）；洞永不愈合（mergeIterations 排序只对**到达的**
 *     数据排序，41..80 根本没到）。
 *
 * 修复契约：**迭代号跳变本身就是数据丢失的数学证据**（后端引擎逐迭代 +1 顺序
 * append，`evNumber > maxHeld + 1` 不可能是正常事件）⇒ 状态机在 `iteration` /
 * `stream` 两个 case 检测「delta/流式迭代号没有接上已持有窗口」⇒ 记录洞签名
 * （同一洞只报一次）⇒ `gapReloadToken` 自增 ⇒ `AgentPanel` 丢弃带洞本地窗口
 * （reset）+ 权威 reload（REST /api/history 的 DB 折叠窗口）⇒ union 补齐。
 *
 * 与 `unreachableGapSig`（history_replaced 路径）的分工：那个判「reload **之后**
 * 仍修不好的洞」（权威窗口外）；本测试判「SSE 增量路径上**正在产生**的洞」——
 * DB 里一切都在，只需触发一次 reload。
 *
 * 判别力（mutation 自证）：
 *   ① 去掉 `iteration` case 的跳变检测 ⇒ T1 红；
 *   ② 去掉 `stream` case 的跳变检测 ⇒ T2 红；
 *   ③ 去掉同洞去重 ⇒ T5 红（重载风暴）；
 *   ④ 把「接上的补洞 delta」（restore 快照自带修复）也当丢失 ⇒ T4 红（多余 reload）。
 */

import { describe, expect, it } from 'vitest'
import { reduce } from './reduce'
import {
  EMPTY_LIVE,
  commitViaFold,
  initialChatState,
  iterNum,
  turnID,
  type ChatState,
  type DomainEvent,
  type Turn,
} from './types'
import type { WebIteration } from '@/types/shared'

const T445 = turnID(445)

function win(from: number, to: number): WebIteration[] {
  const out: WebIteration[] = []
  for (let i = from; i <= to; i++) {
    out.push({ iteration: i, content: `iter-${i}`, reasoning: '', tools: [], toolCount: 0 })
  }
  return out
}

const nums = (its: readonly WebIteration[]): number[] => its.map((i) => i.iteration)

/** 用户实测形态：live turn 445，本地已持有 [1..40]，流式停在迭代 40。 */
function localLive(): Turn {
  return {
    id: T445,
    user: null,
    requestID: null,
    phase: {
      kind: 'live',
      data: { ...EMPTY_LIVE, iterations: win(1, 40) as never, iter: iterNum(40), streaming: true },
    },
  }
}

/** 建档：本地 live 窗口经 history_replaced 灌进状态机（active 快照）。 */
function seeded(): ChatState {
  const t = localLive()
  const snapshot = t.phase.kind === 'live' ? t.phase.data : EMPTY_LIVE
  return reduce(initialChatState('chat-AE90'), {
    type: 'history_replaced',
    legacy: [],
    turns: [t],
    active: { turnID: T445, snapshot },
    lastSeq: null,
    todos: [],
  } as unknown as DomainEvent)
}

/** 迭代 81 的 commit 事件（done）：delta 只带 81 自己（正常形态——增量 feed）。 */
const iterDone81: DomainEvent = {
  type: 'iteration',
  turnID: T445,
  phase: 'tool_exec',
  iter: 81,
  seq: 501,
  content: '',
  reasoning: '',
  activeTools: [],
  completedTools: [],
  iterationsDelta: win(81, 81),
  todos: [],
} as unknown as DomainEvent

describe('P0：SSE 增量路径丢失中间迭代（41..80）⇒ 跳变必须触发会话重载', () => {
  it('★ T1 核心复现：81 commit 跳变 append（delta 未接上 [1..40]）⇒ gapReloadToken 自增', () => {
    let s = seeded()
    expect(s.gapReloadToken).toBe(0)
    // 用户观察：81 stream 时能看到（stream 帧先到，此处直接推进 iter）。
    s = reduce(s, iterDone81)
    // 数据照常保留（append 不丢），但洞 41..80 是「数据丢失」证据 ⇒ 必须报出。
    const t = s.turns.get(T445)!
    if (t.phase.kind !== 'live') throw new Error('turn 必须是 live')
    expect(nums(t.phase.data.iterations)).toEqual([...nums(win(1, 40)), 81])
    // ★ 修复判据：跳变 = 丢失 ⇒ 会话重载信号（AgentPanel reset + reload 补洞）。
    expect(s.gapReloadToken).toBe(1)
  })

  it('★ T2 stream 帧的迭代号直接跳（40 → 81）同样触发', () => {
    let s = seeded()
    s = reduce(s, {
      type: 'stream',
      turnID: T445,
      seq: 502,
      iteration: 81,
      content: 'live text of iter 81',
      streamingTools: [],
      genui: '',
    } as unknown as DomainEvent)
    expect(s.gapReloadToken).toBe(1)
  })

  it('T3 无假阳性：连续 commit / 正常前进 / 同迭代流式帧 ⇒ 不触发', () => {
    let s = seeded()
    // 同迭代 40 的 commit delta（正常形态，appendedMax = maxHeld）。
    s = reduce(s, {
      type: 'iteration',
      turnID: T445,
      iter: 40,
      seq: 401,
      content: '',
      reasoning: '',
      activeTools: [],
      completedTools: [],
      iterationsDelta: win(40, 40),
      todos: [],
    } as unknown as DomainEvent)
    // 正常前进到 41（delta 接上：min=41 = maxHeld+1）。
    s = reduce(s, {
      type: 'iteration',
      turnID: T445,
      iter: 41,
      seq: 402,
      content: '',
      reasoning: '',
      activeTools: [],
      completedTools: [],
      iterationsDelta: win(41, 41),
      todos: [],
    } as unknown as DomainEvent)
    // 流式帧 41（连续）。
    s = reduce(s, {
      type: 'stream',
      turnID: T445,
      seq: 403,
      iteration: 41,
      content: 'x',
      streamingTools: [],
      genui: '',
    } as unknown as DomainEvent)
    expect(s.gapReloadToken).toBe(0)
  })

  it('★ T4 restore 快照补洞（delta=[41..84] 自己接上）⇒ 不触发（快照在修，不制造多余 reload）', () => {
    let s = seeded()
    s = reduce(s, {
      type: 'iteration',
      turnID: T445,
      iter: 84,
      seq: 410,
      content: '',
      reasoning: '',
      activeTools: [],
      completedTools: [],
      iterationsDelta: win(41, 84), // 恢复快照：区间完整、min=41 = maxHeld+1
      todos: [],
    } as unknown as DomainEvent)
    expect(s.gapReloadToken).toBe(0)
    const t = s.turns.get(T445)!
    if (t.phase.kind !== 'live') throw new Error('turn 必须是 live')
    expect(nums(t.phase.data.iterations)).toEqual(nums(win(1, 84)))
  })

  it('★ T5 同一洞去重（防重载风暴）：后续 82/83 commit（洞仍在）不再自增；新洞再触发', () => {
    let s = seeded()
    s = reduce(s, iterDone81)
    expect(s.gapReloadToken).toBe(1)
    // reload 在途（REST 未回）时 82 的 commit 到达 —— 洞 41..80 未变 ⇒ 同签名去重。
    s = reduce(s, {
      type: 'iteration',
      turnID: T445,
      iter: 82,
      seq: 503,
      content: '',
      reasoning: '',
      activeTools: [],
      completedTools: [],
      iterationsDelta: win(82, 82),
      todos: [],
    } as unknown as DomainEvent)
    expect(s.gapReloadToken).toBe(1)
    // reload 修复（REST 全量 [5..84] + regionsBefore=4）：本地 live turn 仍活跃
    // ⇒ live-wins union（既有语义）：[1..40] ∪ [5..84] = [1..84] 连续 —— 洞补齐。
    s = reduce(s, {
      type: 'history_replaced',
      legacy: [],
      turns: [
        {
          id: T445,
          user: null,
          requestID: null,
          phase: { kind: 'committed', payload: commitViaFold(win(5, 84) as never, '', 0, undefined, 4) },
        } as Turn,
      ],
      active: null,
      lastSeq: null,
      todos: [],
    } as unknown as DomainEvent)
    const t = s.turns.get(T445)!
    if (t.phase.kind !== 'live') throw new Error('turn 必须保持 live（live-wins）')
    expect(nums(t.phase.data.iterations)).toEqual(nums(win(1, 84)))
    expect(t.phase.data.regionsBefore).toBe(4)
    // 洞已补 ⇒ 渲染层 continuousIterations 不再截断（81 此后并入历史正常显示）。
    const union = t.phase.data.iterations as WebIteration[]
    expect(union.filter((it) => it.iteration === 81).length).toBe(1)
  })

  it('★ T5b stream 帧序列防风暴：81→82→83 帧连发（洞不变）⇒ token 只自增一次', () => {
    let s = seeded()
    s = reduce(s, {
      type: 'stream',
      turnID: T445,
      seq: 502,
      iteration: 81,
      content: 'a',
      streamingTools: [],
      genui: '',
    } as unknown as DomainEvent)
    expect(s.gapReloadToken).toBe(1)
    // 后续帧（同洞，maxHeld 仍 40）：签名只锚定洞下界 ⇒ 去重生效。
    // 若签名含上界（from-1），每帧变化 ⇒ 每帧自增 = 重载风暴（mutation 实证）。
    s = reduce(s, {
      type: 'stream',
      turnID: T445,
      seq: 503,
      iteration: 82,
      content: 'ab',
      streamingTools: [],
      genui: '',
    } as unknown as DomainEvent)
    s = reduce(s, {
      type: 'stream',
      turnID: T445,
      seq: 504,
      iteration: 83,
      content: 'abc',
      streamingTools: [],
      genui: '',
    } as unknown as DomainEvent)
    expect(s.gapReloadToken).toBe(1)
    expect(s.lostIterGapSig).toBe('445:gapFrom41')
  })

  it('T6 空窗口不判（reset 后回放帧 82 到达 ⇒ 无「已持有」基准，不触发——reload 本就在途）', () => {
    // reset 后（AgentPanel 已因 token++ 丢弃本地窗口）：turns 空，回放的 stream 帧
    // 走 lazyAdoptLive 重建（iterations=[]）⇒ 空窗口没有跳变基准 ⇒ 不自增。
    let s = initialChatState('chat-AE90')
    s = reduce(s, {
      type: 'stream',
      turnID: T445,
      seq: null,
      iteration: 82,
      content: 'replayed frame after reset',
      streamingTools: [],
      genui: '',
    } as unknown as DomainEvent)
    expect(s.gapReloadToken).toBe(0)
  })
})
