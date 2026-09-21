/**
 * integrate.ts — 新状态机 ↔ 现有渲染组件的适配层（M4 切换的唯一边界）。
 *
 * 职责：
 *   - historyToReplaced：useChatMessages 的 DB 历史（ChatMessage[]）→ history_replaced
 *   - rowsToChatMessages：deriveRows 的 Row[] → ChatMessage[]（MessageList 现有 props）
 *   - liveProgressFromState：ChatState → ProgressSnapshot 兼容对象（liveProgress prop）
 *
 * 这是 Row 判别联合 → ChatMessage 的受控映射（纯函数）：live/frozen 行
 * isPartial=true（MessageList 的 V5/V8 语义），committed isPartial=false。
 * 旧三文件删除后此文件是唯一保留的形状转换点。
 */

import {
  historyProgressToLive,
  normalizeWebIteration,
} from '@/components/agent/normalize'
import {
  EMPTY_PROGRESS_SNAPSHOT,
  type ChatMessage,
  type ProgressSnapshot,
  type TodoItem,
  type WebIteration,
  type WebSubAgentProgress,
  type WebToolProgress,
} from '@/types/shared'
import { commitViaFold, commitViaText, iterNum, nonEmptyArr, nonEmptyStr, turnID as mkTurnID, type ChatState, type DomainEvent, type LegacyRow, type LiveSnapshot, type Turn } from './types'
import type { Row } from './derive'

// ─── history → history_replaced ───────────────────────────────

export function historyToReplaced(
  messages: readonly ChatMessage[],
  initialProgress: unknown,
): DomainEvent {
  const legacy: LegacyRow[] = []
  const byTurn = new Map<number, { user: ChatMessage | null; assistants: ChatMessage[] }>()

  for (const m of messages) {
    if (m.role === 'system') continue
    // ⚠️ useChatMessages 的 store（旧 MessageStore）仍在运行 —— 它往 messages
    // 数组里写乐观行/echo 行/patchUser 行。这些行与状态机的 pendingUsers/
    // turns.user 重复（双渲染根因）。过滤策略：
    // - 无 dbID 的行（乐观/echo 副本）一律跳过 —— 渲染源是状态机
    // - 有 dbID 的行（DB 权威历史）放行进 turns/legacy
    if (m.dbID === undefined) continue
    if (!m.turnID || m.turnID <= 0) {
      legacy.push({
        id: m.id,
        role: m.role === 'user' ? 'user' : 'assistant',
        content: m.content,
        iterations: m.iterations,
        iterationsTruncated: m.iterationsTruncated ?? 0,
        timestamp: m.timestamp,
        dbID: m.dbID,
      })
      continue
    }
    let slot = byTurn.get(m.turnID)
    if (!slot) {
      slot = { user: null, assistants: [] }
      byTurn.set(m.turnID, slot)
    }
    if (m.role === 'user' && !slot.user) slot.user = m
    else if (m.role === 'assistant') slot.assistants.push(m)
  }

  const turns: Turn[] = []
  for (const [tid, slot] of byTurn) {
    const id = mkTurnID(tid)
    const user = slot.user
      ? {
          id: slot.user.id,
          content: nonEmptyStr(slot.user.content) ?? ('' as never),
          timestamp: slot.user.timestamp,
          isNotification: slot.user.isNotification === true,
          queued: false,
          sending: false,
          requestID: slot.user.requestID ?? null,
          turnHint: undefined,
          dbID: slot.user.dbID,
        }
      : null
    // 多个 assistant 行（异常历史）合并：iterations 连接，content 取最后非空。
    const iterations = slot.assistants.flatMap((a) => a.iterations ?? [])
    const lastContent = [...slot.assistants].reverse().find((a) => a.content !== '')?.content ?? ''
    // 后端按 turn 尾部截断迭代（历史响应有界化）⇒ 丢弃数量必须透传到渲染层，
    // 由 AssistantMessage 显示「更早的 N 个迭代」，绝不静默缺块。
    const itsTruncated = slot.assistants.reduce((n, a) => n + (a.iterationsTruncated ?? 0), 0)
    const nonEmptyIts = nonEmptyArr(iterations)
    const payload =
      nonEmptyIts !== null
        ? commitViaFold(nonEmptyIts, lastContent, itsTruncated)
        : nonEmptyStr(lastContent) !== null
          ? commitViaText(nonEmptyStr(lastContent)!, [])
          : null
    turns.push({
      id,
      user,
      phase: payload
        ? { kind: 'committed', payload }
        : // 无产出 assistant 行：frozen 空壳（derive 跳过渲染，user 行保留）。
          { kind: 'frozen', data: { ...EMPTY_SNAPSHOT } },
      requestID: user?.requestID ?? null,
    })
  }

  // active：DB 权威的 in-flight turn（刷新恢复 live）。
  // ⚠️ phase=done 的快照不恢复 —— turn 已结束（text/idle 兜底会收尾，
  // 或 DB 行已 commit）；恢复成 live 会让后续事件错挂（切回会话后
  // "看不到新进度"的帮凶之一）。
  let active: { turnID: ReturnType<typeof mkTurnID>; snapshot: LiveSnapshot } | null = null
  const hpRaw = initialProgress as { turn_id?: number; phase?: string; iteration?: number; stream_content?: string; content?: string; reasoning_stream_content?: string; iteration_history?: unknown[]; active_tools?: unknown[]; streaming?: boolean; todos?: unknown } | null
  // ⛔ 快照迭代**不截断**（用户 2026-09-21：「不能有任何 gap，任何 gap 都是破坏线性一致性」）。
  // 见 normalize.ts 顶部的说明：客户端与服务端各截一次 ⇒ 两个不相邻窗口 ⇒ 合并出 gap ⇒
  // 渲染只能在 gap 处截断（"历史停在旧位置 / 中间迭代不见"），而且取不回来。
  // 体积与渲染性能由渲染层解决（TurnBody 迭代级窗口化），不得丢数据。
  const hp = hpRaw
  if (
    hp &&
    typeof hp.turn_id === 'number' &&
    hp.turn_id > 0 &&
    hp.phase !== 'done' &&
    hp.phase !== 'frozen'
  ) {
    const live = historyProgressToLive(hp as never)
    active = { turnID: mkTurnID(hp.turn_id), snapshot: snapshotToLive(live) }
  }
  // 会话级 todos：快照（含 phase=done）携带的 todos 必须提取 —— turn 已结束
  // 但 todos 存活渲染（E2E："todos survive after turn completes / session
  // switch"；active 分支排除 done 会让 todos 随之丢失 —— 14 个 E2E 失败的
  // 共同根因之一）。
  const snapshotTodos: TodoItem[] = Array.isArray(hp?.todos)
    ? (hp.todos as { id?: unknown; text?: unknown; done?: unknown; status?: unknown }[]).map((t) => ({
        id: typeof t.id === 'number' ? t.id : 0,
        text: typeof t.text === 'string' ? t.text : '',
        // 新格式优先；老数据兼容 done: true → "done"
        status: typeof t.status === 'string' && t.status
          ? t.status
          : t.done === true ? 'done' : 'pending',
      }))
    : []

  return { type: 'history_replaced', legacy, turns, active, lastSeq: null, todos: snapshotTodos }
}

function snapshotToLive(live: ProgressSnapshot): LiveSnapshot {
  return {
    iter: iterNum(Math.max(1, live.iteration || live.lastIter || 1)),
    streaming: live.streaming,
    content: live.streamContent || live.content || '',
    reasoning: live.reasoningStreamContent || '',
    iterations: live.iterationHistory ?? [],
    activeTools: live.activeTools ?? [],
    streamingTools: live.streamingTools ?? [],
    genui: live.genuiContent ?? '',
    subAgents: live.subAgents ?? [],
    todos: live.todos ?? [],
    tokenUsage: live.tokenUsage ?? null,
    streamStats: live.streamStats ?? null,
  }
}

const EMPTY_SNAPSHOT: LiveSnapshot = {
  iter: iterNum(1),
  streaming: false,
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

// ─── rows → ChatMessage[]（渲染数据源） ────────────────────────

/**
 * Row → ChatMessage 的**对象恒等 memo**（长 turn 卡顿回归的根因修复）。
 *
 * PERF 不变量：Row 引用不变 ⇒ 输出 ChatMessage 必须恒等。
 * MessageList 的 `MessageItem`（memo）以 `message` 引用为边界；每帧新建对象会让
 * **全部可见行**逐帧重渲染 —— 而每个 assistant 行内部又是该 turn 的整棵迭代树，
 * 于是流式帧代价重新变成 O(turn 迭代数)。旧 MessageStore.toRows() 对未变更行
 * 直接 `rows.push(slot.assistant)`（对象恒等），本 memo 是新状态机管线上的等价保证。
 *
 * ⚠️ 配套契约：派生出的 ChatMessage 视为**只读**（渲染层不得就地 mutate）——
 * bindTurnIDs/orderMessageRows 需要改动时都先拷贝对象。
 */
const msgByRow = new WeakMap<Row, ChatMessage>()

export function rowsToChatMessages(rows: readonly Row[]): ChatMessage[] {
  const out: ChatMessage[] = []
  for (const r of rows) {
    let msg = msgByRow.get(r)
    if (msg === undefined) {
      msg = rowToChatMessage(r)
      msgByRow.set(r, msg)
    }
    out.push(msg)
  }
  return out
}

function rowToChatMessage(r: Row): ChatMessage {
  switch (r.kind) {
    case 'user':
      return {
        id: r.id,
        role: 'user',
        content: r.content,
        iterations: [],
        timestamp: r.timestamp,
        isPartial: false,
        // pendingUsers 行的 turnID 是 MAX_SAFE_INTEGER（deriveRows 排序用）——
        // 保持原值不改为 0：MessageList 按 turnID 排序时大值在底部（发送中
        // 行出现在最后，与 deriveRows 的排列一致）。改为 0 会让它排到所有
        // turn 前面（0 最小）→ 发送中行出现在最上方，发送完毕绑定真实
        // turnID 后"瞬移"回底部（用户报告的闪烁）。
        turnID: r.turnID,
        persisted: r.turnID !== Number.MAX_SAFE_INTEGER,
        isNotification: r.isNotification,
        queued: r.queued,
        sending: r.sending,
        dbID: r.dbID,
      }
    case 'live':
      return {
        id: r.id,
        role: 'assistant',
        content: r.content,
        // 透传引用（不拷贝）：iterations 的引用稳定性是 TurnBody→CommittedTurn
        // memo 生效的前提（详见 rowsToChatMessages 的 memo 契约）。
        iterations: r.iterations as WebIteration[],
        iterationsTruncated: r.iterationsTruncated ?? 0,
        timestamp: '',
        isPartial: true,
        turnID: r.turnID,
      }
    case 'frozen':
      return {
        id: r.id,
        role: 'assistant',
        content: r.content,
        iterations: r.iterations as WebIteration[],
        iterationsTruncated: r.iterationsTruncated ?? 0,
        timestamp: '',
        isPartial: true,
        // frozen ≠ live：见 ChatMessage.frozen 的注释（不变量：busy ⇒ 必须有
        // 进行中信号；frozen 行不得占用 live 槽位 / 抑制 busy 占位符）。
        frozen: true,
        turnID: r.turnID,
      }
    case 'committed':
      return {
        id: r.id,
        role: 'assistant',
        content: r.content,
        iterations: r.iterations as WebIteration[],
        iterationsTruncated: r.iterationsTruncated ?? 0,
        timestamp: '',
        isPartial: false,
        turnID: r.turnID,
        persisted: true,
      }
  }
}

// ─── ChatState → liveProgress（ProgressSnapshot 兼容） ────────

/**
 * MessageList 的 liveProgress 消费面（LiveIteration/AssistantMessage）：
 * streaming/phase/lastIter/iterationHistory/activeTools/completedTools/
 * streamingTools/streamContent/reasoningStreamContent/genuiContent/todos/
 * tokenUsage/turnID。
 * completedTools=[]：已完成工具由 iterations 携带（TurnBody 渲染）——
 * 与 committed 路径同构，避免双源。
 */
export function liveProgressFromState(s: ChatState): ProgressSnapshot {
  const activeTurn = s.activeTurn
  // 会话级 todos：无 active turn（turn 已结束）时也返回 todos —— todos 在
  // turn 生命周期外存活（E2E："todos survive after turn completes / text
  // event / session switch"）。其余字段在无 live 时空。
  // ⚠️ 不拷贝：todos 的引用稳定性是 TodoPullOut 等消费方 memo 的前提（同
  // iterationHistory —— 见下方注释）。
  const todos = s.todos as TodoItem[]
  // 会话级 goal：与 todos 同语义 —— turn 结束后仍存活（GoalBanner 读
  // liveProgress.goal；agent set_goal_complete 后 banner 实时切换到 completed
  // 样式，不依赖 get_goal RPC 的 session 切换重取）。
  const goal = s.goal
  if (activeTurn === null) return { ...EMPTY_PROGRESS_SNAPSHOT, todos, goal }
  const t = s.turns.get(activeTurn)
  if (!t || t.phase.kind !== 'live') return { ...EMPTY_PROGRESS_SNAPSHOT, todos, goal }
  const d = t.phase.data
  return {
    ...EMPTY_PROGRESS_SNAPSHOT,
    eventSeq: s.lastSeq !== null ? s.lastSeq : 0,
    phase: d.progressPhase || (d.streaming ? 'thinking' : 'tool_exec'),
    iteration: d.iter,
    lastIter: d.iter,
    streamContent: d.content,
    reasoningStreamContent: d.reasoning,
    content: d.iterations.length > 0 ? '' : d.content,
    streaming: d.streaming,
    // ⚠️ PERF 不变量（长 turn 卡顿回归的根因修复）：这里的数组必须**透传
    // reduce 的引用**，绝不 `[...]` 拷贝。每个流式帧 state 都是新引用 → 本
    // 函数每帧执行；拷贝会让 LiveIteration 的字段级 useMemo、TurnBody 的
    // `useMemo([iterations])` → CommittedTurn（memo）逐帧全部失效，流式帧
    // 代价变成 O(turn 迭代数)。reduce 的 stream case 对这些字段是 `...prev`
    // 透传（引用天然稳定）—— 在渲染边界的最后一米把它拷坏正是回归本身。
    // 只读契约：消费方（LiveIteration/AssistantMessage/TurnBody）不得 mutate。
    activeTools: d.activeTools as WebToolProgress[],
    completedTools: [],
    streamingTools: d.streamingTools as WebToolProgress[],
    iterationHistory: d.iterations as WebIteration[],
    genuiContent: d.genui,
    // todos 统一读会话级（iteration/phase_done 事件同步写入；live data 的
    // todos 仅作 hydration union 的中间态）。
    todos,
    goal,
    subAgents: d.subAgents as WebSubAgentProgress[],
    tokenUsage: d.tokenUsage,
    streamStats: d.streamStats,
    turnID: t.id,
  }
}

/** normalize 历史迭代（供 integrate 消费方复用）。 */
export { normalizeWebIteration }
