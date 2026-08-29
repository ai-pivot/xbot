/**
 * reduce.test.ts — 状态机转移表测试：8 个历史 P0 各一个回归 + 不变量断言。
 *
 * 每个 test 的头注释标注它根治的历史 bug（design doc §6 映射表）。
 */

import { describe, expect, it } from 'vitest'
import { deriveRows } from './derive'
import { normalizeEvent } from './normalize'
import { reduce } from './reduce'
import {
  commitViaFold,
  initialChatState,
  iterNum,
  turnID,
  type ChatState,
  type DomainEvent,
  type Turn,
} from './types'
import type { WebIteration } from '@/types/shared'

// ─── 测试 DSL ─────────────────────────────────────────────────

const T1 = turnID(1)
const T2 = turnID(2)

function run(events: readonly DomainEvent[], from: ChatState = initialChatState('chat-1')): ChatState {
  return events.reduce(reduce, from)
}

const iteration1 = (turn: ReturnType<typeof turnID>, content = '', iter = 1): DomainEvent => ({
  type: 'iteration',
  turnID: turn,
  iter: iterNum(iter),
  seq: 10 as never,
  content: content || undefined,
  reasoning: undefined,
  activeTools: [],
  completedTools: [],
  iterationsDelta: [],
  todos: undefined,
  subAgents: undefined,
  tokenUsage: undefined,
  streamStats: undefined,
})

const started = (turn: ReturnType<typeof turnID>, requestID: string | null = null): DomainEvent => ({
  type: 'turn_started',
  turnID: turn,
  requestID,
  trigger: 'user',
  content: null,
})

const textFinal = (turn: ReturnType<typeof turnID> | null, content: string | null, cancelled = false): DomainEvent => ({
  type: 'text_final',
  turnID: turn,
  content: content === null ? null : (content as never),
  progressHistory: [],
  cancelled,
})

const phaseDone = (turn: ReturnType<typeof turnID>, finalIteration: DomainEvent extends never ? never : {
  iteration: number
  content: string
  reasoning: string
  tools: never[]
  toolCount: number
} | null): DomainEvent => ({
  type: 'phase_done',
  turnID: turn,
  seq: 99 as never,
  finalIteration,
  todos: undefined,
}) as DomainEvent

// ─── I1-I6 不变量断言器（性质测试的基石） ─────────────────────

export function assertInvariants(s: ChatState): void {
  // I1 槽位唯一 + 三态互斥（类型已保证 —— 运行时复核）。
  let liveCount = 0
  for (const t of s.turns.values()) {
    expect(['live', 'frozen', 'committed']).toContain(t.phase.kind)
    if (t.phase.kind === 'live') liveCount++
    // I2 committed 可渲染。
    if (t.phase.kind === 'committed') {
      const p = t.phase.payload
      if (p.via === 'text') expect(p.content.length).toBeGreaterThan(0)
      else expect(p.iterations.length).toBeGreaterThan(0)
    }
  }
  // I3 活动唯一。
  expect(liveCount).toBeLessThanOrEqual(1)
  if (s.activeTurn !== null) {
    const t = s.turns.get(s.activeTurn)
    expect(t).toBeDefined()
    expect(t?.phase.kind).toBe('live')
  } else {
    expect(liveCount).toBe(0)
  }
}

// ─── 回归测试 ─────────────────────────────────────────────────

describe('TDSM reduce — 历史 P0 回归', () => {
  it('REPRO: 迭代前进时 stream 事件携带 iteration 清空旧 content（老 content 到新迭代）', () => {
    // 迭代 1 完整产出 content='老内容'（live.iter=1）。
    const s0 = run([
      started(T1),
      iteration1(T1, '老内容', 1),
    ])
    // 迭代 2 开始：后端 stamp iteration=2 的 stream 事件先到（reasoning 已产出，
    // content 尚未产出）。修复前前端丢弃 iteration → stream case 只做全量替换
    // → content 残留迭代 1 的 '老内容'（"老 content 到新迭代"竞态）。
    const s1 = reduce(s0, {
      type: 'stream', turnID: T1, seq: null,
      iteration: iterNum(2),
      content: undefined, reasoning: '新思考', streamingTools: undefined, genui: undefined, streamStats: undefined,
    })
    const t1 = s1.turns.get(T1)!
    if (t1.phase.kind !== 'live') throw new Error('must stay live')
    expect(t1.phase.data.iter).toBe(2)
    expect(t1.phase.data.content).toBe('')          // 旧 content 清空（不残留）
    expect(t1.phase.data.reasoning).toBe('新思考') // 新 reasoning 保留
    // 同迭代后续 stream（iteration=2）不误清空，累积保留。
    const s2 = reduce(s1, {
      type: 'stream', turnID: T1, seq: null,
      iteration: iterNum(2),
      content: '新内容', reasoning: undefined, streamingTools: undefined, genui: undefined, streamStats: undefined,
    })
    const t2 = s2.turns.get(T1)!
    if (t2.phase.kind !== 'live') throw new Error('must stay live')
    expect(t2.phase.data.content).toBe('新内容')
  })

  it('Bug1: cancel 后新 turn 的事件不被旧 turn guard 拦截（SSE 更新但前端卡死）', () => {
    // turn 1 流式产出 → cancel ack（text_final cancelled）→ turn 2 正常接收事件。
    const s = run([
      started(T1),
      { type: 'stream', turnID: T1, seq: null, iteration: null, content: '部分内容', reasoning: undefined, streamingTools: undefined, genui: undefined, streamStats: undefined },
      iteration1(T1, '', 1),
      textFinal(T1, null, true), // cancel ack：content null（截断）→ fold
      started(T2),
      iteration1(T2, '新 turn 迭代内容', 1),
    ])
    assertInvariants(s)
    // turn 2 正常写入（不被任何 guard 拦截）。
    const t2 = s.turns.get(T2)!
    expect(t2.phase.kind).toBe('live')
    if (t2.phase.kind === 'live') expect(t2.phase.data.content).toBe('新 turn 迭代内容')
    // turn 1 已 committed（cancel fold，数据保全）。
    expect(s.turns.get(T1)?.phase.kind).toBe('committed')
  })

  it('Bug2: 切换会话（history_replaced）后新会话 turn_id=1 的事件正常（无残留拦截）', () => {
    // 会话 A：turn 50 结束。
    const sA = run([
      started(turnID(50)),
      textFinal(turnID(50), '会话A的回复'),
    ])
    expect(sA.turns.get(turnID(50))?.phase.kind).toBe('committed')
    // 切换会话 B：history_replaced 全量替换（旧 turns 清空 —— 无全局残留）。
    const sB = reduce(sA, {
      type: 'history_replaced',
      legacy: [],
      turns: [],
      active: null,
      lastSeq: null, todos: [],
    })
    // 会话 B：turn_id=1 的事件正常写入。
    const sB2 = run([started(T1), iteration1(T1, '会话B内容')], sB)
    assertInvariants(sB2)
    const t1 = sB2.turns.get(T1)!
    expect(t1.phase.kind).toBe('live')
    if (t1.phase.kind === 'live') expect(t1.phase.data.content).toBe('会话B内容')
  })

  it('Bug3: turn_started 提前 fold 保全 content —— 迟到 text 到达前数据不丢', () => {
    // turn 1 流式产出（text 未到）→ 用户发新消息 turn_started(2) fold。
    const s = run([
      started(T1),
      { type: 'stream', turnID: T1, seq: null, iteration: null, content: '流式输出的完整回复', reasoning: undefined, streamingTools: undefined, genui: undefined, streamStats: undefined },
      started(T2), // text 未到 —— fold commit（数据保全）
    ])
    assertInvariants(s)
    const t1 = s.turns.get(T1)!
    expect(t1.phase.kind).toBe('committed')
    if (t1.phase.kind === 'committed') {
      expect(t1.phase.payload.content).toBe('流式输出的完整回复')
    }
    // 迟到 text(1) 到达：committed 幂等（不重复、不丢）。
    const s2 = reduce(s, textFinal(T1, '流式输出的完整回复'))
    expect(s2).toBe(s) // 引用不变 —— 零渲染
    expect(s2.turns.get(T1)?.phase.kind).toBe('committed')
  })

  it('Bug4: normalize 处理 Go nil slice（tools:null）—— 状态机永不见 null 数组', () => {
    const evs = normalizeEvent(
      {
        type: 'progress_structured',
        progress: {
          phase: 'tool_exec',
          turn_id: 1,
          iteration: 1,
          seq: 5,
          active_tools: null,
          completed_tools: null,
          iteration_history: [{ iteration: 1, content: 'x', tools: null }],
        },
      },
      'chat-1',
    )
    expect(evs).not.toBeNull()
    const ev = evs?.[0]
    expect(ev?.type).toBe('iteration')
    if (ev?.type === 'iteration') {
      expect(ev.activeTools).toEqual([])
      expect(ev.iterationsDelta).toHaveLength(1)
      expect(ev.iterationsDelta[0].tools).toEqual([])
    }
    // 状态机消化后 derive 不抛（渲染层无 null 可见 —— T1）。
    const s = run([started(T1), ...(evs ?? [])])
    expect(() => deriveRows(s)).not.toThrow()
  })

  it('Bug9+多事件: progress_structured 同时携带结构化+流式载荷 → [stream, iteration]（get_active_progress 合并快照形状）', () => {
    const evs = normalizeEvent(
      {
        type: 'progress_structured',
        progress: {
          phase: 'tool_exec',
          turn_id: 2,
          iteration: 2,
          seq: 9,
          stream_content: '流式与结构化并存',
          genui_content: 'export default function App(){}',
          active_tools: [{ name: 'Shell', status: 'running' }],
        },
      },
      'chat-1',
    )
    expect(evs).not.toBeNull()
    if (!evs || evs.length < 2) throw new Error(`expected [stream, iteration], got ${JSON.stringify(evs)}`)
    expect(evs[0].type).toBe('stream') // stream 先应用
    expect(evs[1].type).toBe('iteration')
    if (evs[0].type === 'stream') expect(evs[0].genui).toContain('App')
    // 状态机消化：genui 与 activeTools 同时生效（非空 phase 不再丢流式载荷）。
    const s = run([started(T2), ...evs])
    const t = s.turns.get(T2)
    if (t?.phase.kind !== 'live') throw new Error('must be live')
    expect(t.phase.data.genui).toContain('App')
    expect(t.phase.data.activeTools.map((x) => x.name)).toContain('Shell')
  })

  it('Bug5: 最后迭代经 phase_done fold —— text 到达前已保留（iter 产生了就不消失）', () => {
    const finalIter = { iteration: 1, content: '最后迭代内容', reasoning: '', tools: [], toolCount: 0 }
    const s = run([
      started(T1),
      iteration1(T1),
      phaseDone(T1, finalIter), // PhaseDone 补记最后迭代
    ])
    assertInvariants(s)
    let t1 = s.turns.get(T1)!
    expect(t1.phase.kind).toBe('live')
    if (t1.phase.kind === 'live') {
      expect(t1.phase.data.iterations).toHaveLength(1)
      expect(t1.phase.data.iterations[0].content).toBe('最后迭代内容')
    }
    // text 到达 → commit（iterations 保留 —— T3）。
    const s2 = reduce(s, textFinal(T1, '最终回复'))
    t1 = s2.turns.get(T1)!
    expect(t1.phase.kind).toBe('committed')
    if (t1.phase.kind === 'committed') {
      expect(t1.phase.payload.iterations).toHaveLength(1)
      expect(t1.phase.payload.iterations[0].content).toBe('最终回复')
      expect(t1.phase.payload.content).toBe('最终回复')
    }
  })

  it('Bug6: commit 后每 turn 恰一行 —— 无 ghost turn-N-live 行', () => {
    const s = run([
      started(T1),
      iteration1(T1, 'iter1 内容'),
      textFinal(T1, '最终回复'),
      started(T2),
      iteration1(T2, '新 turn'),
    ])
    const rows = deriveRows(s)
    // turn 1 恰一行（committed）；turn 2 恰一行（live）。
    const t1Rows = rows.filter((r) => r.turnID === 1)
    const t2Rows = rows.filter((r) => r.turnID === 2)
    expect(t1Rows).toHaveLength(1)
    expect(t2Rows).toHaveLength(1)
    expect(t1Rows[0].kind).toBe('committed')
    expect(t2Rows[0].kind).toBe('live')
    // 无 id 同时含 -live 与 -c 的行。
    expect(rows.some((r) => r.kind === 'live' && r.id.includes('1-'))).toBe(false)
  })

  it('Bug7: isPartial 只在 live/frozen —— pending user 沉底、turn 内 user<assistant', () => {
    const userRow = {
      id: 'u1',
      content: '用户消息' as never,
      timestamp: '2026-01-01T00:00:00Z',
      isNotification: false,
      queued: false,
      sending: false,
      requestID: 'req-1',
      turnHint: undefined,
      dbID: undefined,
    }
    const s = run([
      { type: 'user_sent', row: userRow },
      started(T1, 'req-1'), // requestID 精确绑定
      iteration1(T1, '流式中'),
    ])
    const rows = deriveRows(s)
    // isPartial 只在 assistant live/frozen 行；user 行永远非 partial。
    for (const r of rows) {
      if (r.kind === 'user') continue
      else expect(r.isPartial).toBe(true)
    }
    // turn 内顺序：user 在 assistant 前（T5）。
    const userIdx = rows.findIndex((r) => r.kind === 'user' && r.content === '用户消息')
    const liveIdx = rows.findIndex((r) => r.kind === 'live')
    expect(userIdx).toBeGreaterThanOrEqual(0)
    expect(userIdx).toBeLessThan(liveIdx)
    // 绑定成功：pendingUsers 空。
    expect(s.pendingUsers).toHaveLength(0)
  })

  it('Bug8: 发新消息收尸旧 turn —— 每 turn 恰一行（无重复渲染）', () => {
    const s = run([
      started(T1),
      iteration1(T1, '旧 turn 内容'),
      started(T2), // 收尸 fold
      iteration1(T2, '新 turn 内容'),
    ])
    assertInvariants(s)
    const rows = deriveRows(s)
    const t1Rows = rows.filter((r) => r.turnID === 1)
    expect(t1Rows).toHaveLength(1)
    expect(t1Rows[0].kind).toBe('committed')
    if (t1Rows[0].kind === 'committed') expect(t1Rows[0].content).toBe('旧 turn 内容')
  })

  it('session(idle) 兜底：live 有产出 → frozen 定格；无产出 → 删槽（幽灵行灭绝）', () => {
    // 有产出的 live：PhaseDone + text 都丢 → idle → frozen 定格。
    const sA = run([
      started(T1),
      { type: 'stream', turnID: T1, seq: null, iteration: null, content: '已产出内容', reasoning: undefined, streamingTools: undefined, genui: undefined, streamStats: undefined },
      { type: 'session', busy: false },
    ])
    assertInvariants(sA)
    const t1 = sA.turns.get(T1)!
    expect(t1.phase.kind).toBe('frozen')
    if (t1.phase.kind === 'frozen') expect(t1.phase.data.content).toBe('已产出内容')
    // frozen 行渲染（isPartial 保留 activeTools 通道）。
    const rowsA = deriveRows(sA)
    expect(rowsA.filter((r) => r.turnID === 1)).toHaveLength(1)

    // 无产出的 live：idle → 删槽。
    const sB = run([started(T1), { type: 'session', busy: false }])
    assertInvariants(sB)
    expect(sB.turns.size).toBe(0)
    expect(deriveRows(sB)).toHaveLength(0)
  })

  it('frozen 后迟到 text_final（cancel 补齐）→ committed（决策点1：text 权威路径）', () => {
    // cancel：live → text_final(cancelled) fold commit（带 progressHistory 补齐）。
    const s = run([
      started(T1),
      iteration1(T1, 'cancel 前的迭代内容'),
      {
        type: 'text_final',
        turnID: T1,
        content: null, // cancel：text 无 content
        progressHistory: [
          { iteration: 1, content: 'cancel 补齐的权威迭代内容', reasoning: '', tools: [], toolCount: 0 },
          { iteration: 2, content: 'cancel 前进行中的迭代（补齐）', reasoning: '', tools: [], toolCount: 0 },
        ],
        cancelled: true,
      },
    ])
    assertInvariants(s)
    const t1 = s.turns.get(T1)!
    expect(t1.phase.kind).toBe('committed')
    if (t1.phase.kind === 'committed') {
      // 权威 progressHistory 覆盖同号 + append 补齐 —— T3 + 权威数据。
      expect(t1.phase.payload.iterations).toHaveLength(2)
      expect(t1.phase.payload.iterations[0].content).toBe('cancel 补齐的权威迭代内容')
      expect(t1.phase.payload.iterations[1].content).toBe('cancel 前的迭代内容')
    }
  })

  it('AskUser cancel: finalText 落到当前迭代（live.iter），不覆盖上一个已完成迭代', () => {
    // 场景：turn 已完成 iter1、iter2（iterations=[1,2]）；AskUser 工具在 iter3 中
    // 调用 → WaitingUser（live.iter=3，iter3 未完成、无 in-flight 工具 ——
    // activeTools 为空）。用户点 Cancel → text_final(cancelled, content=null)，
    // cancel ack 不带 progressHistory。回归 bug：旧代码把 finalText
    // （= nonEmptyStr(live.content) = 'iter3 content'）无条件覆盖到【最后一个
    // 已存在迭代】（iter2）→ iter2 的 content 变成 iter3 的文本（用户报告：
    // "askuser 取消后迭代渲染混乱顺序错乱"——已完成迭代内容被当前迭代文本覆盖）。
    // ⚠️ 不能 spread iteration1() 再加 seq —— DomainEvent union 的 turn_started
    // 成员没有 seq 属性，tsc build 报错。用完整字面量构造。
    const iterEvent = (iter: number, content: string, seq: number, delta: WebIteration[]): DomainEvent => ({
      type: 'iteration',
      turnID: T1,
      iter: iterNum(iter),
      seq: seq as never,
      content,
      reasoning: undefined,
      activeTools: [],
      completedTools: [],
      iterationsDelta: delta,
      todos: undefined,
      subAgents: undefined,
      tokenUsage: undefined,
      streamStats: undefined,
    })
    const s = run([
      started(T1),
      iteration1(T1, 'iter1 content', 1),
      iterEvent(2, 'iter2 content', 11, [{ iteration: 1, content: 'iter1 content', reasoning: '', tools: [], toolCount: 0 }]),
      iterEvent(3, 'iter3 content', 12, [{ iteration: 2, content: 'iter2 content', reasoning: '', tools: [], toolCount: 0 }]),
    ])
    const t = s.turns.get(T1)!
    expect(t.phase.kind).toBe('live')
    if (t.phase.kind === 'live') {
      expect(t.phase.data.iter).toBe(3)
      expect(t.phase.data.iterations.map((i) => i.iteration)).toEqual([1, 2])
      expect(t.phase.data.content).toBe('iter3 content')
    }

    const s2 = run([textFinal(T1, null, true)], s)
    const t2 = s2.turns.get(T1)!
    expect(t2.phase.kind).toBe('committed')
    if (t2.phase.kind === 'committed') {
      // iter2 内容必须保持（不被 iter3 的文本覆盖）。
      expect(t2.phase.payload.iterations.find((i) => i.iteration === 2)?.content).toBe('iter2 content')
      // 当前迭代 iter3 的内容必须保留（追加为完成迭代）。
      const it3 = t2.phase.payload.iterations.find((i) => i.iteration === 3)
      expect(it3?.content).toBe('iter3 content')
    }
  })

  it('I5: 同 turn 内 seq 重放丢弃（引用不变 —— 零渲染）', () => {
    const s0 = run([started(T1), iteration1(T1, '内容')])
    const replay = reduce(s0, iteration1(T1, '内容')) // 同 seq 重放
    expect(replay).toBe(s0)
    // 旧 turn（非 active）事件也引用不变。
    const stale = reduce(s0, iteration1(turnID(99), '旧事件'))
    expect(stale).toBe(s0)
  })

  it('legacy 前缀段：无 turn_id 历史按 DB 顺序渲染（决策点2：独立只读段）', () => {
    const s = reduce(initialChatState('chat-1'), {
      type: 'history_replaced',
      legacy: [
        { id: 'h1', role: 'user', content: '旧消息1', iterations: [], timestamp: 't1', dbID: 1 },
        { id: 'h2', role: 'assistant', content: '旧回复1', iterations: [], timestamp: 't2', dbID: 2 },
      ],
      turns: [],
      active: null,
      lastSeq: null, todos: [],
    })
    const rows = deriveRows(s)
    expect(rows).toHaveLength(2)
    expect(rows[0].kind === 'user' && rows[0].content).toBe('旧消息1')
    expect(rows[1].kind === 'committed' && rows[1].content).toBe('旧回复1')
  })

  it('history_replaced MERGE：committed turn 不在 DB 快照里也保留（发 user msg 后 agent 消息不消失）', () => {
    // turn 1 经 text_final commit（只存在于状态机 —— messages 未同步）。
    const s0 = run([
      started(T1),
      { type: 'stream', turnID: T1, seq: null, iteration: null, content: 'turn1 回复', reasoning: undefined, streamingTools: undefined, genui: undefined, streamStats: undefined },
      textFinal(T1, 'turn1 回复'),
    ])
    expect(s0.turns.get(T1)?.phase.kind).toBe('committed')
    // 用户发新消息 → user_echo → messages 变化 → history_replaced（不含 turn 1）。
    const s1 = reduce(s0, {
      type: 'history_replaced',
      legacy: [],
      turns: [], // DB 快照还没有 turn 1（appendAssistant 接线已移除）
      active: null,
      lastSeq: null, todos: [],
    })
    // 修复后：turn 1 的 committed 数据保留（不消失）。
    const t1 = s1.turns.get(T1)
    expect(t1).toBeDefined()
    expect(t1?.phase.kind).toBe('committed')
    const rows = deriveRows(s1)
    expect(rows.some((r) => r.kind === 'committed' && r.content === 'turn1 回复')).toBe(true)
  })

  it('history_replaced MERGE：live turn + activeTurn 存活（echo 竞态不打断打字机）', () => {
    // turn_started 建 live → echo 触发的 history_replaced 到达（时序颠倒）。
    const s0 = run([
      started(T1),
      { type: 'stream', turnID: T1, seq: 5 as never, iteration: null, content: '流式前半', reasoning: undefined, streamingTools: undefined, genui: undefined, streamStats: undefined },
    ])
    const s1 = reduce(s0, { type: 'history_replaced', legacy: [], turns: [], active: null, lastSeq: null, todos: [] })
    // 修复后：live turn 存活 + activeTurn 保持 → 后续 stream 继续接收。
    expect(s1.activeTurn).toBe(T1)
    expect(s1.turns.get(T1)?.phase.kind).toBe('live')
    const s2 = reduce(s1, {
      type: 'stream',
      turnID: T1,
      seq: 6 as never,
      iteration: null,
      content: '流式后半（打字机继续）',
      reasoning: undefined,
      streamingTools: undefined,
      genui: undefined, streamStats: undefined,
    })
    const t = s2.turns.get(T1)
    if (t?.phase.kind === 'live') expect(t.phase.data.content).toBe('流式后半（打字机继续）')
    else throw new Error('live turn died across history_replaced')
  })

  it('REPRO: turn 已绑定乐观 user 后,user_echo(同 request) 不得再产生 pending 行（双 user 行+双思考中）', () => {
    // 真实发送时序：user_sent(乐观 R) → turn_started(R 绑定进 turn1.user) → user_echo(同 R)。
    // 漏洞：user_echo 到达时 turn1.user 已非 null → 不入 turn，反被追加进 pendingUsers
    // → deriveRows 输出两条 user（turn1.user + pending echo）→ 双 user 行 + 双思考中。
    const opt = { id: 'opt-1', content: '用户消息', isNotification: false, queued: false, sending: false, requestID: 'req-1' }
    const s = run([
      { type: 'user_sent', row: { ...opt, content: '用户消息' as never, timestamp: 't', turnHint: undefined, dbID: undefined } },
      started(T1, 'req-1'), // requestID 绑定到 turn1.user，pending 移除
      iteration1(T1, '流式中'), // 思考中（thinking live）
    ]) as ChatState
    // 乐观已绑定：turn1.user 就位、pending 空。
    expect(s.turns.get(T1)?.user?.requestID ?? null).toBe('req-1')
    expect(s.pendingUsers).toHaveLength(0)
    // user_echo：同 requestID、turnHint 指向已绑定 turn —— 不应再产生第二行。
    const s2 = reduce(s, {
      type: 'user_echo',
      row: {
        id: 'echo-1', content: '用户消息' as never, timestamp: 't',
        isNotification: false, queued: false, sending: false,
        requestID: 'req-1', turnHint: 1, dbID: undefined,
      } ,
    })
    const userRows = deriveRows(s2).filter((r) => r.kind === 'user')
    // 修复后：同一条 user 恰好一行（turn 内已绑定，echo 幂等去重）。
    expect(userRows).toHaveLength(1)
    expect(s2.pendingUsers).toHaveLength(0)
    // 渲染顺序：user 在 live(思考中) 之前、无 pending 底部幽灵。
    const idxUser = deriveRows(s2).findIndex((r) => r.kind === 'user')
    const idxLive = deriveRows(s2).findIndex((r) => r.kind === 'live')
    expect(idxUser).toBeGreaterThanOrEqual(0)
    expect(idxUser).toBeLessThan(idxLive)
    expect(deriveRows(s2)).toHaveLength(2) // user + live assistant
  })

  it('REPRO: 后端真实字段(id)的 user_echo 经 normalizeEvent —— requestID 必须解析非 null（字段名不匹配 = 双行根因）', () => {
    // 后端 web_inbound.go 把 requestID 序列化到 WSMessage.ID（json:"id"），不带
    // request_id 字段。normalizeUserEcho 旧代码只读 env.request_id → 永远 null →
    // 幂等检查全部跳过 → echo 无条件追加 pendingUsers → 双 user 行 + 双思考中
    //（第二个思考中是 busy placeholder，px-3 缩进多空格 —— 用户报告）。
    // 兜底失效场景（"概率很低"）：REST ack 先到把 MessageStore 行标 persisted →
    // useChatMessages 的 echo 处理跳过 → history_replaced 不触发 → 状态机 echo 残留。
    const raw = {
      type: 'user_echo',
      id: 'req-9',
      content: '用户消息',
      ts: 1723600000,
      turn_id: 3,
      chat_id: 'chat-1',
    }
    const evs = normalizeEvent(raw, 'chat-1')
    expect(evs).not.toBeNull()
    expect(evs).toHaveLength(1)
    const ev = evs![0] as { type: string; row: { requestID: string | null; turnHint: number | undefined } }
    expect(ev.type).toBe('user_echo')
    // requestID 必须从后端的 id 字段解析出来 —— 这是幂等去重的前提。
    expect(ev.row.requestID).toBe('req-9')
    expect(ev.row.turnHint).toBe(3)
  })

  it('REPRO: REST ack 先到竞态 —— 真实字段 echo(id) 不产生第二行（双 user+双思考中根治）', () => {
    // 完整时序（用户偶发场景）：user_sent → REST ack（turnHint 回填）→
    // turn_started 绑定 → user_echo（后端 id 字段）→ 幂等，绝不双行。
    const s0 = run([
      { type: 'user_sent', row: { id: 'opt-1', content: '用户消息' as never, timestamp: 't', isNotification: false, queued: false, sending: false, requestID: 'req-9', turnHint: undefined, dbID: undefined } },
      { type: 'user_ack', requestID: 'req-9', dbID: 0, turnHint: 3, queued: false },
    ])
    const evs = normalizeEvent({
      type: 'user_echo', id: 'req-9', content: '用户消息', ts: 1723600000, turn_id: 3, chat_id: 'chat-1',
    }, 'chat-1')!
    // echo 先于 turn_started 到达（SSE 与 dequeue 竞态）→ ②替换 pending 乐观行（不新增）。
    const s1 = evs.reduce(reduce, s0)
    expect(s1.pendingUsers).toHaveLength(1)
    // turn_started(R) 绑定 → pending 清空。
    const s2 = run([started(turnID(3), 'req-9'), iteration1(turnID(3), '思考中')], s1)
    expect(s2.pendingUsers).toHaveLength(0)
    // 迟到的重复 echo（重连 replay）→ ①幂等返回。
    const s3 = evs.reduce(reduce, s2)
    expect(deriveRows(s3).filter((r) => r.kind === 'user')).toHaveLength(1)
    expect(deriveRows(s3)).toHaveLength(2) // user + live(思考中)，无第二份
  })

  it('REPRO: notification echo 在 turn_started(notification) 绑定后到达不产生第二行（F#1 双行）', () => {
    // 真实时序：后端 drainAndProcessNotifications 注入通知 turn →
    // turn_started(trigger=notification, content=通知全文) 经 SSE progress 通道
    // 先到 → reduce 用 turn_start.content 构造 notif user 行（isNotification=true）。
    // 后端 web.go InjectUserMessage 的 inject_user echo 后到 —— WSMessage 只有
    // Type/TS/ChatID/Content 四个字段（无 id/turn_id/is_notification）→ normalize
    // 后 requestID=null + turnHint=undefined → ③ 不命中（hint 缺失）→ ④ 无条件
    // append pendingUsers → 同一通知渲染两行（turn.user 的 notif-${turnID} 行 +
    // 沉底 echo 行）。
    const s0 = run([
      {
        type: 'turn_started', turnID: T1, requestID: null, trigger: 'notification',
        content: '[System Notification] bg task completed',
      },
      iteration1(T1, '思考中'),
    ])
    // turn_started(notification) 已构造 notif user 行。
    expect(s0.turns.get(T1)?.user?.isNotification).toBe(true)
    expect(s0.pendingUsers).toHaveLength(0)

    // inject_user echo：后端真实形状（无 requestID、无 turnHint）。
    const evs = normalizeEvent({
      type: 'inject_user',
      content: '[System Notification] bg task completed',
      ts: 1723600000,
      chat_id: 'chat-1',
    }, 'chat-1')!
    expect(evs).toHaveLength(1)
    const s1 = evs.reduce(reduce, s0)
    // 修复后：内容幂等丢弃 —— 同一通知恰好一行（notif 行），echo 不入 pending。
    const userRows = deriveRows(s1).filter((r) => r.kind === 'user')
    expect(userRows).toHaveLength(1)
    expect(s1.pendingUsers).toHaveLength(0)
    // 保留的行是 turn.user 的 notif 行（不是沉底 echo 行）。
    expect(userRows[0].id).toBe('notif-1')
  })

  it('F#9: 同毫秒两条 echo 的 id 不碰撞（React key + TanStack 高度测量串行）', () => {
    // 后端连续注入两条消息（同一毫秒内）→ normalizeUserEcho 的
    // `echo-${turn}-${Date.now()}` id 相同 → React key 重复 + TanStack
    // Virtual 高度测量串行。echoSeq 单调后缀保证唯一。
    const a = normalizeEvent({ type: 'inject_user', content: '第一条', chat_id: 'chat-1' }, 'chat-1')
    const b = normalizeEvent({ type: 'inject_user', content: '第二条', chat_id: 'chat-1' }, 'chat-1')
    expect(a).toHaveLength(1)
    expect(b).toHaveLength(1)
    const idA = (a![0] as Extract<DomainEvent, { type: 'user_echo' }>).row.id
    const idB = (b![0] as Extract<DomainEvent, { type: 'user_echo' }>).row.id
    expect(idA).not.toBe(idB)
  })

  it('REPRO: gap 修复 delta 携带旧迭代 —— 不得清空 live 正在流式更新的迭代（CR #1 committedNow）', () => {
    // 场景：前端缺失中间迭代（iterations=[1,2,5]，缺 3,4），live 已流式到迭代 5。
    // 进来一个 gap 修复事件：iterationsDelta 补了早前丢失的迭代 3，ev.iter=3。
    // 旧代码 committedNow = !advanced && appendedNew && ev.iter <= appendedMax
    //   → appendedNew=true（[1,2,3,5] > [1,2,5]），3 <= 3 → true → 误判为 "刚
    //   commit 迭代" → 清空 content/reasoning → **live 迭代 5 的流式内容被误清**。
    // CR 建议：条件为 appendedMax === prev.iter —— delta 补的是旧迭代 3，而 live
    // 当前迭代是 5（3 !== 5），不 committed，保留 live 流式内容。
    const s0 = run([
      started(T1),
      iteration1(T1, '迭代1内容', 1),
      iteration1(T1, '迭代2内容', 2),
    ])
    // 直接落到迭代 5，但只带 delta 5（模拟 3,4 在前端丢失）。
    const sCur = reduce(s0, {
      type: 'iteration', turnID: T1, iter: iterNum(5), seq: 15 as never,
      content: '迭代5内容', reasoning: undefined, activeTools: [], completedTools: [],
      iterationsDelta: [{ iteration: 5, content: '迭代5内容', reasoning: '', tools: [], toolCount: 0 }] as never,
      todos: undefined, subAgents: undefined, tokenUsage: undefined, streamStats: undefined,
    })
    const tLive = sCur.turns.get(T1)!
    if (tLive.phase.kind !== 'live') throw new Error('must stay live')
    expect(tLive.phase.data.iter).toBe(5)
    expect(tLive.phase.data.content).toBe('迭代5内容')
    // 确认 iterations 缺 3,4（前端缺失中间迭代的前提）。
    const liveIters = tLive.phase.data.iterations.map((it) => it.iteration)
    expect(liveIters).not.toContain(3)
    expect(liveIters).not.toContain(4)

    // gap 修复事件：delta 补迭代 3，ev.iter=3（落后于 live 的流式迭代 5）。
    const sGap = reduce(sCur, {
      type: 'iteration', turnID: T1, iter: iterNum(3), seq: 30 as never,
      content: undefined, reasoning: undefined, activeTools: [], completedTools: [],
      iterationsDelta: [{ iteration: 3, content: '迭代3内容(补)', reasoning: '', tools: [], toolCount: 0 }] as never,
      todos: undefined, subAgents: undefined, tokenUsage: undefined, streamStats: undefined,
    })
    const tGap = sGap.turns.get(T1)!
    if (tGap.phase.kind !== 'live') throw new Error('must stay live')
    // 修复后：committedNow=false（appendedMax=3 !== prev.iter=5）→ live 保留流式内容。
    expect(tGap.phase.data.content).toBe('迭代5内容')
    // 补进来的迭代 3 合并进 iterations（append-only，不丢失）。
    const iters = tGap.phase.data.iterations.map((it) => it.iteration)
    expect(iters).toContain(3)
    expect(iters).toContain(5)
  })

  it('REPRO: 发新消息后上一 turn 最后迭代消失 —— 过时 DB 中间快照不得覆盖状态机 committed', () => {
    // 场景：turn 1 已完成（状态机 committed：iterations=[1,2] 全量，text='最终回复'）。
    // chat.messages 里的 turn 1 DB 行是【过时中间快照】（reload/replay_gap 在 turn
    // 运行中拉过一次，DB 只有中间行 iterations=[1]；或最终行尚未持久化）。
    // 发新消息 → REST ack patchUser → messages 变化 → history_replaced 携带过时
    // 快照 → step1 else 分支用 incoming 覆盖 committed → 迭代 2 丢失（用户报告：
    // "发送新消息之后上一个 agent turn 最后一条迭代消息直接消失"）。
    const iterEv = (turn: ReturnType<typeof turnID>, iter: number, content: string): DomainEvent => ({
      type: 'iteration', turnID: turn, iter: iterNum(iter), seq: (10 + iter) as never,
      content: undefined, reasoning: undefined, activeTools: [], completedTools: [],
      iterationsDelta: [{ iteration: iter, content, reasoning: '', tools: [], toolCount: 0 }],
      todos: undefined, subAgents: undefined, tokenUsage: undefined, streamStats: undefined,
    })
    const s0 = run([
      started(T1),
      iterEv(T1, 1, '迭代1内容'),
      iterEv(T1, 2, '迭代2内容（最后迭代）'),
      textFinal(T1, '最终回复'),
    ])
    expect(s0.turns.get(T1)?.phase.kind).toBe('committed')
    const phase0 = s0.turns.get(T1)!.phase
    if (phase0.kind !== 'committed') throw new Error('turn 1 must be committed')
    expect(phase0.payload.iterations).toHaveLength(2)
    // DB 过时中间快照：只有迭代 1（最终迭代 2 的行尚未持久化），非空壳。
    const s1 = reduce(s0, {
      type: 'history_replaced',
      legacy: [],
      turns: [{
        id: T1,
        user: { id: 'db-u1', content: 'q1' as never, timestamp: 't', isNotification: false, queued: false, sending: false, requestID: null, turnHint: undefined, dbID: 11 },
        phase: {
          kind: 'committed',
          payload: {
            via: 'fold',
            iterations: [{ iteration: 1, content: '迭代1内容', reasoning: '', tools: [], toolCount: 0 }] as never,
            content: '',
          },
        },
        requestID: null,
      }],
      active: null,
      lastSeq: null, todos: [],
    })
    const t1 = s1.turns.get(T1)
    if (t1?.phase.kind !== 'committed') throw new Error('turn 1 must stay committed')
    // 修复后：迭代 union —— 最后迭代 2 必须保留（append-only，DB 快照落后不减数据）。
    expect(t1.phase.payload.iterations.map((it) => it.iteration)).toEqual([1, 2])
    expect(t1.phase.payload.iterations[1].content).toBe('最终回复')
    // text 的最终回复不丢（incoming 空 content 不得清空已有）。
    if (t1.phase.payload.via === 'text') expect(t1.phase.payload.content).toBe('最终回复')
    const rows = deriveRows(s1)
    expect(rows.filter((r) => r.kind === 'committed')[0]?.iterations).toHaveLength(2)
  })

  it('REPRO: todo_write 后 history_replaced 不覆盖实时 todos（快照旧值覆盖根因）', () => {
    // todo_write 执行 → progress 事件带新 todos → state.todos 更新。
    // 随后 REST ack / 工具副作用触发 history_replaced，携带 active_progress
    // 快照的【旧 todos】（快照可能滞后于实时 progress 事件）。
    // 旧逻辑 ev.todos.length>0 ? ev.todos : s.todos 用旧值覆盖 → todo 列表消失。
    const s0 = run([
      { type: 'iteration', turnID: T1, iter: iterNum(1), seq: 10 as never, content: undefined, reasoning: undefined, activeTools: [], completedTools: [], iterationsDelta: [], todos: [{ id: 1, text: '新任务', status: "pending" }, { id: 2, text: '任务2', status: "pending" }], subAgents: undefined, tokenUsage: undefined, streamStats: undefined },
    ])
    expect(s0.todos).toHaveLength(2)
    // history_replaced 携带快照旧 todos（只有 1 条，旧值）。
    const s1 = reduce(s0, {
      type: 'history_replaced', legacy: [], turns: [], active: null, lastSeq: null,
      todos: [{ id: 1, text: '新任务', status: "pending" }],
    })
    // 修复后：实时 state.todos（2 条）优先，不被快照旧值（1 条）覆盖。
    expect(s1.todos).toHaveLength(2)
    expect(s1.todos[1].text).toBe('任务2')
  })

  it('history_replaced hydration：ev.active 创建 live turn（刷新恢复 in-flight）', () => {
    const s = reduce(initialChatState('chat-1'), {
      type: 'history_replaced',
      legacy: [],
      turns: [],
      active: {
        turnID: turnID(7),
        snapshot: {
          iter: iterNum(2),
          streaming: true,
          content: '刷新前流到一半的内容',
          reasoning: '',
          iterations: [{ iteration: 1, content: '已完成迭代', reasoning: '', tools: [], toolCount: 0 }],
          activeTools: [],
          streamingTools: [],
          genui: '',
          subAgents: [],
          todos: [],
          tokenUsage: null,
          streamStats: null,
        },
      },
      lastSeq: null, todos: [],
    })
    expect(s.activeTurn).toBe(turnID(7))
    const t = s.turns.get(turnID(7))
    if (t?.phase.kind !== 'live') throw new Error('hydration must create a live turn')
    expect(t.phase.data.content).toBe('刷新前流到一半的内容')
    // 后续 stream 事件继续喂养（恢复后打字机继续）。
    const s2 = reduce(s, {
      type: 'stream',
      turnID: turnID(7),
      seq: null,
      iteration: null,
      content: '恢复后的流式内容',
      reasoning: undefined,
      streamingTools: undefined,
      genui: undefined,
      streamStats: undefined,
    })
    const t2 = s2.turns.get(turnID(7))
    if (t2?.phase.kind === 'live') expect(t2.phase.data.content).toBe('恢复后的流式内容')
    else throw new Error('hydrated live turn must accept stream events')
  })

  it('stream_stats 字段级合并：迭代内 0 值不覆盖，无数据保留前一帧，迭代前进才重置', () => {
    const T1 = turnID(1)
    let s = reduce(initialChatState('chat-1'), { type: 'turn_started', turnID: T1, trigger: 'user', content: '', requestID: null })
    // 帧1：有效 tkps=50，ttft=500
    s = reduce(s, {
      type: 'stream', turnID: T1, seq: null, iteration: iterNum(1),
      content: 'abc', reasoning: undefined, streamingTools: undefined, genui: undefined,
      streamStats: { ttftMs: 500, tpotMs: 0, tokensPerSec: 50, totalMs: 1000, chunks: 1 },
    })
    const t1 = s.turns.get(T1)
    if (t1?.phase.kind !== 'live') throw new Error('stream must keep live')
    expect(t1.phase.data.streamStats?.tokensPerSec).toBe(50)
    expect(t1.phase.data.streamStats?.ttftMs).toBe(500)

    // 帧2：后端滑动窗口无数据 → 全 0（"无数据"而非"速度为0"）→ 保留前一帧有效值
    s = reduce(s, {
      type: 'stream', turnID: T1, seq: null, iteration: iterNum(1),
      content: 'abc', reasoning: undefined, streamingTools: undefined, genui: undefined,
      streamStats: { ttftMs: 0, tpotMs: 0, tokensPerSec: 0, totalMs: 1500, chunks: 2 },
    })
    const t2 = s.turns.get(T1)
    if (t2?.phase.kind !== 'live') throw new Error('stream must keep live')
    expect(t2.phase.data.streamStats?.tokensPerSec).toBe(50)
    expect(t2.phase.data.streamStats?.ttftMs).toBe(500)

    // 帧3：完全无 stream_stats 字段 → 也保留
    s = reduce(s, {
      type: 'stream', turnID: T1, seq: null, iteration: iterNum(1),
      content: 'abcd', reasoning: undefined, streamingTools: undefined, genui: undefined, streamStats: undefined,
    })
    const t3 = s.turns.get(T1)
    if (t3?.phase.kind !== 'live') throw new Error('stream must keep live')
    expect(t3.phase.data.streamStats?.tokensPerSec).toBe(50)

    // 帧4：新数据出现 → 只更新该字段
    s = reduce(s, {
      type: 'stream', turnID: T1, seq: null, iteration: iterNum(1),
      content: 'abcde', reasoning: undefined, streamingTools: undefined, genui: undefined,
      streamStats: { ttftMs: 500, tpotMs: 0, tokensPerSec: 120, totalMs: 2500, chunks: 3 },
    })
    const t4 = s.turns.get(T1)
    if (t4?.phase.kind !== 'live') throw new Error('stream must keep live')
    expect(t4.phase.data.streamStats?.tokensPerSec).toBe(120)
    expect(t4.phase.data.streamStats?.ttftMs).toBe(500)

    // 帧5：迭代前进 → tokensPerSec 重置为 0（新迭代从零开始），但 ttftMs 保留
    // （TTFT 是 per-Run 的，迭代间不变 —— 后端闭包 firstChunkAt - requestStartAt 固定）。
    s = reduce(s, {
      type: 'stream', turnID: T1, seq: null, iteration: iterNum(2),
      content: 'x', reasoning: undefined, streamingTools: undefined, genui: undefined,
      streamStats: { ttftMs: 0, tpotMs: 0, tokensPerSec: 0, totalMs: 0, chunks: 0 },
    })
    const t5 = s.turns.get(T1)
    if (t5?.phase.kind !== 'live') throw new Error('stream must keep live')
    expect(t5.phase.data.streamStats?.tokensPerSec).toBe(0)
    // ttftMs 保留前一迭代的值（per-Run 不变）。
    expect(t5.phase.data.streamStats?.ttftMs).toBe(500)
  })
})

// ─── 重启 resume 后切换会话竞态：SSE 增量先到（lazy live 只含 resume 后迭代）+
// fetchHistory 后到（committed 全量 1..k）→ "live 胜"必须 union，否则 iter 1..k
// 竞态性消失（用户报告："重启后 turn n 的 iter 1..k 全消失"、"切换那个会话
// 有时候能看到迭代有时候看不到"）。时序 A（fetchHistory 先到）走 committed
// 遮蔽解除路径正常；时序 B（SSE 先到）走 step 1 "live 胜"——旧代码直接保留
// live（只含增量）丢弃 incoming committed 的全量迭代 → 概率性丢 1..k。
describe('TDSM reduce — 重启 resume 切换会话竞态（live 胜 union）', () => {
  const T7 = turnID(7)
  const mkIter = (n: number, c: string): WebIteration => ({ iteration: n, content: c, reasoning: '', tools: [], toolCount: 0 })

  const committedTurn = (): Turn => ({
    id: T7,
    user: {
      id: 'db-u7', content: 'user msg' as never, timestamp: 't', isNotification: false,
      queued: false, sending: false, requestID: null, turnHint: 7, dbID: 7,
    },
    phase: { kind: 'committed', payload: commitViaFold([mkIter(1, 'iter 1'), mkIter(2, 'iter 2'), mkIter(3, 'iter 3')] as never, 'final') },
    requestID: null,
  })

  it('REPRO: SSE 增量先到（lazy live 只含 resume 后的迭代 4）→ history_replaced 的 committed（1..3）后到 → live 胜时必须 union（不丢 1..3）', () => {
    // 时序 B：重启 resume 后切换/重连会话——SSE 增量事件先于 fetchHistory 到达
    //（lazy 采纳建立 live，只含 resume Run 的迭代 4）。
    const s0 = run([
      // 无 turn_started（重启后 resume 的 turn_started 已过 SSE buffer /
      // lazy 采纳场景），iteration 事件 lazy 建立 live。SSE push 协议：事件
      // 携带新完成的迭代 delta（resume Run 的迭代 4）。
      {
        type: 'iteration', turnID: T7, iter: iterNum(4), seq: 10 as never,
        content: 'resumed iter 4', reasoning: undefined, activeTools: [], completedTools: [],
        iterationsDelta: [mkIter(4, 'resumed iter 4')], todos: undefined, subAgents: undefined,
        tokenUsage: undefined, streamStats: undefined,
      } as never,
    ])
    const t0 = s0.turns.get(T7)
    if (t0?.phase.kind !== 'live') throw new Error('lazy 采纳应建立 live')
    expect(t0.phase.data.iterations.map((i) => i.iteration)).toEqual([4])
    expect(s0.activeTurn).toBe(T7)

    // fetchHistory 后到：history_replaced 携带 DB 全量（committed 1..3 +
    // user 行——重启前 Run 持久化的迭代）。
    const s1 = reduce(s0, {
      type: 'history_replaced', legacy: [], turns: [committedTurn()], active: null, lastSeq: null, todos: [],
    })

    // live 胜（SSE 比 DB 新）——但 incoming committed 的 1..3 必须 union 进
    // live（旧代码直接保留 live 丢弃 1..3 → "iter 1..k 全消失"）。
    const t1 = s1.turns.get(T7)
    if (t1?.phase.kind !== 'live') throw new Error('live turn died across history_replaced')
    expect(s1.activeTurn).toBe(T7)
    expect(t1.phase.data.iterations.map((i) => i.iteration)).toEqual([1, 2, 3, 4])
    // user 行嫁接（lazy live 无 user，DB 行补）。
    expect(t1.user?.content).toBe('user msg')
    // 渲染：单 turn（不分裂）。
    const rows = deriveRows(s1)
    expect(rows.filter((r) => r.kind === 'live')).toHaveLength(1)
  })

  it('同号迭代 live 权威（SSE 比 DB 新）——union 时 live 的 4 覆盖 committed 同号 4', () => {
    // committed 携带过时的迭代 4（DB 快照滞后于 SSE），live 的 4（SSE 较新）
    // 在 union 中覆盖同号（mergeIterations 权威方向）。
    const s0 = run([{
      type: 'iteration', turnID: T7, iter: iterNum(4), seq: 10 as never,
      content: 'resumed iter 4 (new)', reasoning: undefined, activeTools: [], completedTools: [],
      iterationsDelta: [mkIter(4, 'resumed iter 4 (new)')], todos: undefined, subAgents: undefined,
      tokenUsage: undefined, streamStats: undefined,
    } as never])
    const staleTurn: Turn = {
      id: T7,
      user: null,
      phase: { kind: 'committed', payload: commitViaFold([mkIter(1, 'a'), mkIter(2, 'b'), mkIter(4, 'stale iter 4')] as never, '') },
      requestID: null,
    }
    const s1 = reduce(s0, { type: 'history_replaced', legacy: [], turns: [staleTurn], active: null, lastSeq: null, todos: [] })
    const t1 = s1.turns.get(T7)
    if (t1?.phase.kind !== 'live') throw new Error('live turn died')
    expect(t1.phase.data.iterations.map((i) => i.iteration)).toEqual([1, 2, 4])
    const it4 = t1.phase.data.iterations.find((i) => i.iteration === 4)
    expect(it4?.content).toBe('resumed iter 4 (new)')
  })
})
