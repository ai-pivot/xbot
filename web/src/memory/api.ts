/**
 * memory/api.ts —— 长期记忆面板的数据访问。
 *
 * 与 `memory_*` 工具同一套后端（XbotMemory.ListMemories / UpdateMemory /
 * DeleteMemory），所以写入闸门（长度 / 注入模式 / 协议控制标签 / importance
 * 钳制 / 去重）在面板里同样生效。
 */
import { backendFetch, postAPI, readEnvelope } from '@/lib/api'

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
  // 服务端统一用 {ok, data, error} 信封：列表在 data.items 里，不是顶层 items。
  const env = await readEnvelope<{ items?: MemoryItem[] } | MemoryItem[]>(res)
  if (!res.ok || !env.ok) {
    throw new Error(env.error?.message ?? `加载记忆失败 (HTTP ${res.status})`)
  }
  const payload = env.data
  // 兼容两种历史形态：{items:[...]} 与直接数组。
  if (Array.isArray(payload)) return payload
  return payload?.items ?? []
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
