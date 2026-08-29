/**
 * derive.ts — 渲染模型：deriveRows : ChatState → readonly Row[]（纯函数）。
 *
 * 定理（design doc §5.3）：
 *   T1 total       —— 穷尽 switch + I2/I6 类型保证 ⇒ 无 TypeError 可达 ⇒ DOM 永不消失
 *   T4 无 ghost 行 —— 每 turn 恰经一次 assistantRow（判别联合三选一）⇒ 至多一行
 *   T5 线性一致    —— legacy 前缀 ⊕ turnID 升序 ⊕ (user < assistant)，纯函数
 *
 * 渲染组件只做 Row → DOM 映射；isPartial 语义收窄至 live/frozen（kind 判别，
 * 不再有 find(isPartial) 启发式 —— Bug 7 根治点）。
 */

import type { TodoItem, WebIteration, WebSubAgentProgress, WebToolProgress } from '@/types/shared'
import type { ChatState, LiveSnapshot, Turn } from './types'

// ─── Row 判别联合（渲染层唯一数据契约） ────────────────────────

export interface UserRowView {
  readonly kind: 'user'
  readonly id: string
  readonly content: string
  readonly timestamp: string
  readonly isNotification: boolean
  readonly queued: boolean
  readonly sending: boolean
  readonly dbID: number | undefined
  /** 排序键（turnID；pending 行 = Infinity 沉底）。 */
  readonly turnID: number
}

/** live assistant 行 —— 唯一接收实时进度的行（kind 判别，无启发式）。 */
export interface LiveRowView {
  readonly kind: 'live'
  readonly id: string
  readonly turnID: number
  readonly isPartial: true
  readonly streaming: boolean
  readonly content: string
  readonly reasoning: string
  readonly iterations: readonly WebIteration[]
  readonly activeTools: readonly WebToolProgress[]
  readonly streamingTools: readonly WebToolProgress[]
  readonly genui: string
  readonly subAgents: readonly WebSubAgentProgress[]
  readonly todos: readonly TodoItem[]
  readonly lastIter: number
}

/** frozen assistant 行（cancel 定格 / idle 兜底）—— isPartial=true 保 activeTools 渲染。 */
export interface FrozenRowView {
  readonly kind: 'frozen'
  readonly id: string
  readonly turnID: number
  readonly isPartial: true
  readonly content: string
  readonly reasoning: string
  readonly iterations: readonly WebIteration[]
  readonly activeTools: readonly WebToolProgress[]
  readonly genui: string
  readonly lastIter: number
}

export interface CommittedRowView {
  readonly kind: 'committed'
  readonly id: string
  readonly turnID: number
  readonly isPartial: false
  readonly content: string
  readonly iterations: readonly WebIteration[]
}

export type Row = UserRowView | LiveRowView | FrozenRowView | CommittedRowView

// ─── deriveRows：ρ（T5 顺序 = legacy ⊕ turnID 升序 ⊕ user<assistant） ──

export function deriveRows(s: ChatState): readonly Row[] {
  const turnRows: Row[] = []
  const sorted = [...s.turns.values()].sort((a, b) => a.id - b.id)
  for (const t of sorted) {
    if (t.user) turnRows.push(userRowOf(t))
    const ar = assistantRow(t)
    if (ar !== null) turnRows.push(ar)
  }

  // pendingUsers（未绑定的乐观行）：沉到底部（发送中/排队 —— 归属 turn 未知）。
  const pending: Row[] = s.pendingUsers.map(userRowView)

  // legacy 段保持 DB 顺序：user/assistant 交错（非 turn 模型 —— 直接按原序映射）。
  const legacySorted: Row[] = s.legacy.map((l): Row =>
    l.role === 'user'
      ? {
          kind: 'user',
          id: l.id,
          content: l.content,
          timestamp: l.timestamp,
          isNotification: false,
          queued: false,
          sending: false,
          dbID: l.dbID,
          turnID: 0,
        }
      : {
          kind: 'committed',
          id: l.id,
          turnID: 0,
          isPartial: false,
          content: l.content,
          iterations: l.iterations,
        },
  )

  return [...legacySorted, ...turnRows, ...pending]
}

// ─── assistantRow：穷尽 switch（T4：每 turn 至多一行） ─────────

function assistantRow(t: Turn): Row | null {
  switch (t.phase.kind) {
    case 'live': {
      const d = t.phase.data
      return {
        kind: 'live',
        id: `turn-${t.id}-live`,
        turnID: t.id,
        isPartial: true,
        streaming: d.streaming,
        content: d.content,
        reasoning: d.reasoning,
        iterations: d.iterations,
        activeTools: d.activeTools,
        streamingTools: d.streamingTools,
        genui: d.genui,
        subAgents: d.subAgents,
        todos: d.todos,
        lastIter: d.iter,
      }
    }
    case 'frozen': {
      // 空壳 frozen（完全无产出）不出行 —— Bug 6/8 的"幽灵行"根治点。
      if (!hasVisibleOutput(t.phase.data)) return null
      const d = t.phase.data
      // cancel 时正在执行的工具（activeTools，已标 error）折进最后迭代 ——
      // rowsToChatMessages 不向渲染层传 activeTools（liveProgress 在 frozen 时
      // 为空），TurnBody 从 iterations 读工具。保证"已渲染内容永不消失"
      //（cancel 后正在执行的 tool 保留在最新迭代 —— 用户/测试要求）。
      const errTools = d.activeTools.map(markError)
      const iterations = foldToolsIntoIterations(d.iterations, errTools, d.iter)
      return {
        kind: 'frozen',
        id: `turn-${t.id}`,
        turnID: t.id,
        isPartial: true,
        content: d.content,
        reasoning: d.reasoning,
        iterations,
        activeTools: errTools,
        genui: d.genui,
        lastIter: d.iter,
      }
    }
    case 'committed': {
      // I2：payload 必可渲染（via text → content 非空；via fold → iterations 非空）。
      // via 仅区分构造路径的 I2 保证，消费侧无差异 —— content 两分支同源
      //（恒等三元已删，F#7）。
      return {
        kind: 'committed',
        id: `turn-${t.id}-c`,
        turnID: t.id,
        isPartial: false,
        content: t.phase.payload.content,
        iterations: t.phase.payload.iterations,
      }
    }
  }
}

function userRowOf(t: Turn): Row {
  const u = t.user!
  return {
    kind: 'user',
    id: u.id,
    content: u.content,
    timestamp: u.timestamp,
    isNotification: u.isNotification,
    queued: u.queued,
    sending: u.sending,
    dbID: u.dbID,
    turnID: t.id,
  }
}

function userRowView(u: ChatState['pendingUsers'][number]): Row {
  return {
    kind: 'user',
    id: u.id,
    content: u.content,
    timestamp: u.timestamp,
    isNotification: u.isNotification,
    queued: u.queued,
    sending: u.sending,
    dbID: u.dbID,
    turnID: Number.MAX_SAFE_INTEGER,
  }
}

function hasVisibleOutput(d: LiveSnapshot): boolean {
  return (
    d.content !== '' ||
    d.reasoning !== '' ||
    d.iterations.length > 0 ||
    d.genui !== '' ||
    d.activeTools.length > 0 ||
    d.streamingTools.length > 0
  )
}

/** frozen 的进行中工具标 error（cancel 语义 —— "已渲染内容永不误导"）。 */
function markError(t: WebToolProgress): WebToolProgress {
  return t.status === 'running' || t.status === 'generating' || t.status === 'pending'
    ? { ...t, status: 'error' }
    : t
}

/** cancel 时正在执行的工具折进最后迭代（渲染层从 iterations 读工具）。
 *  最后迭代已存在 → 合并 tools；不存在（iterations 空）→ 追加含工具的迭代。 */
function foldToolsIntoIterations(
  its: readonly WebIteration[],
  tools: readonly WebToolProgress[],
  lastIter: number,
): readonly WebIteration[] {
  if (tools.length === 0) return its
  const arr = [...its]
  const idx = arr.findIndex((it) => it.iteration === lastIter)
  if (idx >= 0) {
    arr[idx] = {
      ...arr[idx],
      tools: [...arr[idx].tools, ...tools],
      toolCount: (arr[idx].toolCount ?? 0) + tools.length,
    }
  } else {
    arr.push({ iteration: lastIter, content: '', reasoning: '', tools: [...tools], toolCount: tools.length })
  }
  return arr
}
