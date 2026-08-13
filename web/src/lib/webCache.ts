import type { ProgressEvent, SessionInfo } from '@/types/shared'

export const SESSION_TREE_CACHE_KEY = 'xbot_session_tree'

/** Stable identity for caches whose server-side source is scoped by channel + chat ID. */
export function sessionCacheKey(channel: string | null | undefined, chatID: string): string {
  return `${channel || 'web'}:${chatID}`
}

/** Last SSE sequence processed for each channel-qualified session. */
export const lastSeqCache = new Map<string, number>()
/** Last progress_structured iteration seen for each channel-qualified session.
 *  Used to detect "gap crosses an iteration boundary" — if a seq gap spans a
 *  change in iteration id, an iteration's completion delta may have been lost
 *  (the ONLY real-data-loss signal; iteration deltas cannot be backfilled by
 *  later snapshots). */
export const lastIterationCache = new Map<string, number>()
/** Latest structured progress event for each channel-qualified session — SSE
 *  reconnect recovery (restoreActiveProgress) uses it to replay the newest
 *  snapshot when the ring buffer evicted events. NOT a render cache. */
export const progressSnapshotCache = new Map<string, ProgressEvent>()
const progressGenerationCache = new Map<string, number>()
let webCacheEpoch = 0

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

export function hasLastSeq(cacheKey: string): boolean {
  return lastSeqCache.has(cacheKey)
}

export function setLastSeq(cacheKey: string, seq: number): void {
  if (!hasLastSeq(cacheKey) || seq > getLastSeq(cacheKey)) lastSeqCache.set(cacheKey, seq)
}

export function resetLastSeq(cacheKey: string): void {
  lastSeqCache.delete(cacheKey)
}

/** Last progress_structured iteration seen for a session (for cross-iteration
 *  gap detection). 0 = none seen yet. */
export function getLastIteration(cacheKey: string): number {
  return lastIterationCache.get(cacheKey) ?? 0
}

export function setLastIteration(cacheKey: string, iteration: number): void {
  lastIterationCache.set(cacheKey, iteration)
}

export function resetLastIteration(cacheKey: string): void {
  lastIterationCache.delete(cacheKey)
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

/** Remove every in-memory cache entry owned by one channel-qualified session. */
export function clearSessionCaches(cacheKey: string): void {
  lastSeqCache.delete(cacheKey)
  progressSnapshotCache.delete(cacheKey)
  progressGenerationCache.delete(cacheKey)
}

/** Changes whenever authentication-scoped Web caches are invalidated. */
export function getWebCacheEpoch(): number {
  return webCacheEpoch
}

export function clearWebCaches(): void {
  webCacheEpoch += 1
  try {
    localStorage.removeItem(SESSION_TREE_CACHE_KEY)
  } catch {
    // Memory caches still need to be cleared when storage is unavailable.
  }
  lastSeqCache.clear()
  progressSnapshotCache.clear()
  progressGenerationCache.clear()
}
