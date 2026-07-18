/**
 * HTTP API client for the Agent workspace (Spec 4).
 *
 * History and Web-only session metadata are fetched through Web REST APIs so
 * shared RPC contracts stay aligned with non-Web clients. File upload remains
 * a multipart POST.
 */
import type { WSConnection } from '@/types/ws'
import type { SessionSelector } from '@/types/shared'
import { postAPI } from '@/lib/api'

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
