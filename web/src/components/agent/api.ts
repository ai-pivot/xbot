/**
 * HTTP API client for the Agent workspace (Spec 4).
 *
 * History and Web-only session metadata are fetched through Web REST APIs so
 * shared RPC contracts stay aligned with non-Web clients. File upload remains
 * a multipart POST.
 *
 * LLM subscription/model RPCs (Spec D) go through WSConnection.rpc → POST /api/rpc.
 */
import type { WSConnection } from '@/types/ws'
import type { SessionSelector, Subscription, ModelEntry, PerModelConfig } from '@/types/shared'

/** History message row (protocol.HistoryMessage). */
export interface HistMsg {
  role: 'user' | 'assistant'
  content: string
  timestamp?: string
  iterations?: unknown[]
}

/** Raw active-progress snapshot (protocol.ProgressEvent). */
export interface HistProgress {
  phase?: string
  iteration?: number
  thinking?: string
  active_tools?: unknown[]
  completed_tools?: unknown[]
  sub_agents?: unknown[]
  stream_content?: string
  /** Total wall-clock of the active turn (ms). */
  elapsed_wall?: number
  iteration_history?: unknown[]
  todos?: { id: number; text: string; done: boolean }[]
}

/** /api/history response. */
export interface HistoryResponse {
  ok?: boolean
  messages?: HistMsg[]
  processing?: boolean
  active_progress?: HistProgress | null
  last_seq?: number
  chat_id?: string
  channel?: string
}

/** Upload response (channel/web/web_file.go handleCloudUpload). */
export interface UploadResponse {
  ok?: boolean
  upload_key?: string
  name?: string
  size?: number
  mime?: string
  error?: string
}

function sessionQuery(session?: SessionSelector | null): string {
  if (!session) return ''
  const q = new URLSearchParams()
  q.set('channel', session.channel)
  q.set('chat_id', session.chatID)
  return `?${q.toString()}`
}

async function getJSON<T>(url: string): Promise<T> {
  const res = await fetch(url, { headers: { Accept: 'application/json' } })
  const data = (await res.json().catch(() => ({}))) as T & { error?: string }
  if (!res.ok) throw new Error(data?.error || `request ${res.status}`)
  return data
}

async function sendJSON<T>(url: string, method: string, body: unknown): Promise<T> {
  const res = await fetch(url, {
    method,
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(body),
  })
  const data = (await res.json().catch(() => ({}))) as T & { error?: string }
  if (!res.ok) throw new Error(data?.error || `request ${res.status}`)
  return data
}

/** Fetch conversation history through the Web-only snapshot API. */
export async function fetchHistory(_ws: WSConnection, session?: SessionSelector | null): Promise<HistoryResponse> {
  return getJSON<HistoryResponse>(`/api/history${sessionQuery(session)}`)
}

export async function fetchCwd(session?: SessionSelector | null): Promise<{ dir?: string }> {
  return getJSON<{ dir?: string }>(`/api/cwd${sessionQuery(session)}`)
}

export async function setCwd(session: SessionSelector, dir: string): Promise<{ dir?: string }> {
  return sendJSON<{ dir?: string }>('/api/cwd', 'PUT', {
    channel: session.channel,
    chat_id: session.chatID,
    dir,
  })
}

export async function fetchCronTasks<T>(session: SessionSelector): Promise<T[]> {
  const data = await getJSON<{ tasks?: T[] }>(`/api/tasks${sessionQuery(session)}`)
  return data.tasks ?? []
}

export async function fetchBackgroundTasks<T>(session: SessionSelector): Promise<T[]> {
  const data = await getJSON<{ tasks?: T[] }>(`/api/background-tasks${sessionQuery(session)}`)
  return data.tasks ?? []
}

export async function fetchCommands<T>(): Promise<T[]> {
  const data = await getJSON<{ commands?: T[] }>('/api/commands')
  return data.commands ?? []
}

export async function fetchSessionSubscription(session: SessionSelector): Promise<Record<string, string>> {
  return getJSON<Record<string, string>>(`/api/session-subscription${sessionQuery(session)}`)
}

export async function rewindHistory<T>(session: SessionSelector, cutoffMS: number): Promise<T> {
  return sendJSON<T>('/api/history/rewind', 'POST', {
    channel: session.channel,
    chat_id: session.chatID,
    cutoff_ms: cutoffMS,
  })
}

/** Upload a single file; returns the server-issued upload key + metadata. */
export async function uploadFile(file: File): Promise<UploadResponse> {
  const form = new FormData()
  form.append('file', file)
  const res = await fetch('/api/files/upload', { method: 'POST', body: form })
  const data = (await res.json().catch(() => ({}))) as UploadResponse
  if (!res.ok || !data.ok || !data.upload_key) {
    throw new Error(data?.error || `upload ${res.status}`)
  }
  return data
}

/* ---------------------------------------------------------------------------
 * LLM Subscription & Model Management RPCs (Spec D — LLM 配置设计).
 *
 * All calls go through WSConnection.rpc → POST /api/rpc. The backend resolves
 * sender_id from the auth context, so we never pass it in params.
 * ------------------------------------------------------------------------- */

// ── Subscription CRUD ──

export async function listSubscriptions(ws: WSConnection): Promise<Subscription[]> {
  return ws.rpc<Subscription[]>('list_subscriptions', {})
}

export async function addSubscription(
  ws: WSConnection,
  sub: {
    name: string
    provider: string
    base_url: string
    api_key: string
    model: string
    active?: boolean
  },
): Promise<void> {
  await ws.rpc('add_subscription', {
    sub: {
      id: '',
      name: sub.name,
      provider: sub.provider,
      base_url: sub.base_url,
      api_key: sub.api_key,
      model: sub.model,
      active: sub.active ?? false,
      max_output_tokens: 0,
      thinking_mode: '',
      api_type: '',
    },
  })
}

export async function updateSubscription(
  ws: WSConnection,
  id: string,
  sub: {
    name: string
    provider: string
    base_url: string
    api_key: string
    model: string
    active?: boolean
    max_output_tokens?: number
    thinking_mode?: string
    api_type?: string
  },
): Promise<void> {
  await ws.rpc('update_subscription', {
    id,
    sub: {
      name: sub.name,
      provider: sub.provider,
      base_url: sub.base_url,
      api_key: sub.api_key,
      model: sub.model,
      active: sub.active ?? false,
      max_output_tokens: sub.max_output_tokens ?? 0,
      thinking_mode: sub.thinking_mode ?? '',
      api_type: sub.api_type ?? '',
    },
  })
}

export async function removeSubscription(ws: WSConnection, id: string): Promise<void> {
  await ws.rpc('remove_subscription', { id })
}

export async function renameSubscription(ws: WSConnection, id: string, name: string): Promise<void> {
  await ws.rpc('rename_subscription', { id, name })
}

export async function setDefaultSubscription(
  ws: WSConnection,
  id: string,
  chatID?: string,
): Promise<void> {
  await ws.rpc('set_default_subscription', { id, chat_id: chatID ?? '' })
}

export async function setSubscriptionEnabled(
  ws: WSConnection,
  subID: string,
  enabled: boolean,
): Promise<void> {
  await ws.rpc('set_subscription_enabled', { sub_id: subID, enabled })
}

// ── Model Management ──

export async function updatePerModelConfig(
  ws: WSConnection,
  id: string,
  model: string,
  config: PerModelConfig,
): Promise<void> {
  await ws.rpc('update_per_model_config', { id, model, config })
}

export async function setModelEnabled(
  ws: WSConnection,
  subID: string,
  model: string,
  enabled: boolean,
): Promise<void> {
  await ws.rpc('set_model_enabled', { sub_id: subID, model, enabled })
}

export async function removeModel(ws: WSConnection, subID: string, model: string): Promise<void> {
  await ws.rpc('remove_model', { sub_id: subID, model })
}

export async function upsertModel(
  ws: WSConnection,
  subID: string,
  model: string,
  maxContext = 0,
  maxOutput = 0,
  apiType = '',
): Promise<void> {
  await ws.rpc('upsert_model', {
    sub_id: subID,
    model,
    max_context: maxContext,
    max_output: maxOutput,
    api_type: apiType,
  })
}

// ── Model Selection & Query ──

export async function selectModel(
  ws: WSConnection,
  channel: string,
  subID: string,
  model: string,
  chatID: string,
): Promise<void> {
  await ws.rpc('select_model', {
    sub_id: subID,
    model,
    chat_id: chatID,
    channel,
  })
}

export async function listAllModelEntries(ws: WSConnection): Promise<ModelEntry[]> {
  return ws.rpc<ModelEntry[]>('list_all_model_entries', {})
}

export async function refreshModelEntries(ws: WSConnection): Promise<ModelEntry[]> {
  return ws.rpc<ModelEntry[]>('refresh_model_entries', {})
}

export async function getSessionSubscription(
  ws: WSConnection,
  channel: string,
  chatID: string,
): Promise<{ subscription_id?: string; model?: string }> {
  return ws.rpc<{ subscription_id?: string; model?: string }>('get_session_subscription', {
    channel,
    chat_id: chatID,
  })
}

// ── User-Level Settings ──

export async function getUserThinkingMode(ws: WSConnection): Promise<string> {
  return ws.rpc<string>('get_user_thinking_mode', {})
}

export async function setUserThinkingMode(ws: WSConnection, mode: string): Promise<void> {
  await ws.rpc('set_user_thinking_mode', { mode })
}

export async function getLLMConcurrency(ws: WSConnection): Promise<number> {
  return ws.rpc<number>('get_llm_concurrency', {})
}

export async function setLLMConcurrency(ws: WSConnection, personal: number): Promise<void> {
  await ws.rpc('set_llm_concurrency', { personal })
}

// ── Tier Config (via generic settings RPC) ──

export async function getSettings(
  ws: WSConnection,
  namespace: string,
): Promise<Record<string, string>> {
  return ws.rpc<Record<string, string>>('get_settings', { namespace, sender_id: '' })
}

export async function setSetting(
  ws: WSConnection,
  namespace: string,
  key: string,
  value: string,
): Promise<void> {
  await ws.rpc('set_setting', { namespace, sender_id: '', key, value })
}

// ── Masked API Key Utility ──

/**
 * Check if an API key string is a masked value (contains '****').
 * Used to detect unchanged API keys from the server's masked response.
 * Per the Spec: "如果输入值与 masked 格式匹配，则保留原 key 不发送".
 */
export function isMaskedAPIKey(key: string): boolean {
  return key.includes('****')
}
