/**
 * HTTP API client for the Agent workspace (Spec 4).
 *
 * History and Web-only session metadata are fetched through Web REST APIs so
 * shared RPC contracts stay aligned with non-Web clients. File upload remains
 * a multipart POST.
 */
import type { WSConnection } from '@/types/ws'
import type { SessionSelector } from '@/types/shared'

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
