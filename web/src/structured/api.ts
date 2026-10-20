import { APIError, postAPI } from '@/lib/api'
import type { InboxResult, StructuredAnswerInput, StructuredMessage } from './types'

/** 统一读取 {ok,data,error} 信封的 GET。 */
async function getAPI<T>(endpoint: string, signal?: AbortSignal): Promise<T> {
  const response = await fetch(endpoint, { headers: { Accept: 'application/json' }, signal })
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
