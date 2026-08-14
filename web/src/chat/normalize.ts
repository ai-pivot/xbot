/**
 * normalize.ts — raw SSE/WS JSON → DomainEvent 的唯一入口（I6 保证点）。
 *
 * 职责（全项目唯一的 null/格式处理点）：
 *   - Go nil slice 序列化的 JSON null → []（Bug 4 根治：null.tools 进渲染层）
 *   - snake_case → 判别联合字段
 *   - 数值校验（turn_id/iteration/seq）
 *   - 非法事件 → null（调用方丢弃，状态机零污染）
 *
 * normalize 之后的世界里不存在 null 数组字段 —— 类型未声明 null 即不存在。
 * 渲染层与 reducer 永远不再做格式防御（也禁止 `as`，ESLint no-as）。
 */

import {
  normalizeWebIteration,
  parseWebIterations,
} from '@/components/agent/normalize'
import {
  normalizeWebSubAgents,
  normalizeWebTools,
} from '@/components/agent/progressStore'
import type { TodoItem } from '@/types/shared'
import {
  eventSeq,
  iterNum,
  nonEmptyStr,
  turnID,
  type DomainEvent,
  type UserRow,
} from './types'

/** raw WS/SSE 消息的信封（channel/web 转发的形状）。 */
interface RawEnvelope {
  type?: unknown
  content?: unknown
  progress?: unknown
  progress_history?: unknown
  cancelled?: unknown
  turn_id?: unknown
  chat_id?: unknown
}

function asRecord(v: unknown): Record<string, unknown> | null {
  return v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : null
}

function optStr(v: unknown): string | undefined {
  return typeof v === 'string' ? v : undefined
}

function optTurnID(v: unknown): number | null {
  return typeof v === 'number' && Number.isInteger(v) && v > 0 ? v : null
}

function optTodos(v: unknown): TodoItem[] | undefined {
  if (!Array.isArray(v)) return undefined
  return v.map((t) => {
    const r = asRecord(t)
    return {
      id: typeof r?.id === 'number' ? r.id : 0,
      text: typeof r?.text === 'string' ? r.text : '',
      done: Boolean(r?.done),
    }
  })
}

/**
 * normalizeEvent — 把一条 raw WS/SSE 消息规范化为 DomainEvent。
 * 返回 null 表示事件非法或不属于本 chat（调用方直接丢弃）。
 *
 * 支持的 raw type：
 *   progress_structured → turn_started / iteration / phase_done（按 phase 分流）
 *   stream_content      → stream
 *   text                → text_final
 *   session             → session
 */
export function normalizeEvent(raw: unknown, chatID: string): DomainEvent | null {
  const env = asRecord(raw)
  if (!env) return null

  // chat 过滤：消息携带的 chat_id 不匹配本 chat → 丢弃。双剥 channel 前缀
  // （本地可能带 "web:" 前缀而远端 bare，或反之 —— 两个方向都兼容）。
  const msgChat = optStr(env.chat_id)
  if (msgChat && stripChannel(msgChat) !== stripChannel(chatID)) return null

  switch (typeof env.type === 'string' ? env.type : '') {
    case 'progress_structured':
      return normalizeProgress(env)
    case 'stream_content':
      return normalizeStream(env)
    case 'text':
      return normalizeText(env)
    case 'session':
      return normalizeSession(env)
    case 'user_echo':
    case 'inject_user':
      return normalizeUserEcho(env)
    default:
      return null
  }
}

/** channel 前缀剥离（"web:chat-1" vs "chat-1" 兼容）。 */
function stripChannel(chatID: string): string {
  const i = chatID.indexOf(':')
  return i >= 0 ? chatID.slice(i + 1) : chatID
}

// ─── progress_structured → turn_started / iteration / phase_done ──

function normalizeProgress(env: Record<string, unknown>): DomainEvent | null {
  const p = asRecord(env.progress)
  if (!p) return null

  const turn = optTurnID(p.turn_id)
  const phase = typeof p.phase === 'string' ? p.phase : ''
  const seq = typeof p.seq === 'number' ? eventSeq(p.seq) : null

  // ── turn_started ──
  if (phase === 'turn_started') {
    if (!turn) return null
    const ts = asRecord(p.turn_start)
    const rawTrigger = optStr(ts?.trigger)
    const trigger: 'user' | 'resume' | 'notification' =
      rawTrigger === 'resume' ? 'resume' : rawTrigger === 'notification' ? 'notification' : 'user'
    return { type: 'turn_started', turnID: turnID(turn), requestID: optStr(ts?.request_id) ?? null, trigger }
  }

  // ── phase_done（PhaseDone：turn 结束，text/cancel ack 随后到） ──
  if (phase === 'done') {
    if (!turn || !seq) return null
    // 后端 recordFinalIteration attach 的最后迭代快照（可能 null/缺失）。
    const rawHist = Array.isArray(p.iteration_history) ? p.iteration_history : []
    const normalized = rawHist.map(normalizeWebIteration).filter(Boolean)
    const finalIteration = normalized.length > 0 ? normalized[normalized.length - 1] : null
    return {
      type: 'phase_done',
      turnID: turnID(turn),
      seq,
      finalIteration,
      todos: optTodos(p.todos),
    }
  }

  // ── iteration（普通结构化事件：thinking / tool_exec / …） ──
  if (!turn || !seq) return null
  const rawDelta = Array.isArray(p.iteration_history) ? p.iteration_history : []
  // token_usage（ContextRing/会话上下文刷新）。
  const rawTU = asRecord(p.token_usage)
  const tokenUsage =
    rawTU && typeof rawTU.prompt_tokens === 'number'
      ? {
          promptTokens: rawTU.prompt_tokens,
          completionTokens: typeof rawTU.completion_tokens === 'number' ? rawTU.completion_tokens : 0,
          totalTokens: typeof rawTU.total_tokens === 'number' ? rawTU.total_tokens : 0,
        }
      : undefined
  return {
    type: 'iteration',
    turnID: turnID(turn),
    iter: iterNum(typeof p.iteration === 'number' && p.iteration >= 1 ? p.iteration : 1),
    seq,
    content: optStr(p.content),
    reasoning: optStr(p.reasoning),
    // Go nil slice → JSON null → []（I6：normalize 之后无 null 数组）
    activeTools: normalizeWebTools(Array.isArray(p.active_tools) ? p.active_tools : []),
    completedTools: normalizeWebTools(Array.isArray(p.completed_tools) ? p.completed_tools : []),
    iterationsDelta: rawDelta.map(normalizeWebIteration).filter(Boolean),
    todos: optTodos(p.todos),
    subAgents: Array.isArray(p.sub_agents)
      ? normalizeWebSubAgents(p.sub_agents as unknown[])
      : undefined,
    tokenUsage,
  }
}

// ─── stream_content → stream ──

function normalizeStream(env: Record<string, unknown>): DomainEvent | null {
  const p = asRecord(env.progress)
  if (!p) return null
  const turn = optTurnID(p.turn_id)
  if (!turn) return null
  return {
    type: 'stream',
    turnID: turnID(turn),
    seq: typeof p.seq === 'number' ? eventSeq(p.seq) : null,
    content: optStr(p.stream_content),
    reasoning: optStr(p.reasoning_stream_content),
    streamingTools: Array.isArray(p.streaming_tools)
      ? normalizeWebTools(p.streaming_tools)
      : undefined,
    genui: optStr(p.genui_content),
  }
}

// ─── text → text_final ──

function normalizeText(env: Record<string, unknown>): DomainEvent | null {
  const turn = optTurnID(env.turn_id)
  const content = typeof env.content === 'string' ? env.content : ''
  const progressHistory = parseWebIterations(optStr(env.progress_history))
  return {
    type: 'text_final',
    turnID: turn !== null ? turnID(turn) : null,
    content: nonEmptyStr(content),
    progressHistory,
    cancelled: env.cancelled === true,
  }
}

// ─── session → session ──

function normalizeSession(env: Record<string, unknown>): DomainEvent | null {
  const s = asRecord(env.session)
  const action = optStr(s?.action)
  if (action !== 'busy' && action !== 'idle') return null
  return { type: 'session', busy: action === 'busy' }
}

// ─── user_echo / inject_user → user_echo ──

function normalizeUserEcho(env: Record<string, unknown>): DomainEvent | null {
  const content = typeof env.content === 'string' ? env.content : ''
  if (content === '') return null
  const turn = optTurnID(env.turn_id)
  return {
    type: 'user_echo',
    row: {
      id: `echo-${turn ?? 'x'}-${Date.now()}`,
      content: content as never,
      timestamp: new Date().toISOString(),
      isNotification: env.is_notification === true,
      queued: false,
      sending: false,
      requestID: optStr(env.request_id) ?? null,
      turnHint: turn ?? undefined,
      dbID: undefined,
    },
  }
}

// ─── 本地事件构造器（非 SSE —— UI 侧直接构造已规范化的 DomainEvent） ──

let localSeq = 0

/** 乐观 user 行创建（sendMessage 瞬间）。 */
export function userSentEvent(row: Omit<UserRow, 'id'>): DomainEvent {
  return { type: 'user_sent', row: { ...row, id: `local-${Date.now()}-${++localSeq}` } }
}

/** history reload / hydration / rewind：全量替换状态（后端权威）。 */
export function historyReplacedEvent(ev: {
  legacy: readonly import('./types').LegacyRow[]
  turns: readonly import('./types').Turn[]
  active: { turnID: number; snapshot: import('./types').LiveSnapshot } | null
  lastSeq: number | null
}): DomainEvent {
  return {
    type: 'history_replaced',
    legacy: ev.legacy,
    turns: ev.turns,
    active: ev.active ? { turnID: turnID(ev.active.turnID), snapshot: ev.active.snapshot } : null,
    lastSeq: ev.lastSeq !== null ? eventSeq(ev.lastSeq) : null,
  }
}
