import type { ProgressEvent, SessionInfo } from '@/types/shared'
import { ANNOTATION_STORAGE_PREFIX } from './messageAnnotations'

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


/** Bounded snapshot cache; every insert is trimmed to the recovery-critical fields. */
class ProgressSnapshotCache extends BoundedSessionCache<ProgressEvent> {
  // 已停用（用户 2026-10-20：「web 缓存现在全是错的，导致前端状态异常」）。
  //
  // 这个缓存把 progress 快照（含 iteration/turn/iteration_history）留在内存里，
  // SSE 重连后由 restoreActiveProgress 读回来恢复「正在运行」的视图。问题是
  // 快照是**某个过去时刻**的：turn 已结束、iteration 已推进、或用户在别的
  // 会话里操作过之后，恢复出来就是过期状态 —— 前端显示「还在跑」「迭代号
  // 回退」，而没有任何机制能保证它和服务端当前状态一致。
  //
  // 停用后重连完全依赖服务端：SSE 重放（ring buffer）+ /api/history 重新拉取，
  // 慢一点，但不会出现脏状态。
  override set(_cacheKey: string, _value: ProgressEvent): this {
    return this
  }

  override get(_cacheKey: string): ProgressEvent | undefined {
    return undefined
  }

  override has(_cacheKey: string): boolean {
    return false
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
    for (let index = localStorage.length - 1; index >= 0; index--) {
      const key = localStorage.key(index)
      if (key?.startsWith(ANNOTATION_STORAGE_PREFIX)) localStorage.removeItem(key)
    }
  } catch {
    // Memory caches still need to be cleared when storage is unavailable.
  }
  lastSeqCache.clear()
  lastIterationCache.clear()
  progressSnapshotCache.clear()
  progressGenerationCache.clear()
}
