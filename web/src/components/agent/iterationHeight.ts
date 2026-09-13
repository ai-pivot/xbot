/**
 * iterationHeight.ts — 迭代块高度：**实例作用域**的实测缓存 + settle 语义 + 内容估算。
 *
 * 为什么必须"实例作用域"（2026-09-13 用户报告「切换 session 后出现空 tool iter」）：
 * 高度 key 只能用 `turnID:iteration`，而 **turnID 是每会话独立编号** —— 会话 A 的
 * `1084:810` 与会话 B 的 `1084:810` 完全无关却会撞车；若缓存是模块级（跨会话留存），
 * 切到 B 后 B 的块会读到 A 的"已结算高度" → 立刻被判定可冻结 → 内容卸载 →
 * **空块**（用户现场）。⇒ 每次挂载（每个 CommittedTurn 实例）各持一份 tracker，
 * 缓存的生存期与"这一行的这次挂载"一致，绝不跨会话/跨行串味。
 *
 * settle 契约（首版窗口化事故后确立，别再退化成"一次测量就冻结"）：
 *   1. `record` 只在**同值连续两次测量**（差 ≤ 2px）且间隔 ≥ `SETTLE_MS` 时标记 settled；
 *   2. **只有 settled 的高度才允许冻结内容**（窗口化卸载）；
 *   3. 高度一变 → 立即 unsettle（必须重新稳定）；
 *   4. 估算只用于「从未渲染过」的块的显示占位，**绝不作为冻结依据**；
 *   5. ⛔ 占位高度绝不允许常数（`contain-intrinsic-size: auto 320px` 曾导致"鬼打墙"）。
 */
import type { WebIteration } from '@/types/shared'

/** 两次一致测量之间的最小间隔（短于此不足以证明"稳定"）。 */
export const ITERATION_HEIGHT_SETTLE_MS = 200

export interface IterationHeightTracker {
  get(key: string): number | undefined
  isSettled(key: string): boolean
  /** @returns changed —— 缓存值是否变化（变化即原冻结不再可信）；settled —— 现在可否冻结。 */
  record(key: string, height: number, now: number): { changed: boolean; settled: boolean }
  unsettle(key: string, now: number): void
  /** 调试/测试：清空。 */
  clear(): void
}

/** 每次挂载一份（生存期 = 该行这次挂载），避免跨会话 key 撞车。 */
export function createIterationHeightTracker(): IterationHeightTracker {
  const heights = new Map<string, number>()
  const observedAt = new Map<string, number>()
  const settled = new Set<string>()
  return {
    get: (key) => heights.get(key),
    isSettled: (key) => settled.has(key),
    record(key, height, now) {
      if (!(height > 0) || !Number.isFinite(height)) {
        return { changed: false, settled: settled.has(key) }
      }
      const prev = heights.get(key)
      const changed = prev === undefined || Math.abs(prev - height) > 2
      if (changed) {
        heights.set(key, height)
        observedAt.set(key, now)
        settled.delete(key) // 值变了 → 必须重新稳定
        return { changed: true, settled: false }
      }
      if (!settled.has(key)) {
        const at = observedAt.get(key)
        if (at !== undefined && now - at >= ITERATION_HEIGHT_SETTLE_MS) settled.add(key)
      }
      return { changed: false, settled: settled.has(key) }
    },
    unsettle(key, now) {
      settled.delete(key)
      observedAt.set(key, now)
    },
    clear() {
      heights.clear()
      observedAt.clear()
      settled.clear()
    },
  }
}

export function iterationHeightKey(turnID: number | undefined, iteration: number | undefined): string {
  return `${turnID ?? 0}:${iteration ?? 0}`
}

/**
 * 内容估算（仅用于「从未渲染过」的块显示占位；**不作为冻结依据**）。
 * 与 MessageList.estimateRowByContent 的迭代部分同源，宁可略高估不要低估。
 */
export function estimateIterationHeight(iter: WebIteration): number {
  const textLen = (iter.content?.length ?? 0) + (iter.reasoning?.length ?? 0)
  const lines = Math.ceil(textLen / 90) || 1
  const tools = iter.tools?.length ?? 0
  const subAgents = iter.subAgents?.length ?? 0
  const raw = 70 + lines * 21 + 34 + Math.ceil(tools / 4) * 20 + subAgents * 40
  return Math.min(Math.max(raw, 140), 6000)
}
