/**
 * types.ts — 类型驱动状态机（TDSM）的核心类型层。
 *
 * 设计原则（docs/agent/web-rewrite-design.md §3）：
 *   非法状态不可表示（illegal states unrepresentable）。
 *   - Turn 的 live/frozen/committed 三态互斥由判别联合保证（取代
 *     live 槽 + committed 槽并存 + frozen 标志的 2-bits-3-态组合）。
 *   - committed 数据必可渲染：content 非空或 iterations 非空，
 *     由 CommittedPayload 判别联合 + NonEmpty 构造函数保证。
 *   - ID 类数值经 Brand 防混用；空数据经 NonEmpty 表达下界。
 *
 * 本文件是纯类型 + 唯一构造函数。全项目仅允许在此文件内使用 `as`
 * 断言（构造函数内部）——渲染层/reducer 一律禁止（ESLint no-as 规则管 辖）。
 */

import type { QueueItemPayload, TodoItem, WebIteration, WebSubAgentProgress, WebToolProgress } from '@/types/shared'

// ─── Brand：ID 防混淆 ─────────────────────────────────────────

declare const __brand: unique symbol
type Brand<T, B extends string> = T & { readonly [__brand]: B }

/** Per-chat 单调 turn 序号（后端 chatProcessLoop 分配，DB 恢复）。 */
export type TurnID = Brand<number, 'TurnID'>
/** Per-turn 1-based 迭代号。0 永远表示"未初始化/未跟踪"。 */
export type IterNum = Brand<number, 'IterNum'>
/** Per-run 语义进度序号（protocol.ProgressEvent.Seq），单调递增。 */
export type EventSeq = Brand<number, 'EventSeq'>

export const turnID = (n: number): TurnID => (n > 0 ? (n as TurnID) : (1 as TurnID))
export const iterNum = (n: number): IterNum => (n >= 1 ? (n as IterNum) : (1 as IterNum))
export const eventSeq = (n: number): EventSeq => n as EventSeq

// ─── NonEmpty：非空下界（消灭"空数据渲染"类 bug） ─────────────

/** 非空字符串。构造后非空性永真（immutable）—— 拿到类型即拿到保证。 */
export type NonEmptyS = Brand<string, 'NonEmptyS'>

export function nonEmptyStr(s: string | null | undefined): NonEmptyS | null {
  if (typeof s !== 'string' || s.length === 0) return null
  return s as NonEmptyS
}

/** 非空只读数组。committed 渲染数据的下界保证。 */
export type NonEmpty<T> = readonly [T, ...T[]]

export function nonEmptyArr<T>(xs: readonly T[] | null | undefined): NonEmpty<T> | null {
  if (!Array.isArray(xs) || xs.length === 0) return null
  return xs as unknown as NonEmpty<T>
}

// ─── Live 快照（live / frozen 共享的数据形状） ────────────────

/** live 态的完整数据。frozen 是它的定格副本（cancel：数据全保留）。 */
export interface LiveSnapshot {
  /** 当前迭代号（1-based）。 */
  readonly iter: IterNum
  /** LLM 是否仍在产出（PhaseDone 置 false，数据不动）。 */
  readonly streaming: boolean
  /** 流式累积文本（后端全量推送 —— 覆盖语义，非 append）。 */
  readonly content: string
  /** 流式累积思考（覆盖语义）。 */
  readonly reasoning: string
  /** 已完成迭代（append-only —— I4：只增不删，dedup by iteration#）。 */
  readonly iterations: readonly WebIteration[]
  /** 当前迭代的执行中工具。 */
  readonly activeTools: readonly WebToolProgress[]
  /** 流式检测中的工具（参数未生成完）。 */
  readonly streamingTools: readonly WebToolProgress[]
  /** GenUI 流式代码（元数据驱动，任何 ui.mode=genui 工具）。 */
  readonly genui: string
  /** SubAgent 进度树。 */
  readonly subAgents: readonly WebSubAgentProgress[]
  /** TODO 列表（monotonic 更新）。 */
  readonly todos: readonly TodoItem[]
  /** Token 用量（iteration 事件携带，ContextRing/会话上下文刷新用）。 */
  readonly tokenUsage: { readonly promptTokens: number; readonly completionTokens: number; readonly totalTokens: number } | null
  /** 实时流式时序（iteration 事件携带 stream_stats，TTFT/tokens-per-sec）。 */
  readonly streamStats: { readonly ttftMs: number; readonly tpotMs: number; readonly tokensPerSec: number; readonly totalMs: number; readonly chunks: number } | null
}

export const EMPTY_LIVE: LiveSnapshot = {
  iter: iterNum(1),
  streaming: true,
  content: '',
  reasoning: '',
  iterations: [],
  activeTools: [],
  streamingTools: [],
  genui: '',
  subAgents: [],
  todos: [],
  tokenUsage: null,
  streamStats: null,
}

// ─── TurnPhase：三态判别联合（核心不变量 I1） ─────────────────

/**
 * 一个 turn 的生命周期。三态互斥 —— 由判别联合保证。
 *
 * 历史教训（design doc §1.2）：
 *   - ghost turn-N-live 行 = live 槽与 committed 槽并存（旧 MessageStore 双轨）
 *     → 新设计单槽位 + 判别联合，并存不可表示（T4）。
 *   - "iter 全消失只剩空壳" = committed 数据可为空
 *     → CommittedPayload 强制非空（I2）。
 */
export type TurnPhase =
  | { readonly kind: 'live'; readonly data: LiveSnapshot }
  | { readonly kind: 'frozen'; readonly data: LiveSnapshot }
  | { readonly kind: 'committed'; readonly payload: CommittedPayload }

/**
 * committed 的可渲染性 —— 类型层表达（I2）：
 *   via 'text'    ：权威 text 事件提交（content 非空必真）。
 *   via 'fold'    ：turn_started 提前固化 / cancel 定格（iterations 非空必真）。
 * 不存在 { content:"", iterations:[] } 的组合 —— 构造函数签名不接受。
 */
export type CommittedPayload =
  | { readonly via: 'text'; readonly content: NonEmptyS; readonly iterations: readonly WebIteration[] }
  | { readonly via: 'fold'; readonly iterations: NonEmpty<WebIteration>; readonly content: string }

/** 唯一合法的 committed 构造入口（reducer 内使用）。 */
export function commitViaText(
  content: NonEmptyS,
  iterations: readonly WebIteration[],
): CommittedPayload {
  return { via: 'text', content, iterations }
}

/** fold 构造：iterations 必须非空（类型强制）；content 可为空字符串。 */
export function commitViaFold(
  iterations: NonEmpty<WebIteration>,
  content: string,
): CommittedPayload {
  return { via: 'fold', iterations, content }
}

// ─── Turn / ChatState ─────────────────────────────────────────

/** 乐观 user 行（发送瞬间创建，turn_started 后经 requestID 绑定进 turn）。 */
export interface UserRow {
  readonly id: string
  readonly content: NonEmptyS
  readonly timestamp: string
  readonly isNotification: boolean
  readonly queued: boolean
  readonly sending: boolean
  readonly requestID: string | null
  /** turn_id 提示（user_echo 在 turn_started 之前到达时用于绑定）。 */
  readonly turnHint: number | undefined
  readonly dbID: number | undefined
}

export interface Turn {
  readonly id: TurnID
  /** 触发本次 turn 的 user 行（乐观行绑定后移入；通知 turn 可为 null）。 */
  readonly user: UserRow | null
  readonly phase: TurnPhase
  /** turn_started 携带的 request_id —— 乐观 user 精确绑定（V2 语义）。 */
  readonly requestID: string | null
}

/** legacy 历史（无 turn_id，前缀段，只读 —— reload 全量替换）。 */
export interface LegacyRow {
  readonly id: string
  readonly role: 'user' | 'assistant'
  readonly content: string
  readonly iterations: readonly WebIteration[]
  readonly timestamp: string
  readonly dbID: number | undefined
}

/**
 * 单 chat 的全部消息状态 —— 单一事实源。
 *
 * I1（槽位唯一）：turns 是 ReadonlyMap —— key 唯一由 Map 语义保证，
 *   live/committed 并存（ghost 行）不可表示。
 * I3（活动唯一）：activeTurn 是唯一的"当前 turn"指针 —— 取代旧设计的
 *   4 个 ref guard（finalizedRef/phaseDoneRef/turnCommittedRef/
 *   finalizedTurnIDRef，共 70 处引用）。
 */
export interface ChatState {
  readonly chatID: string
  readonly turns: ReadonlyMap<TurnID, Turn>
  readonly legacy: readonly LegacyRow[]
  /** 唯一 live turn 的指针（I3）；null = 无活动 turn。 */
  readonly activeTurn: TurnID | null
  readonly lastSeq: EventSeq | null
  /** session busy/idle（侧边栏指示 + idle 清壳）。 */
  readonly busy: boolean
  /** 待绑定的乐观 user 队列（turn_started 之前存在 —— 发送瞬间创建）。 */
  readonly pendingUsers: readonly UserRow[]
  /** 会话级 todos —— turn 结束（text/idle）后存活（E2E 语义："todos survive
   *  after turn completes / text event / session switch"）。progress 事件
   * （iteration/phase_done）携带 todos 时更新；hydration（active_progress，
   * 含 phase=done 快照）回填。渲染层（liveProgressFromState）统一读此处。 */
  readonly todos: readonly TodoItem[]
  /** 排队中的消息（Staging Tray 数据源）。queue_state SSE 事件全量替换。 */
  readonly queue: readonly QueueItemPayload[]
}

export function initialChatState(chatID: string): ChatState {
  return { chatID, turns: new Map(), legacy: [], activeTurn: null, lastSeq: null, busy: false, pendingUsers: [], todos: [], queue: [] }
}

// ─── DomainEvent：闭合的事件联合（normalize 之后的纯世界） ────

/**
 * 规范化后的事件。全部数组字段非 null（Go nil slice → [] 在 normalize
 * 一次性完成 —— I6）。reducer 对其穷尽 switch。
 */
export type DomainEvent =
  | {
      readonly type: 'turn_started'
      readonly turnID: TurnID
      readonly requestID: string | null
      readonly trigger: 'user' | 'resume' | 'notification'
      /** turn_start.content —— notification trigger 携带通知内容（后端
       * TurnStartInfo{Trigger, Content, RequestID}）。弱网下 inject_user WS
       * 消息可能丢失，turn_started 是通知内容的唯一载体 —— 必须用它构造
       * user 行，否则只显示"思考中"看不到通知（用户报告）。 */
      readonly content: string | null
    }
  | {
      readonly type: 'iteration'
      /** null = turn_id 缺失/为 0（后端某些路径 cfg.TurnID=0）→ reduce 回退
       *  activeTurn（与 stream 事件一致）。todos 是会话级状态，不因 turn 缺失
       *  而丢弃。 */
      readonly turnID: TurnID | null
      readonly iter: IterNum
      /** null = 事件未携带 seq（E2E mock 省略）—— I5 基准不推进（无重放检测）。 */
      readonly seq: EventSeq | null
      readonly content: string | undefined
      readonly reasoning: string | undefined
      readonly activeTools: readonly WebToolProgress[]
      readonly completedTools: readonly WebToolProgress[]
      /** 本事件携带的已完成迭代增量（dedup by iteration#）。 */
      readonly iterationsDelta: readonly WebIteration[]
      readonly todos: readonly TodoItem[] | undefined
      readonly subAgents: readonly WebSubAgentProgress[] | undefined
      /** Token 用量（ContextRing/会话上下文刷新用）。 */
      readonly tokenUsage: NonNullable<LiveSnapshot['tokenUsage']> | undefined
      /** 实时流式时序（iteration 事件携带 stream_stats：TTFT/tokens-per-sec）。 */
      readonly streamStats: NonNullable<LiveSnapshot['streamStats']> | undefined
    }
  | {
      readonly type: 'stream'
      /** null = 事件未携带 turn_id（已知后端 gap）→ reduce 回退 activeTurn。 */
      readonly turnID: TurnID | null
      readonly seq: EventSeq | null
      /** 后端 stamp 的迭代号（getActiveIteration）。迭代前进时 reduce 用它
       *  清空旧 content/reasoning —— 否则迭代 N+1 的 stream 到达时，若
       *  content 尚未产出，迭代 N 的旧 content 残留到新迭代（"老 content
       *  到新迭代"竞态，用户报告）。 */
      readonly iteration: IterNum | null
      /** 全量累积文本（覆盖语义 —— 后端 delta_push 默认关闭）。 */
      readonly content: string | undefined
      readonly reasoning: string | undefined
      readonly streamingTools: readonly WebToolProgress[] | undefined
      readonly genui: string | undefined
      /** 实时流式时序（stream_stats：tokens/sec / TTFT）。每个 stream SSE
       *  帧都携带，live 据此实时更新 tkps。 */
      readonly streamStats: NonNullable<LiveSnapshot['streamStats']> | undefined
    }
  | {
      readonly type: 'phase_done'
      /** null = turn_id 缺失/为 0 → reduce 回退 activeTurn。 */
      readonly turnID: TurnID | null
      /** null = 事件未携带 seq（E2E mock 省略）—— I5 基准不推进。 */
      readonly seq: EventSeq | null
      /** 后端 recordFinalIteration 补记的最后迭代（normalize 后无 null 数组）。 */
      readonly finalIteration: WebIteration | null
      readonly todos: readonly TodoItem[] | undefined
    }
  | {
      readonly type: 'text_final'
      readonly turnID: TurnID | null
      readonly content: NonEmptyS | null
      readonly progressHistory: readonly WebIteration[]
      readonly cancelled: boolean
    }
  | {
      readonly type: 'session'
      readonly busy: boolean
    }
  | {
      readonly type: 'history_replaced'
      readonly legacy: readonly LegacyRow[]
      readonly turns: readonly Turn[]
      readonly active: { readonly turnID: TurnID; readonly snapshot: LiveSnapshot } | null
      readonly lastSeq: EventSeq | null
      /** 会话级 todos（active_progress 快照携带 —— 含 phase=done 的快照，
       *  turn 已结束但 todos 存活渲染）。 */
      readonly todos: readonly TodoItem[]
    }
  | {
      /** 乐观 user 创建（本地事件，非 SSE）。 */
      readonly type: 'user_sent'
      readonly row: UserRow
    }
  | {
      /** 后端 queue-admission 的 user 回声（带权威 turn_id，可能先于 turn_started）。 */
      readonly type: 'user_echo'
      readonly row: UserRow
    }
  | {
      /** REST 发送成功（ack）：清除 sending，回填服务端信息。dbID=0 表示
       *  未知（user 行 DB id 由 agent loop 持久化后才有，history reload 回填）。 */
      readonly type: 'user_ack'
      readonly requestID: string
      readonly dbID: number
      /** 服务端分配的 turn_id（排队消息为 0/缺失）。 */
      readonly turnHint?: number
      /** 消息入队（chat 忙，排队等待执行）。 */
      readonly queued?: boolean
    }
  | {
      /** REST 发送失败：移除乐观行（对齐旧 removeById 语义）。 */
      readonly type: 'user_fail'
      readonly requestID: string
    }
  | {
      /** 排队消息快照（queue_state SSE 事件 → Staging Tray 数据源）。
       *  全量替换语义：后端推送整个队列状态，前端直接替换。 */
      readonly type: 'queue_state'
      readonly queue: readonly QueueItemPayload[]
    }
