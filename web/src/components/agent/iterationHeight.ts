/**
 * iterationHeight.ts — 迭代块高度：实测缓存 + **结算(settle) 语义** + 内容估算。
 *
 * 背景（2026-09-13）：
 *   ①「手机上 iter 多了还是很卡，点什么交互都要等几秒」→ 交互成本 ∝ DOM 规模
 *     （移动端 + CPU 4× 实测：N=15 653 节点/样式失效 89ms/开设置 307ms；
 *      N=60 2348 节点/262ms/655ms；contain 三变体无差别）⇒ 必须迭代级窗口化：
 *      远离视口的块只留外壳 + 固定高度，内容卸载。
 *   ② 首版窗口化把**瞬态测量**当可信高度 → 内容被错误的过小高度冻结
 *     （现场：`data-window-muted="true" style="height: 26.6562px"` 的空块，
 *      内容永久消失）—— 因为卸载后再没有测量机会。
 *
 * 因此本模块的契约（**别再退化成"一次测量就冻结"**）：
 *   1. `recordIterationHeight` 只在**同一 key 连续两次测量一致**（差值 ≤ 2px）
 *      且间隔 ≥ `ITERATION_HEIGHT_SETTLE_MS` 时，才把该高度标记为 **settled**；
 *   2. **只有 settled 的高度才允许用来冻结内容**（窗口化卸载）；
 *   3. 高度一旦变化 → 立即 `unsettle`（必须重新稳定）；变高/变矮的块不得继续冻结；
 *   4. 估算（`estimateIterationHeight`）只用于「从未渲染过」的块的**占位/提示**，
 *      绝不作为冻结依据（也绝不作为滚动锚点，见下）；
 *   5. ⛔ 占位高度**绝不允许常数**（`contain-intrinsic-size: auto 320px` 曾导致
 *      「鬼打墙」：真实块远高于占位值 → 总高边滚边涨 → 滚动锚定把内容顶回去，
 *      实测总高 14,704 → 31,609）。
 */
import type { WebIteration } from '@/types/shared'

/** 两次一致测量之间的最小间隔（低于此间隔的重复测量不足以证明"稳定"）。 */
export const ITERATION_HEIGHT_SETTLE_MS = 200

const heightCache = new Map<string, number>()
/** 当前值首次被观测到的时间（用于判定"稳定满 SETTLE_MS"）。 */
const valueObservedAt = new Map<string, number>()
/** 已结算（稳定）的 key —— 只有它们允许被窗口化冻结。 */
const settledKeys = new Set<string>()

export function iterationHeightKey(turnID: number | undefined, iteration: number | undefined): string {
  return `${turnID ?? 0}:${iteration ?? 0}`
}

export function getCachedIterationHeight(key: string): number | undefined {
  return heightCache.get(key)
}

export function isIterationHeightSettled(key: string): boolean {
  return settledKeys.has(key)
}

/**
 * 记录一次实测高度。
 * @returns changed —— 缓存值是否变化（变化即意味着原来的冻结不再可信）；
 *          settled —— 该 key 现在是否处于"稳定"状态（可用于冻结）。
 */
export function recordIterationHeight(
  key: string,
  height: number,
  now: number,
): { changed: boolean; settled: boolean } {
  if (!(height > 0) || !Number.isFinite(height)) {
    return { changed: false, settled: settledKeys.has(key) }
  }
  const prev = heightCache.get(key)
  const changed = prev === undefined || Math.abs(prev - height) > 2
  if (changed) {
    heightCache.set(key, height)
    valueObservedAt.set(key, now)
    settledKeys.delete(key) // 值变了 → 必须重新稳定
    return { changed: true, settled: false }
  }
  if (!settledKeys.has(key)) {
    const at = valueObservedAt.get(key)
    if (at !== undefined && now - at >= ITERATION_HEIGHT_SETTLE_MS) settledKeys.add(key)
  }
  return { changed: false, settled: settledKeys.has(key) }
}

/** 显式解冻（例如复核发现高度不符）。 */
export function unsettleIterationHeight(key: string, now: number): void {
  settledKeys.delete(key)
  valueObservedAt.set(key, now)
}

/** 测试/调试用：清空全部状态。 */
export function clearIterationHeightCache(): void {
  heightCache.clear()
  valueObservedAt.clear()
  settledKeys.clear()
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
