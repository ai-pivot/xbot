/**
 * regionsLoaded.test.ts — `iterations_loaded` case（T8）+ `mergeIterations` 四象限（T7）
 * + 区域段与「无法追赶的 gap」的关系（T10）。
 *
 * 背景（方案 `docs/plan-history-fold-windowing.md` §3.2-3.4）：
 *   - D2：turn 首次只下发最后 K 个**展示区域**（= `mergeToolRuns` 输出块），
 *     更早的经 `POST /api/regions` 整段取回 ⇒ `iterations_loaded` 事件。
 *   - D3：窗口内迭代的工具详情省略（`tools_folded`），浮层打开时按需 hydrate
 *     （同号**完整覆盖轻字段**）；**轻字段永不覆盖已加载的完整数据**（四象限）。
 *   - D4：区域段是**显式可取回窗口**（`regions_before`），不是洞 ⇒ 绝不触发
 *     会话重载（`gapReloadToken` 不得自增）。
 *
 * 判别力（mutation 自证见任务报告）：
 *   ① 四象限去掉「轻不覆盖完整」⇒ T7 的 ★ 用例必红；
 *   ② case 幂等短路去掉（无条件返回新 state）⇒ 「幂等重放返回原引用」必红；
 *   ③ case 里把 `regionsBefore` 当必给（缺省也覆盖）⇒ 「缺省不动」必红。
 */

import { describe, expect, it } from 'vitest'
import { reduce } from './reduce'
import { deriveRows } from './derive'
import {
  commitViaFold,
  commitViaText,
  EMPTY_LIVE,
  initialChatState,
  iterNum,
  turnID,
  type DomainEvent,
  type Turn,
} from './types'
import type { WebIteration, WebToolProgress } from '@/types/shared'

const T7 = turnID(7)
const TID = 7

// ─── 测试 DSL ─────────────────────────────────────────────────

/** 一个工具：full=false 即「轻字段形态」（summary/args/detail 已省略）。 */
const tool = (n: number, full: boolean): WebToolProgress => ({
  name: `t${n}`,
  label: `t${n}`,
  status: 'done',
  elapsedMs: 1,
  summary: full ? `SUM${n}` : '',
  detail: full ? `DET${n}` : '',
  args: full ? `ARG${n}` : '',
  toolHints: '',
})

/** 一个迭代：folded=true ⇒ 后端 `tools_folded` 轻字段形态（详情已省略）；false = 完整。 */
function it_(n: number, folded: boolean, fullDetail = !folded): WebIteration {
  return {
    iteration: n,
    content: `c${n}`,
    reasoning: '',
    tools: [tool(n, fullDetail)],
    toolCount: 1,
    toolsFolded: folded,
  }
}

/** 连续窗口 [from..to]。 */
function win(from: number, to: number, folded = false): WebIteration[] {
  const out: WebIteration[] = []
  for (let i = from; i <= to; i++) out.push(it_(i, folded))
  return out
}

const nums = (its: readonly WebIteration[]): number[] => its.map((x) => x.iteration)

const itsOf = (t: Turn): readonly WebIteration[] =>
  t.phase.kind === 'committed' ? t.phase.payload.iterations : t.phase.data.iterations

const rbOf = (t: Turn): number | undefined =>
  t.phase.kind === 'committed' ? t.phase.payload.regionsBefore : undefined

const committedTurn = (its: WebIteration[], regionsBefore?: number): Turn => ({
  id: T7,
  user: null,
  phase: { kind: 'committed', payload: commitViaFold(its as never, '', 0, undefined, regionsBefore) },
  requestID: null,
})

/** DB 权威历史刷新（history_replaced）。 */
const replaced = (its: WebIteration[], regionsBefore?: number): DomainEvent => ({
  type: 'history_replaced',
  legacy: [],
  turns: [committedTurn(its, regionsBefore)],
  active: null,
  lastSeq: null,
  todos: [],
})

/** 区域段 / 迭代详情到达。 */
const loaded = (tid: number, its: readonly WebIteration[], regionsBefore?: number): DomainEvent => ({
  type: 'iterations_loaded',
  turnID: tid,
  iterations: its,
  regionsBefore,
})

const started = (tid = TID): DomainEvent => ({
  type: 'turn_started',
  turnID: turnID(tid),
  requestID: null,
  trigger: 'user',
  content: null,
})

/** 带迭代增量的结构化事件（把 live 灌成有迭代的形态）。 */
const iterDelta = (tid: number, its: readonly WebIteration[]): DomainEvent => ({
  type: 'iteration',
  turnID: turnID(tid),
  iter: iterNum(its[its.length - 1]?.iteration ?? 1),
  seq: 5 as never,
  content: undefined,
  reasoning: undefined,
  activeTools: [],
  completedTools: [],
  iterationsDelta: its,
  todos: undefined,
  subAgents: undefined,
  tokenUsage: undefined,
  streamStats: undefined,
})

// ─── T8：iterations_loaded case ───────────────────────────────

describe('T8 iterations_loaded：三态 union + 幂等 + regionsBefore 三态', () => {
  it('committed turn：区域段 union 并入（段 40..51 + 窗口 52..66 ⇒ 40..66 连续）', () => {
    let s = reduce(initialChatState('c1'), replaced(win(52, 66, true), 12))
    expect(rbOf(s.turns.get(T7)!)).toBe(12)
    const before = s.turns.get(T7)!
    s = reduce(s, loaded(TID, win(40, 51, true), 0))
    expect(nums(itsOf(s.turns.get(T7)!))).toEqual(Array.from({ length: 27 }, (_, i) => 40 + i))
    expect(s.turns.get(T7)).not.toBe(before) // 真实变化：payload 换新
    expect(rbOf(s.turns.get(T7)!)).toBe(0) // 权威覆盖：该 turn 到顶（分隔条消失）
  })

  it('live turn：段 union 并入 data.iterations，且不触碰 activeTurn/lastSeq', () => {
    let s = reduce(initialChatState('c1'), started())
    s = reduce(s, iterDelta(TID, win(60, 66, true)))
    expect(nums(itsOf(s.turns.get(T7)!))).toEqual([60, 61, 62, 63, 64, 65, 66])
    const seqBefore = s.lastSeq
    const activeBefore = s.activeTurn
    s = reduce(s, loaded(TID, win(52, 59, true)))
    expect(nums(itsOf(s.turns.get(T7)!))).toEqual(Array.from({ length: 15 }, (_, i) => 52 + i))
    expect(s.turns.get(T7)!.phase.kind).toBe('live')
    expect(s.activeTurn).toBe(activeBefore)
    expect(s.lastSeq).toBe(seqBefore)
  })

  it('frozen turn：定格后仍可被段 hydrate（union 走 data.iterations）', () => {
    let s = reduce(initialChatState('c1'), started())
    s = reduce(s, iterDelta(TID, win(60, 66, true)))
    s = reduce(s, { type: 'session_idle' })
    expect(s.turns.get(T7)!.phase.kind).toBe('frozen')
    expect(s.activeTurn).toBeNull()
    s = reduce(s, loaded(TID, win(52, 59, true)))
    expect(s.turns.get(T7)!.phase.kind).toBe('frozen')
    expect(nums(itsOf(s.turns.get(T7)!))).toEqual(Array.from({ length: 15 }, (_, i) => 52 + i))
  })

  it('turnID 缺失/0 ⇒ 回退 activeTurn；无 activeTurn ⇒ 静默返回原 state', () => {
    let s = reduce(initialChatState('c1'), started())
    s = reduce(s, iterDelta(TID, win(60, 66, true)))
    s = reduce(s, loaded(0, win(52, 59, true)))
    expect(nums(itsOf(s.turns.get(T7)!))).toHaveLength(15)
    // 无 activeTurn（新会话）时 turnID=0 无归属 ⇒ 原 state。
    const fresh = initialChatState('c1')
    expect(reduce(fresh, loaded(0, win(1, 3, true)))).toBe(fresh)
  })

  it('turn 不存在 ⇒ 静默返回原 state（不凭空造 turn）', () => {
    const s = initialChatState('c1')
    expect(reduce(s, loaded(99, win(1, 5, true), 3))).toBe(s)
  })

  it('幂等重放：同一段再来一次 ⇒ 返回原 state 引用（零渲染）', () => {
    let s = reduce(initialChatState('c1'), replaced(win(52, 66, true), 12))
    s = reduce(s, loaded(TID, win(40, 51, true), 1))
    const snapshot = s
    // 新对象、同号、同轻形态、同 regionsBefore ⇒ union 无变化、计数无变化。
    s = reduce(s, loaded(TID, win(40, 51, true), 1))
    expect(s).toBe(snapshot)
  })

  it('regionsBefore 缺省（详情端点）⇒ 保留现值；显式携带 ⇒ 权威覆盖', () => {
    let s = reduce(initialChatState('c1'), replaced(win(52, 66, true), 12))
    // 无迭代 + 缺省 ⇒ 完全 no-op（原引用）。
    const snapshot = s
    s = reduce(s, loaded(TID, [], undefined))
    expect(s).toBe(snapshot)
    expect(rbOf(s.turns.get(T7)!)).toBe(12)
    // 详情 hydrate（完整单迭代、缺省计数）⇒ 计数不动。
    s = reduce(s, loaded(TID, [it_(60, false)], undefined))
    expect(rbOf(s.turns.get(T7)!)).toBe(12)
    // 区域段（携带计数）⇒ 权威覆盖。
    s = reduce(s, loaded(TID, [it_(60, false)], 3))
    expect(rbOf(s.turns.get(T7)!)).toBe(3)
  })

  it('绝不触碰 activeTurn/lastSeq/busy/gapReloadToken/unreachableGapSig/sessionRunning', () => {
    let s = reduce(initialChatState('c1'), replaced(win(52, 66, true), 12))
    s = reduce(s, { type: 'session', busy: true })
    s = reduce(s, started(9)) // 另一个 turn 处于 live（activeTurn=9）—— 段 hydrate 不得动它
    expect(s.activeTurn).toBe(turnID(9))
    const before = {
      activeTurn: s.activeTurn, lastSeq: s.lastSeq, busy: s.busy,
      gapReloadToken: s.gapReloadToken, sig: s.unreachableGapSig, running: s.sessionRunning,
    }
    const next = reduce(s, loaded(TID, win(40, 51, true), 3))
    expect(next).not.toBe(s) // 确实应用了段
    expect(next.activeTurn).toBe(before.activeTurn)
    expect(next.lastSeq).toBe(before.lastSeq)
    expect(next.busy).toBe(before.busy)
    expect(next.gapReloadToken).toBe(before.gapReloadToken)
    expect(next.unreachableGapSig).toBe(before.sig)
    expect(next.sessionRunning).toBe(before.running)
  })
})

// ─── T7：mergeIterations 四象限 ───────────────────────────────

describe('T7 mergeIterations 四象限（轻 / 完整）', () => {
  it('完整(incoming) vs 轻(prev) ⇒ incoming 胜（浮层 / 区域段 hydrate）', () => {
    let s = reduce(initialChatState('c1'), replaced(win(52, 66, true), 0))
    const light60 = itsOf(s.turns.get(T7)!).find((x) => x.iteration === 60)!
    expect(light60.tools[0].summary).toBe('')
    s = reduce(s, loaded(TID, [it_(60, false)], undefined))
    const full60 = itsOf(s.turns.get(T7)!).find((x) => x.iteration === 60)!
    expect(full60).not.toBe(light60)
    expect(full60.tools[0].summary).toBe('SUM60')
    expect(full60.tools[0].detail).toBe('DET60')
    expect(full60.toolsFolded).toBe(false)
  })

  it('★轻(incoming) vs 完整(prev) ⇒ prev 胜：轻字段永不覆盖已加载的完整数据', () => {
    let s = reduce(initialChatState('c1'), replaced(win(52, 66, false), 0))
    const prev60 = itsOf(s.turns.get(T7)!).find((x) => x.iteration === 60)!
    expect(prev60.tools[0].summary).toBe('SUM60')
    // reload / 区域段形态：同号轻字段（详情已省略）不得抹掉已加载的完整数据。
    s = reduce(s, loaded(TID, win(52, 66, true), 0))
    const after60 = itsOf(s.turns.get(T7)!).find((x) => x.iteration === 60)!
    expect(after60).toBe(prev60) // 引用透传（未被覆盖、未被重建）
    expect(after60.tools[0].summary).toBe('SUM60')
    expect(after60.tools[0].args).toBe('ARG60')
  })

  it('★ reload 全窗口轻字段覆盖本地完整数据：整段引用稳定（零重建）', () => {
    const s = reduce(initialChatState('c1'), replaced(win(52, 66, false), 0))
    const prevIts = itsOf(s.turns.get(T7)!)
    const nextS = reduce(s, loaded(TID, win(52, 66, true), 0))
    const nextIts = itsOf(nextS.turns.get(T7)!)
    expect(nextIts).toBe(prevIts) // 轻 incoming 全被 prev 挡下 ⇒ 数组引用也不变
  })

  it('完整 vs 完整 ⇒ incoming 胜（既有权威方向不变）', () => {
    let s = reduce(initialChatState('c1'), replaced(win(52, 66, false), 0))
    const newer = { ...it_(60, false), content: 'c60-new' }
    s = reduce(s, loaded(TID, [newer], undefined))
    expect(itsOf(s.turns.get(T7)!).find((x) => x.iteration === 60)).toBe(newer)
  })

  it('轻 vs 轻 ⇒ prev 胜（引用稳定，幂等零渲染）', () => {
    let s = reduce(initialChatState('c1'), replaced(win(52, 66, true), 0))
    const prev60 = itsOf(s.turns.get(T7)!).find((x) => x.iteration === 60)!
    s = reduce(s, loaded(TID, [it_(60, true)], 0))
    expect(itsOf(s.turns.get(T7)!).find((x) => x.iteration === 60)).toBe(prev60)
  })

  it('新迭代号一律 append（无论轻重）—— I4 只增不减', () => {
    let s = reduce(initialChatState('c1'), replaced(win(60, 66, false), 0))
    s = reduce(s, loaded(TID, [it_(59, true)], 0))
    const its = itsOf(s.turns.get(T7)!)
    expect(nums(its)).toEqual([59, 60, 61, 62, 63, 64, 65, 66])
    expect(its[0].toolsFolded).toBe(true) // 新增的轻字段迭代原样保留
    expect(its[1].tools[0].summary).toBe('SUM60') // 既有完整迭代未被牵连
  })
})

// ─── T10：区域段不是 gap ─────────────────────────────────────

describe('T10 区域段（regions_before）不是 gap ⇒ 不触发会话重载', () => {
  it('窗口 52..66 + 段 40..51 到达后 union 连续 ⇒ unreachableGapSig 恒空、gapReloadToken 不自增', () => {
    let s = reduce(initialChatState('c1'), replaced(win(52, 66, true), 12))
    expect(s.unreachableGapSig).toBe('')
    expect(s.gapReloadToken).toBe(0)
    s = reduce(s, loaded(TID, win(40, 51, true), 0))
    expect(s.gapReloadToken).toBe(0)
    expect(s.unreachableGapSig).toBe('')
    // 段到达后下一次 reload（同一窗口）：本地 40..66 连续 ⇒ 仍无 gap、不回退。
    s = reduce(s, replaced(win(52, 66, true), 0))
    expect(s.gapReloadToken).toBe(0)
    expect(s.unreachableGapSig).toBe('')
    expect(nums(itsOf(s.turns.get(T7)!))).toEqual(Array.from({ length: 27 }, (_, i) => 40 + i))
  })

  it('段缺一块 ⇒ 本事件不检测（case 不跑 gap 判据），下一次 history_replaced 才检测', () => {
    let s = reduce(initialChatState('c1'), replaced(win(52, 66, true), 12))
    const seg = win(40, 51, true).filter((x) => x.iteration !== 46)
    s = reduce(s, loaded(TID, seg, 0))
    // iterations_loaded 只做 union —— 绝不因本地有洞触发重载（那不是它的职责）。
    expect(s.gapReloadToken).toBe(0)
    expect(s.unreachableGapSig).toBe('')
    expect(nums(itsOf(s.turns.get(T7)!))).not.toContain(46)
    // 下一次权威 reload：洞 46 落在权威窗口 [52..66] 之外 ⇒ 无法追赶 ⇒ 触发重载。
    s = reduce(s, replaced(win(52, 66, true), 0))
    expect(s.gapReloadToken).toBe(1)
    expect(s.unreachableGapSig).toBe('7:gap46-46')
  })
})

// ─── L1：live 闭环（busy 快照折叠视图的区域窗口声明全链） ────────
// 方案 §3.5（P1）+ callbacks.go:333 切 GetActiveProgressFolded 的正确性前提：
// busy 会话切回时 active_progress 是折叠窗口 + iteration_regions_before 声明 ⇒
// live 行顶部同样渲染「更早区域」分隔条；段加载 / 提交 / reload 全程 regionsBefore 不丢。

const liveTurn = (its: WebIteration[], regionsBefore?: number): Turn => ({
  id: T7,
  user: null,
  phase: { kind: 'live', data: { ...EMPTY_LIVE, iterations: its, regionsBefore } },
  requestID: null,
})

const withTurn = (t: Turn) => reduce(initialChatState('c1'), { type: 'history_replaced', legacy: [], turns: [t], active: null, lastSeq: null, todos: [] } as DomainEvent)

const liveRbOf = (t: Turn): number | undefined =>
  t.phase.kind !== 'committed' ? t.phase.data.regionsBefore : undefined

describe('L1 live regionsBefore 闭环', () => {
  it('live 段加载：iterations union + regionsBefore 显式携带时权威更新', () => {
    let s = withTurn(liveTurn(win(52, 66), 8))
    s = reduce(s, loaded(TID, win(40, 51), 5))
    const t = s.turns.get(T7)!
    expect(t.phase.kind).toBe('live')
    expect(liveRbOf(t)).toBe(5)
    expect(nums(itsOf(t))).toEqual(Array.from({ length: 27 }, (_, i) => 40 + i))
  })

  it('live 段加载：regionsBefore 缺省（详情端点语义）⇒ 窗口声明不动', () => {
    let s = withTurn(liveTurn(win(52, 66), 8))
    s = reduce(s, loaded(TID, win(50, 51), undefined))
    expect(liveRbOf(s.turns.get(T7)!)).toBe(8)
  })

  it('★ text_final 提交携带：live 的 regionsBefore 随 commitViaFold 进 committed payload（提交瞬间分隔条不消失）', () => {
    const s = withTurn(liveTurn(win(52, 66), 5))
    const fin = reduce(s, {
      type: 'text_final',
      turnID: T7,
      content: null,
      progressHistory: [],
      cancelled: false,
    } as DomainEvent)
    const t = fin.turns.get(T7)!
    expect(t.phase.kind).toBe('committed')
    // mutation：text_final 的 commitViaFold 漏传第 5 参 ⇒ 本断言必红（分隔条在提交瞬间消失）。
    expect(rbOf(t)).toBe(5)
  })

  it('★ mergeTurnData 取 min：本地已加载段（regionsBefore 更小）不被 reload 的更大声明覆盖', () => {
    let s = withTurn(committedTurn(win(40, 66), 3))
    // 服务端 reload 声明它自己窗口的剩余数（不知道本地已加载 40..51）⇒ min 保留本地真实剩余。
    s = reduce(s, replaced(win(52, 66), 5))
    expect(rbOf(s.turns.get(T7)!)).toBe(3)
    // 单侧有值 ⇒ 取该侧。
    s = reduce(s, replaced(win(52, 66), 5))
    expect(rbOf(s.turns.get(T7)!)).toBe(3)
  })

  it('★ via:text 的 mergeTurnData 同样保留 regionsBefore（commitViaText 第 4 参——轮 2 自审修复的遗漏）', () => {
    // 本地 committed via:'text'（最终文本权威）× incoming reload：text 分支曾漏传
    // 第 4 参 ⇒ regionsBefore 丢失、分隔条消失。mutation：去掉 commitViaText 调用的
    // regionsBefore ⇒ 本条必红。
    const textTurn: Turn = {
      id: T7,
      user: null,
      requestID: null,
      phase: { kind: 'committed', payload: commitViaText('final answer' as never, win(52, 60), undefined, 3) },
    }
    let s = withTurn(textTurn)
    s = reduce(s, replaced(win(52, 60), 5))
    expect(rbOf(s.turns.get(T7)!)).toBe(3)
  })

  it('normalize→snapshot 链：iteration_regions_before >0 才透传（0/缺省不造键）', async () => {
    const { historyProgressToLive } = await import('@/components/agent/normalize')
    const raw = {
      phase: 'tool_exec',
      iteration: 60,
      turn_id: TID,
      iteration_regions_before: 4,
    } as never
    const snap = historyProgressToLive(raw)
    expect(snap.iterationRegionsBefore).toBe(4)
    expect(historyProgressToLive({ phase: 'tool_exec', turn_id: TID, iteration: 1 } as never).iterationRegionsBefore).toBeUndefined()
    expect(historyProgressToLive({ phase: 'tool_exec', turn_id: TID, iteration_regions_before: 0 } as never).iterationRegionsBefore).toBeUndefined()
  })

  it('snapshotToLive 透传：historyToReplaced 的 active 快照携带 regionsBefore（busy 恢复升级 live）', async () => {
    const { historyToReplaced } = await import('./integrate')
    // historyToReplaced 的第二参 = **原始 HistProgress**（内部经 historyProgressToLive →
    // snapshotToLive 解析；生产链路 useChatMessages 的 setInitialProgress(data.active_progress)
    // 传的正是原始 JSON）⇒ 这里必须喂 snake_case 原始形状。
    const ev = historyToReplaced(
      [{ id: 'm1', dbID: 1, role: 'assistant', content: '', turnID: TID, iterations: win(52, 60), timestamp: '', isPartial: false }],
      { phase: 'tool_exec', iteration: 60, turn_id: TID, iteration_regions_before: 4, iteration_history: [] },
    )
    if (ev.type !== 'history_replaced') throw new Error('expected history_replaced')
    expect(ev.active).not.toBeNull()
    expect(ev.active!.snapshot.regionsBefore).toBe(4)
  })
})

// ─── U1：user 行可见性规则（2026-10-01 用户规则，回归修复的守护） ────────────────
// 用户原话：「如果用户看到了一个用户输入，那么这个用户输入之后的所有消息就必须是完整
// 的，不能是接下来动态加载的。所以这种情况如果需要动态加载，你不能渲染那个用户的输入。」
// —— committed turn 的 regionsBefore > 0（更早区域待动态加载）⇒ user 行不渲染
//（否则「user 输入悬在折叠内容上方」破坏对话时间线视觉）；live/frozen 除外。
// mutation：去掉 deriveRows 的 userHiddenByFold 过滤 ⇒ 第一条必红。

describe('U1 user 行可见性规则（user 可见 ⇒ 其后内容完整）', () => {
  const stateWith = (t: Turn) =>
    reduce(initialChatState('c1'), {
      type: 'history_replaced',
      legacy: [],
      turns: [t],
      active: null,
      lastSeq: null,
      todos: [],
    } as DomainEvent)
  const userOf = { id: 'turn-7-user', content: '帮我优化历史加载', timestamp: '', isNotification: false } as never
  const committedWithUser = (rb?: number): Turn => ({
    id: T7,
    user: userOf,
    requestID: null,
    phase: { kind: 'committed', payload: commitViaFold(win(52, 66) as never, '', 0, undefined, rb) },
  })

  it('★ committed regionsBefore>0 ⇒ user 行不渲染（不能渲染悬空的 user 输入）', () => {
    const rows = deriveRows(stateWith(committedWithUser(6)))
    expect(rows.some((r) => r.kind === 'user')).toBe(false)
    expect(rows.some((r) => r.kind === 'committed')).toBe(true) // assistant 折叠行仍在
  })

  it('regionsBefore=0/undefined ⇒ user 行正常渲染（绝大多数 turn 零变化）', () => {
    expect(deriveRows(stateWith(committedWithUser())).some((r) => r.kind === 'user')).toBe(true)
    expect(deriveRows(stateWith(committedWithUser(0))).some((r) => r.kind === 'user')).toBe(true)
  })

  it('live turn regionsBefore>0 ⇒ user 行仍渲染（正在生成的对话不能藏用户刚发的消息）', () => {
    const s = stateWith({
      id: T7,
      user: userOf,
      requestID: null,
      phase: { kind: 'live', data: { ...EMPTY_LIVE, iterations: win(52, 66), regionsBefore: 5 } },
    })
    expect(deriveRows(s).some((r) => r.kind === 'user')).toBe(true)
  })

  it('★ 段加载完成（regionsBefore 权威归零）⇒ user 行随完整内容一起出现', () => {
    let s = stateWith(committedWithUser(6))
    s = reduce(s, loaded(TID, win(40, 51), 0)) // 服务端显式下发 0（归零声明）
    expect(deriveRows(s).some((r) => r.kind === 'user')).toBe(true)
  })
})
