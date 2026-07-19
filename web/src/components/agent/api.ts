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
import type { ContextUsage, ModelEntry, PerModelConfig, SessionSelector, Subscription } from '@/types/shared'
import { postAPI } from '@/lib/api'

/** History message row (protocol.HistoryMessage). */
export interface HistMsg {
  role: 'user' | 'assistant'
  content: string
  timestamp?: string
  iterations?: unknown[]
  /** SSE sequence number (present when the message was delivered via SSE
   *  before being persisted to DB). Used as a stable dedup key — no string
   *  matching needed. */
  seq?: number
}

/** Raw active-progress snapshot (protocol.ProgressEvent). */
export interface HistProgress {
  /** Semantic progress-log watermark (protocol.ProgressEvent.Seq). */
  seq?: number
  phase?: string
  iteration?: number
  thinking?: string
  content?: string
  active_tools?: unknown[]
  completed_tools?: unknown[]
  sub_agents?: unknown[]
  stream_content?: string
  /** Total wall-clock of the active turn (ms). */
  elapsed_wall?: number
  iteration_history?: unknown[]
  todos?: { id: number; text: string; done: boolean }[]
  cwd?: string
}

/** /api/history response. */
export interface HistoryResponse {
  messages?: HistMsg[]
  processing?: boolean
  active_progress?: HistProgress | null
  last_seq?: number
  chat_id?: string
  channel?: string
}

/** Upload response (channel/web/web_file.go handleCloudUpload). */
export interface UploadResponse {
  upload_key?: string
  name?: string
  size?: number
  mime?: string
}

/** Fetch conversation history through the Web-only snapshot API. */
export async function fetchHistory(_ws: WSConnection, session?: SessionSelector | null): Promise<HistoryResponse> {
  return postAPI<HistoryResponse>('/api/history', sessionBody(session))
}

export async function fetchCwd(session?: SessionSelector | null): Promise<{ dir?: string }> {
  const status = await postAPI<SessionStatusResponse<unknown, unknown>>('/api/session/status', sessionBody(session))
  return { dir: status.cwd }
}

export async function setCwd(session: SessionSelector, dir: string): Promise<{ dir?: string }> {
  await postAPI('/api/rpc', {
    method: 'set_cwd',
    params: { channel: session.channel, chat_id: session.chatID, dir },
  })
  return { dir }
}

export async function fetchCronTasks<T>(session: SessionSelector): Promise<T[]> {
  const data = await fetchSessionStatus<T, unknown>(session)
  return data.tasks ?? []
}

export async function fetchBackgroundTasks<T>(session: SessionSelector): Promise<T[]> {
  const data = await fetchSessionStatus<unknown, T>(session)
  return data.background_tasks ?? []
}

export async function fetchCommands<T>(): Promise<T[]> {
  const commands = await postAPI<Array<string | T>>('/api/rpc', {
    method: 'list_command_names',
    params: {},
  })
  return commands.map((command) => (
    typeof command === 'string' ? { name: command } as T : command
  ))
}

export async function fetchSessionSubscription(session: SessionSelector): Promise<Record<string, string>> {
  return postAPI<Record<string, string>>('/api/rpc', {
    method: 'get_session_subscription',
    params: sessionBody(session),
  })
}

export async function rewindHistory<T>(session: SessionSelector, cutoffMS: number): Promise<T> {
  return postAPI<T>('/api/history/rewind', {
    channel: session.channel,
    chat_id: session.chatID,
    cutoff_ms: cutoffMS,
  })
}

/** Upload a single file; returns the server-issued upload key + metadata. */
export async function uploadFile(file: File): Promise<UploadResponse> {
  const form = new FormData()
  form.append('file', file)
  const data = await postAPI<UploadResponse>('/api/files/upload', form)
  if (!data.upload_key) throw new Error('upload response missing upload_key')
  return data
}

interface SessionStatusResponse<CronTask, BackgroundTask> {
  tasks?: CronTask[]
  background_tasks?: BackgroundTask[]
  token_usage?: Record<string, unknown>
  cwd?: string
}

export function fetchSessionStatus<CronTask = unknown, BackgroundTask = unknown>(
  session: SessionSelector,
): Promise<SessionStatusResponse<CronTask, BackgroundTask>> {
  return postAPI('/api/session/status', sessionBody(session))
}

function sessionBody(session?: SessionSelector | null): { channel?: string; chat_id?: string } {
  if (!session) return {}
  return { channel: session.channel, chat_id: session.chatID }
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

export async function getContextUsage(
  ws: WSConnection,
  channel: string,
  chatID: string,
): Promise<ContextUsage> {
  return ws.rpc<ContextUsage>('get_context_usage', {
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
