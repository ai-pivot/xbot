/**
 * reduce.test.ts — 状态机转移表测试：8 个历史 P0 各一个回归 + 不变量断言。
 *
 * 每个 test 的头注释标注它根治的历史 bug（design doc §6 映射表）。
 */

import { describe, expect, it } from 'vitest'
import { MessageStore } from '@/components/agent/messageStore'
import { normalizeWebIteration } from '@/components/agent/normalize'
import { deriveRows } from './derive'
import { historyToReplaced, liveProgressFromState } from './integrate'
import { normalizeEvent } from './normalize'
import { reduce } from './reduce'
import {
  commitViaFold,
  commitViaText,
  EMPTY_LIVE,
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

// ─── Loop2 F1/F2：in-flight 工具折叠（"已渲染内容永不消失"） ────
//
// F1（derive frozen）：frozen 行的 errTools 只折 activeTools —— streamingTools
// （参数流式生成中，generating）在 cancel/text 丢失定格时消失。
// F2（reduce foldPhase）：turn_started 收尸路径 commitViaFold 不折 in-flight
// 工具（text_final 的 foldInFlightTools 有折 —— 收尸路径没有）。
describe('Loop2 — in-flight 工具折叠（frozen / 收尸 / text_final 三路径同语义）', () => {
  const runningTool = (name: string) => ({
    name, label: '', status: 'running', elapsedMs: 0, summary: '', detail: '', args: '', toolHints: '',
  })
  const generatingTool = (name: string) => ({
    name, label: '', status: 'generating', elapsedMs: 0, summary: '', detail: '', args: '', toolHints: '',
  })

  it('F1: frozen 行渲染折入 streamingTools（generating 工具 cancel/idle 定格不消失）', () => {
    // stream 事件带 streamingTools（参数生成中）+ content（hasOutput 成立）→
    // session(idle) 定格 frozen（text/PhaseDone 丢失的兜底路径）→ deriveRows。
    // 修复前：errTools 只折 activeTools（空）→ generating 工具从 frozen 行消失
    // （违反"已渲染内容永不消失"）。
    const s = run([
      started(T1),
      {
        type: 'stream', turnID: T1, seq: null, iteration: null,
        content: '流式输出到一半', reasoning: undefined,
        streamingTools: [generatingTool('Read') as never],
        genui: undefined, streamStats: undefined,
      },
      { type: 'session', busy: false },
    ])
    const t1 = s.turns.get(T1)!
    if (t1.phase.kind !== 'frozen') throw new Error(`must be frozen, got ${t1.phase.kind}`)
    // frozen 的 LiveSnapshot 保留 streamingTools（session idle freeze 全保留）。
    expect(t1.phase.data.streamingTools.map((t) => t.name)).toContain('Read')

    const rows = deriveRows(s)
    const frozenRow = rows.find((r) => r.kind === 'frozen' && r.turnID === 1)
    if (!frozenRow || frozenRow.kind !== 'frozen') throw new Error('frozen row must render')
    // 修复后：streamingTools 折进最后迭代（标 error —— generating → error）。
    const folded = frozenRow.iterations.flatMap((it) => it.tools)
    const readTool = folded.find((t) => t.name === 'Read')
    expect(readTool).toBeDefined()
    expect(readTool?.status).toBe('error')
  })

  it('F1（对照）: frozen 行仍折入 activeTools（running 工具，既有行为不回归）', () => {
    // activeTools（running）经 session(idle) frozen 后折入 —— 修复不得破坏。
    const s = run([
      started(T1),
      {
        type: 'iteration', turnID: T1, iter: iterNum(1), seq: 10 as never,
        content: '流式中', reasoning: undefined,
        activeTools: [runningTool('Shell') as never],
        completedTools: [], iterationsDelta: [],
        todos: undefined, subAgents: undefined, tokenUsage: undefined, streamStats: undefined,
      },
      { type: 'session', busy: false },
    ])
    const rows = deriveRows(s)
    const frozenRow = rows.find((r) => r.kind === 'frozen' && r.turnID === 1)
    if (!frozenRow || frozenRow.kind !== 'frozen') throw new Error('frozen row must render')
    const folded = frozenRow.iterations.flatMap((it) => it.tools)
    expect(folded.map((t) => t.name)).toContain('Shell')
    expect(folded.find((t) => t.name === 'Shell')?.status).toBe('error')
  })

  it('F2: turn_started 收尸（foldPhase）折 in-flight 工具 —— activeTools+streamingTools 标 error 进最后迭代', () => {
    // turn 1 流式中：iteration 事件带 activeTools（Shell running）+ 已完成迭代 1
    // 快照（iterationsDelta）+ stream 事件带 streamingTools（Read generating）。
    // 用户发新消息 → turn_started(2) 收尸 turn 1 → foldPhase commit。
    // 修复前：commitViaFold 只带已完成迭代（iterationsDelta 的 iter1 无工具）
    // —— Shell/Read 从 committed payload 消失（text_final 有 foldInFlightTools，
    // 收尸路径没有 —— 语义分叉）。
    const s = run([
      started(T1),
      {
        type: 'iteration', turnID: T1, iter: iterNum(1), seq: 10 as never,
        content: undefined, reasoning: undefined,
        activeTools: [runningTool('Shell') as never],
        completedTools: [],
        iterationsDelta: [{ iteration: 1, content: '迭代1完成', reasoning: '', tools: [], toolCount: 0 }],
        todos: undefined, subAgents: undefined, tokenUsage: undefined, streamStats: undefined,
      },
      {
        type: 'stream', turnID: T1, seq: null, iteration: null,
        content: undefined, reasoning: '思考中',
        streamingTools: [generatingTool('Read') as never],
        genui: undefined, streamStats: undefined,
      },
      started(T2), // 收尸 turn 1（text 未到 —— fold commit）
    ])
    assertInvariants(s)
    const t1 = s.turns.get(T1)!
    if (t1.phase.kind !== 'committed') throw new Error(`turn 1 must be committed (fold), got ${t1.phase.kind}`)
    // 收尸 committed：in-flight 工具折进最后迭代（iter1）—— 与 text_final 的
    // foldInFlightTools 同语义（"已渲染内容永不消失"）。
    const it1 = t1.phase.payload.iterations.find((it) => it.iteration === 1)
    const toolNames = it1?.tools.map((t) => t.name) ?? []
    expect(toolNames).toContain('Shell')
    expect(toolNames).toContain('Read')
    expect(it1?.tools.find((t) => t.name === 'Shell')?.status).toBe('error')
    expect(it1?.tools.find((t) => t.name === 'Read')?.status).toBe('error')
    // 已有迭代内容不被折叠破坏。
    expect(it1?.content).toBe('迭代1完成')
  })

  it('F2b: 收尸时无已完成迭代 + content 流式中 → in-flight 工具折入新迭代（content 写进迭代内 —— v55 渲染）', () => {
    // iterations 为空 + content 非空 + streamingTools 非空 → 修复前
    // commitViaText(text, [])（工具丢失）；修复后折入新迭代（iteration=live.iter，
    // content 写进迭代 —— v55 hasIterations 时不渲染顶层 content）。
    const s = run([
      started(T1),
      {
        type: 'stream', turnID: T1, seq: null, iteration: null,
        content: '流式输出中', reasoning: undefined,
        streamingTools: [generatingTool('Read') as never],
        genui: undefined, streamStats: undefined,
      },
      started(T2), // 收尸
    ])
    assertInvariants(s)
    const t1 = s.turns.get(T1)!
    if (t1.phase.kind !== 'committed') throw new Error(`turn 1 must be committed, got ${t1.phase.kind}`)
    // 折入新迭代：工具 + 流式 content 都在迭代内。
    expect(t1.phase.payload.iterations).toHaveLength(1)
    const it1 = t1.phase.payload.iterations[0]
    expect(it1.tools.map((t) => t.name)).toContain('Read')
    expect(it1.tools.find((t) => t.name === 'Read')?.status).toBe('error')
    // v55：顶层 content 不渲染（hasIterations）—— 流式文本必须存在于迭代内。
    expect(it1.content).toBe('流式输出中')
  })

  it('F2c: 收尸路径与 text_final 的 in-flight 折叠语义一致（同输入同输出）', () => {
    // 同样的 live 状态（activeTools=Shell running + streamingTools=Read generating
    // + 迭代1快照），经收尸（turn_started fold）与经 text_final 两条路径 commit，
    // 最后迭代的工具集合必须一致（foldInFlightToIterations 共用 —— 语义永不分叉）。
    const base = (): DomainEvent[] => [
      started(T1),
      {
        type: 'iteration', turnID: T1, iter: iterNum(1), seq: 10 as never,
        content: undefined, reasoning: undefined,
        activeTools: [runningTool('Shell') as never],
        completedTools: [],
        iterationsDelta: [{ iteration: 1, content: '迭代1', reasoning: '', tools: [], toolCount: 0 }],
        todos: undefined, subAgents: undefined, tokenUsage: undefined, streamStats: undefined,
      },
      {
        type: 'stream', turnID: T1, seq: null, iteration: null,
        content: undefined, reasoning: '思考中',
        streamingTools: [generatingTool('Read') as never],
        genui: undefined, streamStats: undefined,
      },
    ]
    // 路径 A：turn_started 收尸 fold。
    const sA = run([...base(), started(T2)])
    // 路径 B：text_final commit（cancel —— content null 走 fold）。
    const sB = run([...base(), textFinal(T1, null, true)])
    const pA = sA.turns.get(T1)!.phase
    const pB = sB.turns.get(T1)!.phase
    if (pA.kind !== 'committed' || pB.kind !== 'committed') throw new Error('both must be committed')
    const toolsA = (pA.payload.iterations.find((it) => it.iteration === 1)?.tools ?? []).map((t) => `${t.name}:${t.status}`).sort()
    const toolsB = (pB.payload.iterations.find((it) => it.iteration === 1)?.tools ?? []).map((t) => `${t.name}:${t.status}`).sort()
    expect(toolsA).toEqual(toolsB)
    expect(toolsA).toContain('Read:error')
    expect(toolsA).toContain('Shell:error')
  })

  it('F2 对照: text_final 的既有折叠行为不回归（activeTools+streamingTools 都折）', () => {
    // text_final(cancel) 的既有 foldInFlightTools 语义（reduce.ts:464）——
    // 提取共享 helper 后不得改变。
    const s = run([
      started(T1),
      {
        type: 'iteration', turnID: T1, iter: iterNum(2), seq: 10 as never,
        content: undefined, reasoning: undefined,
        activeTools: [runningTool('Shell') as never],
        completedTools: [],
        iterationsDelta: [{ iteration: 1, content: 'iter1', reasoning: '', tools: [], toolCount: 0 }],
        todos: undefined, subAgents: undefined, tokenUsage: undefined, streamStats: undefined,
      },
      {
        type: 'stream', turnID: T1, seq: null, iteration: null,
        content: undefined, reasoning: 'r',
        streamingTools: [generatingTool('Grep') as never],
        genui: undefined, streamStats: undefined,
      },
      textFinal(T1, null, true), // cancel：content null → fold 路径
    ])
    const t1 = s.turns.get(T1)!
    if (t1.phase.kind !== 'committed') throw new Error('must be committed')
    // live.iter=2（iteration 事件）→ in-flight 折进迭代 2（追加新迭代）。
    const it2 = t1.phase.payload.iterations.find((it) => it.iteration === 2)
    expect(it2?.tools.map((t) => t.name).sort()).toEqual(['Grep', 'Shell'])
  })
})

// ─── REPRO: session 事件嵌套 chat_id 必须过滤（跨 session 污染根治） ──────────
// 用户报告（100% 复现）：两个 active session 平铺，cancel 右边（B），左边
// （A）立刻变成 idle view，live progress 消失。
// 根因：后端 SendSessionState 构造 WSMessage{Type, TS, Session} —— 顶层
// chat_id 未设置，chat_id 只在嵌套 env.session.chat_id。第一轮 user 级
// fan-out（broadcastSessionStateToWebClients）把 B 的 busy/idle 送达 A 的
// SSE 连接（seq=0）；normalizeEvent 的 chat 过滤只查顶层 env.chat_id →
// B 的 session(idle) 直接通过 → A 的 ChatStore reduce session case 把 A 的
// live turn 冻结/删除 + busy=false（"左边立刻变 idle view"）。
// master 无 fan-out（B 的 idle 只走 B 的 route）→ 无此 bug。
describe('REPRO: session 事件嵌套 chat_id 过滤（跨 session 污染根治）', () => {
  const bIdleFanout = {
    type: 'session',
    ts: 1730000000,
    // 后端真实形态：顶层无 chat_id（SendSessionState 只填 Session 字段）。
    session: { channel: 'web', chat_id: 'chat-B', action: 'idle' },
  }

  it('B 的 session(idle)（fan-out 副本，顶层 chat_id 未设）不得进入 A 的状态机', () => {
    const evs = normalizeEvent(bIdleFanout, 'chat-A')
    expect(evs).toBeNull() // 修复前：[{type:'session', busy:false}]（红灯）
  })

  it('B 的 session(busy) 同样被过滤', () => {
    const evs = normalizeEvent(
      { type: 'session', session: { channel: 'web', chat_id: 'chat-B', action: 'busy' } },
      'chat-A',
    )
    expect(evs).toBeNull()
  })

  it('A 自己的 session(idle)（嵌套 chat_id 匹配）正常通过 —— 不回归', () => {
    const idle = normalizeEvent(
      { type: 'session', session: { channel: 'web', chat_id: 'chat-A', action: 'idle' } },
      'chat-A',
    )
    expect(idle).toEqual([{ type: 'session', busy: false }])
  })

  it('A 自己的 session(busy) 带 channel 前缀也通过（stripChannel 兼容）', () => {
    const busy = normalizeEvent(
      { type: 'session', session: { channel: 'web', chat_id: 'web:chat-A', action: 'busy' } },
      'chat-A',
    )
    expect(busy).toEqual([{ type: 'session', busy: true }])
  })

  it('端到端：B 的 idle 不再冻结 A 的 live turn；A 自己的 idle 正常收尾', () => {
    // A 的 live turn（streaming reasoning 中，有产出）。
    let s = run([started(T1)], initialChatState('chat-A'))
    s = reduce(s, {
      type: 'stream', turnID: T1, seq: null, iteration: null,
      content: undefined, reasoning: 'A 正在流式思考',
      streamingTools: [], genui: undefined, streamStats: undefined,
    })
    expect(s.activeTurn).toBe(T1)
    expect(s.turns.get(T1)?.phase.kind).toBe('live')

    // B 的 idle fan-out 到达 A 的连接 → normalizeEvent 必须过滤。
    const evs = normalizeEvent(bIdleFanout, 'chat-A')
    expect(evs).toBeNull()

    // 修复前（事件直接进 reduce）的对照 —— A 的 live turn 被冻结（症状）：
    const polluted = reduce(s, { type: 'session', busy: false })
    expect(polluted.activeTurn).toBeNull() // ← 被 B 的 idle 冻结（这就是 bug）
    expect(polluted.turns.get(T1)?.phase.kind).toBe('frozen')

    // A 自己的 idle 才收尾 A 的 turn。
    const own = normalizeEvent(
      { type: 'session', session: { channel: 'web', chat_id: 'chat-A', action: 'idle' } },
      'chat-A',
    )
    expect(own).toEqual([{ type: 'session', busy: false }])
    const s2 = reduce(s, own![0])
    expect(s2.activeTurn).toBeNull()
    expect(s2.turns.get(T1)?.phase.kind).toBe('frozen') // A 自己的 idle：正常收尾
  })
})

// ─── REPRO: user msg 消失（iPhone 切走/切回 + resync + 错误回复未持久化）──────
//
// 2026-09-02 14:18 实录（DOM 铁证）：turn-24-c → turn-26-c 直接相邻，user(26)
// 无处渲染。DB 铁证：user(26) 行存在（id=1408796, turn_id=26）+ turn 26 无
// assistant 行（LLM 错误回复不持久化——Error processing path 不写 DB）。
// 时序：切走 app → SSE 断（turn_started(26) 丢失）→ turn 26 以 LLM 错误结束
// （text 经 SSE replay 到达——lazy iteration 建立空 user 的 turn + commit）→
// 切回 → reload → history_replaced incoming：turns[26] = {DB user, frozen 空壳}
// （DB 无 assistant 行 → 空壳）→ step1 "空壳不覆盖"分支 `turns.set(h.id, cur)`
// 直接保留状态机的 committed（user=null——turn_started 丢失，绑定失败），
// **incoming 的 DB user 行被丢弃** → user 消失。
//
// 根因：空壳分支（cur 非 live + incoming frozen 无输出）缺 user 嫁接 ——
// live 胜分支有 `cur.user ? cur : { ...cur, user: h.user }`，空壳分支没有。
describe('REPRO: 空壳 incoming（DB user-only）不丢 user —— turn_started 丢失场景', () => {
  const T26 = turnID(26)

  it('committed（错误回复）+ incoming 空壳（DB 只有 user 行）→ user 必须从 DB 嫁接', () => {
    // 状态机侧（切回时 SSE replay 建立，无 turn_started —— user 绑定失败）：
    // lazy iteration 建立 live（无 user）→ text_final（LLM 错误回复）commit。
    const s0 = run([
      {
        type: 'iteration', turnID: T26, iter: iterNum(1), seq: 10 as never,
        content: 'LLM 服务调用失败', reasoning: undefined, activeTools: [], completedTools: [],
        iterationsDelta: [{ iteration: 1, content: 'LLM 服务调用失败', reasoning: '', tools: [], toolCount: 0 }],
        todos: undefined, subAgents: undefined, tokenUsage: undefined, streamStats: undefined,
      } as never,
      textFinal(T26, 'LLM 服务调用失败，请稍后重试或检查配置。'),
    ])
    const t0 = s0.turns.get(T26)!
    if (t0.phase.kind !== 'committed') throw new Error('场景前提：错误回复已 commit')
    expect(t0.user).toBeNull() // 场景前提：turn_started 丢失 → user 未绑定

    // 切回 reload：DB 行 user(26) 存在 + 无 assistant 行（错误回复不持久化）
    // → historyToReplaced 构造 frozen 空壳（"无产出 assistant 行"）。
    const s1 = reduce(s0, {
      type: 'history_replaced',
      legacy: [],
      turns: [{
        id: T26,
        user: {
          id: 'db-u26', content: '继续修复那个渲染问题' as never, timestamp: 't',
          isNotification: false, queued: false, sending: false,
          requestID: null, turnHint: 26, dbID: 1408796,
        },
        phase: { kind: 'frozen', data: { ...EMPTY_LIVE } }, // DB 无 assistant → 空壳
        requestID: null,
      }],
      active: null, lastSeq: null, todos: [],
    })

    // 修复断言：空壳不覆盖 committed（错误回复保留）+ user 从 DB 嫁接。
    const t1 = s1.turns.get(T26)!
    if (t1.phase.kind !== 'committed') throw new Error('空壳不得覆盖 committed（错误回复消失）')
    // RED（修复前 null —— DB user 被丢弃）：user 必须嫁接。
    expect(t1.user?.content).toBe('继续修复那个渲染问题')
    expect(t1.user?.dbID).toBe(1408796) // DB 权威行（rewind 需要 dbID）
    // 渲染：user + committed 两行（user 消失根治）。
    const rows = deriveRows(s1)
    expect(rows.filter((r) => r.kind === 'user').map((r) => r.content)).toContain('继续修复那个渲染问题')
    expect(rows.some((r) => r.kind === 'committed' && r.turnID === 26)).toBe(true)
  })

  it('对照组：state 侧 user 已绑定 → 空壳分支不覆盖（既有行为不回归）', () => {
    // turn_started 正常到达（user_sent 的 pendingUser 先入 → 绑定成功）→
    // committed → 空壳 incoming → 既有行为：保留 state 的 user（只补空，不覆盖已有）。
    const s0 = run([
      { type: 'user_sent', row: { id: 'opt-26', content: '用户消息' as never, timestamp: 't', isNotification: false, queued: false, sending: false, requestID: 'req-26', turnHint: undefined, dbID: undefined } },
      started(T26, 'req-26'), // requestID 绑定 → turns[26].user
      textFinal(T26, '错误回复'),
    ])
    expect(s0.turns.get(T26)?.user?.requestID).toBe('req-26')

    const s1 = reduce(s0, {
      type: 'history_replaced',
      legacy: [],
      turns: [{
        id: T26,
        user: {
          id: 'db-u26', content: 'DB user 行' as never, timestamp: 't',
          isNotification: false, queued: false, sending: false,
          requestID: null, turnHint: 26, dbID: 1408796,
        },
        phase: { kind: 'frozen', data: { ...EMPTY_LIVE } },
        requestID: null,
      }],
      active: null, lastSeq: null, todos: [],
    })
    const t1 = s1.turns.get(T26)!
    // 已绑定的 user 保留（嫁接方向：只补空，不覆盖已有）。
    expect(t1.user?.requestID).toBe('req-26')
  })
})

// ─── REPRO: cron 通知 turn 的 user 消失（tab 缓存 + resync 场景）──────────────
// 2026-09-02 14:35 实录（tenant 166286，电脑端 tab 后台）：cron 每分钟触发同内容
// 通知 turn（"背诵出师表"）→ tab 后台（SSE 断，turn_started/text 丢失）→ tab 恢复
// （resync reload）→ DOM 实证 turn-1030-c / turn-1031-c 相邻渲染，user 行消失
//（DB 铁证：user 行存在 turn_id 正确）。根因链候选：③.5 的内容幂等（同内容
// notification echo 被 turn N-1 的 notif 行误杀）+ turn_started 丢失（SSE 断连
// 窗口）→ turn N 的 user 两条腿全断（echo 误杀 + notif 构造丢失）→ 只剩
// reload 的 DB user 嫁接（mergeTurnData cur.user ?? h.user）。本组测试验证
// 每条腿的最终归属。
describe('REPRO: cron 通知 turn user 消失（tab 缓存 + resync）', () => {
  const N1029 = turnID(1029)
  const N1030 = turnID(1030)
  const N1031 = turnID(1031)
  const notifStarted = (t: ReturnType<typeof turnID>, content: string): DomainEvent => ({
    type: 'turn_started', turnID: t, requestID: null, trigger: 'notification', content,
  })
  const dbUserTurn = (t: ReturnType<typeof turnID>, n: number): Turn => ({
    id: t,
    user: {
      id: `db-u-${n}`, content: '⏰ [定时任务触发] 背诵一次出师表（全文，不要省略）' as never,
      timestamp: 't', isNotification: true, queued: false, sending: false,
      requestID: null, turnHint: n, dbID: 1000000 + n,
    },
    phase: { kind: 'committed', payload: commitViaText('出师表正文' as never, []) },
    requestID: null,
  })

  it('turn N 的 echo（同内容）被 ③.5 误杀 + turn_started 丢失 → reload 后 DB user 嫁接（不丢）', () => {
    // tab 活跃时 turn 1029 完整到达（notif 行 + commit）。
    const s = run([
      notifStarted(N1029, '⏰ [定时任务触发] 背诵一次出师表（全文，不要省略）'),
      iteration1(N1029, '出师表正文'),
      textFinal(N1029, '出师表正文（全文）'),
    ])
    expect(s.turns.get(N1029)?.user?.isNotification).toBe(true)
    expect(s.turns.get(N1029)?.phase.kind).toBe('committed')

    // tab 后台：turn 1030 的 turn_started/iteration/text 全部丢失（SSE 断）。
    // tab 恢复：inject_user echo(1030)（normalizeEvent 真实形状：无 id/无 turn_id
    // —— normalizeUserEcho requestID=null, turnHint=undefined）→ ③.5 内容幂等
    // 误杀（turn 1029 的 notif 行同内容——cron 每分钟同任务）→ ④ 不达（return s）。
    const echoEvs = normalizeEvent(
      { type: 'inject_user', content: '⏰ [定时任务触发] 背诵一次出师表（全文，不要省略）', ts: 1726000000, chat_id: 'chat-1' },
      'chat-1',
    )!
    expect(echoEvs).toHaveLength(1)
    const sAfterEcho = echoEvs.reduce(reduce, s)
    expect(sAfterEcho.pendingUsers.filter((u) => u.isNotification)).toHaveLength(0) // ③.5 误杀 ✓

    // resync reload：DB rows（turn 1030 user+assistant）→ history_replaced。
    // 状态机无 turn 1030（SSE 断连丢失）→ step 1 else → turns.set(h.id, h)
    // —— incoming 的 DB user 直接进 → user 必须在。
    const s2 = reduce(sAfterEcho, {
      type: 'history_replaced', legacy: [],
      turns: [dbUserTurn(N1030, 1030)],
      active: null, lastSeq: null, todos: [],
    })
    const t1030 = s2.turns.get(N1030)
    expect(t1030?.user?.dbID).toBe(1001030) // RED 或 GREEN：DB user 必须在
    expect(t1030?.user?.isNotification).toBe(true)

    // 三个 cron turn 的完整链（1029 notif + 1030/1031 DB）+ 渲染 user 行。
    const s3 = reduce(s2, {
      type: 'history_replaced', legacy: [],
      turns: [dbUserTurn(N1029, 1029), dbUserTurn(N1030, 1030), dbUserTurn(N1031, 1031)],
      active: null, lastSeq: null, todos: [],
    })
    const rows = deriveRows(s3)
    const userRows = rows.filter((r) => r.kind === 'user').map((r) => (r as { turnID: number }).turnID)
    expect(userRows).toContain(1029)
    expect(userRows).toContain(1030)
    expect(userRows).toContain(1031)
  })

  it('SSE replay 先到（iteration lazy 建立 user=null 的 turn）→ reload 后到 → mergeTurnData 嫁接 DB user', () => {
    // 时序 B：SSE replay 的 iteration(1030)（lazy——turn_started 丢失）+ text(1030)
    // commit（user=null）→ reload 的 incoming（DB user + committed）后到。
    const s = run([
      {
        type: 'iteration', turnID: N1030, iter: iterNum(1), seq: 10 as never,
        content: '出师表正文', reasoning: undefined, activeTools: [], completedTools: [],
        iterationsDelta: [{ iteration: 1, content: '出师表正文', reasoning: '', tools: [], toolCount: 0 }],
        todos: undefined, subAgents: undefined, tokenUsage: undefined, streamStats: undefined,
      } as never,
      textFinal(N1030, '出师表正文（全文）'),
    ])
    const t0 = s.turns.get(N1030)!
    expect(t0.phase.kind).toBe('committed')
    expect(t0.user).toBeNull() // 场景前提：SSE replay 路径无 user（echo 被误杀 + notif 丢失）

    // reload：incoming turns[1030]（DB user + committed）→ mergeTurnData 嫁接。
    const s2 = reduce(s, {
      type: 'history_replaced', legacy: [],
      turns: [dbUserTurn(N1030, 1030)],
      active: null, lastSeq: null, todos: [],
    })
    expect(s2.turns.get(N1030)?.user?.dbID).toBe(1001030)
    expect(s2.turns.get(N1030)?.user?.isNotification).toBe(true)
  })
})

// ─── REPRO 连续序列：cron 每 1 分钟同内容通知 turn（tab 持续开着的真实序列）──
// 电脑端 tab 不关（SSE 持续）——cron 通知 turn 1029/1030/1031 顺序到达：
// turn_started(1029, notif) → text(1029) → turn_started(1030, notif)（收尸 1029
// 若 live）→ text(1030) → turn_started(1031)（收尸 1030）→ text(1031)。
// DOM 实证（user 报告）：1030-c 与 1031-c 相邻（user(1031) 缺失——1030 与
// 1031 之间无 user 行）。本测试验证 M4 reduce 层的连续 notif 构造 + 收尸链。
describe('REPRO: 连续 cron 通知 turn 链（tab 持续开的 SSE 完整序列）', () => {
  const NOTIF = '⏰ [定时任务触发] 背诵一次出师表（全文，不要省略）'
  const t1029 = turnID(1029), t1030 = turnID(1030), t1031 = turnID(1031)
  const notifStart = (t: ReturnType<typeof turnID>): DomainEvent => ({
    type: 'turn_started', turnID: t, requestID: null, trigger: 'notification', content: NOTIF,
  })

  it('连续 3 个 notif turn（同内容）——每个 turn 的 user 都必须构造（DOM: user(1031) 消失）', () => {
    const s = run([
      notifStart(t1029),
      iteration1(t1029, '出师表 回复 1029'),
      textFinal(t1029, '出师表 回复 1029'),
      notifStart(t1030), // 1029 已 committed（text 到达）→ 不触发收尸
      iteration1(t1030, '出师表 回复 1030'),
      textFinal(t1030, '出师表 回复 1030'),
      notifStart(t1031),
      iteration1(t1031, '出师表 回复 1031'),
      textFinal(t1031, '出师表 回复 1031'),
    ])
    // 三个 turn 的 notif user 全部构造（DOM: 1030-c/1031-c 相邻——user(1031) 缺失）。
    for (const [t, n] of [[t1029, 1029], [t1030, 1030], [t1031, 1031]] as const) {
      const tt = s.turns.get(t)
      expect(tt?.user, `turn ${n} user must exist (notif construction)`).toBeDefined()
      expect(tt?.user?.isNotification, `turn ${n} user isNotification`).toBe(true)
      expect(tt?.user?.content, `turn ${n} user content`).toBe(NOTIF)
      expect(tt?.phase.kind, `turn ${n} committed`).toBe('committed')
    }
    // 渲染：每 turn 的 user → committed 序列（user 在 assistant 前——T5）。
    const rows = deriveRows(s)
    const userRows = rows.filter((r) => r.kind === 'user')
    expect(userRows).toHaveLength(3)
    // DOM 实证复现点：1030-c 与 1031-c 之间必须有 user(1031)。
    const idx1030c = rows.findIndex((r) => r.kind === 'committed' && r.turnID === 1030)
    const idx1031c = rows.findIndex((r) => r.kind === 'committed' && r.turnID === 1031)
    expect(idx1030c).toBeGreaterThanOrEqual(0)
    expect(idx1031c).toBe(idx1030c + 2) // 中间必有 user(1031)（DOM 实证是 +1 → user 缺失）
  })

  it('收尸链变体：text 迟到（turn_started(1031) 在 text(1030) 前到达——收尸 1030 的 live）', () => {
    // cron turn 的回复生成中（1030 live）→ 下一分钟的 turn_started(1031) 先到
    // （收尸 1030 的 live → fold committed）→ 1030 的 text 后到（幂等——已 committed）。
    const s = run([
      notifStart(t1029),
      iteration1(t1029, '回复 1029'),
      textFinal(t1029, '回复 1029'),
      notifStart(t1030),
      iteration1(t1030, '回复 1030（live 中）'),
      notifStart(t1031), // text(1030) 未到 → 收尸 1030 的 live（fold，user 保留）
      textFinal(t1030, '回复 1030'), // 迟到（幂等——committed 已定）
      iteration1(t1031, '回复 1031'),
      textFinal(t1031, '回复 1031'),
    ])
    // 收尸后 1030 的 user 保留（fold 保留 user——old.user !== null → fold 而非 frozen）。
    const tt1030 = s.turns.get(t1030)
    expect(tt1030?.user?.content).toBe(NOTIF)
    expect(tt1030?.phase.kind).toBe('committed')
    // 1031 的 notif user 构造 ✓。
    expect(s.turns.get(t1031)?.user?.content).toBe(NOTIF)
    // 渲染：3 个 user 行。
    expect(deriveRows(s).filter((r) => r.kind === 'user')).toHaveLength(3)
  })
})

// ─── REPRO: turn_started 丢失（tab 后台 SSE 节流）+ cron 同内容 echo 误杀 ──────
// 2026-09-02 14:35 实录（tenant 166286，电脑端 tab 后台）：cron 每分钟同内容通知
// turn（"背诵出师表"）→ tab 后台浏览器节流丢弃 turn_started(1031)（连接不断——
// 无 resync/reload）→ iteration(1031) lazy 采纳（activeTurn=1031, user=null）→
// text(1031) commit → inject_user echo(1031) 到达（无 turnHint——inject_user
// WSMessage 四字段无 turn_id）→ ③ hint 匹配不命中（turnHint=undefined）→ ③.5
// 内容幂等误杀（turns 里 1029/1030 的 notif user 同内容"背诵出师表"匹配 →
// echo 丢弃）→ user(1031) 永缺（DOM 实证：turn-1030-c 与 turn-1031-c 相邻，
// 之间无 user 行）。修复：③ hint 匹配扩展 activeTurn 兜底——echo 无 turnHint
// 时挂 active turn 的空 user 槽（turn 进行中——lazy 采纳的 activeTurn 是权威
// 归属），在 ③.5 误杀之前恢复 user。
describe('REPRO: turn_started 丢失 + cron 同内容 echo —— activeTurn 兜底挂载', () => {
  const NOTIF = '⏰ [定时任务触发] 背诵一次出师表（全文，不要省略）'
  const N1029 = turnID(1029)
  const N1031 = turnID(1031)

  it('lazy 采纳（turn_started 丢失）+ echo（无 turnHint，同内容被旧 notif 匹配）→ user 挂 activeTurn', () => {
    // tab 前台：turn 1029 正常（notif user 构造 + commit）。
    const s0 = run([
      { type: 'turn_started', turnID: N1029, requestID: null, trigger: 'notification', content: NOTIF },
      iteration1(N1029, '回复 1029'),
      textFinal(N1029, '回复 1029'),
    ])
    expect(s0.turns.get(N1029)?.user?.isNotification).toBe(true)

    // tab 后台：turn_started(1031) 丢失（浏览器节流）→ iteration(1031) lazy 采纳
    //（activeTurn=1031, user=null——M4 无 turn_started 的 notif 构造）。
    const s1 = run([
      {
        type: 'iteration', turnID: N1031, iter: iterNum(1), seq: 20 as never,
        content: '回复 1031', reasoning: undefined, activeTools: [], completedTools: [],
        iterationsDelta: [{ iteration: 1, content: '回复 1031', reasoning: '', tools: [], toolCount: 0 }],
        todos: undefined, subAgents: undefined, tokenUsage: undefined, streamStats: undefined,
      } as never,
    ], s0)
    expect(s1.activeTurn).toBe(N1031) // lazy 采纳建立 activeTurn
    expect(s1.turns.get(N1031)?.user).toBeNull() // 场景前提：user 未构造

    // echo(1031) 到达（inject_user 真实形状：无 turn_id → turnHint=undefined；
    // 同内容 NOTIF —— 修复前 ③ 不命中 + ③.5 被 1029 的 notif user 误杀）。
    const evs = normalizeEvent(
      { type: 'inject_user', content: NOTIF, ts: 1726000000, chat_id: 'chat-1' },
      'chat-1',
    )!
    expect(evs).toHaveLength(1)
    const s2 = evs.reduce(reduce, s1)

    // 修复后：③ activeTurn 兜底 —— echo 挂载 active turn（1031）的空 user 槽。
    // 修复前：user=null（③ turnHint=undefined 不命中 → ③.5 误杀丢弃）。
    const t1031 = s2.turns.get(N1031)
    expect(t1031?.user?.content, 'echo 必须挂载 activeTurn 的空 user（SSE turn_started 丢失恢复）').toBe(NOTIF)
    // echo row 本身不带 is_notification（inject_user WSMessage 四字段）—— 挂载后
    // 显示为普通 user 行（content 完整，比消失好；🔔 badge 是 notif 行的增强样式）。
    expect(t1031?.user?.isNotification).toBe(false)

    // turn 完成（text）→ user 保留（committed turn 的 user 不丢）。
    const s3 = run([textFinal(N1031, '回复 1031')], s2)
    expect(s3.turns.get(N1031)?.user?.content).toBe(NOTIF)
    expect(s3.turns.get(N1031)?.phase.kind).toBe('committed')
    // 渲染：user 行存在（user 消失根治）。
    expect(deriveRows(s3).some((r) => r.kind === 'user' && r.content === NOTIF)).toBe(true)
  })

  it('对照组：echo 在 turn 完成后到达（activeTurn=null）→ ③.5 误杀（旧行为）——不挂已完成 turn', () => {
    // turn 1031 已完成（text commit → activeTurn=null）→ echo 到达 → ③ activeTurn
    // 无可挂（null）→ ③.5 误杀（1029 同内容匹配）→ 不产生重复行（F#1 语义保持）。
    const s0 = run([
      { type: 'turn_started', turnID: N1029, requestID: null, trigger: 'notification', content: NOTIF },
      textFinal(N1029, '回复 1029'),
    ])
    const evs = normalizeEvent(
      { type: 'inject_user', content: NOTIF, ts: 1726000000, chat_id: 'chat-1' },
      'chat-1',
    )!
    const s1 = evs.reduce(reduce, s0)
    // ②/③/④ 全不命中（activeTurn=null，无 pending）→ ③.5 误杀（不新增行）。
    expect(s1.pendingUsers).toHaveLength(0)
    expect(deriveRows(s1).filter((r) => r.kind === 'user')).toHaveLength(1) // 只有 1029 的 notif 行
  })
})

// ─── REPRO: web 压缩提示永不渲染（progressPhase 三处断点，2026-09-03）──────────
// 用户报告"web 端一直不渲染压缩提示"。后端链路是通的（runCompression 设置
// Phase=PhaseCompressing → notifyProgress 推 structured 事件——Web 的 autoNotify
// =true 因 no-op ProgressNotifier 非 nil），但前端 M4 三处断点：
// ① normalize 的 iteration 事件丢 p.phase；② LiveSnapshot 无 progressPhase 字段
//（types.ts:159 的 TurnPhase 是 turn 三态 live/frozen/committed，不是 progress
// phase）；③ liveProgressFromState 硬编码 `d.streaming ? 'thinking' : 'tool_exec'`
// → MessageList/AssistantMessage 的 `liveProgress?.phase === 'compressing'`
// 永远 false（agent.compressing 提示永不渲染）。
// 修复：phase 全链透传——normalize 携带 → reduce 写 LiveSnapshot.progressPhase
// → liveProgressFromState 透传（派生仅作 fallback）。
describe('REPRO: progressPhase 全链透传（web 压缩提示）', () => {
  it("normalize: progress_structured(phase='compressing') → iteration 事件携带 phase", () => {
    const evs = normalizeEvent(
      {
        type: 'progress_structured',
        progress: {
          phase: 'compressing',
          turn_id: 5,
          iteration: 1,
          seq: 10,
        },
      },
      'chat-1',
    )
    expect(evs).not.toBeNull()
    const iter = evs!.find((e) => e.type === 'iteration')
    expect(iter).toBeDefined()
    expect((iter as { phase: string | undefined }).phase).toBe('compressing')
  })

  it("reduce + liveProgressFromState: iteration(phase='compressing') → liveProgress.phase='compressing'", () => {
    // 后端真实链：turn_started → iteration(phase=compressing)。
    const s = run([
      { type: 'turn_started', turnID: T1, requestID: 'r1', trigger: 'user', content: '任务' },
      {
        type: 'iteration',
        turnID: T1,
        phase: 'compressing',
        iter: iterNum(1),
        seq: 10 as never,
        content: undefined,
        reasoning: undefined,
        activeTools: [],
        completedTools: [],
        iterationsDelta: [],
        todos: undefined,
        subAgents: undefined,
        tokenUsage: undefined,
        streamStats: undefined,
      } as never,
    ])
    // 修复前：liveProgressFromState 硬编码 thinking|tool_exec——'compressing'
    // 永不出现 → MessageList 的提示条件永远 false。
    const live = liveProgressFromState(s)
    expect(live.phase).toBe('compressing')
  })

  it('对照组：无 phase 的 iteration 事件 → 派生 fallback（thinking）不回归', () => {
    const s = run([
      { type: 'turn_started', turnID: T1, requestID: 'r1', trigger: 'user', content: null },
      iteration1(T1, '流式中'),
    ])
    const live = liveProgressFromState(s)
    expect(live.phase).toBe('thinking') // streaming=true → thinking（旧派生保持）
  })
})

// ─── REPRO: 压缩行渲染在压缩发生的位置（chat_F64D4096DA6F）──────────────────
// 2026-09-03 用户报告："压缩后没显示 compacted context，看起来和没压缩完全一样"。
// 根因：压缩行（[Compacted context]，turn_id=0）渲染在 legacy 前缀段 = 消息列表
// 最顶部——1700 条消息的会话里用户永远看不到。修复：LegacyRow.anchorTurnID
//（= 首个 incoming turn 的 turnID）+ deriveRows 按锚插入 turns 之间。
describe('REPRO: 压缩行渲染在压缩发生的位置（anchorTurnID 锚）', () => {
  const mkIncomingTurn = (n: number) => ({
    id: turnID(n),
    user: {
      id: `db-u-${n}`, content: `tail user ${n}` as never, timestamp: 't',
      isNotification: false, queued: false, sending: false,
      requestID: null, turnHint: n, dbID: 9000000 + n,
    },
    phase: { kind: 'committed' as const, payload: commitViaText(`tail reply ${n}` as never, []) },
    requestID: null,
  })
  const COMPACT = '[Compacted context]\n\n# Working State: 测试摘要'

  it('merge 场景：旧 turns（merge 保留）+ 压缩行 → 压缩行在旧 turns 之后、tail 之前', () => {
    // 状态机有旧 turns 1029/1763（committed——merge 会保留）。
    const s0 = run([
      { type: 'turn_started', turnID: turnID(1029), requestID: 'r1', trigger: 'user', content: '旧任务1' },
      textFinal(turnID(1029), '旧回复1'),
      { type: 'turn_started', turnID: turnID(1763), requestID: 'r2', trigger: 'user', content: '旧任务2' },
      textFinal(turnID(1763), '旧回复2'),
    ])
    // reload（history_compacted 触发）：incoming = 压缩行（anchor=首个 incoming
    // turn 1764）+ tail turn 1764。merge 保留旧 turns（committed）。
    const s1 = reduce(s0, {
      type: 'history_replaced',
      legacy: [{
        id: 'compact-1', role: 'user', content: COMPACT, iterations: [],
        timestamp: 't', dbID: 1412775, anchorTurnID: 1764,
      }],
      turns: [mkIncomingTurn(1764)],
      active: null, lastSeq: null, todos: [],
    })
    const rows = deriveRows(s1)
    // 顺序断言：旧 turns → 压缩行 → tail turn。
    const idxOld = rows.findIndex((r) => r.kind === 'committed' && r.turnID === 1029)
    const idxOld2 = rows.findIndex((r) => r.kind === 'committed' && r.turnID === 1763)
    const idxCompact = rows.findIndex((r) => r.kind === 'user' && r.content === COMPACT)
    const idxTail = rows.findIndex((r) => r.kind === 'committed' && r.turnID === 1764)
    expect(idxOld).toBeGreaterThanOrEqual(0)
    expect(idxOld2).toBeGreaterThan(idxOld)
    expect(idxCompact).toBeGreaterThan(idxOld2) // 压缩行在旧 turns 之后
    expect(idxCompact).toBeLessThan(idxTail)    // 且在 tail turn 之前
  })

  it('刷新场景：状态机空 + incoming（压缩行 + tail）→ 压缩行在顶部（tail 之前）', () => {
    const s1 = run([{
      type: 'history_replaced',
      legacy: [{
        id: 'compact-1', role: 'user', content: COMPACT, iterations: [],
        timestamp: 't', dbID: 1412775, anchorTurnID: 1764,
      }],
      turns: [mkIncomingTurn(1764), mkIncomingTurn(1765)],
      active: null, lastSeq: null, todos: [],
    }])
    const rows = deriveRows(s1)
    // 压缩行是第一行（无旧 turns——刷新后 active 只有压缩行 + tail）。
    const first = rows[0]
    expect(first.kind).toBe('user')
    if (first.kind === 'user') expect(first.content).toBe(COMPACT)
    // tail turns 在压缩行之后（rows: 压缩行, user(1764), committed(1764)）。
    const idxTail = rows.findIndex((r) => r.kind === 'committed' && r.turnID === 1764)
    expect(idxTail).toBe(2)
  })

  it('普通无锚 legacy 行保持前缀段（旧行为不回归）', () => {
    const s1 = run([{
      type: 'history_replaced',
      legacy: [
        { id: 'legacy-1', role: 'user', content: '老消息（无 turn）', iterations: [], timestamp: 't', dbID: 1 },
        { id: 'compact-1', role: 'user', content: COMPACT, iterations: [], timestamp: 't', dbID: 2, anchorTurnID: 1764 },
      ],
      turns: [mkIncomingTurn(1764)],
      active: null, lastSeq: null, todos: [],
    }])
    const rows = deriveRows(s1)
    // 普通行在前缀段（第 0），压缩行按锚在 tail 之前（第 1）。
    expect(rows[0].kind).toBe('user')
    if (rows[0].kind === 'user') expect(rows[0].content).toBe('老消息（无 turn）')
    const idxCompact = rows.findIndex((r) => r.kind === 'user' && r.content === COMPACT)
    const idxTail = rows.findIndex((r) => r.kind === 'committed' && r.turnID === 1764)
    expect(idxCompact).toBe(1)
    expect(idxTail).toBe(3)
  })
})

// ─── REPRO: 完整链集成（真实快照恢复形状）——压缩行经 MessageStore 后渲染位置 ──
// chat_F64D4096DA6F 用户报告（03:11 修复部署后仍看不到）。Go 验证 GetHistory
// 输出（真实形状）：[0] 压缩行(id=1412774, turn_id=0) + [1] continuation
// (id=1412774 重复——快照 HistoryID=0→record.HistoryID) + [2] tail-user
// (id=1412772, turn_id=0——快照恢复丢失 turn 归属) + [3] turn1759 assistant
// + [4] turn1760...。本测试走完整链：MessageStore.mergeHistory（useChatMessages
// reload 的真实路径）→ toRows → historyToReplaced → reduce → deriveRows。
describe('REPRO: 完整链——压缩行经 MessageStore 后渲染（真实快照形状）', () => {
  it('reload 链（mergeHistory → toRows → historyToReplaced → deriveRows）压缩行在 turns 之间', () => {
    // parseHistoryMessages 的输出形状（真实 GetHistory → web RPC 的前端形状）：
    // 压缩行 + continuation 共享 dbID=1412774（快照 HistoryID=0 → record.HistoryID
    // 替代——两条共用）；tail-user turn_id=0（快照恢复丢失）；turn 1759+ 正常。
    const rows = [
      { id: 'db-1412774', role: 'user', content: '[Compacted context]\n\n# Working State: 出师表 cron', iterations: [], timestamp: 't', isPartial: false, turnID: 0, displayOnly: false, persisted: true, dbID: 1412774 },
      { id: 'db-1412774-1', role: 'user', content: 'This conversation was compacted from a longer session.', iterations: [], timestamp: 't', isPartial: false, turnID: 0, displayOnly: false, persisted: true, dbID: 1412774 },
      { id: 'db-1412772', role: 'user', content: '⏰ [定时任务触发] 背诵一次出师表', iterations: [], timestamp: 't', isPartial: false, turnID: 0, displayOnly: false, persisted: true, dbID: 1412772 },
      { id: 'db-1412779', role: 'assistant', content: '出师表全文', iterations: [], timestamp: 't', isPartial: false, turnID: 1759, displayOnly: false, persisted: true, dbID: 1412779 },
      { id: 'db-1412780', role: 'user', content: '⏰ [定时任务触发] 背诵一次出师表', iterations: [], timestamp: 't', isPartial: false, turnID: 1760, displayOnly: false, persisted: true, dbID: 1412780 },
      { id: 'db-1412782', role: 'assistant', content: '出师表全文2', iterations: [], timestamp: 't', isPartial: false, turnID: 1760, displayOnly: false, persisted: true, dbID: 1412782 },
    ] as never[]
    const store = new MessageStore()
    store.mergeHistory(rows, { replace: true })
    const toRows = store.toRows()
    // toRows 含压缩行（legacy——dbID/turnID 保留）？
    const compactInRows = toRows.filter((m: { content?: string }) => (m.content ?? '').startsWith('[Compacted context]'))
    expect(compactInRows.length, '压缩行必须在 MessageStore.toRows 输出里').toBe(1)
    // 完整链：historyToReplaced → reduce → deriveRows。
    const ev = historyToReplaced(toRows as never, null)
    const s = reduce(initialChatState('chat-1'), ev)
    const rowsOut = deriveRows(s)
    // 断言：压缩行渲染在 turns 之间（不在前缀段顶部）——锚=1759（最小 turnID）。
    const legacyPrefix = rowsOut.filter((r) => r.kind === 'user' || r.kind === 'committed')
    const idxCompact = legacyPrefix.findIndex((r) => r.kind === 'user' && r.content.startsWith('[Compacted context]'))
    expect(idxCompact, '压缩行必须渲染').toBeGreaterThanOrEqual(0)
    const idxTurn1759 = legacyPrefix.findIndex((r) => r.turnID === 1759)
    // 顺序断言（真实渲染顺序）：前缀段（continuation + tail-user，无锚 turn_id=0
    // legacy）→ 压缩行（锚 1759）→ turn 1759 → turn 1760。
    // 压缩行不在顶部（continuation 在前——turn_id=0 的无锚 legacy 前缀段），
    // 在 turn 1759 之前（锚生效），且 tail-user（前缀段最后一条）在它之前。
    expect(idxCompact).toBeGreaterThan(0) // 不在顶部（前缀段在前）
    expect(idxCompact).toBeLessThan(idxTurn1759) // 在 turn 1759 之前（锚 1759）
    expect(legacyPrefix[idxCompact - 1]?.kind).toBe('user') // 紧邻前一行是无锚 legacy（tail-user）
    expect(idxTurn1759).toBe(idxCompact + 1) // 紧邻后一行是 turn 1759
  })
})

// ─── REPRO: 多压缩标记各自锚定压缩点（不堆叠在窗口顶部）────────────────────
// 用户报告（chat_F64D4096DA6F，2026-09-03 03:45）："所有上下文已压缩都渲染在
// 最顶上，整整三条，并且我们会动态加载内容所以几乎看不到"——loadMore 多批
// 后旧实现（anchorTurnID = 全局 firstIncomingTurnID）让所有标记共享同一锚
//（当前窗口最小 turnID——随翻页变小）→ 3 条标记堆叠在列表最顶部。修复：
// 逐标记锚定自己的压缩点（后继第一条消息的 turnID）。
describe('REPRO: 多压缩标记各自压缩点（loadMore 多批场景）', () => {
  const COMPACT = (n: number) => `[Compacted context]\n\n# Working State 压缩 ${n}`

  it('3 条标记各自插在压缩点（后继消息前），不堆叠', () => {
    // loadMore 后的完整消息流：旧消息（turn 100）+ 标记1 + turn 101 段 +
    // 标记2 + turn 102 段 + 标记3 + turn 103 段（三次压缩 09-01/02/03）。
    const rows = [
      { id: 'db-1', role: 'user', content: 'user 100', iterations: [], timestamp: 't', isPartial: false, turnID: 100, displayOnly: false, persisted: true, dbID: 1 },
      { id: 'db-2', role: 'assistant', content: 'reply 100', iterations: [], timestamp: 't', isPartial: false, turnID: 100, displayOnly: false, persisted: true, dbID: 2 },
      // 标记 1（压缩点——dbID 夹在前后消息之间：2 < c1 < 3）
      { id: 'db-c1', role: 'user', content: COMPACT(1), iterations: [], timestamp: 't', isPartial: false, turnID: 0, displayOnly: false, persisted: true, dbID: 3 },
      { id: 'db-3', role: 'user', content: 'user 101', iterations: [], timestamp: 't', isPartial: false, turnID: 101, displayOnly: false, persisted: true, dbID: 4 },
      { id: 'db-4', role: 'assistant', content: 'reply 101', iterations: [], timestamp: 't', isPartial: false, turnID: 101, displayOnly: false, persisted: true, dbID: 5 },
      // 标记 2（dbID 夹在 4 与 5 之间）
      { id: 'db-c2', role: 'user', content: COMPACT(2), iterations: [], timestamp: 't', isPartial: false, turnID: 0, displayOnly: false, persisted: true, dbID: 6 },
      { id: 'db-5', role: 'user', content: 'user 102', iterations: [], timestamp: 't', isPartial: false, turnID: 102, displayOnly: false, persisted: true, dbID: 7 },
      { id: 'db-6', role: 'assistant', content: 'reply 102', iterations: [], timestamp: 't', isPartial: false, turnID: 102, displayOnly: false, persisted: true, dbID: 8 },
      // 标记 3（dbID 夹在 6 与 7 之间）
      { id: 'db-c3', role: 'user', content: COMPACT(3), iterations: [], timestamp: 't', isPartial: false, turnID: 0, displayOnly: false, persisted: true, dbID: 9 },
      { id: 'db-7', role: 'user', content: 'user 103', iterations: [], timestamp: 't', isPartial: false, turnID: 103, displayOnly: false, persisted: true, dbID: 10 },
      { id: 'db-8', role: 'assistant', content: 'reply 103', iterations: [], timestamp: 't', isPartial: false, turnID: 103, displayOnly: false, persisted: true, dbID: 11 },
    ] as never[]
    const ev = historyToReplaced(rows, null)
    const s = reduce(initialChatState('chat-1'), ev)
    const out = deriveRows(s)
    const idx = (needle: string) => out.findIndex((r) => (r.kind === 'user' || r.kind === 'committed') && r.content === needle)
    const i100 = idx('user 100'), iC1 = idx(COMPACT(1)), i101 = idx('user 101')
    const iC2 = idx(COMPACT(2)), i102 = idx('user 102')
    const iC3 = idx(COMPACT(3)), i103 = idx('user 103')
    // 各自压缩点：标记 N 在 turn N-1 的消息之后、turn N 之前。
    expect(i100).toBeGreaterThanOrEqual(0)
    expect(iC1).toBeGreaterThan(i100) // 标记1 在 turn 100 之后
    expect(iC1).toBeLessThan(i101)   // 且在 turn 101 之前
    expect(iC2).toBeGreaterThan(i101) // 标记2 在 turn 101 之后
    expect(iC2).toBeLessThan(i102)    // 且在 turn 102 之前
    expect(iC3).toBeGreaterThan(i102) // 标记3 在 turn 102 之后
    expect(iC3).toBeLessThan(i103)    // 且在 turn 103 之前
    // 不堆叠：三条标记互不相邻（各自间隔一条消息段）。
    expect(iC2 - iC1).toBeGreaterThanOrEqual(2)
    expect(iC3 - iC2).toBeGreaterThanOrEqual(2)
  })
})

// ─── REPRO: toRows legacy 前置下的多标记锚 + loadMore 批次不重复 ─────────────
// 用户报告（chat_F64D4096DA6F，04:00）："往上加载历史都是重复的，而且
// [Compacted context] 还是永远在顶上"。根因：chat.messages 来自
// MessageStore.toRows()——legacy 行前置在数组最前 + turnIDs 升序——所有标记
// 的"数组后继"都是同一条（最旧 turn）→ anchor 全部=最旧 turn → deriveRows
// 全部插在已加载消息顶部。修复：dbID 序锚（压缩记录 id 的 DB 时间顺序）。
// 本测试走真实链：MessageStore.mergeHistory（初始 replace + loadMore 增量）
// → toRows（legacy 前置扭曲顺序）→ historyToReplaced → deriveRows。
describe('REPRO: toRows legacy 前置 + loadMore 多批次多标记', () => {
  const COMPACT = (n: number) => `[Compacted context]\n\n# Working State 压缩 ${n}`
  const mkUser = (turn: number, dbID: number, content: string) => ({
    id: `db-${dbID}`, role: 'user' as const, content, iterations: [], timestamp: 't',
    isPartial: false, turnID: turn, displayOnly: false, persisted: true, dbID,
  })
  const mkAsst = (turn: number, dbID: number, content: string) => ({
    id: `db-${dbID}`, role: 'assistant' as const, content, iterations: [], timestamp: 't',
    isPartial: false, turnID: turn, displayOnly: false, persisted: true, dbID,
  })

  it('dbID 序锚：多标记各自压缩点（不被 toRows 的 legacy 前置扭曲）+ 批次合并无重复', () => {
    const store = new MessageStore()
    // 初始加载（replace——最近批次）：turn 1700-1720（dbID > 标记 B）。
    store.mergeHistory([
      mkUser(1700, 1412780, 'user 1700'),
      mkAsst(1700, 1412781, 'reply 1700'),
      mkUser(1710, 1415000, 'user 1710'),
      mkAsst(1710, 1415001, 'reply 1710'),
      mkUser(1720, 1420000, 'user 1720'),
      mkAsst(1720, 1420001, 'reply 1720'),
    ], { replace: true })
    // loadMore 批次 1（增量）：标记 B（压缩点 dbID=1412774）+ 更早消息
    //（标记 dbID 落在消息 dbID 序中间——1640/1650 的 dbID < 标记 B）。
    store.mergeHistory([
      mkUser(1650, 1406220, 'user 1650'),
      mkAsst(1650, 1406221, 'reply 1650'),
      // 标记 B（turn_id=0——API 派生豁免后保持）
      { id: 'db-1412774', role: 'user' as const, content: COMPACT(2), iterations: [], timestamp: 't', isPartial: false, turnID: 0, displayOnly: false, persisted: true, dbID: 1412774 },
      mkUser(1700, 1412780, 'user 1700'), // 同 dbID（批次边界重叠——去重后单条）
      mkAsst(1700, 1412781, 'reply 1700'),
    ])
    // loadMore 批次 2（增量）：标记 C（更早压缩点 dbID=1406215）+ turn 1640。
    store.mergeHistory([
      mkUser(1640, 1406000, 'user 1640'),
      mkAsst(1640, 1406001, 'reply 1640'),
      // 标记 C
      { id: 'db-1406215', role: 'user' as const, content: COMPACT(1), iterations: [], timestamp: 't', isPartial: false, turnID: 0, displayOnly: false, persisted: true, dbID: 1406215 },
      mkUser(1650, 1406220, 'user 1650'), // 同 dbID 重叠
      mkAsst(1650, 1406221, 'reply 1650'),
    ])

    const toRows = store.toRows()
    // toRows 的 legacy 前置：标记 B、C 在数组最前（顺序扭曲——dbID 序修复的
    // 前提）。断言 toRows 结构（legacy 前 + turns 升序）。
    expect(toRows[0]?.content).toBe(COMPACT(2)) // legacy 前置（push 顺序）
    // 完整链：historyToReplaced → M4 → deriveRows。
    const ev = historyToReplaced(toRows as never, null)
    const s = reduce(initialChatState('chat-1'), ev)
    const rows = deriveRows(s)
    const out = rows.filter((r) => r.kind === 'user' || r.kind === 'committed') as { kind: string; content: string; turnID: number }[]

    // 期望渲染序（dbID 序锚——标记在各自压缩点）：
    // 1640 → 标记C → 1650 → 标记B → 1700 → 1710 → 1720。
    const idx = (needle: string) => out.findIndex((r) => r.content === needle)
    const i1640 = idx('user 1640'), iC = idx(COMPACT(1))
    const i1650 = idx('user 1650'), iB = idx(COMPACT(2))
    const i1700 = idx('user 1700'), i1720 = idx('user 1720')
    expect(i1640).toBeGreaterThanOrEqual(0)
    expect(iC).toBeGreaterThan(i1640)          // 标记 C 在 1640 之后（压缩点后）
    expect(iC).toBeLessThan(i1650)             // 且在 1650 之前
    expect(iB).toBeGreaterThan(i1650)           // 标记 B 在 1650 之后
    expect(iB).toBeLessThan(i1700)             // 且在 1700 之前
    expect(i1700).toBeGreaterThan(iB)
    expect(i1720).toBeGreaterThan(i1700)

    // 不重复：每个 turn 的 user/assistant 恰一条 + 标记各一条。
    const contents = out.map((r) => r.content)
    for (const needle of ['user 1640', 'reply 1640', 'user 1650', 'reply 1650', 'user 1700', 'reply 1700', 'user 1710', 'reply 1710', 'user 1720', 'reply 1720', COMPACT(1), COMPACT(2)]) {
      expect(contents.filter((c) => c === needle), `${needle} 恰一次`).toHaveLength(1)
    }
  })
})

// ─── REPRO: 旧形状标记（turnID>0）强制 legacy——不进 turn user 槽 ────────────
// chat_F64D4096DA6F 04:10 DOM 铁证：同 db-1412774 两条 + data-turn-id=1759
// （标记被关联到 turn 1759——userRowOf 路径 = M4 turn 1759 的 user 槽被标记
// 占据 + anchoredLegacy 双渲染）。旧 API 形状（54bf1f1b 派生豁免部署前——
// deriveTurnIDs Pass 1 把标记 turn_id 派生为后继 turn id）的标记 turnID>0
// 进过 MessageStore slot(1759).user 与 M4 byTurn。修复：标记行强制 legacy
// （不管 turnID）——绝不占 turn 的 user 槽（用户约束：一个 turn 只能一个
// user 一个 assistant）。
describe('REPRO: 旧形状标记（turnID>0）强制 legacy 不进 user 槽', () => {
  const COMPACT = '[Compacted context]\n\n# Working State 测试'

  it('标记 turnID=1759（旧 API 形状）→ 不进 turn 1759 user 槽 + 单条渲染', () => {
    const store = new MessageStore()
    // 旧形状：标记行 turnID=1759（54bf1f1b 前 Pass 1 派生的形状）。
    store.mergeHistory([
      { id: 'db-1412774', role: 'user' as const, content: COMPACT, iterations: [], timestamp: 't', isPartial: false, turnID: 1759, displayOnly: false, persisted: true, dbID: 1412774 },
      { id: 'db-1412780', role: 'user' as const, content: '真实 user 1759', iterations: [], timestamp: 't', isPartial: false, turnID: 1759, displayOnly: false, persisted: true, dbID: 1412780 },
      { id: 'db-1412781', role: 'assistant' as const, content: 'reply 1759', iterations: [], timestamp: 't', isPartial: false, turnID: 1759, displayOnly: false, persisted: true, dbID: 1412781 },
    ], { replace: true })
    const toRows = store.toRows()
    // MessageStore 层：标记不进 slot(1759).user（真实 user 占据）。
    const user1759 = toRows.find((m: { content?: string }) => m.content === '真实 user 1759')
    expect(user1759, 'turn 1759 的真实 user 必须在（不被标记挤掉）').toBeDefined()
    const markerInRows = toRows.filter((m: { content?: string }) => m.content === COMPACT)
    expect(markerInRows, '标记恰好一条（legacy）').toHaveLength(1)
    // M4 层：标记进 legacy（不进 byTurn）+ dbID 序锚 + 单条渲染。
    const ev = historyToReplaced(toRows as never, null)
    const s = reduce(initialChatState('chat-1'), ev)
    const turn1759 = s.turns.get(turnID(1759))
    expect(turn1759?.user?.content, 'M4 turn 1759 的 user 是真实 user（不被标记占据）').toBe('真实 user 1759')
    const rows = deriveRows(s)
    const out = rows.filter((r) => r.kind === 'user' || r.kind === 'committed') as { content: string }[]
    const markers = out.filter((r) => r.content === COMPACT)
    expect(markers, '标记单条渲染（无双条）').toHaveLength(1)
    // 标记在 turn 1759 之前（dbID 1412774 < 1412780——锚=后继消息 turn 1759）。
    const idxMarker = out.findIndex((r) => r.content === COMPACT)
    const idxReal = out.findIndex((r) => r.content === '真实 user 1759')
    expect(idxMarker).toBeGreaterThanOrEqual(0)
    expect(idxMarker).toBeLessThan(idxReal)
  })
})

// ─── REPRO: 3 压缩点 × loadMore 多批次 × 旧形状（turnID>0 标记）完整链 ──────
// 用户报告（chat_F64D4096DA6F 04:17）："它直接覆盖渲染在正常 msg 上。
// 重复了七八个"（DB 实际 3 个压缩点：1399628/1406215/1412774）。完整链
// 验证：初始批次 + loadMore 批次（各含压缩点标记，含旧形状 turnID>0）
// → MessageStore → toRows → historyToReplaced → deriveRows——3 个标记
// 各自压缩点位置、不覆盖 user、总数恰 3（不重复）。
describe('REPRO: 3 压缩点 loadMore 完整链（旧形状标记不覆盖不重复）', () => {
  const COMPACT = (n: number) => `[Compacted context]\n\n# Working State 压缩 ${n}`
  const mkUser = (turn: number, dbID: number, c: string) => ({ id: `db-${dbID}`, role: 'user' as const, content: c, iterations: [], timestamp: 't', isPartial: false, turnID: turn, displayOnly: false, persisted: true, dbID })
  const mkAsst = (turn: number, dbID: number, c: string) => ({ id: `db-${dbID}`, role: 'assistant' as const, content: c, iterations: [], timestamp: 't', isPartial: false, turnID: turn, displayOnly: false, persisted: true, dbID })
  // 旧形状标记（54bf1f1b 部署前的 API——turnID 派生为后继 turn）
  const mkMarkerOld = (turn: number, dbID: number, n: number) => ({ id: `db-${dbID}`, role: 'user' as const, content: COMPACT(n), iterations: [], timestamp: 't', isPartial: false, turnID: turn, displayOnly: false, persisted: true, dbID })

  it('3 标记各自位置、user 不被覆盖、总数恰 3（不重复）', () => {
    const store = new MessageStore()
    // 初始批次（最近）：turn 1750+ 消息 + 标记 3（1412774——含旧形状 turnID=1759）
    store.mergeHistory([
      mkUser(1750, 1412700, 'user 1750'), mkAsst(1750, 1412701, 'reply 1750'),
      mkMarkerOld(1759, 1412774, 3),
      mkUser(1759, 1412780, 'user 1759'), mkAsst(1759, 1412781, 'reply 1759'),
      mkUser(1760, 1412900, 'user 1760'), mkAsst(1760, 1412901, 'reply 1760'),
    ], { replace: true })
    // loadMore 批次 1（更早）：标记 2（1406215 旧形状）+ turn 1700-1710
    store.mergeHistory([
      mkMarkerOld(1710, 1406215, 2),
      mkUser(1710, 1406216, 'user 1710'), mkAsst(1710, 1406217, 'reply 1710'),
      mkUser(1700, 1406000, 'user 1700'), mkAsst(1700, 1406001, 'reply 1700'),
    ])
    // loadMore 批次 2（最早）：标记 1（1399628 旧形状）+ turn 1600-1650
    store.mergeHistory([
      mkMarkerOld(1650, 1399628, 1),
      mkUser(1650, 1399630, 'user 1650'), mkAsst(1650, 1399631, 'reply 1650'),
      mkUser(1600, 1399000, 'user 1600'), mkAsst(1600, 1399001, 'reply 1600'),
    ])

    const toRows = store.toRows()
    // MessageStore 层：标记不进 slot（强制 addLegacy）——真实 user 不被覆盖。
    const user1759 = toRows.find((m: { content?: string }) => m.content === 'user 1759')
    expect(user1759, 'turn 1759 的真实 user 在（标记不覆盖）').toBeDefined()
    // 完整链：M4 → deriveRows。
    const ev = historyToReplaced(toRows as never, null)
    const s = reduce(initialChatState('chat-1'), ev)
    // M4 层：3 个 turn 的 user 都是真实 user（不被标记覆盖）。
    for (const turn of [1750, 1759, 1760, 1710, 1700, 1650, 1600]) {
      const t = s.turns.get(turnID(turn))
      expect(t?.user?.content, `turn ${turn} 的 user 是真实 user`).toContain(`user ${turn}`)
    }
    const rows = deriveRows(s)
    const out = rows.filter((r) => r.kind === 'user' || r.kind === 'committed') as { content: string }[]
    // 标记总数恰 3（不重复——旧形状 turnID>0 不产生双路径）。
    for (const n of [1, 2, 3]) {
      const markers = out.filter((r) => r.content === COMPACT(n))
      expect(markers, `标记 ${n} 恰一条`).toHaveLength(1)
    }
    // 各自位置：标记 N 插在压缩点之后第一条消息（锚 turn）的 user 之前、
    // 前一个 turn 的 reply 之后（deriveRows 的 anchoredLegacy 按
    // anchorTurnID <= t.id 插入——turn 的 user 之前）。
    const idx = (needle: string) => out.findIndex((r) => r.content === needle)
    const iM1 = idx(COMPACT(1)), iM2 = idx(COMPACT(2)), iM3 = idx(COMPACT(3))
    expect(iM1).toBeGreaterThanOrEqual(0)
    expect(iM1).toBeGreaterThan(idx('reply 1600')) // 标记 1 在 reply 1600 之后
    expect(iM1).toBeLessThan(idx('user 1650'))     // 且在 user 1650（锚）之前
    expect(iM2).toBeGreaterThan(idx('reply 1700')) // 标记 2 在 reply 1700 之后
    expect(iM2).toBeLessThan(idx('user 1710'))     // 且在 user 1710（锚）之前
    expect(iM3).toBeGreaterThan(idx('reply 1750')) // 标记 3 在 reply 1750 之后
    expect(iM3).toBeLessThan(idx('user 1759'))     // 且在 user 1759（锚）之前
  })
})

// ─── E2E: 真实 API 数据（tenant 166286）——多批 loadMore 累积标记数 ──────────
// 用户报告（chat_F64D4096DA6F 04:29）："重复渲染了几十个截不完"——用真实
// web handleHistory 路径的 dump（Go 程序从生产 DB 拉取的
// ConvertMessagesToHistoryWithIterations 输出）跑前端完整链。
describe('E2E: 真实 API 数据多批 loadMore', () => {
  it('单批（98 行 1 标记）→ MessageStore→M4→deriveRows 恰 1 条标记', async () => {
    const apiRows = (await import('./__fixtures__/api-batch0.json')).default as {
      id?: number; role: string; content: string; turn_id?: number; iterations?: unknown[]
    }[]
    // parseHistoryMessages 的模拟（与 useChatMessages 相同的形状转换）
    const idCounts = new Map<string, number>()
    const parsed = apiRows.map((m, i) => {
      const baseId = m.id != null ? `db-${m.id}` : `hist-0-${i}`
      const n = (idCounts.get(baseId) ?? 0) + 1
      idCounts.set(baseId, n)
      return {
        id: n > 1 ? `${baseId}-${n}` : baseId,
        role: m.role,
        content: m.content ?? '',
        iterations: (Array.isArray(m.iterations) ? m.iterations.map(normalizeWebIteration).filter(Boolean) : []) as never[],
        timestamp: new Date().toISOString(),
        isPartial: false,
        displayOnly: false,
        persisted: true,
        turnID: typeof m.turn_id === 'number' ? m.turn_id : 0,
        dbID: m.id ?? undefined,
      }
    })
    // MessageStore（reload replace）
    const store = new MessageStore()
    store.mergeHistory(parsed as never, { replace: true })
    const toRows1 = store.toRows()
    const markers1 = toRows1.filter((m: { content?: string }) => (m.content ?? '').startsWith('[Compacted context]'))
    // M4
    const ev = historyToReplaced(toRows1 as never, null)
    const s = reduce(initialChatState('chat-1'), ev)
    const rows1 = deriveRows(s)
    const out1 = rows1.filter((r) => r.kind === 'user' || r.kind === 'committed') as { content: string }[]
    const markers1M4 = out1.filter((r) => r.content.startsWith('[Compacted context]'))
    expect(markers1.length, `MessageStore 标记条数 = API 返回数（实际 ${markers1.length}）`).toBe(1)
    expect(markers1M4.length, `M4 渲染标记条数（实际 ${markers1M4.length}）`).toBe(1)
    // 极端模拟：游标错误时同批数据重复 merge 5 次（addLegacy 按 id 去重）
    for (let round = 0; round < 5; round++) {
      store.mergeHistory(parsed as never)
    }
    const toRows6 = store.toRows()
    const markers6 = toRows6.filter((m: { content?: string }) => (m.content ?? '').startsWith('[Compacted context]'))
    expect(markers6.length, `重复 merge 同批 5 次后标记仍 = 1（实际 ${markers6.length}）`).toBe(1)
  })
})
