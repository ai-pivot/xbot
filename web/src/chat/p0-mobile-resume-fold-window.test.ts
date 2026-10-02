import { describe, expect, it } from 'vitest'

import {
  EMPTY_LIVE,
  commitViaFold,
  initialChatState,
  iterNum,
  turnID,
  type DomainEvent,
  type Turn,
} from './types'
import { reduce } from './reduce'
import { deriveRows } from './derive'

/**
 * P0（2026-10-02 用户报告）：「手机熄屏后过段时间重新看会话，历史进度还在熄屏的
 * 时候，此时 sse 推送新 tool 过来，该 tool 一旦执行完毕就消失，前端渲染的历史消息
 * 永远不更新。」
 *
 * 根因链（三个症状一体的机制）：
 *   熄屏期间 turn 跑了 40+ 迭代 → 亮屏 resync（增量 > 30 ⇒ resync_required）→
 *   reload 的 active_progress 是【折叠窗口】[52..90]（GetActiveProgressFolded，
 *   regionsBefore=6）——与本地熄屏前的【完整低号迭代】[1..40] union 后产生洞
 *   [41..51]（窗口起点 52 > 本地尾部 40+1；服务端折叠窗口不含本地已有段）。
 *   连续前缀守卫（continuousIterations 只渲染连续段）在洞处截断 ⇒ 只渲染 [1..40]
 *   ——历史「永远停在熄屏时」；SSE 完成的迭代 91..N 全部 union 进 data 但永远落在
 *   截断之外——「历史永不更新」；activeTools 走 LiveIteration 独立渲染通道不受
 *   contiguous 影响——tool running 时可见，done 后从 activeTools 清除、completed
 *   迭代又在截断区之外——「执行完毕就消失」。
 *
 * 修复：union 有洞（本地尾部+1 < incoming 窗口起点）⇒ **折叠窗口权威替换**——
 * 本地旧低号迭代丢弃（regionsBefore 声明保底：窗口之前的区域用户上滚可完整取回，
 * 服务端按区域算的计数天然涵盖被丢段）。无洞时保留既有 union（重启 resume 的
 * SSE 增量 × DB 全量不丢任何一侧——那条路径没有洞）。
 */

const TID = 7
const T7 = turnID(7)

const it_ = (n: number, folded = false) => ({
  iteration: n,
  content: `c${n}`,
  reasoning: '',
  tools: folded
    ? [{ name: 'Shell', label: `Shell:${n}`, status: 'done', elapsedMs: 1 }]
    : [{ name: 'Shell', label: `Shell:${n}`, status: 'done', elapsedMs: 1, summary: 's', args: '{}', detail: 'd' }],
  toolCount: 1,
  ...(folded ? { toolsFolded: true } : {}),
})

const win = (from: number, to: number, folded = false) =>
  Array.from({ length: to - from + 1 }, (_, i) => it_(from + i, folded))

const nums = (its: readonly { iteration: number }[]): number[] => its.map((x) => x.iteration)

/** 熄屏前的本地 live turn：迭代 1..40 完整（非折叠），当前迭代 41。 */
const localLiveBeforeScreenOff = (): Turn => ({
  id: T7,
  user: { id: 'u7', content: '跑个大任务', timestamp: '', isNotification: false } as never,
  requestID: null,
  phase: {
    kind: 'live',
    data: { ...EMPTY_LIVE, iterations: win(1, 40) as never, iter: iterNum(41), streaming: true },
  },
})

/** 亮屏 resync reload：DB 折叠窗口 committed + active 折叠快照（iter 已到 91）。 */
const resumeReload = (t: Turn): DomainEvent =>
  ({
    type: 'history_replaced',
    legacy: [],
    turns: [
      {
        id: T7,
        user: t.user,
        requestID: null,
        phase: { kind: 'committed', payload: commitViaFold(win(52, 90, true) as never, '', 0, undefined, 6) },
      } as Turn,
    ],
    active: {
      turnID: T7,
      snapshot: {
        ...EMPTY_LIVE,
        iterations: win(52, 90, true) as never,
        iter: iterNum(91),
        streaming: true,
        regionsBefore: 6,
      },
    },
    lastSeq: null,
    todos: [],
  }) as unknown as DomainEvent

describe('P0：熄屏恢复 × 折叠窗口的洞（历史冻结 + tool done 消失）——追赶语义', () => {
  it('★ 恢复窗口与本地低号段有洞 ⇒ 数据双方保留（union 不丢任何一侧）+ gapSig 豁免（可追赶，不触发 reload）', () => {
    const local = localLiveBeforeScreenOff()
    let s = reduce(initialChatState('c1'), {
      type: 'history_replaced',
      legacy: [],
      turns: [local],
      active: null,
      lastSeq: null,
      todos: [],
    } as DomainEvent)
    s = reduce(s, resumeReload(local))

    const t = s.turns.get(T7)!
    expect(t.phase.kind).toBe('live')
    if (t.phase.kind !== 'live') return
    // 追赶语义：本地低号段（1..40）与恢复窗口（52..90）**都保留**（union——洞 41..51
    // 由 regionsBefore=6 声明可追赶，由 useRegionWindow 的自动填洞取回；不丢弃任何一侧）。
    const got = nums(t.phase.data.iterations)
    expect(got).toEqual([...Array.from({ length: 40 }, (_, i) => 1 + i), ...Array.from({ length: 39 }, (_, i) => 52 + i)])
    expect(t.phase.data.regionsBefore).toBe(6)
    // ★ 豁免：可追赶的洞不得触发整会话 reload（reload 只会拿到同样的折叠窗口，
    // 洞依然存在——死循环；追赶由段加载完成）。
    expect(s.gapReloadToken).toBe(0)
    expect(s.unreachableGapSig).toBe('')
  })

  it('★ 段事件填洞（追赶完成）⇒ 序列连续 + 后续 SSE 事件通道活（历史更新、tool done 不消失）', () => {
    const local = localLiveBeforeScreenOff()
    let s = reduce(initialChatState('c1'), {
      type: 'history_replaced',
      legacy: [],
      turns: [local],
      active: null,
      lastSeq: null,
      todos: [],
    } as DomainEvent)
    s = reduce(s, resumeReload(local))

    // 自动追赶取回的段 [41..51]（POST /api/regions 响应 → iterations_loaded）。
    s = reduce(s, {
      type: 'iterations_loaded',
      turnID: TID,
      iterations: win(41, 51) as never,
      regionsBefore: 3,
    } as unknown as DomainEvent)
    let t = s.turns.get(T7)!
    if (t.phase.kind !== 'live') throw new Error('turn 必须是 live')
    let got = nums(t.phase.data.iterations)
    expect(got, '洞填上后序列必须连续 1..90（历史恢复完整）').toEqual(
      Array.from({ length: 90 }, (_, i) => 1 + i),
    )

    // tool done：迭代 91 完成事件 union 进历史（通道活——contiguous 不再截断）。
    s = reduce(s, {
      type: 'iteration',
      turnID: TID,
      iter: 92,
      seq: iterNum(200),
      iterationsDelta: [it_(91)],
      activeTools: [],
      streamingTools: [],
      todos: [],
    } as unknown as DomainEvent)
    t = s.turns.get(T7)!
    if (t.phase.kind !== 'live') throw new Error('turn 必须是 live')
    got = nums(t.phase.data.iterations)
    expect(got).toContain(91)
    expect(got).toEqual(Array.from({ length: 91 }, (_, i) => 1 + i))
    expect(t.phase.data.iter).toBe(92)
  })

  it('无洞路径不回归：本地 41..50 × incoming 窗口 51..90（衔接）⇒ 保留既有 union（不丢任何一侧）', () => {
    const local: Turn = {
      id: T7,
      user: null,
      requestID: null,
      phase: { kind: 'live', data: { ...EMPTY_LIVE, iterations: win(41, 50) as never, iter: iterNum(51), streaming: true } },
    }
    let s = reduce(initialChatState('c1'), {
      type: 'history_replaced',
      legacy: [],
      turns: [local],
      active: null,
      lastSeq: null,
      todos: [],
    } as DomainEvent)
    s = reduce(s, {
      type: 'history_replaced',
      legacy: [],
      turns: [
        {
          id: T7,
          user: null,
          requestID: null,
          phase: { kind: 'committed', payload: commitViaFold(win(51, 90, true) as never, '', 0, undefined, 2) },
        } as Turn,
      ],
      active: null,
      lastSeq: null,
      todos: [],
    } as DomainEvent)

    const t = s.turns.get(T7)!
    if (t.phase.kind !== 'live') throw new Error('live 胜（衔接 union 不替换）')
    const got = nums(t.phase.data.iterations)
    expect(got).toEqual(Array.from({ length: 50 }, (_, i) => 41 + i)) // 41..90 全保留（既有 union）
  })

  it('渲染层验证：恢复后 deriveRows 产出的 turn 内容立即包含窗口（历史更新到熄屏后的最新）', () => {
    const local = localLiveBeforeScreenOff()
    let s = reduce(initialChatState('c1'), {
      type: 'history_replaced',
      legacy: [],
      turns: [local],
      active: null,
      lastSeq: null,
      todos: [],
    } as DomainEvent)
    s = reduce(s, resumeReload(local))

    const rows = deriveRows(s)
    const assistant = rows.find((r) => r.kind === 'live' || r.kind === 'frozen' || r.kind === 'committed')
    expect(assistant, 'turn 行必须存在').toBeTruthy()
    // live 行的迭代含窗口迭代（52..90——渲染入口的数据源）。
    const iterations =
      ((assistant as unknown as { iterations?: { iteration: number }[] }).iterations) ?? []
    expect(iterations.some((x) => x.iteration >= 52 && x.iteration <= 90)).toBe(true)
  })
})
