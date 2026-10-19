/**
 * 指挥室数据层：真实 API + SSE 实时事件 + 演示数据兜底。
 *
 * 真实接口（见 channel/web/web_group.go）：
 *   GET /api/groups
 *   GET /api/groups/{id}/members
 *   GET /api/groups/{id}/messages?cursor=&limit=&channel=&chat_id=
 *   POST /api/groups（演示建群）
 *   POST /api/groups/{id}/messages（演示写入 + group.message）
 */
import type { Group, GroupMember, GroupMessage, MessagesPage } from './types'

export interface CourtSnapshot {
  groups: Group[]
  members: GroupMember[]
  messages: GroupMessage[]
  hasMore: boolean
  nextCursor: number
  source: 'api' | 'seed'
  error?: string
}

async function getJSON<T>(url: string, signal?: AbortSignal): Promise<T> {
  const res = await fetch(url, { credentials: 'same-origin', signal, headers: { Accept: 'application/json' } })
  const body = await res.json().catch(() => null) as
    ({ ok?: boolean; data?: T; error?: { code?: string; message?: string } | string }) | null
  if (!res.ok) {
    const raw = body?.error
    const msg = typeof raw === 'string' ? raw : raw?.message
    throw new Error(msg || `HTTP ${res.status}`)
  }
  // 统一响应信封：{ok, data, error}（writeJSON）。兼容旧式扁平响应。
  if (body && typeof body === 'object' && 'data' in body && body.data != null) return body.data
  return body as T
}

export async function fetchGroups(signal?: AbortSignal): Promise<Group[]> {
  const body = await getJSON<{ ok: boolean; groups: Group[] }>('/api/groups', signal)
  return body.groups ?? []
}

export async function fetchMembers(groupID: string, signal?: AbortSignal): Promise<GroupMember[]> {
  const body = await getJSON<{ ok: boolean; members: GroupMember[] }>(
    `/api/groups/${encodeURIComponent(groupID)}/members`, signal)
  return body.members ?? []
}

export async function fetchMessages(
  groupID: string,
  opts: { cursor?: number; limit?: number; channel?: string; chatID?: string } = {},
  signal?: AbortSignal,
): Promise<MessagesPage> {
  const q = new URLSearchParams()
  if (opts.cursor) q.set('cursor', String(opts.cursor))
  q.set('limit', String(opts.limit ?? 100))
  if (opts.channel) q.set('channel', opts.channel)
  if (opts.chatID) q.set('chat_id', opts.chatID)
  return getJSON<MessagesPage>(`/api/groups/${encodeURIComponent(groupID)}/messages?${q.toString()}`, signal)
}

/** 订阅 group.message（复用现有 SSE 通道）。返回取消函数。 */
export function subscribeGroupMessages(
  chatID: string,
  onMessage: (msg: GroupMessage) => void,
): () => void {
  if (typeof EventSource === 'undefined') return () => {}
  const params = new URLSearchParams({ chat_id: chatID, channel: 'web' })
  const source = new EventSource(`/api/sse?${params.toString()}`)
  const handler = (ev: MessageEvent) => {
    try {
      const payload = JSON.parse(ev.data) as { result?: { group_id?: string; message?: GroupMessage } }
      if (payload.result?.message) onMessage(payload.result.message)
    } catch {
      // 非 JSON / 其他事件：忽略，不影响时间线。
    }
  }
  source.addEventListener('group.message', handler)
  return () => {
    source.removeEventListener('group.message', handler)
    source.close()
  }
}
