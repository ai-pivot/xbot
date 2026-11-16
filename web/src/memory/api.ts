/**
 * memory/api.ts —— 长期记忆面板的数据访问。
 *
 * 与 `memory_*` 工具同一套后端（XbotMemory.ListMemories / UpdateMemory /
 * DeleteMemory），所以写入闸门（长度 / 注入模式 / 协议控制标签 / importance
 * 钳制 / 去重）在面板里同样生效。
 */
import { backendFetch, postAPI } from '@/lib/api'

export interface MemoryItem {
  id: number
  type: string
  content: string
  keywords: string
  tags: string
  importance: number
  scope: string
  created_at: string
}

export interface MemoryScope {
  channel: string
  chat_id: string
}

function scopeQuery(scope: MemoryScope): Record<string, string> {
  return { channel: scope.channel, chat_id: scope.chat_id }
}

export async function fetchMemories(
  scope: MemoryScope,
  opts: { type?: string; limit?: number; signal?: AbortSignal } = {},
): Promise<MemoryItem[]> {
  const q = new URLSearchParams(scopeQuery(scope))
  q.set('type', opts.type ?? 'all')
  q.set('limit', String(opts.limit ?? 100))
  const res = await backendFetch(`/api/memory?${q.toString()}`, { signal: opts.signal })
  const body = (await res.json().catch(() => null)) as
    | { ok?: boolean; items?: MemoryItem[]; error?: { message?: string } }
    | null
  if (!res.ok || !body?.ok) {
    throw new Error(body?.error?.message ?? `加载记忆失败 (HTTP ${res.status})`)
  }
  return body.items ?? []
}

export async function updateMemoryItem(
  scope: MemoryScope,
  input: {
    id: number
    content: string
    keywords: string
    tags: string
    importance: number
  },
): Promise<void> {
  await postAPI('/api/memory/update', { ...scopeQuery(scope), ...input })
}

export async function deleteMemoryItem(scope: MemoryScope, id: number): Promise<void> {
  await postAPI('/api/memory/delete', { ...scopeQuery(scope), id })
}
