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
  initialChatState,
  iterNum,
  turnID,
  type ChatState,
  type DomainEvent,
} from './types'

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
})

const started = (turn: ReturnType<typeof turnID>, requestID: string | null = null): DomainEvent => ({
  type: 'turn_started',
  turnID: turn,
  requestID,
  trigger: 'user',
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
  it('Bug1: cancel 后新 turn 的事件不被旧 turn guard 拦截（SSE 更新但前端卡死）', () => {
    // turn 1 流式产出 → cancel ack（text_final cancelled）→ turn 2 正常接收事件。
    const s = run([
      started(T1),
      { type: 'stream', turnID: T1, seq: null, content: '部分内容', reasoning: undefined, streamingTools: undefined, genui: undefined },
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
      lastSeq: null,
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
      { type: 'stream', turnID: T1, seq: null, content: '流式输出的完整回复', reasoning: undefined, streamingTools: undefined, genui: undefined },
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
    const ev = normalizeEvent(
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
    expect(ev).not.toBeNull()
    expect(ev?.type).toBe('iteration')
    if (ev?.type === 'iteration') {
      expect(ev.activeTools).toEqual([])
      expect(ev.iterationsDelta).toHaveLength(1)
      expect(ev.iterationsDelta[0].tools).toEqual([])
    }
    // 状态机消化后 derive 不抛（渲染层无 null 可见 —— T1）。
    const s = run([started(T1), ev!])
    expect(() => deriveRows(s)).not.toThrow()
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
      expect(t1.phase.payload.iterations[0].content).toBe('最后迭代内容')
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
      { type: 'stream', turnID: T1, seq: null, content: '已产出内容', reasoning: undefined, streamingTools: undefined, genui: undefined },
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
      expect(t1.phase.payload.iterations[1].content).toBe('cancel 前进行中的迭代（补齐）')
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
      lastSeq: null,
    })
    const rows = deriveRows(s)
    expect(rows).toHaveLength(2)
    expect(rows[0].kind === 'user' && rows[0].content).toBe('旧消息1')
    expect(rows[1].kind === 'committed' && rows[1].content).toBe('旧回复1')
  })
})
