import type {
  ChatMessage,
  ProgressEvent,
  SessionInfo,
} from '@/types/shared'

export const SESSION_TREE_CACHE_KEY = 'xbot_session_tree'

/** Stable identity for caches whose server-side source is scoped by channel + chat ID. */
export function sessionCacheKey(channel: string | null | undefined, chatID: string): string {
  return `${channel || 'web'}:${chatID}`
}

/** Per-conversation rendered messages, keyed by sessionCacheKey. */
export const messagesCache = new Map<string, ChatMessage[]>()
/** Last SSE sequence processed for each channel-qualified session. */
export const lastSeqCache = new Map<string, number>()
/** Latest structured progress event for each channel-qualified session. */
export const progressSnapshotCache = new Map<string, ProgressEvent>()
const progressGenerationCache = new Map<string, number>()

interface StoredSessionTree {
  version: 1
  sessions: SessionInfo[]
  subAgents: SessionInfo[]
}

export function loadSessionTreeCache(): StoredSessionTree | null {
  try {
    const raw = localStorage.getItem(SESSION_TREE_CACHE_KEY)
    if (!raw) return null
    const parsed = JSON.parse(raw) as Partial<StoredSessionTree>
    if (parsed.version !== 1 || !Array.isArray(parsed.sessions) || !Array.isArray(parsed.subAgents)) {
      return null
    }
    return parsed as StoredSessionTree
  } catch {
    return null
  }
}

export function saveSessionTreeCache(sessions: SessionInfo[], subAgents: SessionInfo[]): void {
  const value: StoredSessionTree = { version: 1, sessions, subAgents }
  try {
    localStorage.setItem(SESSION_TREE_CACHE_KEY, JSON.stringify(value))
  } catch {
    // Storage may be unavailable or full; the in-memory state remains authoritative.
  }
}

export function getLastSeq(cacheKey: string): number {
  return lastSeqCache.get(cacheKey) ?? 0
}

export function setLastSeq(cacheKey: string, seq: number): void {
  if (seq > getLastSeq(cacheKey)) lastSeqCache.set(cacheKey, seq)
}

export function resetLastSeq(cacheKey: string): void {
  lastSeqCache.delete(cacheKey)
}

export function getProgressGeneration(cacheKey: string): number {
  return progressGenerationCache.get(cacheKey) ?? 0
}

export function bumpProgressGeneration(cacheKey: string): number {
  const next = getProgressGeneration(cacheKey) + 1
  progressGenerationCache.set(cacheKey, next)
  return next
}

export function clearProgressSnapshot(cacheKey: string): void {
  progressSnapshotCache.delete(cacheKey)
}

export function clearWebCaches(): void {
  try {
    localStorage.removeItem(SESSION_TREE_CACHE_KEY)
  } catch {
    // Memory caches still need to be cleared when storage is unavailable.
  }
  messagesCache.clear()
  lastSeqCache.clear()
  progressSnapshotCache.clear()
  progressGenerationCache.clear()
}
