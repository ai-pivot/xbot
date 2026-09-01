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
import type { QueueItemPayload, TodoItem } from '@/types/shared'
import {
  eventSeq,
  iterNum,
  nonEmptyStr,
  turnID,
  type DomainEvent,
  type LiveSnapshot,
} from './types'

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
      // 新格式 status 必填；老数据兼容 done: true → "done"
      status: typeof r?.status === 'string' && r.status
        ? r.status
        : r?.done === true ? 'done' : 'pending',
    }
  })
}

/**
 * normalizeEvent — 把一条 raw WS/SSE 消息规范化为 DomainEvent。
 * 返回 null 表示事件非法或不属于本 chat（调用方直接丢弃）。
 *
 * 支持的 raw type：
 *   progress_structured → turn_started / iteration / phase_done / stream（按 phase + 载荷分流）
 *   stream_content      → stream
 *   text                → text_final
 *   session             → session
 *   user_echo/inject_user → user_echo
 *
 * 返回【事件数组】：一条 progress_structured 可能同时携带结构化载荷与流式
 * 载荷（旧前端在同一 handler 里两者都处理 —— get_active_progress 合并
 * streamState 的快照就是 stream_content/genui_content 与 active_tools 并存）。
 * 此时产出 [stream, structured] 两个事件（stream 先应用）。null/空数组 = 丢弃。
 */
export function normalizeEvent(raw: unknown, chatID: string): readonly DomainEvent[] | null {
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
    case 'queue_state': {
      const qs = asRecord(env.queue_state)
      const items = qs && Array.isArray(qs.items) ? qs.items as QueueItemPayload[] : []
      return [{ type: 'queue_state', queue: items }]
    }
    default:
      return null
  }
}

/** channel 前缀剥离（"web:chat-1" vs "chat-1" 兼容）。 */
function stripChannel(chatID: string): string {
  const i = chatID.indexOf(':')
  return i >= 0 ? chatID.slice(i + 1) : chatID
}

// ─── progress_structured → turn_started / iteration / phase_done / stream ──

/** progress 载荷是否携带流式字段（stream/genui/streamingTools）。 */
function hasStreamPayload(p: Record<string, unknown>): boolean {
  return (
    p.stream_content !== undefined ||
    p.reasoning_stream_content !== undefined ||
    p.genui_content !== undefined ||
    p.streaming_tools !== undefined
  )
}

/** 从 progress 载荷构造 stream 事件。 */
function streamEventFrom(
  p: Record<string, unknown>,
  turn: number | null,
  seq: ReturnType<typeof eventSeq> | null,
): DomainEvent {
  return {
    type: 'stream',
    turnID: turn !== null ? turnID(turn) : null,
    seq,
    iteration: typeof p.iteration === 'number' && p.iteration >= 1 ? iterNum(p.iteration) : null,
    content: optStr(p.stream_content),
    reasoning: optStr(p.reasoning_stream_content),
    streamingTools: Array.isArray(p.streaming_tools)
      ? normalizeWebTools(p.streaming_tools)
      : undefined,
    genui: optStr(p.genui_content),
    streamStats: parseStreamStats(p),
  }
}

/** 从 progress 载荷解析 stream_stats（tokens/sec / TTFT）。 */
function parseStreamStats(
  p: Record<string, unknown>,
): NonNullable<LiveSnapshot['streamStats']> | undefined {
  const rawSS = asRecord(p.stream_stats)
  if (!rawSS) return undefined
  const tokensPerSec = typeof rawSS.tokens_per_sec === 'number' ? rawSS.tokens_per_sec : 0
  const ttftMs = typeof rawSS.ttft_ms === 'number' ? rawSS.ttft_ms : 0
  // 完全无数据（后端未带 stream_stats 或全零）→ undefined，不覆盖 live。
  if (tokensPerSec <= 0 && ttftMs <= 0) return undefined
  return {
    ttftMs,
    tpotMs: typeof rawSS.tpot_ms === 'number' ? rawSS.tpot_ms : 0,
    tokensPerSec,
    totalMs: typeof rawSS.total_ms === 'number' ? rawSS.total_ms : 0,
    chunks: typeof rawSS.chunks === 'number' ? rawSS.chunks : 0,
  }
}

function normalizeProgress(env: Record<string, unknown>): readonly DomainEvent[] | null {
  const p = asRecord(env.progress)
  if (!p) return null

  const turn = optTurnID(p.turn_id)
  const phase = typeof p.phase === 'string' ? p.phase : ''
  const seq = typeof p.seq === 'number' ? eventSeq(p.seq) : null
  const streamPayload = hasStreamPayload(p)

  // ── turn_started ──
  if (phase === 'turn_started') {
    if (!turn) return null
    const ts = asRecord(p.turn_start)
    const rawTrigger = optStr(ts?.trigger)
    const trigger: 'user' | 'resume' | 'notification' =
      rawTrigger === 'resume' ? 'resume' : rawTrigger === 'notification' ? 'notification' : 'user'
    return [{ type: 'turn_started', turnID: turnID(turn), requestID: optStr(ts?.request_id) ?? null, trigger, content: optStr(ts?.content) ?? null }]
  }

  // ── 纯流式帧（打字机/genui 流）──
  // ⚠️ Web channel 把【所有】ProgressEvent 转发为 type='progress_structured'
  // （旧 useProgressStream 注释："the Web channel forwards ALL ProgressEvents
  // as type=progress_structured, including stream callbacks"）—— 不存在独立的
  // 'stream_content' 消息类型！打字机帧 = progress_structured + phase='' +
  // stream_content/reasoning_stream_content/genui_content 字段。必须在
  // iteration fallback 之前分流：否则被误判为 iteration 事件，流式字段被完全
  // 忽略 → 打字机死掉 / GenUI 流式预览不渲染（E2E genui-render 复现）。
  if (phase === '' && streamPayload) {
    return [streamEventFrom(p, turn, seq)]
  }

  // ── phase_done（PhaseDone：turn 结束，text/cancel ack 随后到） ──
  if (phase === 'done') {
    // seq 缺失（E2E mock 省略）宽容：事件照常产出（reduce 的 I5 对 null seq
    // 不推进基准）。真实后端 ProgressEvent 总带 seq。
    // turn 缺失（turn_id=0）也照常产出 —— todos 是会话级状态，不因 turn 缺失
    // 而丢弃（turnID 置 null，reduce 回退 activeTurn）。
    // 后端 recordFinalIteration attach 的最后迭代快照（可能 null/缺失）。
    const rawHist = Array.isArray(p.iteration_history) ? p.iteration_history : []
    const normalized = rawHist.map(normalizeWebIteration).filter((x): x is NonNullable<typeof x> => x !== null)
    const finalIteration = normalized.length > 0 ? normalized[normalized.length - 1] : null
    const done: DomainEvent = {
      type: 'phase_done',
      turnID: turn !== null ? turnID(turn) : null,
      seq,
      finalIteration,
      todos: optTodos(p.todos),
    }
    // 快照合并场景：done + 流式载荷并存 → stream 先应用（收尾定格流式文本）。
    return streamPayload ? [streamEventFrom(p, turn, seq), done] : [done]
  }

  // ── iteration（普通结构化事件：thinking / tool_exec / …） ──
  // seq 缺失（E2E mock 省略）宽容：事件照常产出（reduce 的 I5 对 null seq
  // 不推进基准 —— `ev.seq <= s.lastSeq` 对 null 恒 false，不误杀）。
  // turn 缺失（turn_id=0）也照常产出 —— todos 是会话级，不因 turn 缺失丢弃。
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
  // stream_stats（实时流式时序：TTFT/tokens-per-sec）—— iteration 事件携带，
  // reduce 写入 live，liveProgressFromState 输出到 ProgressSnapshot.streamStats。
  const rawSS = asRecord(p.stream_stats)
  const streamStats =
    rawSS && (typeof rawSS.tokens_per_sec === 'number' || typeof rawSS.ttft_ms === 'number')
      ? {
          ttftMs: typeof rawSS.ttft_ms === 'number' ? rawSS.ttft_ms : 0,
          tpotMs: typeof rawSS.tpot_ms === 'number' ? rawSS.tpot_ms : 0,
          tokensPerSec: typeof rawSS.tokens_per_sec === 'number' ? rawSS.tokens_per_sec : 0,
          totalMs: typeof rawSS.total_ms === 'number' ? rawSS.total_ms : 0,
          chunks: typeof rawSS.chunks === 'number' ? rawSS.chunks : 0,
        }
      : undefined
  const iter: DomainEvent = {
    type: 'iteration',
    turnID: turn !== null ? turnID(turn) : null,
    iter: iterNum(typeof p.iteration === 'number' && p.iteration >= 1 ? p.iteration : 1),
    seq,
    content: optStr(p.content),
    reasoning: optStr(p.reasoning),
    // Go nil slice → JSON null → []（I6：normalize 之后无 null 数组）
    activeTools: normalizeWebTools(Array.isArray(p.active_tools) ? p.active_tools : []),
    completedTools: normalizeWebTools(Array.isArray(p.completed_tools) ? p.completed_tools : []),
    iterationsDelta: rawDelta.map(normalizeWebIteration).filter((x): x is NonNullable<typeof x> => x !== null),
    todos: optTodos(p.todos),
    subAgents: Array.isArray(p.sub_agents)
      ? normalizeWebSubAgents(p.sub_agents as unknown[])
      : undefined,
    tokenUsage,
    streamStats,
  }
  // 快照合并场景（get_active_progress mergeStreamState）：结构化 + 流式载荷
  // 并存 → [stream, iteration]（stream 先应用；iteration 的 content 存在时
  // 覆盖 stream 文本 —— 旧 DUPLICATE PREVENTION 语义）。
  return streamPayload ? [streamEventFrom(p, turn, seq), iter] : [iter]
}

// ─── stream_content → stream ──

function normalizeStream(env: Record<string, unknown>): readonly DomainEvent[] | null {
  const p = asRecord(env.progress)
  if (!p) return null
  // turn_id 缺失（已知后端 gap：部分 stream 事件无 turn_id）不丢弃 ——
  // 透传 null，reduce 回退 activeTurn。旧前端对 stream 事件同样无 turn_id
  // 要求（打字机依赖此宽容性）。
  const turn = optTurnID(p.turn_id)
  return [
    {
      type: 'stream',
      turnID: turn !== null ? turnID(turn) : null,
      seq: typeof p.seq === 'number' ? eventSeq(p.seq) : null,
      iteration: typeof p.iteration === 'number' && p.iteration >= 1 ? iterNum(p.iteration) : null,
      content: optStr(p.stream_content),
      reasoning: optStr(p.reasoning_stream_content),
      streamingTools: Array.isArray(p.streaming_tools)
        ? normalizeWebTools(p.streaming_tools)
        : undefined,
      genui: optStr(p.genui_content),
      streamStats: parseStreamStats(p),
    },
  ]
}

// ─── text → text_final ──

function normalizeText(env: Record<string, unknown>): readonly DomainEvent[] | null {
  const turn = optTurnID(env.turn_id)
  const content = typeof env.content === 'string' ? env.content : ''
  const progressHistory = parseWebIterations(optStr(env.progress_history))
  return [
    {
      type: 'text_final',
      turnID: turn !== null ? turnID(turn) : null,
      content: nonEmptyStr(content),
      progressHistory,
      cancelled: env.cancelled === true,
    },
  ]
}

// ─── session → session ──

function normalizeSession(env: Record<string, unknown>): readonly DomainEvent[] | null {
  const s = asRecord(env.session)
  const action = optStr(s?.action)
  if (action !== 'busy' && action !== 'idle') return null
  return [{ type: 'session', busy: action === 'busy' }]
}

// ─── user_echo / inject_user → user_echo ──

// F#9：同毫秒两条 echo（后端连续注入）→ Date.now() 相同 → id 碰撞 →
// React key 重复 + TanStack Virtual 高度测量串行。echoSeq 单调后缀保证唯一
//（与 useChatMessages.ts 乐观行 id 的既有模式一致）。
let echoSeq = 0

function normalizeUserEcho(env: Record<string, unknown>): readonly DomainEvent[] | null {
  // F#10：nonEmptyStr smart constructor 直接产出 NonEmptyS（原 gate +
  // `as never` 绕过 branded 类型 —— no-as 规则）。'' → null（非法，丢弃）。
  const content = nonEmptyStr(typeof env.content === 'string' ? env.content : '')
  if (content === null) return null
  const turn = optTurnID(env.turn_id)
  // ⚠️ requestID 字段兼容（双 user 行 + 双思考中根因）：后端 web_inbound.go
  // 把 requestID 序列化到 WSMessage.ID（json:"id"），不带 request_id 字段。
  // 旧代码只读 env.request_id → 永远 null → reduce 的幂等检查全部跳过 →
  // echo 无条件追加进 pendingUsers → 同一 user 在 turns[].user 和 pending
  // 各一份（双 user 行；第二个"思考中"是底部 busy placeholder，px-3 缩进
  // 多空格 —— 用户报告）。大部分时候 useChatMessages 的 echo 处理（读 msg.id，
  // 字段正确）触发 history_replaced → step5 按 turnHint 清理兜底；但 REST ack
  // 先到把 MessageStore 行标 persisted 时该兜底跳过 → 偶发双行。requestID
  // 正确解析后幂等在 reduce 层结构生效，不依赖任何兜底。
  const requestID = optStr(env.request_id) ?? optStr(env.id) ?? null
  return [{
    type: 'user_echo',
    row: {
      id: `echo-${turn ?? 'x'}-${Date.now()}-${echoSeq++}`,
      content,
      timestamp: new Date().toISOString(),
      isNotification: env.is_notification === true,
      queued: false,
      sending: false,
      requestID,
      turnHint: turn ?? undefined,
      dbID: undefined,
    },
  },
  ]
}

// ─── 本地事件构造器（非 SSE —— UI 侧直接构造已规范化的 DomainEvent） ──
// F#8：userSentEvent/historyReplacedEvent 已删除 —— grep 全项目零引用
//（useAgentChatState 直接内联构造 DomainEvent，不经过这两个包装）。
