import { APIError, postAPI } from '@/lib/api'
import type {
  InboxResolveAction,
  InboxResolveResult,
  InboxResult,
  StructuredAnswerInput,
  StructuredMessage,
} from './types'

/** 统一读取 {ok,data,error} 信封的 GET。 */
async function getAPI<T>(endpoint: string, signal?: AbortSignal): Promise<T> {
  // no-store：收件箱/消息流是实时数据，浏览器 HTTP 缓存会把"刚答完还在"的旧结果
  // 协议式地喂回来（实测：答完后卡片还挂着）。
  const response = await fetch(endpoint, { headers: { Accept: 'application/json' }, signal, cache: 'no-store' })
  type Envelope = { ok?: boolean; data?: T; error?: { code?: string; message?: string } }
  let envelope: Envelope
  try {
    envelope = (await response.json()) as Envelope
  } catch {
    envelope = {}
  }
  if (!response.ok || !envelope.ok) {
    throw new APIError(
      envelope.error?.message || `request failed with status ${response.status}`,
      envelope.error?.code || 'invalid_response',
      response.status,
    )
  }
  return envelope.data as T
}

export interface StructuredScope {
  channel?: string
  chat_id?: string
}

function scopeQuery(scope: StructuredScope): string {
  const q = new URLSearchParams()
  if (scope.channel) q.set('channel', scope.channel)
  if (scope.chat_id) q.set('chat_id', scope.chat_id)
  return q.toString()
}

export async function fetchStructuredMessages(
  scope: StructuredScope,
  opts: { kind?: string; cursor?: number; after_id?: number; limit?: number; signal?: AbortSignal } = {},
): Promise<{ items: StructuredMessage[]; has_more: boolean; next_cursor: number }> {
  const q = new URLSearchParams(scopeQuery(scope))
  if (opts.kind) q.set('kind', opts.kind)
  if (opts.after_id && opts.after_id > 0) q.set('after_id', String(opts.after_id))
  q.set('cursor', String(opts.cursor ?? 0))
  q.set('limit', String(opts.limit ?? 200))
  return getAPI(`/api/messages?${q.toString()}`, opts.signal)
}

export async function fetchInbox(
  scope: StructuredScope,
  opts: { cursor?: number; limit?: number; signal?: AbortSignal } = {},
): Promise<InboxResult> {
  const q = new URLSearchParams(scopeQuery(scope))
  q.set('cursor', String(opts.cursor ?? 0))
  q.set('limit', String(opts.limit ?? 50))
  return getAPI(`/api/inbox?${q.toString()}`, opts.signal)
}

export interface AppendInput {
  kind: string
  payload?: Record<string, unknown>
  correlation_id?: string
  reply_to?: string
  idempotency_key?: string
  to?: string
  text?: string
  channel?: string
  chat_id?: string
}

export async function appendStructuredMessage(
  input: AppendInput,
): Promise<{ message_id: number; duplicate: boolean; message: StructuredMessage }> {
  return postAPI('/api/message', input)
}

export async function answerStructuredMessage(input: {
  correlation_id: string
  answers?: StructuredAnswerInput[]
  cancelled?: boolean
  idempotency_key?: string
  channel?: string
  chat_id?: string
}): Promise<{ message_id: number; duplicate: boolean; message: StructuredMessage }> {
  return postAPI('/api/answer', input)
}

/**
 * 用户看板卡片的处理入口（N1：状态流转的唯一通道）。
 *
 * 与 /api/answer、/api/message(kind=decision) 的区别：这两个是 **agent 侧**的
 * 回流通道（靠 correlation_id / reply_to 寻址）；用户在看板上处理 to="user"
 * 卡片时服务端要跑 CAS（pending → resolved）+ 幂等键 + 向 origin 会话回流，
 * 语义都封装在 /api/inbox/resolve 里。前端只发一张卡 + 一个动作。
 *
 * 幂等边界（服务端保证，前端只用它决定要不要扣数字）：
 * - 同 action 重复提交 → 200 + duplicate=true（不重复回流）；
 * - 已处理但换了 action/idempotency_key → 409 conflict。
 */
export async function resolveInboxItem(input: {
  message_id: number
  action: InboxResolveAction
  comment?: string
  idempotency_key?: string
  channel?: string
  chat_id?: string
}): Promise<InboxResolveResult> {
  // 后端 handler 写的是 `{"ok":true,"result":<StructuredResolveResult>}`，
  // 经 writeJSON 信封化后 = `{ok, data:{result:{...}}}` —— 结果在 **data.result**，
  // 不是 data 本体。直接读 data.duplicate 会永远 undefined，
  // 于是「首次/幂等」判不开，重复点会重复扣角标（N1 ③ 要防的正是这个）。
  const raw = await postAPI<InboxResolveResult & { result?: InboxResolveResult }>(
    '/api/inbox/resolve',
    input,
  )
  return raw.result ?? raw
}
