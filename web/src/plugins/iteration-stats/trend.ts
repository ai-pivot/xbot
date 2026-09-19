/**
 * xbot.iteration-stats —— 多时间粒度趋势聚合（**纯函数**：无 React / 无 DOM / 无副作用）。
 *
 * 数据源：核心 RPC `get_session_usage_stats` 的 `recent_iterations`
 * （`storage/sqlite/session.go` 已按时间正序返回，但我们**不依赖顺序** ——
 * 本模块按时间戳落桶，顺序无关）。
 *
 * ⛔ `limit` 被服务端硬钳制到 **500**（`session.go`：>500→500、<1→20）⇒ 明细最多
 * 500 行，更早的历史**被截断**。因此本模块显式产出 `coverageStart`（样本覆盖起点）
 * 与每个桶的 `covered` 标志：`covered=false` 的桶表示「**未知**是否有用量」，
 * 渲染层必须留空 + 图例说明，**绝不能当 0 用量画**（那是在编数据）。
 *
 * ── 时间契约（重要，别改）────────────────────────────────────────────────
 * 服务端 `iteration_history.created_at` 是 SQLite `CURRENT_TIMESTAMP` 的产物：
 * `YYYY-MM-DD HH:MM:SS`，**无时区标记、实际是 UTC**（实测 DB：与 `datetime('now')`
 * 同秒）。RPC 类型注释写的是 "RFC3339"，两者都要兼容 —— `parseSampleTime` 同时接受
 * naive-UTC / RFC3339(Z|±HH:MM) / ISO-T 无时区 / epoch 秒 / epoch 毫秒。
 * `new Date("2026-09-19 16:03:51")` 在 V8 里按**本地时间**解析 ⇒ 东八区整体偏移
 * 8 小时、趋势桶全错；naive 串必须显式补 `Z`。
 *
 * 分桶按调用方**显式传入**的 `tzOffsetMinutes`（东八区 = +480）切「本地墙钟」边界：
 * 先平移到墙钟空间、按固定宽度取整、再平移回 UTC 瞬时。于是 `formatBucketLabel`
 * 只靠 UTC getter 就能得到确定性的本地标签（不依赖进程 TZ，跨机器/CI 结果一致）。
 * 代价：固定偏移（不做夏令时切换）—— 换取纯函数可确定性测试与稳定标签。
 */
import type { UsageIterationRow } from '@/plugin-api'

// ── 粒度 ───────────────────────────────────────────────────────────────────

export type Granularity = 'minute' | 'hour' | 'day'

export interface GranularitySpec {
  /** 分桶宽度（毫秒）。 */
  readonly bucketMs: number
  /** 窗口内桶数（含当前桶）。 */
  readonly windowBuckets: number
}

/** 细 → 粗的展示顺序（分段控件按此渲染）。 */
export const GRANULARITY_ORDER: readonly Granularity[] = ['minute', 'hour', 'day']

/** 三粒度：分钟级 ~60 分钟 · 小时级 ~24 小时 · 天级 ~30 天。 */
export const GRANULARITY_SPECS: Readonly<Record<Granularity, GranularitySpec>> = {
  minute: { bucketMs: 60_000, windowBuckets: 60 },
  hour: { bucketMs: 3_600_000, windowBuckets: 24 },
  day: { bucketMs: 86_400_000, windowBuckets: 30 },
}

/** 桶数上限（防御：图表元素数与桶数成正比，窗口宽度不能被外部无限放大）。 */
export const MAX_WINDOW_BUCKETS = 360

export function granularitySpec(granularity: Granularity): GranularitySpec {
  return GRANULARITY_SPECS[granularity] ?? GRANULARITY_SPECS.hour
}

/** 归一化桶数到 [1, MAX_WINDOW_BUCKETS]。 */
export function normalizeWindowBuckets(count: number): number {
  if (!Number.isFinite(count)) return GRANULARITY_SPECS.hour.windowBuckets
  const n = Math.floor(count)
  if (n < 1) return 1
  if (n > MAX_WINDOW_BUCKETS) return MAX_WINDOW_BUCKETS
  return n
}

// ── 时间解析 / 墙钟分桶 ────────────────────────────────────────────────────

/** 无时区标记的 `YYYY-MM-DD HH:MM:SS[.fff]`（SQLite CURRENT_TIMESTAMP 形态）。 */
const NAIVE_DATE_TIME = /^(\d{4})-(\d{2})-(\d{2})[ T](\d{2}):(\d{2})(?::(\d{2})(?:\.(\d{1,6}))?)?$/
/** 带时区标记的尾巴（Z / ±HH:MM / ±HHMM）。 */
const ZONE_SUFFIX = /(?:z|[+-]\d{2}:?\d{2})$/i

/**
 * 解析一条样本的时间戳 → epoch 毫秒；无法解析返回 null。
 *
 * **绝不猜**：猜出来的时刻会静默把数据画到错误的桶里（比丢一行更糟）——
 * 无法解析行由调用方计入 `unparsableRows` 并在 UI 上可见（数据质量信号）。
 */
export function parseSampleTime(raw: unknown): number | null {
  if (typeof raw !== 'string') return null
  const value = raw.trim()
  if (!value) return null

  // 纯数字：epoch（≥13 位当毫秒，否则当秒）。
  if (/^\d+$/.test(value)) {
    const n = Number(value)
    if (!Number.isFinite(n)) return null
    return value.length >= 13 ? n : n * 1000
  }

  // 无时区标记 ⇒ 补 Z（服务端存 UTC，见文件头契约）。
  const naive = NAIVE_DATE_TIME.exec(value)
  if (naive) {
    const [, y, mo, d, h, mi, s = '00', frac] = naive
    return parseFinite(`${y}-${mo}-${d}T${h}:${mi}:${s}${frac ? `.${frac}` : ''}Z`)
  }

  if (ZONE_SUFFIX.test(value)) return parseFinite(value)

  // ISO-T 但无时区（"2026-09-19T16:03:51"）⇒ 同样按 UTC 解释。
  if (/^\d{4}-\d{2}-\d{2}T/.test(value)) return parseFinite(`${value}Z`)

  return parseFinite(value)
}

function parseFinite(value: string): number | null {
  const ms = Date.parse(value)
  return Number.isFinite(ms) ? ms : null
}

/**
 * 浏览器当前时区偏移（分钟，东为正 —— 与 `Date#getTimezoneOffset()` 反号）。
 * 抽成函数只为让聚合保持纯：调用方注入，测试可固定值。
 */
export function browserTzOffsetMinutes(): number {
  return -new Date().getTimezoneOffset()
}

/** 把时刻取整到桶起点（在 `tzOffsetMinutes` 的墙钟空间里取整）。 */
export function floorToBucket(ts: number, bucketMs: number, tzOffsetMinutes: number): number {
  const shift = tzOffsetMinutes * 60_000
  return Math.floor((ts + shift) / bucketMs) * bucketMs - shift
}

// ── 桶 / 序列 ──────────────────────────────────────────────────────────────

export interface TrendBucket {
  /** 桶起点（含）—— UTC 瞬时毫秒。 */
  readonly start: number
  /** 桶终点（不含）。 */
  readonly end: number
  /** 桶内迭代（LLM 调用）数。 */
  readonly iterations: number
  /** 输入 token（含命中缓存部分）。 */
  readonly input: number
  /** 命中缓存的输入 token（已 clamp 到 ≤ input）。 */
  readonly cached: number
  /** 未命中缓存的输入 token = input - cached。 */
  readonly uncached: number
  /** 输出 token。 */
  readonly output: number
  /** 总 token = input + output。 */
  readonly total: number
  /** 缓存命中率 0..1；桶内无输入 token ⇒ null（不许用 0 冒充"命中率 0%"）。 */
  readonly cacheRate: number | null
  /** 桶内 TTFT 均值（ms，仅统计 >0 的样本）；无样本 ⇒ null。 */
  readonly ttftMs: number | null
  /** 桶内 TPOT 均值（ms，仅统计 >0 的样本）；无样本 ⇒ null。 */
  readonly tpotMs: number | null
  /** 桶内 tok/s 均值（仅统计 >0 的样本）；无样本 ⇒ null。 */
  readonly tokensPerSec: number | null
  /**
   * 该桶是否落在**样本覆盖范围**内（详见文件头 + `TrendSeries.coverageStart`）。
   * false ⇒ 未知是否有用量，渲染层必须留空，不能画 0。
   */
  readonly covered: boolean
}

export interface TrendTotals {
  readonly iterations: number
  readonly input: number
  readonly cached: number
  readonly output: number
  /** 整个窗口的缓存命中率；窗口内无输入 token ⇒ null。 */
  readonly cacheRate: number | null
}

export interface TrendSeries {
  readonly granularity: Granularity
  readonly bucketMs: number
  readonly tzOffsetMinutes: number
  /** 窗口起点（含）。 */
  readonly start: number
  /** 窗口终点（不含）= 最后一个桶的 end。 */
  readonly end: number
  /** 连续桶序列（长度 = 桶数；空桶也在，值为 0）—— 趋势图不能跳桶。 */
  readonly buckets: readonly TrendBucket[]
  /**
   * 样本覆盖起点（窗口内最早的「有覆盖」桶起点）；null = 窗口内没有任何样本。
   * `start < coverageStart` 的桶是未知区（被 LIMIT 截断 / 会话当时还不存在）。
   */
  readonly coverageStart: number | null
  /** 样本里最早 / 最新一条可解析明细的瞬时（含窗口外的行）；null = 无可解析样本。 */
  readonly oldestSampleAt: number | null
  readonly newestSampleAt: number | null
  /** RPC 实际返回的明细行数。 */
  readonly sampleSize: number
  /** 落在窗口内、参与聚合的迭代数。 */
  readonly matchedIterations: number
  /** 因落在窗口外被忽略的迭代数（数据比窗口更老，属正常）。 */
  readonly skippedOutOfWindow: number
  /** `created_at` 无法解析而被忽略的行数（数据质量信号，必须可见）。 */
  readonly unparsableRows: number
  /** 窗口内聚合总量。 */
  readonly totals: TrendTotals
}

interface RawAccumulator {
  start: number
  end: number
  iterations: number
  input: number
  cached: number
  output: number
  ttftSum: number
  ttftCount: number
  tpotSum: number
  tpotCount: number
  tpsSum: number
  tpsCount: number
}

/**
 * 把 per-iteration 明细按粒度分桶成**连续**时间序列。
 *
 * @param rows            原始明细（顺序无关；`null`/`undefined`/缺字段行被安全跳过）
 * @param granularity     分钟 / 小时 / 天
 * @param now             窗口右端基准（"当前时刻"）—— 显式传入，保证纯函数可测
 * @param tzOffsetMinutes 时区偏移（东为正，分钟；见文件头契约）
 * @param windowBuckets   覆盖桶数（缺省用粒度默认值；越界裁剪到 [1, 360]）
 */
export function aggregateTrend(
  rows: readonly UsageIterationRow[] | null | undefined,
  granularity: Granularity,
  now: number,
  tzOffsetMinutes: number,
  windowBuckets?: number,
): TrendSeries {
  const spec = granularitySpec(granularity)
  const bucketMs = spec.bucketMs
  const count = normalizeWindowBuckets(windowBuckets ?? spec.windowBuckets)

  const lastStart = floorToBucket(now, bucketMs, tzOffsetMinutes)
  const firstStart = lastStart - (count - 1) * bucketMs
  const windowEnd = lastStart + bucketMs

  const acc: RawAccumulator[] = Array.from({ length: count }, (_, i) => ({
    start: firstStart + i * bucketMs,
    end: firstStart + (i + 1) * bucketMs,
    iterations: 0,
    input: 0,
    cached: 0,
    output: 0,
    ttftSum: 0,
    ttftCount: 0,
    tpotSum: 0,
    tpotCount: 0,
    tpsSum: 0,
    tpsCount: 0,
  }))

  let matched = 0
  let skippedOutOfWindow = 0
  let unparsableRows = 0
  let oldestSampleAt: number | null = null
  let newestSampleAt: number | null = null

  const list = rows ?? []
  for (const row of list) {
    if (!row || typeof row !== 'object') {
      unparsableRows++
      continue
    }
    const ts = parseSampleTime(row.created_at)
    if (ts === null) {
      unparsableRows++
      continue
    }
    if (oldestSampleAt === null || ts < oldestSampleAt) oldestSampleAt = ts
    if (newestSampleAt === null || ts > newestSampleAt) newestSampleAt = ts

    if (ts < firstStart || ts >= windowEnd) {
      skippedOutOfWindow++
      continue
    }
    const idx = Math.floor((ts - firstStart) / bucketMs)
    const slot = acc[idx]
    if (!slot) {
      // 区间判定已覆盖该范围，理论不可达：保守计数而不是崩掉或静默丢数据。
      skippedOutOfWindow++
      continue
    }
    matched++
    const input = positive(row.input_tokens)
    const cached = Math.min(positive(row.cached_tokens), input)
    slot.iterations++
    slot.input += input
    slot.cached += cached
    slot.output += positive(row.output_tokens)

    const ttft = positive(row.ttft_ms)
    if (ttft > 0) {
      slot.ttftSum += ttft
      slot.ttftCount++
    }
    const tpot = positive(row.tpot_ms)
    if (tpot > 0) {
      slot.tpotSum += tpot
      slot.tpotCount++
    }
    const tps = positive(row.tokens_per_sec)
    if (tps > 0) {
      slot.tpsSum += tps
      slot.tpsCount++
    }
  }

  // 覆盖起点：样本若已延伸到窗口左边（oldestSampleAt < firstStart），整窗都有覆盖；
  // 否则从窗口内最早样本所在桶起算。窗口内无样本 ⇒ null（全新会话 / 全是旧数据）。
  let coverageStart: number | null = null
  if (oldestSampleAt !== null && oldestSampleAt < windowEnd) {
    coverageStart =
      oldestSampleAt < firstStart
        ? firstStart
        : floorToBucket(oldestSampleAt, bucketMs, tzOffsetMinutes)
  }

  const buckets: TrendBucket[] = acc.map((a) => {
    const hasTtft = a.ttftCount > 0
    const hasTpot = a.tpotCount > 0
    const hasTps = a.tpsCount > 0
    return {
      start: a.start,
      end: a.end,
      iterations: a.iterations,
      input: a.input,
      cached: a.cached,
      uncached: Math.max(a.input - a.cached, 0),
      output: a.output,
      total: a.input + a.output,
      cacheRate: a.input > 0 ? a.cached / a.input : null,
      ttftMs: hasTtft ? a.ttftSum / a.ttftCount : null,
      tpotMs: hasTpot ? a.tpotSum / a.tpotCount : null,
      tokensPerSec: hasTps ? a.tpsSum / a.tpsCount : null,
      covered: coverageStart !== null && a.start >= coverageStart,
    }
  })

  const totalsInput = buckets.reduce((s, b) => s + b.input, 0)
  const totalsCached = buckets.reduce((s, b) => s + b.cached, 0)
  const totalsOutput = buckets.reduce((s, b) => s + b.output, 0)

  return {
    granularity,
    bucketMs,
    tzOffsetMinutes,
    start: firstStart,
    end: windowEnd,
    buckets,
    coverageStart,
    oldestSampleAt,
    newestSampleAt,
    sampleSize: list.length,
    matchedIterations: matched,
    skippedOutOfWindow,
    unparsableRows,
    totals: {
      iterations: buckets.reduce((s, b) => s + b.iterations, 0),
      input: totalsInput,
      cached: totalsCached,
      output: totalsOutput,
      cacheRate: totalsInput > 0 ? totalsCached / totalsInput : null,
    },
  }
}

function positive(v: unknown): number {
  const n = typeof v === 'number' ? v : Number(v)
  if (!Number.isFinite(n) || n <= 0) return 0
  return n
}

// ── 派生判据（渲染层用，全部纯函数）────────────────────────────────────────

/** 有样本（iterations>0）的桶数 —— 单点降级 / 空态判据。 */
export function filledBucketCount(series: TrendSeries): number {
  return series.buckets.reduce((n, b) => n + (b.iterations > 0 ? 1 : 0), 0)
}

/** 覆盖桶数（`covered=true`）。 */
export function coveredBucketCount(series: TrendSeries): number {
  return series.buckets.reduce((n, b) => n + (b.covered ? 1 : 0), 0)
}

/** 是否存在"未知区"（窗口起点早于覆盖起点）—— 渲染层需要显示图例说明。 */
export function hasUncoveredWindow(series: TrendSeries): boolean {
  return series.buckets.some((b) => !b.covered)
}

/** 窗口内的峰值（各指标）—— 图表 Y 轴刻度 + tooltip 高亮用。 */
export function seriesPeak(series: TrendSeries): {
  total: number
  input: number
  output: number
  ttftMs: number
  tpotMs: number
} {
  let total = 0
  let input = 0
  let output = 0
  let ttftMs = 0
  let tpotMs = 0
  for (const b of series.buckets) {
    if (b.total > total) total = b.total
    if (b.input > input) input = b.input
    if (b.output > output) output = b.output
    if ((b.ttftMs ?? 0) > ttftMs) ttftMs = b.ttftMs ?? 0
    if ((b.tpotMs ?? 0) > tpotMs) tpotMs = b.tpotMs ?? 0
  }
  return { total, input, output, ttftMs, tpotMs }
}

// ── 标签格式化（确定性：只用 UTC getter + 显式偏移）────────────────────────

function wallClockParts(ts: number, tzOffsetMinutes: number) {
  const d = new Date(ts + tzOffsetMinutes * 60_000)
  return {
    year: d.getUTCFullYear(),
    month: d.getUTCMonth() + 1,
    day: d.getUTCDate(),
    hour: d.getUTCHours(),
    minute: d.getUTCMinutes(),
  }
}

const pad2 = (n: number): string => String(n).padStart(2, '0')

/** 桶标签（本地墙钟）：分钟 `HH:MM` · 小时 `MM-DD HH:00` · 天 `MM-DD`。 */
export function formatBucketLabel(start: number, granularity: Granularity, tzOffsetMinutes: number): string {
  const p = wallClockParts(start, tzOffsetMinutes)
  if (granularity === 'minute') return `${pad2(p.hour)}:${pad2(p.minute)}`
  if (granularity === 'hour') return `${pad2(p.month)}-${pad2(p.day)} ${pad2(p.hour)}:00`
  return `${pad2(p.month)}-${pad2(p.day)}`
}

/** 桶的完整时刻标签（tooltip 标题）：分钟 `MM-DD HH:MM` · 小时 `MM-DD HH:00–HH:59` · 天 `YYYY-MM-DD`。 */
export function formatBucketTitle(start: number, granularity: Granularity, tzOffsetMinutes: number): string {
  const p = wallClockParts(start, tzOffsetMinutes)
  const date = `${p.year}-${pad2(p.month)}-${pad2(p.day)}`
  if (granularity === 'minute') return `${date} ${pad2(p.hour)}:${pad2(p.minute)}`
  if (granularity === 'hour') return `${date} ${pad2(p.hour)}:00–${pad2(p.hour)}:59`
  return date
}

/** 「最早~最新」覆盖范围文案（渲染层标注 LIMIT 截断用）。 */
export function formatCoverageRange(series: TrendSeries): string | null {
  if (series.oldestSampleAt === null || series.newestSampleAt === null) return null
  return `${formatStamp(series.oldestSampleAt, series.tzOffsetMinutes)} ~ ${formatStamp(series.newestSampleAt, series.tzOffsetMinutes)}`
}

/** 完整时间戳（本地墙钟）：`MM-DD HH:MM`。 */
export function formatStamp(ts: number, tzOffsetMinutes: number): string {
  const p = wallClockParts(ts, tzOffsetMinutes)
  return `${pad2(p.month)}-${pad2(p.day)} ${pad2(p.hour)}:${pad2(p.minute)}`
}

// ── 几何（SVG 路径纯函数）─────────────────────────────────────────────────

export interface ChartPoint {
  x: number
  y: number
}

/**
 * 桶 → 折线点。缺失值（`value === null`，如桶内无 TTFT 样本）**断开**而不是当 0：
 * 用 `null` 位置在 `polylinePath` 里分段，避免把"没数据"画成"指标跌到 0"。
 */
export function seriesToPoints(
  values: readonly (number | null)[],
  width: number,
  height: number,
  maxValue: number,
): (ChartPoint | null)[] {
  const n = values.length
  if (n === 0) return []
  const step = n > 1 ? width / (n - 1) : 0
  const span = maxValue > 0 ? maxValue : 1
  return values.map((v, i) => {
    if (v === null || !Number.isFinite(v)) return null
    const x = n > 1 ? i * step : width / 2
    const y = height - (Math.max(v, 0) / span) * height
    return { x, y }
  })
}

/**
 * 平滑曲线路径（Catmull-Rom → 三次贝塞尔，张力 0.5），跳过 null 断点。
 * 单点 / 两点退化时用直线段（不制造假的弯曲）。
 */
export function smoothLinePath(points: readonly (ChartPoint | null)[]): string {
  const segments = splitSegments(points)
  const out: string[] = []
  for (const seg of segments) {
    if (seg.length === 1) {
      const p = seg[0]!
      // 单点段：零长线段（配合端点的圆点标记可见）。
      out.push(`M ${round(p.x)} ${round(p.y)} L ${round(p.x)} ${round(p.y)}`)
      continue
    }
    out.push(`M ${round(seg[0]!.x)} ${round(seg[0]!.y)}`)
    for (let i = 0; i < seg.length - 1; i++) {
      const p0 = seg[i - 1] ?? seg[i]!
      const p1 = seg[i]!
      const p2 = seg[i + 1]!
      const p3 = seg[i + 2] ?? p2
      const c1x = p1.x + (p2.x - p0.x) / 6
      const c1y = p1.y + (p2.y - p0.y) / 6
      const c2x = p2.x - (p3.x - p1.x) / 6
      const c2y = p2.y - (p3.y - p1.y) / 6
      out.push(
        `C ${round(c1x)} ${round(c1y)}, ${round(c2x)} ${round(c2y)}, ${round(p2.x)} ${round(p2.y)}`,
      )
    }
  }
  return out.join(' ')
}

/** 平滑面积路径（曲线 + 基线闭合），跳过 null 断点；无有效点返回空串。 */
export function smoothAreaPath(points: readonly (ChartPoint | null)[], baselineY: number): string {
  const segments = splitSegments(points)
  const out: string[] = []
  for (const seg of segments) {
    if (seg.length === 0) continue
    const line = smoothLinePath(seg)
    if (!line) continue
    const first = seg[0]!
    const last = seg[seg.length - 1]!
    out.push(
      `${line} L ${round(last.x)} ${round(baselineY)} L ${round(first.x)} ${round(baselineY)} Z`,
    )
  }
  return out.join(' ')
}

/**
 * 平滑**带状**路径（堆叠面积的一层）：上边界正向曲线 → 下边界反向曲线 → 闭合。
 * 上/下边界任一侧为 null 的位置断开（无样本桶不画，绝不补 0）。
 */
export function smoothBandPath(
  upper: readonly (ChartPoint | null)[],
  lower: readonly (ChartPoint | null)[],
): string {
  const n = Math.min(upper.length, lower.length)
  const out: string[] = []
  let up: ChartPoint[] = []
  let lo: ChartPoint[] = []

  const flush = () => {
    if (up.length > 0 && lo.length > 0) {
      const top = smoothLinePath(up)
      // 反向的下边界：smoothLinePath 以 "M" 开头是子路径起点，这里要接续成直线段。
      const bottom = demoteMoveTo(smoothLinePath([...lo].reverse()))
      if (top && bottom) out.push(`${top} ${bottom} Z`)
    }
    up = []
    lo = []
  }

  for (let i = 0; i < n; i++) {
    const u = upper[i] ?? null
    const l = lower[i] ?? null
    if (u === null || l === null) {
      flush()
      continue
    }
    up.push(u)
    lo.push(l)
  }
  flush()
  return out.join(' ')
}

/** 把路径开头的 `M x y` 降级为 `L x y`（用于把子路径接续到前一段上）。 */
export function demoteMoveTo(path: string): string {
  return path.replace(/^\s*M\s/, 'L ')
}

/**
 * 指针 → 桶下标（hover 十字线定位）。
 * 容器无布局（宽 ≤ 0）或桶数为 0 ⇒ null（jsdom / 面板隐藏时不会算出一个假的桶）。
 */
export function pointerBucketIndex(
  clientX: number,
  rectLeft: number,
  rectWidth: number,
  count: number,
): number | null {
  if (!Number.isFinite(clientX) || !Number.isFinite(rectLeft) || !Number.isFinite(rectWidth)) return null
  if (rectWidth <= 0 || count <= 0) return null
  const ratio = (clientX - rectLeft) / rectWidth
  if (!Number.isFinite(ratio)) return null
  const idx = Math.floor(ratio * count)
  if (idx < 0) return 0
  if (idx >= count) return count - 1
  return idx
}

/** 连续非空点的分段（null = 断点）。 */
export function splitSegments(points: readonly (ChartPoint | null)[]): ChartPoint[][] {
  const segments: ChartPoint[][] = []
  let cur: ChartPoint[] = []
  for (const p of points) {
    if (p === null) {
      if (cur.length) segments.push(cur)
      cur = []
    } else {
      cur.push(p)
    }
  }
  if (cur.length) segments.push(cur)
  return segments
}

function round(n: number): number {
  return Math.round(n * 100) / 100
}

// ── 数值格式化（显示层；token 数用紧凑单位，绝不四舍五入丢失量级）──────────

/** 紧凑 token 数：1234 → `1.2k`；12_345_678 → `12.3M`。 */
export function formatTokens(n: number): string {
  const v = Number.isFinite(n) ? n : 0
  const abs = Math.abs(v)
  if (abs >= 1_000_000) return `${trimZero(v / 1_000_000)}M`
  if (abs >= 1_000) return `${trimZero(v / 1_000)}k`
  return String(Math.round(v))
}

/** 毫秒：<1000 → `123ms`；否则 `1.2s`。 */
export function formatDuration(ms: number | null): string | null {
  if (ms === null || !Number.isFinite(ms) || ms <= 0) return null
  if (ms < 1000) return `${Math.round(ms)}ms`
  return `${trimZero(ms / 1000)}s`
}

/** 比率 0..1 → `93%`；null → null（不许用 0 冒充）。 */
export function formatRate(rate: number | null): string | null {
  if (rate === null || !Number.isFinite(rate)) return null
  return `${Math.round(rate * 100)}%`
}

function trimZero(n: number): string {
  const s = n >= 100 ? n.toFixed(0) : n >= 10 ? n.toFixed(1) : n.toFixed(2)
  return s.replace(/\.0+$/, '').replace(/(\.\d*?)0+$/, '$1')
}
