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

  it('running=true **绝不**把已结束的 turn 伪装成 live（历史 turn 走 commitViaFold ⇒ 伪造会让普通切会话必现 ghost busy）', () => {
    // 现场：切到一个 busy 会话（running=true），历史里是**已结束**的 turn
    //（DB 还原 ⇒ integrate 用 commitViaFold ⇒ `via !== 'text'` 恒成立）。
    // 上一版据此"提升"它就是"idle 被当作 busy"的反方向 P0 —— 必须不成立。
    let s = initialChatState('web:chatX')
    s = reduce(s, {
      type: 'history_replaced',
      turns: [
        {
          id: T,
          user: null,
          phase: { kind: 'committed', payload: { via: 'fold', content: '', iterations: [mkIter(1, 'one'), mkIter(2, 'two')] } },
          requestID: null,
        },
      ],
      legacy: [],
      lastSeq: null,
      active: null, // active_progress 缺失（切会话竞态）
      todos: [],
    })
    const afterHistory = s
    expect(s.turns.get(T)?.phase.kind, '历史 turn 必须保持 committed').toBe('committed')
    expect(s.activeTurn).toBeNull()

    // 会话 running=true 只更新闸门 —— 不造 live（可视化由渲染层占位符保障）。
    s = reduce(s, evRunning(true))
    expect(s.turns.get(T)?.phase.kind, 'running=true 不得把历史 turn 提升为 live').toBe('committed')
    expect(s.activeTurn).toBeNull()
    expect(s.sessionRunning).toBe(true)
    // 幂等：同一 running=true 再来一次 ⇒ 原 state（零渲染）。
    const once = s
    expect(reduce(s, evRunning(true))).toBe(once)
    // 且历史内容零改动。
    expect(s.turns.get(T)).toBe(afterHistory.turns.get(T))
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
