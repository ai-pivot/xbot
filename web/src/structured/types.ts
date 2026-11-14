/**
 * 结构化消息类型（mirrors protocol/structured.go）。
 */
export type StructuredKind =
  | 'text'
  | 'ask_user'
  | 'answer'
  | 'report'
  | 'decision'
  | 'command'
  | 'event'

export interface StructuredSender {
  type: string
  id: string
  name?: string
}

export interface StructuredMessage {
  message_id: number
  kind: StructuredKind
  correlation_id?: string
  reply_to?: string
  direction: 'in' | 'out'
  answered: boolean
  cancelled: boolean
  text: string
  payload?: Record<string, unknown>
  sender: StructuredSender
  created_at: string
  envelope?: Record<string, unknown>
}

export interface AskOption {
  id: string
  label: string
  description?: string
}

export interface AskQuestion {
  id: string
  question: string
  options?: AskOption[]
  multi_select?: boolean
  allow_other?: boolean
  required?: boolean
}

export interface AskPayload {
  questions: AskQuestion[]
  allow_cancel?: boolean
  context_refs?: string[]
}

export interface ReportEvidence {
  path: string
  kind?: string
  sha256?: string
}

export interface ReportPayload {
  report_id?: string
  department?: string
  summary?: string
  state_ref?: string
  state_sha256?: string
  evidence?: ReportEvidence[]
  blockers?: { id: string; desc: string }[]
  asks?: AskQuestion[]
}

export interface DecisionPayload {
  decision_id?: string
  target_report_id?: string
  target_message_id?: string
  verdict: 'approve' | 'reject' | 'modify' | string
  comments?: string
  modifications?: unknown[]
}

export interface StructuredAnswerInput {
  question_id: string
  option_ids?: string[]
  text?: string
}

export interface InboxResult {
  items: StructuredMessage[]
  has_more: boolean
  next_cursor: number
  unanswered: number
}

/** 用户看板卡片的四个动作（与后端 NoticeAction{Approve,Reject,Answer,Dismiss} 一致）。 */
export type InboxResolveAction = 'approve' | 'reject' | 'answer' | 'dismiss'

/**
 * POST /api/inbox/resolve 的结果（{ok,data} 已由 postAPI 解包）。
 *
 * duplicate=true 表示**幂等命中**：服务端早已把这张卡推进到终态，本次没有再次
 * 流转（不会重复回流给发起 agent）。前端据此决定是否扣减未处理数 —— 见 inboxStore。
 */
export interface InboxResolveResult {
  message_id: number
  duplicate: boolean
  status?: string
  message?: StructuredMessage
}
