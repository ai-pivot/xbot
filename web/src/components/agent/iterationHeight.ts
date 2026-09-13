/**
 * iterationHeight.ts — 迭代块高度：实测缓存 + 内容估算（迭代级窗口化的地基）。
 *
 * 背景（2026-09-13 用户报告「手机上 iter 多了还是很卡，点什么交互都要等几秒」）：
 * 移动端（390×844 + CPU 4×）实测 —— 交互成本与 **DOM 规模**成正比：
 *
 *   N      节点    全局样式失效   打开设置面板
 *   15     653     89ms          307ms
 *   60     2348    262ms(×2.9)   655ms(×2.1)
 *
 * containment（layout/paint）三变体实测**无差别**（274/267/265ms）→ 单纯限定失效
 * 范围救不了：整棵树仍在 DOM 里，任何触碰样式（Radix 面板给 body 加 pointer-events、
 * 主题/CSS 变量）都会横扫全部节点。真机 + 更重的迭代内容（markdown/hljs/KaTeX）
 * → 几秒。⇒ 必须把「每屏挂载的迭代内容」与 N 解耦：远离视口的块只留外壳 + 固定高度。
 *
 * 高度来源（按优先级）：
 *   1. **实测缓存**（`iterationHeightCache`）—— 块一旦渲染过就量出真实高度，
 *      committed 迭代内容不可变 ⇒ 一次实测 = 永久精确（滚动不会再漂）。
 *   2. **内容估算**（`estimateIterationHeight`）—— 只用于「还没被渲染过」的块。
 *      ⚠️ 绝不允许再用常数占位（曾经的 `contain-intrinsic-size: auto 320px`）：
 *      真实块远高于 320px，向上滚动时块逐个兑现真实高度 → 滚动容器总高持续变化 →
 *      滚动锚定把内容顶回去 → 「鬼打墙」（2026-09-13 事故，实测总高 14,704 → 31,609）。
 *      估算必须来自内容长度，误差量级 ±20% 而非 3-6×。
 */
import type { WebIteration } from '@/types/shared'

/** 实测高度缓存：key = `${turnID}:${iteration}`（跨挂载/卸载复用）。 */
const iterationHeightCache = new Map<string, number>()

export function iterationHeightKey(turnID: number | undefined, iteration: number | undefined): string {
  return `${turnID ?? 0}:${iteration ?? 0}`
}

export function getCachedIterationHeight(key: string): number | undefined {
  return iterationHeightCache.get(key)
}

/** 记录实测高度（±1px 内不写，避免无意义的重渲染）。返回是否发生变化。 */
export function setCachedIterationHeight(key: string, height: number): boolean {
  if (!(height > 0)) return false
  const prev = iterationHeightCache.get(key)
  if (prev !== undefined && Math.abs(prev - height) <= 1) return false
  iterationHeightCache.set(key, height)
  return true
}

/** 测试/调试用：清空缓存。 */
export function clearIterationHeightCache(): void {
  iterationHeightCache.clear()
}

/**
 * 内容估算（仅用于从未渲染过的块）。与 MessageList.estimateRowByContent 的迭代部分
 * 同源（行级估算已在用同一套常数），宁可略高估也不要低估。
 */
export function estimateIterationHeight(iter: WebIteration): number {
  const textLen = (iter.content?.length ?? 0) + (iter.reasoning?.length ?? 0)
  const lines = Math.ceil(textLen / 90) || 1
  const tools = iter.tools?.length ?? 0
  const subAgents = iter.subAgents?.length ?? 0
  const raw = 70 + lines * 21 + 34 + Math.ceil(tools / 4) * 20 + subAgents * 40
  return Math.min(Math.max(raw, 140), 6000)
}
