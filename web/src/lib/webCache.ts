import type { ProgressEvent, SessionInfo } from '@/types/shared'

export const SESSION_TREE_CACHE_KEY = 'xbot_session_tree'

/** Stable identity for caches whose server-side source is scoped by channel + chat ID. */
export function sessionCacheKey(channel: string | null | undefined, chatID: string): string {
  return `${channel || 'web'}:${chatID}`
}

/**
 * Hard ceiling on how many channel-qualified sessions keep an in-memory cache
 * entry. Every cache below is keyed by `sessionCacheKey(channel, chatID)`, so
 * WITHOUT a ceiling they grow by one entry per session the user ever opens and
 * are never reclaimed (the reported "前端 cache 总量" leak). Eviction is LRU:
 * touching an entry (get/has/set) makes it most-recently-used; inserting past
 * the ceiling drops the least-recently-used session. The ACTIVE session is
 * touched by every SSE event, so it is never the victim — eviction only ever
 * discards caches for sessions the user is not currently streaming.
 */
export const MAX_CACHED_SESSIONS = 8
/**
 * Max `iteration_history` entries retained inside a cached reconnect snapshot.
 * Recovery only reads the LAST entry's iteration (watermark for re-fetch), so
 * a handful is ample; the point is to stop "sessions × heavy history" from
 * multiplying (a long turn can carry hundreds of iterations per snapshot).
 */
export const MAX_CACHED_ITERATION_HISTORY = 4

/**
 * A `Map<string, V>` whose size never exceeds {@link MAX_CACHED_SESSIONS},
 * ordered by recency. The ceiling and the recency bookkeeping live in the
 * container itself (not in each call site), so no access path — including a
 * direct `.set()` — can grow it unbounded.
 */
class BoundedSessionCache<V> extends Map<string, V> {
  /** Move an existing key to the most-recently-used end (Map = insertion order). */
  private touch(key: string): void {
    if (!super.has(key)) return
    const value = super.get(key) as V
    super.delete(key)
    super.set(key, value)
  }

  override get(key: string): V | undefined {
    const value = super.get(key)
    if (value !== undefined) this.touch(key)
    return value
  }

  override has(key: string): boolean {
    const present = super.has(key)
    if (present) this.touch(key)
    return present
  }

  override set(key: string, value: V): this {
    if (super.has(key)) super.delete(key)
    super.set(key, value)
    while (super.size > MAX_CACHED_SESSIONS) {
      const oldest = super.keys().next().value
      if (oldest === undefined) break
      super.delete(oldest)
    }
    return this
  }
}

/**
 * Reduce a structured progress snapshot to the fields SSE reconnect recovery
 * actually reads — `turn_id`, `iteration`, and the iteration-history entries'
 * `iteration` (the last one is the re-fetch watermark in
 * `restoreActiveProgress`). Streaming payload (`content` / `reasoning` /
 * `stream_content` / tools / todos / …) is never read back from this cache, so
 * caching it only bloats memory. `phase` is kept because it is a cheap,
 * commonly-inspected marker.
 */
function trimProgressSnapshot(progress: ProgressEvent): ProgressEvent {
  const trimmed: ProgressEvent = {}
  if (typeof progress.iteration === 'number') trimmed.iteration = progress.iteration
  if (typeof progress.turn_id === 'number') trimmed.turn_id = progress.turn_id
  if (typeof progress.phase === 'string') trimmed.phase = progress.phase
  const history = progress.iteration_history
  if (Array.isArray(history) && history.length > 0) {
    trimmed.iteration_history = history.slice(-MAX_CACHED_ITERATION_HISTORY).map((entry) => {
      const raw = entry && typeof entry === 'object' ? (entry as { iteration?: unknown }).iteration : undefined
      // Keep a placeholder for a non-conforming entry so the LAST element stays
      // the last element — the watermark derivation must not shift.
      return typeof raw === 'number' ? { iteration: raw } : {}
    })
  }
  return trimmed
}

/** Bounded snapshot cache; every insert is trimmed to the recovery-critical fields. */
class ProgressSnapshotCache extends BoundedSessionCache<ProgressEvent> {
  override set(cacheKey: string, value: ProgressEvent): this {
    return super.set(cacheKey, trimProgressSnapshot(value))
  }
}

/** Last SSE sequence processed for each channel-qualified session. */
export const lastSeqCache = new BoundedSessionCache<number>()
/** Last progress_structured iteration seen for each channel-qualified session.
 *  Used to detect "gap crosses an iteration boundary" — if a seq gap spans a
 *  change in iteration id, an iteration's completion delta may have been lost
 *  (the ONLY real-data-loss signal; iteration deltas cannot be backfilled by
 *  later snapshots). */
export const lastIterationCache = new BoundedSessionCache<number>()

/**
 * Last known TurnID for each channel-qualified session. Unlike
 * `progressSnapshotCache` (cleared by terminal events like `text`/`phase_done`),
 * this cache is **never cleared by terminal events** — it survives turn
 * completion so that SSE reconnect after a long screen-off can detect
 * "the server is now on turn N+1 but we last saw turn N" even when the
 * snapshot cache was already cleared by the previous turn's terminal event.
 *
 * Written on every `progress_structured` event that carries a `turn_id > 0`.
 * Reset only on `resetLastSeq` (seq rollback / session switch).
 */
export const lastTurnIDCache = new BoundedSessionCache<number>()
/** Latest structured progress event for each channel-qualified session — SSE
 *  reconnect recovery (restoreActiveProgress) uses it to replay the newest
 *  snapshot when the ring buffer evicted events. NOT a render cache. */
export const progressSnapshotCache = new ProgressSnapshotCache()
const progressGenerationCache = new BoundedSessionCache<number>()
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
    // Strip volatile live state: the cache exists for first-paint structure
    // (labels, ordering, unread), NOT for running/busy state. Restoring a cached
    // `running: true` makes idle sessions show busy after a page reload until
    // the first session-tree refresh completes — and if that refresh fails
    // (network error), the stale busy persists with nothing to correct it
    // (user report: "明明 idle 却显示 busy"). The refresh restores live state
    // from the server (running / waiting_input).
    const stripVolatile = (nodes: SessionInfo[]): SessionInfo[] =>
      nodes.map((n) => {
        const children = n.children?.length ? stripVolatile(n.children) : n.children
        const volatile = n.running === true || n.status === 'running' || n.status === 'pending' || n.status === 'waiting_input'
        return volatile ? { ...n, running: false, status: 'idle', children } : { ...n, children }
      })
    return {
      version: 1,
      sessions: stripVolatile(parsed.sessions),
      subAgents: stripVolatile(parsed.subAgents),
    }
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
  // TurnID cache follows seq lifecycle: reset on session switch / seq rollback.
  resetLastTurnID(cacheKey)
}

/** Last progress_structured iteration seen for a session (for cross-iteration
 *  gap detection). 0 = none seen yet. */
/** Last known TurnID for a session. Unlike progressSnapshotCache (cleared by
 *  terminal events), this survives turn completion so SSE reconnect after long
 *  screen-off can detect "server is on turn N+1 but we last saw turn N" even
 *  when the snapshot cache was cleared. 0 = none seen yet. */
export function getLastTurnID(cacheKey: string): number {
  return lastTurnIDCache.get(cacheKey) ?? 0
}

export function setLastTurnID(cacheKey: string, turnID: number): void {
  lastTurnIDCache.set(cacheKey, turnID)
}

export function resetLastTurnID(cacheKey: string): void {
  lastTurnIDCache.delete(cacheKey)
}

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
  lastIterationCache.delete(cacheKey)
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
  lastIterationCache.clear()
  progressSnapshotCache.clear()
  progressGenerationCache.clear()
}
