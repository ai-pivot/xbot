/** 通信视图数据结构（与 Go 侧 protocol.Group* 一一对应）。 */

export type MessageKind =
  | 'ask_user'
  | 'answer'
  | 'report'
  | 'decision'
  | 'action'
  | 'text'

export interface Group {
  id: string
  name: string
  owner: string
  created_at: string
  member_count: number
}

export interface GroupMember {
  group_id: string
  member_addr: string
  status: 'alive' | 'left' | string
  joined_at: string
  name?: string
  role?: string
  instance?: string
}

export interface GroupMessage {
  id: number
  group_id: string
  kind: MessageKind | string
  correlation_id?: string
  sender_type: 'agent' | 'user' | 'system' | string
  sender_id: string
  sender_name: string
  role: string
  content: string
  to?: string[]
  payload?: Record<string, unknown>
  created_at: string
  /** ask_user 是否已有 answer/decision 闭环（结构化消息投影）。 */
  answered?: boolean
  /** 消息所属会话的展示名（结构化消息按会话归属；群组消息为空）。 */
  session?: string
  /**
   * 消息落在哪个会话（泳道）里。活动流/线程视图用它判定「这条事件属于哪个部门」：
   * 真实数据由 sources.ts 按会话填；示例数据由 seed.ts 按议题所属部门填。
   */
  laneID?: string
  /** 来源渠道（web / agent / cli …）。结构化消息才有。 */
  sourceChannel?: string
  /** 来源会话 id（完整值 —— 展示用 `shortSessionID()` 截断，tooltip 用完整值）。 */
  sourceChatID?: string
  /** 来源角色名（子代理才有，如 `court.hubu`）。 */
  sourceRole?: string
}

export interface MessagesPage {
  ok: boolean
  items: GroupMessage[]
  next_cursor: number
  has_more: boolean
  channel?: string
  chat_id?: string
}

export interface GroupMessageEvent {
  group_id: string
  message: GroupMessage
}

export interface Lane {
  id: string
  name: string
  type: 'user' | 'agent' | 'system'
  status: 'idle' | 'busy' | 'blocked' | 'left'
  task: string
  progress: number
  /** 组织树缩进层级（0 = 顶层会话）。 */
  depth?: number
  /** 父泳道 id：组织树按它画连线。 */
  parentID?: string
}

/**
 * kind 颜色统一走 CSS 变量（见 index.css 的 --kind-*）：主题切换时自动跟随，
 * 组件里不再出现任何硬编码色值。canvas 用 palette.ts 解析成具体色值。
 */
export const KIND_COLORS: Record<string, string> = {
  ask_user: 'var(--kind-ask-user)',
  answer: 'var(--kind-answer)',
  report: 'var(--kind-report)',
  decision: 'var(--kind-decision)',
  command: 'var(--kind-command)',
  event: 'var(--kind-event)',
  action: 'var(--kind-command)',
  text: 'var(--kind-text)',
}

/** 半透明底色：`color-mix` 让 var() 也能参与 alpha 计算（旧实现是 `${hex}22`）。 */
export function kindTint(kind: string, percent = 14): string {
  return `color-mix(in srgb, ${KIND_COLORS[kind] ?? KIND_COLORS.text} ${percent}%, transparent)`
}

/** 弧线权重：关键决策/汇报明显更粗，text 最细且低透明度。 */
export const KIND_WEIGHT: Record<string, number> = {
  ask_user: 1.8,
  answer: 1.8,
  report: 2.5,
  decision: 2.5,
  command: 1.8,
  event: 1.6,
  action: 1.6,
  text: 1.0,
}

export const KIND_LABELS: Record<string, string> = {
  ask_user: '提问',
  answer: '回答',
  report: '汇报',
  decision: '决策',
  command: '指令',
  event: '事件',
  action: '动作',
  text: '消息',
}

/**
 * 会话/部门状态色。
 *
 * 2026-10-22 用户截图报的图例 bug：`灰=空闲` 与 `灰=已离开` 同为灰、`忙碌` 又
 * 借用 `--status-waiting`（黄）与 `等待处理` 撞色 —— 四个状态只剩两种观感。
 * 现在四个状态各自一个可区分色相（青灰 / 琥珀 / 红 / 绿），并走通信视图专用的
 * `--lane-*` 变量，不去改全局 `--status-*` 的 UI 语义。
 */
export const STATUS_COLORS: Record<string, string> = {
  idle: 'var(--lane-idle)',
  busy: 'var(--lane-busy)',
  blocked: 'var(--lane-waiting)',
  done: 'var(--lane-done)',
  left: 'var(--lane-left)',
}

export const STATUS_LABELS: Record<'idle' | 'busy' | 'blocked' | 'done', string> = {
  idle: '空闲',
  busy: '忙碌',
  blocked: '等待处理',
  done: '已完成',
}

/** 图例里展示的状态（「已离开」在时间窗口内几乎无意义，默认不列）。 */
export const STATUS_LEGEND: { key: keyof typeof STATUS_LABELS; label: string }[] = [
  { key: 'idle', label: '空闲' },
  { key: 'busy', label: '忙碌' },
  { key: 'blocked', label: '等待处理' },
  { key: 'done', label: '已完成' },
]

/** 状态灯颜色：未识别的状态一律按空闲处理（不能显示成透明）。 */
export function laneStatusColor(status: string | undefined): string {
  return STATUS_COLORS[status ?? 'idle'] ?? STATUS_COLORS.idle
}

/** 类型顺序（图例 / 筛选条固定顺序，不随数据抖动）。 */
export const KIND_ORDER = ['ask_user', 'report', 'decision', 'command', 'answer', 'event'] as const

/** 从 member_addr 派生泳道显示名。 */
export function memberDisplayName(m: GroupMember): string {
  if (m.name && m.name !== m.member_addr) return m.name
  const addr = m.member_addr
  if (addr.startsWith('agent:')) {
    const rest = addr.slice('agent:'.length)
    const [role, instance] = rest.split('/')
    return instance ? `${role}/${instance}` : role
  }
  return addr
}

/** 发送者地址归一化：后端 sender_id 可能是 `data/main`，成员表是 `agent:data/main`。 */
export function senderAddr(msg: GroupMessage): string {
  if (msg.sender_type === 'agent' && !msg.sender_id.startsWith('agent:')) {
    return `agent:${msg.sender_id}`
  }
  return msg.sender_id
}

/* ── 来源标识（2026-10-22：通信视图必须能看出事件属于哪个会话）──────────── */

export interface MessageSource {
  /** 渠道：web / agent / cli … */
  channel: string
  /** 会话 id 短名（`…a8f3`）。 */
  short: string
  /** 角色名（子代理才有）。 */
  role?: string
  /** 完整会话 key（tooltip / 复制用）。 */
  key: string
}

/** 来源缺失时的展示文案（不能静默：仍然渲染，只是标成未知）。 */
export const UNKNOWN_SOURCE = '未知来源'

/** 会话 id → 短名：尾部 4 位，前面加省略号（`chat_9f3a…` → `…a8f3`）。 */
export function shortSessionID(id: string | undefined | null): string {
  const v = (id ?? '').trim()
  if (!v) return ''
  if (v.length <= 8) return v
  return `…${v.slice(-4)}`
}

/** 从消息里取出来源；没有来源信息返回 null（调用方显示 `UNKNOWN_SOURCE`）。 */
export function messageSource(m: GroupMessage): MessageSource | null {
  const channel = (m.sourceChannel ?? '').trim()
  const chatID = (m.sourceChatID ?? '').trim()
  if (!channel && !chatID) return null
  return {
    channel: channel || '未知渠道',
    short: shortSessionID(chatID) || '…',
    role: (m.sourceRole ?? '').trim() || undefined,
    key: `${channel || '?'}:${chatID || '?'}`,
  }
}

/** 一行式来源标签：`web·…a8f3` 或 `agent·…a8f3·court.hubu`。 */
export function sourceLabel(m: GroupMessage): string {
  const src = messageSource(m)
  if (!src) return UNKNOWN_SOURCE
  return [src.channel, src.short, src.role].filter(Boolean).join('·')
}
