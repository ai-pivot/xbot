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
import type { ContextUsage, ModelEntry, PerModelConfig, ProgressEvent, SessionSelector, Subscription, TodoItem } from '@/types/shared'
import { postAPI } from '@/lib/api'

/** History message row (protocol.HistoryMessage). */
export interface HistMsg {
  history_id?: number
  role: string
  content: string
  reasoning_content?: string
  tool_call_id?: string
  tool_name?: string
  tool_arguments?: string
  tool_calls?: { id: string; name: string; arguments: string }[]
  timestamp?: string
  id?: number
  iterations?: unknown[]
  /** TurnID of the turn that produced this message. 0 = untracked (old data
   *  before v50 migration). Used by MessageList to dedup committed history
   *  against the live store's active turn. */
  turn_id?: number
  /** SSE sequence number (present when the message was delivered via SSE
   *  before being persisted to DB). Used as a stable dedup key — no string
   *  matching needed. */
  seq?: number
  record_type?: string
  compacted_by?: number
  display_only?: boolean
  compression?: {
    start_history_id?: number
    end_history_id?: number
    source_history_ids?: number[]
  }
}

/** Raw active-progress snapshot (protocol.ProgressEvent). */
export type HistProgress = ProgressEvent

/** /api/history response. */
export interface HistoryResponse {
  messages?: HistMsg[]
  processing?: boolean
  active_progress?: HistProgress | null
  last_seq?: number
  chat_id?: string
  channel?: string
  has_more?: boolean
  oldest_id?: number
}

/** Upload response (channel/web/web_file.go handleCloudUpload). */
export interface UploadResponse {
  upload_key?: string
  name?: string
  size?: number
  mime?: string
}

/** Fetch conversation history through the Web-only snapshot API.
 *  limit: max user turns (default 30, server-side default).
 *  beforeId: pagination cursor — return messages older than this id. */
export async function fetchHistory(_ws: WSConnection, session?: SessionSelector | null, opts?: { limit?: number; beforeId?: number }): Promise<HistoryResponse> {
  return postAPI<HistoryResponse>('/api/history', {
    ...sessionBody(session),
    ...(opts?.limit ? { limit: opts.limit } : {}),
    ...(opts?.beforeId ? { before_id: opts.beforeId } : {}),
  })
}

export async function fetchCwd(session?: SessionSelector | null): Promise<{ dir?: string; todos?: TodoItem[] }> {
  const status = await postAPI<{ cwd?: string; todos?: TodoItem[] }>('/api/session/status', sessionBody(session))
  return { dir: status.cwd, todos: status.todos }
}

export async function setCwd(session: SessionSelector, dir: string): Promise<{ dir?: string }> {
  await postAPI('/api/rpc', {
    method: 'set_cwd',
    params: { channel: session.channel, chat_id: session.chatID, dir },
  })
  return { dir }
}

export async function fetchCronTasks<T>(session: SessionSelector): Promise<T[]> {
  const data = await postAPI<{ tasks?: T[] }>('/api/cron/list', sessionBody(session))
  return data.tasks ?? []
}

export async function fetchBackgroundTasks<T>(session: SessionSelector): Promise<T[]> {
  const data = await postAPI<{ background_tasks?: T[] }>('/api/tasks/list', sessionBody(session))
  return data.background_tasks ?? []
}

export async function fetchCommands<T>(): Promise<T[]> {
  const commands = await postAPI<Array<string | T>>('/api/rpc', {
    method: 'list_command_names',
    params: {},
  })
  return commands.map((command) => (typeof command === 'string' ? ({ name: command } as T) : command))
}

export async function fetchSessionSubscription(session: SessionSelector): Promise<Record<string, string>> {
  return postAPI<Record<string, string>>('/api/rpc', {
    method: 'get_session_subscription',
    params: sessionBody(session),
  })
}

export async function rewindHistory<T>(session: SessionSelector, historyID: number): Promise<T> {
  return postAPI<T>('/api/history/rewind', {
    channel: session.channel,
    chat_id: session.chatID,
    history_id: historyID,
  })
}

// ---------------------------------------------------------------------------
// Session import/export (xbot portable session format)
// ---------------------------------------------------------------------------

/** Portable session format for import/export (Codex-interoperable shape). */
export interface ExportedSession {
  id: string
  model?: string
  system_instructions?: string
  messages: ExportedMessage[]
  usage?: { input_tokens?: number; output_tokens?: number; total_tokens?: number }
  created_at?: string
  updated_at?: string
  /** Complete append-only history rows (xbot extension, lossless restore). */
  records?: ExportedRecord[]
}

export interface ExportedMessage {
  role: string
  content: string | Array<{ type: string; text?: string }>
  reasoning?: string
  detail?: string
  tool_calls?: Array<{ id: string; type: string; function: { name: string; arguments: string } }>
  tool_call_id?: string
  name?: string
  timestamp?: string
}

export interface ExportedRecord {
  history_id: number
  record_type: string
  target_history_id?: number
  record_data?: unknown
  role?: string
  content?: string
  tool_call_id?: string
  tool_name?: string
  tool_arguments?: string
  tool_calls?: unknown
  detail?: string
  reasoning?: string
  display_only?: boolean
  turn_id?: number
  created_at?: string
}

/** Export a session as portable JSON. Returns the full session object. */
export async function exportSession(session: SessionSelector): Promise<ExportedSession> {
  return postAPI<ExportedSession>('/api/rpc', {
    method: 'export_session',
    params: sessionBody(session),
  })
}

/** Export format options for downloadSession. */
export type ExportFormat = 'native' | 'openai' | 'codex' | 'benchmark' | 'multica'

// ---------------------------------------------------------------------------
// Benchmark JSONL format (HLE / mint-bench compatible).
// Mirrors protocol.DemoRecord — one JSON object per line.
// ---------------------------------------------------------------------------

export interface DemoPart {
  part_kind: string // user-prompt | thinking | text | tool-call | tool-return
  content: string
  tool_name: string
  tool_call_id: string
  args: string
}

export interface DemoMessage {
  kind: string // request | response
  parts: DemoPart[]
}

export interface DemoRecord {
  uuid: string
  question: string
  answer: string
  domain: string
  messages: DemoMessage[]
  correct: boolean
  judge_applied: boolean
}

/**
 * Export a session and trigger a browser download in the specified format.
 * - native: xbot portable JSON (full ExportedSession with records)
 * - openai: OpenAI Chat Completions request body ({model, messages:[...]})
 * - codex: Codex JSONL (one JSON object per line, Codex CLI session format)
 * - benchmark: HLE / mint-bench JSONL (one record per user turn, with
 *   uuid/question/answer/domain/messages/correct/judge_applied)
 */
export async function downloadSession(session: SessionSelector, format: ExportFormat = 'native'): Promise<void> {
  let content: string
  let mime: string
  let ext: string

  if (format === 'benchmark') {
    // Benchmark JSONL comes from the dedicated RPC (per-turn records).
    const res = await postAPI<{ records?: DemoRecord[]; count?: number }>('/api/rpc', {
      method: 'export_session_jsonl',
      params: sessionBody(session),
    })
    const records = res.records ?? []
    content = records.map((r) => JSON.stringify(r)).join('\n') + (records.length ? '\n' : '')
    mime = 'application/x-jsonlines'
    ext = 'jsonl'
  } else {
    const data = await exportSession(session)
    switch (format) {
    case 'openai': {
      // Construct an OpenAI Chat Completions API request body.
      const messages: Array<Record<string, unknown>> = []
      if (data.system_instructions) {
        messages.push({ role: 'system', content: data.system_instructions })
      }
      for (const msg of data.messages) {
        const entry: Record<string, unknown> = { role: msg.role, content: typeof msg.content === 'string' ? msg.content : msg.content }
        if (msg.tool_calls?.length) {
          entry.tool_calls = msg.tool_calls
        }
        if (msg.tool_call_id) {
          entry.tool_call_id = msg.tool_call_id
        }
        if (msg.name) {
          entry.name = msg.name
        }
        messages.push(entry)
      }
      const requestBody = {
        model: data.model || 'gpt-4o',
        messages,
      }
      content = JSON.stringify(requestBody, null, 2)
      mime = 'application/json'
      ext = 'json'
      break
    }
    case 'codex': {
      // Codex JSONL: one JSON object per line. Each line is a message
      // in Codex CLI session format with type/role/content.
      const lines: string[] = []
      if (data.system_instructions) {
        lines.push(JSON.stringify({
          type: 'message',
          role: 'system',
          content: [{ type: 'input_text', text: data.system_instructions }],
        }))
      }
      for (const msg of data.messages) {
        const text = typeof msg.content === 'string' ? msg.content : JSON.stringify(msg.content)
        const contentPart = msg.role === 'assistant'
          ? { type: 'output_text', text }
          : { type: 'input_text', text }
        const entry: Record<string, unknown> = {
          type: 'message',
          role: msg.role,
          content: [contentPart],
        }
        if (msg.reasoning) {
          entry.reasoning = msg.reasoning
        }
        if (msg.tool_calls?.length) {
          entry.tool_calls = msg.tool_calls
        }
        if (msg.tool_call_id) {
          entry.tool_call_id = msg.tool_call_id
        }
        if (msg.name) {
          entry.name = msg.name
        }
        lines.push(JSON.stringify(entry))
      }
      content = lines.join('\n')
      mime = 'application/x-jsonlines'
      ext = 'jsonl'
      break
    }
    case 'multica': {
      // Multica JSONL: one JSON object per line, with parentId chain.
      // Format: {type, id, parentId, timestamp, message?: {role, content: [{type, text?}], ...}}
      // Types: session, model_change, thinking_level_change, message
      const lines: string[] = []
      // Session header
      lines.push(JSON.stringify({
        type: 'session',
        version: 3,
        id: data.id || crypto.randomUUID(),
        timestamp: data.created_at || new Date().toISOString(),
        cwd: '',
      }))
      // Model change
      if (data.model) {
        lines.push(JSON.stringify({
          type: 'model_change',
          id: crypto.randomUUID().slice(0, 8),
          parentId: null,
          timestamp: data.created_at || new Date().toISOString(),
          provider: 'macaronai',
          modelId: data.model,
        }))
      }
      // Messages with parentId chain
      let prevId: string | null = null
      for (const msg of data.messages) {
        const id = crypto.randomUUID().slice(0, 8)
        const ts = msg.timestamp || new Date().toISOString()
        // Build content parts in Multica format
        const contentParts: unknown[] = []
        // Reasoning → thinking part
        if (msg.reasoning) {
          contentParts.push({ type: 'thinking', thinking: msg.reasoning })
        }
        // Tool calls → toolCall parts
        if (msg.tool_calls?.length) {
          for (const tc of msg.tool_calls) {
            let args: unknown = tc.function.arguments
            try { args = JSON.parse(tc.function.arguments) } catch { /* keep string */ }
            contentParts.push({
              type: 'toolCall',
              id: tc.id,
              name: tc.function.name,
              arguments: args,
            })
          }
        }
        // Text content
        if (typeof msg.content === 'string' && msg.content) {
          contentParts.push({ type: 'text', text: msg.content })
        } else if (Array.isArray(msg.content)) {
          for (const part of msg.content) {
            if (part.type === 'text' && part.text) {
              contentParts.push({ type: 'text', text: part.text })
            }
          }
        }
        // Tool result
        if (msg.tool_call_id) {
          const entry: Record<string, unknown> = {
            type: 'message',
            id,
            parentId: prevId,
            timestamp: ts,
            message: {
              role: 'toolResult',
              toolCallId: msg.tool_call_id,
              toolName: msg.name || '',
              content: contentParts.length > 0 ? contentParts : [{ type: 'text', text: typeof msg.content === 'string' ? msg.content : '' }],
              isError: false,
              timestamp: Date.now(),
            },
          }
          lines.push(JSON.stringify(entry))
        } else {
          const entry: Record<string, unknown> = {
            type: 'message',
            id,
            parentId: prevId,
            timestamp: ts,
            message: {
              role: msg.role,
              content: contentParts.length > 0 ? contentParts : [{ type: 'text', text: typeof msg.content === 'string' ? msg.content : '' }],
              timestamp: Date.now(),
            },
          }
          lines.push(JSON.stringify(entry))
        }
        prevId = id
      }
      content = lines.join('\n') + '\n'
      mime = 'application/x-jsonlines'
      ext = 'jsonl'
      break
    }
    default: {
      // native: full xbot portable JSON
      content = JSON.stringify(data, null, 2)
      mime = 'application/json'
      ext = 'json'
      break
    }
    }
  }

  // Sanitize the session label for filename
  const label = (session.chatID || 'session').replace(/[^a-zA-Z0-9_-]/g, '_').slice(0, 60)
  const filename = `${label}.${ext}`
  const blob = new Blob([content], { type: `${mime};charset=utf-8` })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
}

/** Import a portable session into an existing (or new) chat. */
export async function importSession(session: SessionSelector, data: ExportedSession): Promise<{ imported: number }> {
  return postAPI<{ imported: number }>('/api/rpc', {
    method: 'import_session',
    params: { channel: session.channel, chat_id: session.chatID, session: data },
  })
}

/** Continue an active interactive SubAgent without generic inbound routing. */
export async function continueInteractiveSession(ws: WSConnection, fullKey: string, content: string): Promise<void> {
  await ws.rpc('continue_interactive_session', { full_key: fullKey, content })
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

function sessionBody(session?: SessionSelector | null): {
  channel?: string
  chat_id?: string
} {
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

export async function setDefaultSubscription(ws: WSConnection, id: string, chatID?: string): Promise<void> {
  await ws.rpc('set_default_subscription', { id, chat_id: chatID ?? '' })
}

export async function setSubscriptionEnabled(ws: WSConnection, subID: string, enabled: boolean): Promise<void> {
  await ws.rpc('set_subscription_enabled', { sub_id: subID, enabled })
}

// ── Model Management ──

export async function updatePerModelConfig(ws: WSConnection, id: string, model: string, config: PerModelConfig): Promise<void> {
  await ws.rpc('update_per_model_config', { id, model, config })
}

export async function setModelEnabled(ws: WSConnection, subID: string, model: string, enabled: boolean): Promise<void> {
  await ws.rpc('set_model_enabled', { sub_id: subID, model, enabled })
}

export async function removeModel(ws: WSConnection, subID: string, model: string): Promise<void> {
  await ws.rpc('remove_model', { sub_id: subID, model })
}

export async function upsertModel(ws: WSConnection, subID: string, model: string, maxContext = 0, maxOutput = 0, apiType = ''): Promise<void> {
  await ws.rpc('upsert_model', {
    sub_id: subID,
    model,
    max_context: maxContext,
    max_output: maxOutput,
    api_type: apiType,
  })
}

// ── Model Selection & Query ──

export async function selectModel(ws: WSConnection, channel: string, subID: string, model: string, chatID: string): Promise<void> {
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

export async function getSessionSubscription(ws: WSConnection, channel: string, chatID: string): Promise<{ subscription_id?: string; model?: string }> {
  return ws.rpc<{ subscription_id?: string; model?: string }>('get_session_subscription', {
    channel,
    chat_id: chatID,
  })
}

export async function getContextUsage(ws: WSConnection, channel: string, chatID: string): Promise<ContextUsage> {
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

export async function getSettings(ws: WSConnection, namespace: string): Promise<Record<string, string>> {
  return ws.rpc<Record<string, string>>('get_settings', {
    namespace,
    sender_id: '',
  })
}

export async function setSetting(ws: WSConnection, namespace: string, key: string, value: string): Promise<void> {
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
