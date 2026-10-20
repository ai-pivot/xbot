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
}

export const KIND_COLORS: Record<string, string> = {
  ask_user: '#fbbf24',
  answer: '#4ade80',
  report: '#38bdf8',
  decision: '#c084fc',
  action: '#f472b6',
  text: '#cbd5e1',
}

/** 弧线权重：关键决策/汇报明显更粗，text 最细且低透明度。 */
export const KIND_WEIGHT: Record<string, number> = {
  ask_user: 1.8,
  answer: 1.8,
  report: 2.5,
  decision: 2.5,
  action: 1.6,
  text: 1.0,
}

export const KIND_LABELS: Record<string, string> = {
  ask_user: '提问',
  answer: '回答',
  report: '汇报',
  decision: '决策',
  action: '动作',
  text: '消息',
}

export const STATUS_COLORS: Record<string, string> = {
  idle: '#4ade80',
  busy: '#fbbf24',
  blocked: '#f87171',
  left: '#94a3b8',
}

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
