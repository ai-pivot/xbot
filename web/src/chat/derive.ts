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
import type { ChatState, LegacyRow, LiveSnapshot, Turn } from './types'

// [TURNDROP] 诊断去重（derive 每帧调用 —— 同一 turnID 的 hollow-frozen 跳过
// 只报告一次；生产保留 console.warn 以便用户复现时捕获触发链）。
const turndropReported = new Set<string>()

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

// ─── legacyRowView：legacy 行（无 turn 消息）→ Row（压缩行复用） ─────

function legacyRowView(l: LegacyRow): Row {
  if (l.role === 'user') {
    return {
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
  }
  return {
    kind: 'committed',
    id: l.id,
    turnID: 0,
    isPartial: false,
    content: l.content,
    iterations: l.iterations,
  }
}

// ─── deriveRows：ρ（T5 顺序 = legacy ⊕ turnID 升序 ⊕ user<assistant） ──

export function deriveRows(s: ChatState): readonly Row[] {
  // 压缩行（legacy 带 anchorTurnID）：按锚插入 turns 之间 —— turnID < anchor
  // 的 turns 之后、>= anchor 的之前（= 压缩发生的时间位置）。此前压缩行渲染
  // 在 legacy 前缀段（列表最顶部）——1700 条消息的会话用户永远看不到
  // （chat_F64D4096DA6F"压缩了但和没压缩一样"）。anchor = 首个 incoming
  // turn 的 turnID（压缩行是 active 第一条，时间位置 = 压缩点 = tail 首行之前
  // 旧消息之后）。普通无 turn 行（无锚）保持前缀段（旧行为）。
  const anchoredLegacy = s.legacy
    .filter((l): l is LegacyRow & { anchorTurnID: number } => l.anchorTurnID !== undefined)
    .sort((a, b) => a.anchorTurnID - b.anchorTurnID)
  const prefixLegacy = s.legacy.filter((l) => l.anchorTurnID === undefined)

  const turnRows: Row[] = []
  const sorted = [...s.turns.values()].sort((a, b) => a.id - b.id)
  let ai = 0
  for (const t of sorted) {
    while (ai < anchoredLegacy.length && anchoredLegacy[ai].anchorTurnID <= t.id) {
      turnRows.push(legacyRowView(anchoredLegacy[ai]))
      ai++
    }
    if (t.user) turnRows.push(userRowOf(t))
    const ar = assistantRow(t)
    if (ar !== null) turnRows.push(ar)
  }
  // 尾部剩余（锚 > 所有 turns 的 turnID —— active 无 tail turn 的防御场景）。
  for (; ai < anchoredLegacy.length; ai++) {
    turnRows.push(legacyRowView(anchoredLegacy[ai]))
  }

  // pendingUsers（未绑定的乐观行）：沉到底部（发送中/排队 —— 归属 turn 未知）。
  const pending: Row[] = s.pendingUsers.map(userRowView)

  // legacy 段保持 DB 顺序：user/assistant 交错（非 turn 模型 —— 直接按原序映射）。
  // 仅无锚的普通行（anchoredLegacy 已按锚插入 turns 之间）。
  const legacySorted: Row[] = prefixLegacy.map(legacyRowView)

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
      if (!hasVisibleOutput(t.phase.data)) {
        // [TURNDROP] 诊断：空壳 frozen 被 derive 跳过 —— turn 从渲染层消失
        // （"整个 turn 的 assistant 消息完全消失"的渲染层形态）。每个 turnID
        // 只打一次（derive 每帧调用，Set 去重防刷屏）。
        turndropReported.add(`hollow-frozen:${t.id}`)
        console.warn('[TURNDROP] derive skipped hollow frozen (turn vanishes from render)', {
          turnID: t.id,
        })
        return null
      }
      const d = t.phase.data
      // cancel 时正在执行的工具折进最后迭代 —— rowsToChatMessages 不向渲染层
      // 传 activeTools（liveProgress 在 frozen 时为空），TurnBody 从 iterations 读
      // 工具。保证"已渲染内容永不消失"（cancel 后正在执行的 tool 保留在最新迭代
      // —— 用户/测试要求）。
      // F1（Loop2）：streamingTools（参数流式生成中，generating）与 activeTools
      // 同折 —— 只折 activeTools 会让 generating 工具在 cancel/text 丢失定格时
      // 从 frozen 行消失（text_final/reduce 的 foldInFlightToIterations 同原则：
      // activeTools + streamingTools 都是"从未完成、不在 iteration_history"的
      // in-flight 工具）。markError 对 done 工具恒等（既有语义：activeTools 里
      // 已完成的工具也保留折入 —— SSE 丢迭代 delta 时 frozen 渲染的最后防线）。
      const errTools = [...d.activeTools, ...d.streamingTools].map(markError)
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
