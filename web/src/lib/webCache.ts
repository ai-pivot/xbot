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
/**
 * 已停用：web 前端 session 缓存全部改为 no-op（用户 2026-10-21「彻底去掉」）。
 *
 * 这个基类原本为 lastSeq / lastIteration / lastTurnID / progressGeneration 提供
 * 按 session 的 LRU 缓存。保留它会带来两类问题：
 *   1. 缓存跨会话 / 跨 turn 残留 → 界面状态与真实状态不一致；
 *   2. 刷新即清空 → 同一个操作在「不刷新」与「刷新后」表现不同。
 * 现在所有子类都不存任何东西，读取一律返回空，调用方自然回退到服务端数据。
 */
class BoundedSessionCache<V> extends Map<string, V> {
  override get(_key: string): V | undefined {
    return undefined
  }

  override has(_key: string): boolean {
    return false
  }

  override set(_key: string, _value: V): this {
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
/**
 * SSE 协议游标（**不是** UI 缓存，必须保留）：重连时靠它跳过已经处理过的事件。
 *
 * 与其它缓存不同，它不参与「界面状态」——删掉会导致每个重连周期重复处理
 * ring buffer 里的事件。仍然是按会话 keyed 的普通 Map，但由 `resetLastSeq`
 * 在会话切换 / 序号回滚时清空。
 */
export const lastSeqCache = new Map<string, number>()
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
  // 已停用（用户 2026-10-21「彻底去掉」）：不再从 localStorage 恢复会话树。
  // 顺手清掉历史版本写下的残留，避免旧数据继续影响首屏。
  try {
    localStorage.removeItem(SESSION_TREE_CACHE_KEY)
  } catch {
    /* storage 不可用时忽略 */
  }
  return null
}

export function saveSessionTreeCache(_sessions: SessionInfo[], _subAgents: SessionInfo[]): void {
  // 已停用：不再写入 localStorage（会话树每次从服务端拉取）。
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
