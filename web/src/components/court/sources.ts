/**
 * 通信视图的真实数据源（2026-10-22 重做）。
 *
 * 旧实现只读 `/api/groups*`，而生产库 groups / group_members 各 0 行、也没有
 * 任何写入者 —— 所以组织树「暂无成员」、中间「暂无通信数据」、线程「暂无线程」
 * 是**永远成立**的，面板等于废物。现在改为从真实存量数据渲染：
 *
 *   组织树：POST /api/session-tree（interactive sub-agents 层级，生产有 9–14 个部门）
 *   通信流：各会话 GET /api/messages，取 kind ∈ STRUCTURED_KINDS 的行
 *           （/api/messages 是**单会话**接口，所以按 session-tree 逐个会话拉，
 *            并发生上限 CONCURRENCY；不新增后端接口）
 *   群组：  GET /api/groups 仍然作为可选叠加数据源（0 行不再导致整页空）
 *
 * 结构化消息的归属说明（重要，别当成 bug）：
 * 落库时 `sender_type/sender_id` 记录的是**发起人**（human / 父 agent），
 * 消息行存放在**目标会话**库里。因此这里的「发送方」= 该消息所属会话的泳道，
 * 这既是唯一可得的事实，也符合「这条事件发生在哪个部门」的阅读方式。
 */
import { postAPI } from '@/lib/api'
import { normalizeSessionTree, type ListSessionTreeResponse, type RawChat } from '@/hooks/useSessionStore'
import { fetchStructuredMessages } from '@/structured/api'
import type { SessionInfo } from '@/types/shared'
import type { GroupMessage, Lane } from './types'

/** 参与通信流的结构化 kind（text 是普通消息，不进时间轴）。 */
export const STRUCTURED_KINDS: readonly string[] = [
  'report', 'ask_user', 'answer', 'decision', 'command', 'event',
]

/** 单次加载最多扫描多少个会话（防 session-tree 历史膨胀时打爆请求数）。 */
const MAX_SESSIONS = 40
/** 并发上限：每个会话一次 /api/messages。 */
const CONCURRENCY = 6
/** 每个会话最多取多少条结构化消息。 */
const PER_SESSION_LIMIT = 200

export interface CourtSession {
  /** 稳定 key：`<channel>:<chatID>`。 */
  key: string
  /** 泳道 id：子代理用 `agent:<role>/<instance>`，主会话用 `session:<channel>:<chatID>`。 */
  laneID: string
  /** 展示名（部门名 / 会话名）。 */
  name: string
  /** 角色名（子代理才有，如 `court.hubu`）。 */
  role?: string
  channel: string
  chatID: string
  type: 'user' | 'agent' | 'system'
  status: Lane['status']
  running: boolean
  lastActive: string
  /** 父泳道 id（层级缩进用）。 */
  parentLaneID?: string
  /** 父**会话** chatID —— 「直属子代理」判定用的是这个，不是泳道 id。 */
  parentChatID?: string
  /** 后端原始 status（`unloaded` 等不映射进 Lane['status']，只用于过滤）。 */
  rawStatus: string
  /**
   * 后端为「父会话已不存在」的子代理**合成**的占位父行（SessionInfo.synthetic）。
   * 它不是真会话，只是归一化时为了让树连起来的脚手架 ⇒ 组织树不给它一行。
   */
  synthetic: boolean
  /** 后端标记的「当前会话」（/api/session-tree 的 is_current）。 */
  isCurrent: boolean
  /** 父会话已不存在（/api/session-tree 的 orphan_subagents）。 */
  orphan: boolean
  /**
   * 是否子代理行（SessionInfo.type === 'agent'）。
   * 注意别用 CourtSession.type 判断：历史上 main 行也被映射成 type='agent'
   * （见下方 flatten 的映射），只有这个字段才是「是不是子代理」的真源。
   */
  isSubAgent: boolean
  depth: number
  historical: boolean
}

export interface CourtEvent extends GroupMessage {
  /** 消息所属会话（泳道 id）。 */
  laneID: string
  sessionKey: string
  sessionName: string
}

interface SessionTreePayload {
  sessions?: SessionInfo[]
  orphan_subagents?: SessionInfo[]
  has_more?: boolean
  next_offset?: number
}

/**
 * 后端 orphan_subagents 是**原始 snake_case 行**（RawChat），不是 SessionInfo。
 * 这里只保留组织树用得到的字段，避免为了一个展示列表把整套归一化逻辑再抄一遍。
 */
function orphanRowToSession(row: RawChat): SessionInfo {
  return {
    chatID: row.full_key || row.chat_id,
    channel: row.channel || 'agent',
    label: row.label || '',
    lastActive: row.last_active || '',
    preview: row.preview || '',
    status: row.status || (row.running ? 'running' : 'idle'),
    isCurrent: row.is_current === true,
    type: 'agent',
    fullKey: row.full_key,
    role: row.role,
    instance: row.instance,
    parentChatID: row.parent_chat_id,
    parentChannel: row.parent_channel,
    historical: true,
    running: row.running === true,
  }
}

function laneStatusOf(s: SessionInfo): Lane['status'] {
  if (s.historical) return 'left'
  if (s.status === 'waiting_input' || s.status === 'pending') return 'blocked'
  // 主会话的 running 只表示「会话活着」（WS 连着），不代表正在跑 turn ——
  // 拿它点黄灯会让整棵树永远是忙碌。只有子代理真正的 turn 才算 busy。
  if (s.type === 'agent' && (s.running || s.status === 'running')) return 'busy'
  return 'idle'
}

function laneIDOf(s: SessionInfo): string {
  const role = s.role || ''
  const instance = s.instance || 'main'
  if (s.type === 'agent' && role) return `agent:${role}/${instance}`
  // 主会话（用户 ↔ agent）没有逻辑地址，用物理 key 当泳道 id。
  return `session:${s.channel}:${s.chatID}`
}

/**
 * 把 /api/session-tree 的树拍平成展示用会话列表（保持父子关系）。
 * 同一 (role, instance) 可能在多棵子树下重复 ⇒ 泳道 id 加序号后缀去重。
 */
export function flattenSessionTree(payload: SessionTreePayload | null | undefined): CourtSession[] {
  const out: CourtSession[] = []
  const used = new Map<string, number>()

  const seenNodes = new Set<string>()

  const visit = (node: SessionInfo, depth: number, parentLaneID?: string, orphan = false) => {
    if (!node || typeof node.chatID !== 'string' || node.chatID === '') return
    // 同一个会话可能既挂在树里（父还在）又出现在 orphan_subagents（历史快照）里；
    // 树里的 chatID 可能被归一化成 full_key，孤儿行只有原始 chat_id ⇒ 优先用 fullKey 做键。
    const nodeKey = `${node.channel || ''}:${node.fullKey || node.chatID}`
    if (seenNodes.has(nodeKey)) return
    seenNodes.add(nodeKey)
    const base = laneIDOf(node)
    const seen = used.get(base) ?? 0
    used.set(base, seen + 1)
    const laneID = seen === 0 ? base : `${base}#${seen + 1}`
    const name = (node.role && node.type === 'agent')
      ? `${node.role}${node.instance && node.instance !== 'main' ? '/' + node.instance : ''}`
      : (node.label || node.chatID)
    out.push({
      key: `${node.channel}:${node.chatID}/${laneID}`,
      laneID,
      name,
      role: node.role || undefined,
      channel: node.channel || 'web',
      chatID: node.chatID,
      type: node.type === 'agent' ? 'agent' : (node.type === 'main' ? 'agent' : 'user'),
      status: laneStatusOf(node),
      running: node.running === true || node.status === 'running',
      lastActive: node.lastActive || '',
      parentLaneID,
      parentChatID: node.parentChatID,
      rawStatus: node.status || '',
      isCurrent: node.isCurrent === true,
      synthetic: node.synthetic === true,
      orphan,
      isSubAgent: node.type === 'agent',
      depth,
      historical: node.historical === true,
    })
    for (const child of node.children ?? []) visit(child, depth + 1, laneID, orphan)
  }

  for (const root of payload?.sessions ?? []) visit(root, 0)
  // 孤儿（父会话已不存在）**也必须进列表**，但标记 orphan ⇒ 默认折叠进「历史」，
  // 不能像旧实现那样静默丢弃（丢弃 = 用户看不到历史部门的最后状态）。
  for (const o of payload?.orphan_subagents ?? []) visit(o, 1, undefined, true)
  return out
}

/**
 * 拉 session-tree（后端只有 POST 路由；空 body 表示不分页、返回全量）。
 *
 * 后端返回的是 **snake_case** 原始行（chat_id / last_active / full_key /
 * children…），归一化交给 `useSessionStore.normalizeSessionTree` —— 侧栏用它
 * 渲染同一棵树，这里复用而不是抄一份（抄一份就会各自漂移）。
 */
export async function fetchSessionTree(signal?: AbortSignal): Promise<CourtSession[]> {
  const payload = await postAPI<ListSessionTreeResponse>('/api/session-tree', {}, { signal })
  const { mainSessions } = normalizeSessionTree(
    payload?.sessions ?? [],
    payload?.orphan_subagents ?? [],
  )
  // 挂在某棵子树下的子代理已在 `mainSessions.children` 里；父已失效的孤儿不在树里，
  // 必须用**原始 orphan_subagents** 补回来 —— 否则「历史部门」会凭空消失。
  return flattenSessionTree({
    sessions: mainSessions,
    orphan_subagents: (payload?.orphan_subagents ?? []).map(orphanRowToSession),
  })
}

/* ------------------------------------------------------------------ *
 * 组织树选择器（2026-10-23 P1 整改）
 *
 * 旧实现直接把 /api/session-tree 的全部 `Sessions` 拍平渲染，而该接口返回的是
 * **全部 Web 会话 + 孤儿历史子代理**（生产 1270 行）⇒ 左栏「1270 个部门/会话」，
 * 里面绝大多数是已销毁会话与 explore 一次性探索代理，真正的 8 个部门被埋掉。
 *
 * 现在只按「当前会话的直属子代理」出树：
 *   1. 直属判定：`parent_chat_id === 当前会话 chatID` 且 `status !== 'unloaded'`；
 *   2. 一次性 explore 代理、历史会话、孤儿代理 ⇒ 全部归入「历史」，默认折叠；
 *   3. 直属条目硬上限 ORG_VISIBLE_LIMIT（默认 20），超出的进「历史」并给出计数，
 *      **不静默丢弃**（丢弃 = 用户以为部门没了）。
 * ------------------------------------------------------------------ */

/** 默认可见的直属条目上限（验收口径：默认 ≤ 20）。 */
export const ORG_VISIBLE_LIMIT = 20

/** role 的命名空间：`court.gongbu` → `court`，`proj.xbot.dev` → `proj.xbot`。 */
export function roleNamespace(role: string): string {
  const parts = role.split('.')
  if (parts.length <= 2) return parts[0]
  return parts.slice(0, 2).join('.')
}

/** 一次性探索代理（explore / explore.xxx）：跑完即弃，默认不算「部门」。 */
export function isEphemeralRole(role?: string): boolean {
  return !!role && /^explore([./]|$)/.test(role)
}

export interface OrgTreeGroup {
  /** 分组键：子代理用 role，主会话用 `__session__`。 */
  key: string
  /** 展示名。 */
  label: string
  /** 组内泳道 id（按最近活跃降序）。 */
  laneIDs: string[]
}

export interface OrgTreeModel {
  /** 当前会话 chatID（定不出来时为 null ⇒ 调用方退回平铺模式）。 */
  currentChatID: string | null
  /** 当前会话本身（若有）。 */
  currentChat: CourtSession | null
  /** 直属子代理（已按上限截断）。 */
  direct: CourtSession[]
  /** 直属子代理按 role 分组。 */
  groups: OrgTreeGroup[]
  /** 因超出上限被折叠进历史的直属条目数。 */
  overflow: number
  /** 历史：其它会话、孤儿、一次性 explore、超限条目。 */
  history: CourtSession[]
  /** 需要拉结构化消息的会话：当前会话 + 直属子代理。 */
  scan: CourtSession[]
}

export interface BuildOrgTreeOptions {
  /** 显式指定当前会话 chatID（缺省用后端 is_current 标记）。 */
  currentChatID?: string | null
  /** 直属条目上限。 */
  limit?: number
  /** 历史参与消息扫描（默认 false：历史只用于展示，不再打后端）。 */
  scanHistory?: boolean
}

function byRecent(a: CourtSession, b: CourtSession): number {
  const ta = a.lastActive ? new Date(a.lastActive).getTime() : 0
  const tb = b.lastActive ? new Date(b.lastActive).getTime() : 0
  if (tb !== ta) return tb - ta
  return a.laneID < b.laneID ? -1 : a.laneID > b.laneID ? 1 : 0
}

/** 解析「当前会话」：优先显式传入，其次后端 is_current 的主会话，最后取第一个主会话。 */
export function resolveCurrentChatID(
  sessions: CourtSession[],
  explicit?: string | null,
): string | null {
  if (explicit) return explicit
  // 后端 is_current 也可能打在**子代理会话**上（通信视图开在某个部门的会话里），
  // 这时「当前会话」就是它自己，不能退回第一个主会话。
  const flagged = sessions.find((s) => s.isCurrent && !s.orphan)
  if (flagged) return flagged.chatID
  const main = sessions.filter((s) => !s.orphan && !s.isSubAgent)
  return main[0]?.chatID ?? null
}

/**
 * 把拍平的会话列表收敛成「当前会话 + 直属子代理 + 折叠历史」的组织树模型。
 * 纯函数：不碰网络，便于单测（生产形状的桩数据直接在测试里构造）。
 */
export function buildOrgTree(
  sessions: CourtSession[],
  opts: BuildOrgTreeOptions = {},
): OrgTreeModel {
  const limit = Math.max(1, opts.limit ?? ORG_VISIBLE_LIMIT)
  const currentChatID = resolveCurrentChatID(sessions, opts.currentChatID)
  const currentChat = currentChatID
    ? sessions.find((s) => s.chatID === currentChatID && !s.orphan) ?? null
    : null

  const direct: CourtSession[] = []
  const history: CourtSession[] = []

  for (const s of sessions) {
    if (s === currentChat) continue
    // unloaded = 已从内存卸载的僵尸子代理行：组织树一行都不给它（连历史也不进，
    // 否则「已销毁会话」会以另一种形式回到列表里）。僵尸行治理是另一条线。
    if (s.isSubAgent && s.rawStatus === 'unloaded') continue
    // 合成父行（父会话已删，后端为挂靠孤儿而造的占位会话）同样不是真会话。
    if (!s.isSubAgent && s.synthetic) continue
    const isDirectChild = !!currentChatID && s.parentChatID === currentChatID && s.isSubAgent
    if (isDirectChild && !s.historical && !isEphemeralRole(s.role)) {
      direct.push(s)
      continue
    }
    history.push(s)
  }

  direct.sort(byRecent)
  const kept = direct.slice(0, limit)
  const overflow = direct.length - kept.length
  if (overflow > 0) history.push(...direct.slice(limit))
  history.sort(byRecent)

  // 分组：按 role 的**命名空间**（court.* / proj.xbot.* …），主会话归到「会话」。
  // 不按完整 role 分组——生产里一个 role 通常只有一个实例，按完整 role 分就是
  // 「10 个组头 + 10 行」，组头反而成了噪音。
  const byKey = new Map<string, CourtSession[]>()
  for (const s of kept) {
    const key = s.isSubAgent && s.role ? roleNamespace(s.role) : '__session__'
    const list = byKey.get(key) ?? []
    list.push(s)
    byKey.set(key, list)
  }
  const groups: OrgTreeGroup[] = [...byKey.entries()]
    .map(([key, rows]) => ({
      key,
      label: key === '__session__' ? '会话' : key,
      laneIDs: rows.map((r) => r.laneID),
    }))
    .sort((a, b) => (a.key === '__session__' ? -1 : b.key === '__session__' ? 1 : a.key < b.key ? -1 : a.key > b.key ? 1 : 0))

  const scan: CourtSession[] = []
  if (currentChat) scan.push(currentChat)
  scan.push(...kept)
  if (opts.scanHistory) scan.push(...history)

  return { currentChatID, currentChat, direct: kept, groups, overflow, history, scan }
}

/** 从 payload 里解析可识别的目标泳道（存在才画弧线，否则是泳道内自环事件）。 */
function targetsOf(payload: Record<string, unknown> | undefined, known: Set<string>): string[] {
  if (!payload) return []
  const raw: string[] = []
  const push = (v: unknown) => {
    if (typeof v === 'string' && v) raw.push(v)
    if (Array.isArray(v)) for (const x of v) if (typeof x === 'string' && x) raw.push(x)
  }
  push(payload.to)
  push(payload.target)
  push(payload.department)
  const out: string[] = []
  for (const addr of raw) {
    const normalized = addr.includes('/') || addr.includes(':') ? addr : `agent:${addr}/main`
    const hit = [normalized, `agent:${addr}`, addr].find((c) => known.has(c))
    if (hit && !out.includes(hit)) out.push(hit)
  }
  return out
}

function summaryOf(kind: string, payload: Record<string, unknown> | undefined, text: string): string {
  if (text) return text
  if (!payload) return kind
  const p = payload
  if (kind === 'report') {
    const items = Array.isArray(p.items) ? p.items.length : 0
    const bits = [
      typeof p.summary === 'string' ? p.summary : '',
      items ? `${items} 项` : '',
      p.needs_approval === true ? '待批准' : '',
      Array.isArray(p.blocked) && p.blocked.length ? `${p.blocked.length} 项阻塞` : '',
    ].filter(Boolean)
    return bits.join(' · ') || '汇报'
  }
  if (kind === 'decision') {
    const verdict = typeof p.verdict === 'string' ? p.verdict : ''
    const label = verdict === 'approve' ? '准' : verdict === 'reject' ? '驳' : verdict === 'modify' ? '改' : verdict
    const comment = typeof p.comment === 'string' ? p.comment : (typeof p.comments === 'string' ? p.comments : '')
    return [label, comment].filter(Boolean).join('：') || '决策'
  }
  if (kind === 'ask_user') {
    const q = Array.isArray(p.questions) ? p.questions[0] : undefined
    if (q && typeof q === 'object' && typeof (q as { question?: string }).question === 'string') {
      return (q as { question: string }).question
    }
    return typeof p.question === 'string' ? p.question : '提问'
  }
  return kind
}

/** 有界并发 map：网络请求按 CONCURRENCY 分批，避免几十个会话同时打后端。 */
async function mapLimit<T, R>(items: T[], limit: number, fn: (item: T) => Promise<R>): Promise<R[]> {
  const out: R[] = new Array(items.length)
  let cursor = 0
  const worker = async () => {
    for (;;) {
      const i = cursor++
      if (i >= items.length) return
      out[i] = await fn(items[i])
    }
  }
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, worker))
  return out
}

/**
 * 拉所有会话的结构化消息。
 * 单个会话失败（404 未知会话 / 403 无权限 / 网络）只跳过它，不拖垮整页。
 */
export async function fetchCourtEvents(
  sessions: CourtSession[],
  signal?: AbortSignal,
): Promise<{ events: CourtEvent[]; failed: number; scanned: number }> {
  const targets = sessions.filter((s) => !s.historical).slice(0, MAX_SESSIONS)
  const known = new Set(sessions.map((s) => s.laneID))
  let failed = 0

  const batches = await mapLimit(targets, CONCURRENCY, async (session) => {
    try {
      const page = await fetchStructuredMessages(
        { channel: session.channel, chat_id: session.chatID },
        { limit: PER_SESSION_LIMIT, signal },
      )
      return page.items
        .filter((m) => STRUCTURED_KINDS.includes(m.kind))
        .map((m) => ({
          message_id: m.message_id,
          kind: m.kind,
          correlation_id: m.correlation_id,
          answered: m.answered,
          cancelled: m.cancelled,
          text: m.text,
          payload: m.payload,
          created_at: m.created_at,
          laneID: session.laneID,
          sessionKey: session.key,
          sessionName: session.name,
          // 来源标识：通信视图里每条事件都要能说出"这是哪个会话/部门发的"。
          sourceChannel: session.channel,
          sourceChatID: session.chatID,
          sourceRole: session.role,
        }))
    } catch {
      failed += 1
      return []
    }
  })

  const raw = batches.flat().sort((a, b) => {
    const ta = new Date(a.created_at).getTime()
    const tb = new Date(b.created_at).getTime()
    if (ta !== tb) return ta - tb
    return a.message_id - b.message_id
  })

  // id 必须全局唯一：每条会话里的 message_id 各自从 1 开始，直接用它会让不同
  // 会话的同号行在选中/去重/束带聚合（`m.id % 7`）时互相污染 ⇒ 这里按时间重编。
  const events: CourtEvent[] = raw.map((m, i) => ({
    id: i + 1,
    group_id: m.sessionKey,
    kind: m.kind,
    correlation_id: m.correlation_id,
    // senderAddr() 只给 `type=agent` 的 id 补 `agent:` 前缀；主会话泳道 id 形如
    // `session:web:chat_x`，必须标 system，否则会被拼成 `agent:session:…` 而失配。
    sender_type: m.laneID.startsWith('agent:') ? 'agent' : 'system',
    sender_id: m.laneID,
    sender_name: m.sessionName,
    role: 'assistant',
    content: summaryOf(m.kind, m.payload, m.text),
    to: targetsOf(m.payload, known).filter((t) => t !== m.laneID),
    payload: m.payload,
    created_at: m.created_at,
    answered: m.answered,
    laneID: m.laneID,
    sessionKey: m.sessionKey,
    sessionName: m.sessionName,
    sourceChannel: m.sourceChannel,
    sourceChatID: m.sourceChatID,
    sourceRole: m.sourceRole,
  }))

  return { events, failed, scanned: targets.length }
}

/** 由会话（+ 事件）推导泳道：状态灯、最近事件、线程闭环进度。 */
export function lanesFromSessions(sessions: CourtSession[], events: CourtEvent[]): Lane[] {
  const lastByLane = new Map<string, CourtEvent>()
  const pendingByLane = new Map<string, number>()
  const threadsByLane = new Map<string, Set<string>>()
  const closedByLane = new Map<string, Set<string>>()
  const answeredCorr = new Set<string>()

  for (const e of events) {
    if (e.correlation_id && (e.kind === 'answer' || e.kind === 'decision')) answeredCorr.add(e.correlation_id)
  }
  for (const e of events) {
    lastByLane.set(e.laneID, e)
    if (e.correlation_id) {
      const set = threadsByLane.get(e.laneID) ?? new Set<string>()
      set.add(e.correlation_id)
      threadsByLane.set(e.laneID, set)
      if (answeredCorr.has(e.correlation_id)) {
        const closed = closedByLane.get(e.laneID) ?? new Set<string>()
        closed.add(e.correlation_id)
        closedByLane.set(e.laneID, closed)
      }
    }
    if (e.kind === 'ask_user' && !answeredCorr.has(e.correlation_id ?? '')) {
      pendingByLane.set(e.laneID, (pendingByLane.get(e.laneID) ?? 0) + 1)
      for (const to of e.to ?? []) pendingByLane.set(to, (pendingByLane.get(to) ?? 0) + 1)
    }
  }

  return sessions.map((s) => {
    const last = lastByLane.get(s.laneID)
    const threads = threadsByLane.get(s.laneID)?.size ?? 0
    const closed = closedByLane.get(s.laneID)?.size ?? 0
    const progress = threads > 0 ? Math.min(1, 0.35 + (closed / threads) * 0.65) : (s.running ? 0.55 : 0.3)
    const pending = pendingByLane.get(s.laneID) ?? 0
    const status: Lane['status'] = s.status === 'left'
      ? 'left'
      : pending > 0
        ? 'blocked'
        : s.status
    return {
      id: s.laneID,
      name: s.name,
      type: s.type === 'agent' ? 'agent' : (s.type === 'user' ? 'user' : 'agent'),
      status,
      task: last ? `最近事件 · ${last.content.slice(0, 60)}` : '暂无结构化消息',
      progress,
      depth: s.depth,
      parentID: s.parentLaneID,
    } satisfies Lane
  })
}
