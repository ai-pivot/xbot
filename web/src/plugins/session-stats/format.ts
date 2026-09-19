/**
 * session-stats 面板与趋势图共用的数字格式化（**单实现**，禁止各写一份 —— 同一逻辑
 * 在两处各写一遍、只修一处是历史 bug 的常见来源）。
 */

/** token 数缩写：12,345 → 12.3k；1,234,567 → 1.23M；1,234,567,890 → 1.23B。 */
export function formatTokenCount(n: number): string {
  if (!n) return '0'
  if (n >= 1_000_000_000) return `${(n / 1_000_000_000).toFixed(2)}B`
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(2)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`
  return String(Math.round(n))
}

/** 整数千分位。 */
export function formatCount(n: number): string {
  return Math.round(n ?? 0).toLocaleString('en-US')
}

/** 0..1 的比率 → "62.3%"；null/NaN ⇒ "—"（**不许把"无数据"渲染成 0%**）。 */
export function formatRatio(ratio: number | null, digits = 1): string {
  if (ratio === null || ratio === undefined || !Number.isFinite(ratio)) return '—'
  return `${(ratio * 100).toFixed(digits)}%`
}
