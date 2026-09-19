/**
 * P0 不变量（用户 2026-09-19 反复点名，多次未修好）：
 *
 *   **「只要输入框是 cancel 按钮，就一定不能上面渲染的内容是 idle 内容」**
 *   —— 形式化：`busy（composer = cancel）⟹ 列表里必须有一个可见的"进行中"信号`。
 *
 * composer 的 busy = `currentSession.running || progressSnapshot.streaming ||
 * busyFallback(activeTurn !== null)`，其中 `currentSession.running` 是**服务端
 * reconcile 后的权威**（session-tree/status REST 对账 + SSE session）。而 turn 的
 * live-ness 此前**只**由事件驱动 ⇒ 破坏点：
 *
 *  (A) 渲染层：`frozen` 行也 `isPartial=true`，被 MessageList 当作 live 行
 *      （`liveId`）⇒ ① 它拿到的 liveProgress 是 `liveProgressFromState` 的**空**
 *      快照（frozen ⇒ activeTurn===null）⇒ 自身不渲染任何进行中信号；② busy 占位
 *      符的条件 `liveId === null` 因此不成立 ⇒ 也被抑制 ⇒ 输入框 cancel、内容像
 *      idle。（本文件的纯状态层面 + MessageList 渲染测试 + E2E 三层守护）
 *
 *  (B) 状态层：一条**迟到/误传/重放**的 coarse `session(idle)`（不带 turn 身份，
 *      可能来自 SSE 重连的 last_event_id 回放窗口）把运行中的 turn 冻结并清
 *      activeTurn ⇒ 两侧权威分叉。结构性修复：`session_running`（服务端
 *      reconcile 权威）是 turn live-ness 的兜底 —— running=true 时最新**未
 *      finalize** 的 turn 必须回 live；running=false 时才允许定格。
 *
 * 本文件覆盖 (B)：纯 reducer 规则（promote / 不 promote / 权威收尾）。
 */
import { describe, expect, it } from 'vitest'

import { reduce } from './reduce'
import { eventSeq, initialChatState, iterNum, nonEmptyStr, turnID } from './types'
import type { DomainEvent } from './types'
import type { WebIteration } from '@/types/shared'

const T = turnID(7)

const mkIter = (n: number, c: string): WebIteration => ({
  iteration: n,
  content: c,
  reasoning: '',
  tools: [],
  toolCount: 0,
})

const evTurnStarted = (): DomainEvent => ({
  type: 'turn_started',
  turnID: T,
  requestID: 'r1',
  trigger: 'user',
  content: '跑一个长任务',
})

const evIteration = (seq: number, iter: number, content: string): DomainEvent => ({
  type: 'iteration',
  turnID: T,
  phase: 'tool_exec',
  iter: iterNum(iter),
  seq: eventSeq(seq),
  content,
  reasoning: '',
  activeTools: [],
  completedTools: [],
  iterationsDelta: [mkIter(iter, content)],
  todos: undefined,
  goal: undefined,
  subAgents: undefined,
  tokenUsage: undefined,
  streamStats: undefined,
})

const evIdle = (): DomainEvent => ({ type: 'session', busy: false })
const evRunning = (running: boolean): DomainEvent => ({ type: 'session_running', running })
/** 最终回复（权威结束信号）：committed via:'text'。 */
const evText = (content: string): DomainEvent => ({
  type: 'text_final',
  turnID: T,
  content: nonEmptyStr(content)!,
  cancelled: false,
  progressHistory: [],
})

describe('P0 不变量：busy（composer=cancel）⟹ store 必须有 live turn', () => {
  it('running=true：迟到 idle 冻结后的 turn 必须被提回 live（内容/迭代全保留，streaming=true）', () => {
    let s = initialChatState('web:chatX')
    s = reduce(s, evRunning(true)) // 会话 running（服务端权威）
    s = reduce(s, evTurnStarted())
    s = reduce(s, evIteration(2, 1, 'one'))
    s = reduce(s, evIteration(3, 2, 'two'))
    expect(s.activeTurn).toBe(T)

    // 迟到/误传的 coarse idle（SSE 回放窗口 / restoreActiveProgress 竞态）——
    // running 仍为 true ⇒ 必须被权威忽略（否则 cancel + 内容像 idle）。
    s = reduce(s, evIdle())
    expect(s.activeTurn, 'running=true 时 coarse idle 不得冻结运行中的 turn').toBe(T)
    expect(s.turns.get(T)?.phase.kind).toBe('live')

    // 同样的保护对 session_idle / agent-idle 生效。
    s = reduce(s, { type: 'session_idle' })
    expect(s.activeTurn).toBe(T)
    expect(s.turns.get(T)?.phase.kind).toBe('live')
  })

  it('running=false：live turn 定格（内容保留、activeTurn 清空 —— 权威收尾）', () => {
    let s = initialChatState('web:chatX')
    s = reduce(s, evRunning(true))
    s = reduce(s, evTurnStarted())
    s = reduce(s, evIteration(2, 1, 'one'))
    s = reduce(s, evRunning(false))

    expect(s.activeTurn).toBeNull()
    const t = s.turns.get(T)
    expect(t?.phase.kind).toBe('frozen')
    if (t?.phase.kind !== 'frozen') throw new Error('frozen')
    expect(t.phase.data.iterations.map((i) => i.iteration)).toEqual([1]) // 内容不丢
  })

  it('running=true 且 turn 已是 fold-committed（无最终回复）⇒ 提回 live（reload 折叠的分叉入口）', () => {
    let s = initialChatState('web:chatX')
    s = reduce(s, evRunning(true))
    s = reduce(s, evTurnStarted())
    // 模拟 reload 把运行中的 turn 折成 committed（DB 中间快照，无最终回复）。
    s = reduce(s, {
      type: 'history_replaced',
      turns: [
        {
          id: T,
          user: null,
          phase: { kind: 'committed', payload: { via: 'fold', content: '', iterations: [mkIter(1, 'one')] } },
          requestID: null,
        },
      ],
      legacy: [],
      lastSeq: null,
      active: null, // active_progress 缺失（竞态）—— 分叉入口
      todos: [],
    })
    expect(s.turns.get(T)?.phase.kind, 'running=true ⇒ reload 折叠也必须被不变量纠正').toBe('live')
    expect(s.activeTurn).toBe(T)
  })

  it('running=true 但 turn 已被**权威 finalizer**（via:text）结束 ⇒ 绝不复活（不得变 busy 幽灵）', () => {
    let s = initialChatState('web:chatX')
    s = reduce(s, evRunning(true))
    s = reduce(s, evTurnStarted())
    s = reduce(s, evIteration(2, 1, 'one'))
    s = reduce(s, evText('最终回复'))
    expect(s.turns.get(T)?.phase.kind).toBe('committed')

    // running 尚未对账翻 false（REST 对账有延迟）—— 但最终回复是权威结束信号，
    // 不允许把已结束的 turn 复活成 live。
    const before = s
    s = reduce(s, evRunning(true))
    expect(s.turns.get(T)?.phase.kind).toBe('committed')
    expect(s.activeTurn).toBeNull()
    expect(s).toBe(before) // 幂等：零渲染
  })
})
