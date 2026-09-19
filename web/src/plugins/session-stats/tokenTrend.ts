/**
 * xbot.session-stats 的 token 趋势聚合 —— **纯函数**（无 React / 无 DOM / 无副作用）。
 *
 * 数据源：`get_session_usage_stats` 的 `recent_iterations`（per-iteration 明细，
 * 服务端已按时间从旧到新排序）。本模块把明细按【分钟 / 小时 / 天】分桶成**连续**
 * 时间序列（空桶补齐 —— 趋势图不能跳桶），供 TokenTrendChart 画堆叠面积图。
 *
 * 时区契约（重要，别改）：
 *   - 服务端 `created_at` 是 SQLite `CURRENT_TIMESTAMP` 的产物：
 *     `YYYY-MM-DD HH:MM:SS`，**无时区标记、实际是 UTC**（实测 DB：MAX(created_at)
 *     与 `datetime('now')` 同秒）。RPC 类型注释写的是 "RFC3339"，两者都要兼容 ——
 *     `parseServerTimestamp` 同时接受 naive-UTC / RFC3339(Z|±HH:MM) / ISO-T 无时区 /
 *     epoch 秒/毫秒。
 *   - `new Date("2026-09-19 16:03:51")` 在 V8 里按**本地时间**解析 ⇒ 东八区会整体
 *     偏移 8 小时，趋势桶全错。所以 naive 串必须显式补 `Z` 再解析。
 *   - 分桶按调用方**显式传入**的 `tzOffsetMinutes`（东八区 = +480）切"本地墙钟"边界：
 *     内部先平移到墙钟空间、按固定宽度取整、再平移回 UTC 瞬时。这样
 *     `formatBucketLabel` 可以只用 UTC getter 就得到确定性的本地标签
 *     （不依赖进程 TZ，跨机器/CI 结果一致）。
 *   - 代价：固定偏移（不做夏令时切换）。跨 DST 的窗口会有 1 小时级的边界偏移，
 *     换来的是纯函数可确定性测试 + 标签稳定。
 */
import type { UsageIterationRow } from '@/plugin-api'

// ── 粒度 ───────────────────────────────────────────────────────────────────

export type TrendGranularity = 'minute' | 'hour' | 'day'

export interface TrendGranularitySpec {
  readonly granularity: TrendGranularity
  /** 分桶宽度（毫秒）。 */
  readonly bucketMs: number
  /** 窗口内桶数（含当前桶）。 */
  readonly windowBuckets: number
}

/** 三种粒度：分钟级 ~60 分钟 · 小时级 ~24 小时 · 天级 ~30 天。 */
export const TREND_GRANULARITIES: Readonly<Record<TrendGranularity, TrendGranularitySpec>> = {
  minute: { granularity: 'minute', bucketMs: 60_000, windowBuckets: 60 },
  hour: { granularity: 'hour', bucketMs: 3_600_000, windowBuckets: 24 },
  day: { granularity: 'day', bucketMs: 86_400_000, windowBuckets: 30 },
}

/** 分段控件的展示顺序（细 → 粗）。 */
export const TREND_GRANULARITY_ORDER: readonly TrendGranularity[] = ['minute', 'hour', 'day']

export function trendGranularitySpec(granularity: TrendGranularity): TrendGranularitySpec {
  return TREND_GRANULARITIES[granularity] ?? TREND_GRANULARITIES.hour
}

/** 桶数上限（防御：窗口桶数不能由外部无限放大 —— 图表元素数必须与它成正比）。 */
const MAX_BUCKETS = 360

// ── 时间解析 / 时区 ────────────────────────────────────────────────────────

/** 无时区标记的 `YYYY-MM-DD HH:MM:SS[.fff]`（SQLite CURRENT_TIMESTAMP 形态）。 */
const NAIVE_DATE_TIME = /^(\d{4})-(\d{2})-(\d{2})[ T](\d{2}):(\d{2})(?::(\d{2})(?:\.(\d{1,6}))?)?$/
/** 带时区标记的尾巴（Z / +08:00 / +0800）。 */
const ZONE_SUFFIX = /(?:z|[+-]\d{2}:?\d{2})$/i

/**
 * 把服务端时间戳解析成 epoch 毫秒；无法解析返回 null（**绝不猜** —— 猜出来的
 * 时间会静默把数据画到错误的桶里）。
 */
export function parseServerTimestamp(raw: string): number | null {
  const value = (raw ?? '').trim()
  if (!value) return null

  // 纯数字：epoch（≥13 位当作毫秒，10 位当作秒）。
  if (/^\d+$/.test(value)) {
    const n = Number(value)
    if (!Number.isFinite(n)) return null
    const ms = value.length >= 13 ? n : n * 1000
    return Number.isFinite(ms) ? ms : null
  }

  // 无时区标记的日期时间 ⇒ 补 Z（服务端存的是 UTC，见文件头契约）。
  const naive = NAIVE_DATE_TIME.exec(value)
  if (naive) {
    const [, y, mo, d, h, mi, s = '00', frac] = naive
    const iso = `${y}-${mo}-${d}T${h}:${mi}:${s}${frac ? `.${frac}` : ''}Z`
    return parseOrNull(iso)
  }

  if (ZONE_SUFFIX.test(value)) return parseOrNull(value)

  // ISO-T 但无时区（"2026-09-19T16:03:51"）→ 同样按 UTC 解释。
  if (/^\d{4}-\d{2}-\d{2}T/.test(value)) return parseOrNull(`${value}Z`)

  return parseOrNull(value)
}

function parseOrNull(value: string): number | null {
  const ms = Date.parse(value)
  return Number.isFinite(ms) ? ms : null
}

/**
 * 浏览器当前时区偏移（分钟，东为正 —— 与 `Date#getTimezoneOffset()` 反号）。
 * 抽成函数是为了让聚合函数保持纯：调用方注入，测试可固定。
 */
export function browserTzOffsetMinutes(): number {
  return -new Date().getTimezoneOffset()
}

/** 把时刻取整到桶起点（在 `tzOffsetMinutes` 的墙钟空间里取整）。 */
export function floorToBucket(ts: number, bucketMs: number, tzOffsetMinutes: number): number {
  const shift = tzOffsetMinutes * 60_000
  return Math.floor((ts + shift) / bucketMs) * bucketMs - shift
}

// ── 分桶 ───────────────────────────────────────────────────────────────────

export interface TokenBucket {
  /** 桶起点（含）—— UTC 瞬时毫秒。 */
  readonly start: number
  /** 桶终点（不含）。 */
  readonly end: number
  /** 桶内 LLM 调用（迭代）数。 */
  readonly calls: number
  /** 输入 token（含命中缓存的部分）。 */
  readonly input: number
  /** 命中缓存的输入 token。 */
  readonly cached: number
  /** 未命中缓存的输入 token = max(input - cached, 0)。 */
  readonly uncached: number
  /** 输出 token。 */
  readonly output: number
  /** 总 token = input + output。 */
  readonly total: number
  /** 缓存命中率 0..1；桶内没有输入 token ⇒ null（不许拿 0 冒充"命中率 0%"）。 */
  readonly cacheRate: number | null
  /**
   * 该桶是否落在**明细覆盖范围**内。
   *
   * ⛔ 服务端明细是 `ORDER BY id DESC LIMIT ≤500`（最新 500 行）⇒ 更早的历史被截断，
   * 我们**不知道**那些桶有没有用量。`covered=false` 的桶绝不能当"0 用量"渲染
   * （那是在编数据）；图表只画 covered 区间，未覆盖区间留空并标注。
   */
  readonly covered: boolean
}

export interface TokenTrend {
  readonly granularity: TrendGranularity
  readonly bucketMs: number
  readonly tzOffsetMinutes: number
  /** 窗口起点（含）。 */
  readonly start: number
  /** 窗口终点（不含）= 最后一个桶的 end。 */
  readonly end: number
  /** 连续桶序列（长度 = windowBuckets，空桶也在，值为 0）。 */
  readonly buckets: readonly TokenBucket[]
  /**
   * 明细覆盖的起点桶（= 窗口内最早一条明细所在桶的 start）；null = 窗口内没有任何明细。
   * 窗口起点 < coverageStart 的桶是"未知区"（样本被 LIMIT 截断 / 会话当时还不存在）。
   */
  readonly coverageStart: number | null
  /** 样本里最早 / 最新一条可解析明细的瞬时（含窗口外的行）；null = 无可解析明细。 */
  readonly oldestSampleAt: number | null
  readonly newestSampleAt: number | null
  /** 参与解析的明细行数（= RPC 实际返回的 recent_iterations 长度）。 */
  readonly sampleSize: number
  /** 落在窗口内、参与聚合的迭代数。 */
  readonly matchedCalls: number
  /** 因落在窗口外被忽略的迭代数（数据比窗口更老，属正常）。 */
  readonly skippedOutOfWindow: number
  /** `created_at` 无法解析而被忽略的行数（数据质量信号，必须可见）。 */
  readonly unparsableRows: number
}



function finalizeBucket(b: {
  start: number
  end: number
  calls: number
  input: number
  cached: number
  output: number
}, covered: boolean): TokenBucket {
  const input = b.input
  const cached = Math.min(b.cached, input)
  return {
    start: b.start,
    end: b.end,
    calls: b.calls,
    input,
    cached,
    uncached: Math.max(input - cached, 0),
    output: b.output,
    total: input + b.output,
    cacheRate: input > 0 ? cached / input : null,
    covered,
  }
}

/**
 * 把 per-iteration 明细按粒度分桶成**连续**时间序列。
 *
 * @param rows          原始明细（顺序无关 —— 本函数按时间戳落桶）
 * @param granularity   分钟 / 小时 / 天
 * @param now           窗口右端基准（"当前时刻"）—— 显式传入，保证纯函数可测
 * @param tzOffsetMinutes 时区偏移（东为正，分钟；见文件头时区契约）
 * @param windowBuckets 覆盖桶数（缺省用粒度默认值；越界会被裁剪到 [1, 360]）
 */
export function bucketizeUsageTrend(
  rows: readonly UsageIterationRow[],
  granularity: TrendGranularity,
  now: number,
  tzOffsetMinutes: number,
  windowBuckets?: number,
): TokenTrend {
  const spec = trendGranularitySpec(granularity)
  const bucketMs = spec.bucketMs
  const count = normalizeBucketCount(windowBuckets ?? spec.windowBuckets)

  const lastStart = floorToBucket(now, bucketMs, tzOffsetMinutes)
  const firstStart = lastStart - (count - 1) * bucketMs
  const windowEnd = lastStart + bucketMs

  const acc = Array.from({ length: count }, (_, i) => ({
    start: firstStart + i * bucketMs,
    end: firstStart + (i + 1) * bucketMs,
    calls: 0,
    input: 0,
    cached: 0,
    output: 0,
  }))

  let matchedCalls = 0
  let skippedOutOfWindow = 0
  let unparsableRows = 0
  let oldestSampleAt: number | null = null
  let newestSampleAt: number | null = null

  for (const row of rows ?? []) {
    const ts = parseServerTimestamp(row?.created_at ?? '')
    if (ts === null) {
      unparsableRows++
      continue
    }
    // 覆盖区间只看**可解析**的样本行（窗口内外都算 —— 窗口外的更老行恰好证明
    // 样本延伸到了窗口左边，那窗口起点就是有覆盖的）。
    if (oldestSampleAt === null || ts < oldestSampleAt) oldestSampleAt = ts
    if (newestSampleAt === null || ts > newestSampleAt) newestSampleAt = ts

    if (ts < firstStart || ts >= windowEnd) {
      skippedOutOfWindow++
      continue
    }
    const idx = Math.floor((ts - firstStart) / bucketMs)
    const slot = acc[idx]
    if (!slot) {
      // 理论不可达（区间判定已覆盖）；保守丢进最近的桶而不是崩掉或静默丢数据。
      skippedOutOfWindow++
      continue
    }
    slot.calls++
    slot.input += row.input_tokens || 0
    slot.cached += row.cached_tokens || 0
    slot.output += row.output_tokens || 0
    matchedCalls++
  }

  // 覆盖起点 = 窗口内最早一条明细所在桶（样本延伸到窗口左边时 = 窗口起点桶）。
  const coverageStart =
    oldestSampleAt === null
      ? null
      : floorToBucket(Math.max(oldestSampleAt, firstStart), bucketMs, tzOffsetMinutes)

  return {
    granularity,
    bucketMs,
    tzOffsetMinutes,
    start: firstStart,
    end: windowEnd,
    buckets: acc.map((b) =>
      finalizeBucket(b, coverageStart !== null && b.start >= coverageStart),
    ),
    coverageStart,
    oldestSampleAt,
    newestSampleAt,
    sampleSize: (rows ?? []).length,
    matchedCalls,
    skippedOutOfWindow,
    unparsableRows,
  }
}

function normalizeBucketCount(value: number): number {
  if (!Number.isFinite(value)) return 1
  return Math.min(MAX_BUCKETS, Math.max(1, Math.round(value)))
}

// ── 摘要 ───────────────────────────────────────────────────────────────────

export interface TokenTrendSummary {
  readonly input: number
  readonly cached: number
  readonly uncached: number
  readonly output: number
  readonly total: number
  readonly calls: number
  /** 窗口整体缓存命中率 0..1；无输入 token ⇒ null。 */
  readonly cacheRate: number | null
  /** 有数据的桶数（0 ⇒ 窗口内无调用；1 ⇒ 只有一个桶有数据，图表要降级）。 */
  readonly activeBuckets: number
  /** 覆盖区间内"确实是 0 用量"的桶数（covered && calls === 0）。 */
  readonly idleBuckets: number
  /** 覆盖范围之外的桶数（样本被 LIMIT 截断 / 会话当时还不存在）—— 图表留空。 */
  readonly uncoveredBuckets: number
  /** token 总量最高的桶（并列取更早的；无数据 ⇒ null）。 */
  readonly peak: TokenBucket | null
}

export function summarizeTrend(trend: TokenTrend): TokenTrendSummary {
  let input = 0
  let cached = 0
  let output = 0
  let calls = 0
  let activeBuckets = 0
  let idleBuckets = 0
  let uncoveredBuckets = 0
  let peak: TokenBucket | null = null

  for (const b of trend?.buckets ?? []) {
    input += b.input
    cached += b.cached
    output += b.output
    calls += b.calls
    if (!b.covered) uncoveredBuckets++
    else if (b.calls > 0) activeBuckets++
    else idleBuckets++
    if (b.total > 0 && (peak === null || b.total > peak.total)) peak = b
  }

  return {
    input,
    cached,
    uncached: Math.max(input - cached, 0),
    output,
    total: input + output,
    calls,
    cacheRate: input > 0 ? cached / input : null,
    activeBuckets,
    idleBuckets,
    uncoveredBuckets,
    peak,
  }
}

// ── 标签 / 坐标轴 ──────────────────────────────────────────────────────────

export interface BucketLabel {
  /** 主标签（X 轴 / tooltip 标题）。 */
  readonly primary: string
  /** 次标签（tooltip 副标题；天级 = 年份）。 */
  readonly secondary: string
}

/**
 * 桶标签（本地墙钟）。用 UTC getter 读"平移后的时间"，因此**不依赖进程 TZ** ——
 * 同一 (start, granularity, tzOffsetMinutes) 在任何机器/CI 上得到同一字符串。
 */
export function formatBucketLabel(
  start: number,
  granularity: TrendGranularity,
  tzOffsetMinutes: number,
): BucketLabel {
  const d = new Date(start + tzOffsetMinutes * 60_000)
  const p2 = (n: number) => String(n).padStart(2, '0')
  const md = `${p2(d.getUTCMonth() + 1)}-${p2(d.getUTCDate())}`
  switch (granularity) {
    case 'day':
      return { primary: md, secondary: String(d.getUTCFullYear()) }
    case 'hour':
      return { primary: `${p2(d.getUTCHours())}:00`, secondary: md }
    case 'minute':
    default:
      return { primary: `${p2(d.getUTCHours())}:${p2(d.getUTCMinutes())}`, secondary: md }
  }
}

/** 覆盖区间/时间跨度的展示标签（比桶标签更完整：天级带年份）。 */
export function formatSpanLabel(
  ts: number,
  granularity: TrendGranularity,
  tzOffsetMinutes: number,
): string {
  const { primary, secondary } = formatBucketLabel(
    floorToBucket(ts, trendGranularitySpec(granularity).bucketMs, tzOffsetMinutes),
    granularity,
    tzOffsetMinutes,
  )
  return granularity === 'day' ? `${secondary}-${primary}` : `${secondary} ${primary}`
}

export interface TokenAxis {
  /** 轴上限（≥ maxValue，且是刻度步长的整数倍）。 */
  readonly max: number
  /** 刻度值（升序，首项 0，末项 = max）。 */
  readonly ticks: readonly number[]
}

/**
 * Y 轴刻度（1/2/5×10ⁿ 的"圆"数）。maxValue<=0 时退化为 {max:1, ticks:[0,1]}
 * —— 保证永远有可渲染的坐标系，调用方不必做除零保护。
 */
export function buildAxis(maxValue: number, tickCount = 4): TokenAxis {
  if (!(maxValue > 0) || !Number.isFinite(maxValue)) return { max: 1, ticks: [0, 1] }
  const step = niceStep(maxValue / Math.max(1, tickCount))
  const max = Math.max(step, Math.ceil(maxValue / step) * step)
  const ticks: number[] = []
  for (let v = 0; v <= max + step / 1e6 && ticks.length < 12; v += step) ticks.push(Number(v.toFixed(6)))
  if (ticks[ticks.length - 1] !== max) ticks.push(max)
  return { max, ticks }
}

/** 取不超过 v 的 1/2/5×10ⁿ 步长（保证刻度数不少于 tickCount）。 */
function niceStep(v: number): number {
  if (!(v > 0) || !Number.isFinite(v)) return 1
  const exp = Math.floor(Math.log10(v))
  const base = Math.pow(10, exp)
  const norm = v / base
  const mult = norm < 1.5 ? 1 : norm < 3 ? 2 : norm < 7 ? 5 : 10
  return mult * base
}

// ── 曲线几何（平滑堆叠面积） ───────────────────────────────────────────────

export interface TrendPoint {
  readonly x: number
  readonly y: number
}

export interface CubicSegment {
  readonly x0: number
  readonly y0: number
  readonly cx1: number
  readonly cy1: number
  readonly cx2: number
  readonly cy2: number
  readonly x1: number
  readonly y1: number
}

/**
 * 单调三次插值（Fritsch–Carlson）—— 数据间的曲线**不会过冲**（不会画出负 token
 * 的假峰），比 Catmull-Rom 更适合面积图。要求 x 严格升序；点数 < 2 ⇒ 无段。
 */
export function monotoneSegments(points: readonly TrendPoint[]): CubicSegment[] {
  const n = points?.length ?? 0
  if (n < 2) return []

  const d: number[] = new Array(n - 1)
  for (let i = 0; i < n - 1; i++) {
    const dx = points[i + 1].x - points[i].x
    d[i] = dx === 0 ? 0 : (points[i + 1].y - points[i].y) / dx
  }

  const m: number[] = new Array(n)
  m[0] = d[0]
  m[n - 1] = d[n - 2]
  for (let i = 1; i < n - 1; i++) {
    m[i] = d[i - 1] * d[i] <= 0 ? 0 : (d[i - 1] + d[i]) / 2
  }

  for (let i = 0; i < n - 1; i++) {
    if (d[i] === 0) {
      m[i] = 0
      m[i + 1] = 0
      continue
    }
    const a = m[i] / d[i]
    const b = m[i + 1] / d[i]
    const s = a * a + b * b
    if (s > 9) {
      const t = 3 / Math.sqrt(s)
      m[i] = t * a * d[i]
      m[i + 1] = t * b * d[i]
    }
  }

  const segments: CubicSegment[] = []
  for (let i = 0; i < n - 1; i++) {
    const dx = points[i + 1].x - points[i].x
    segments.push({
      x0: points[i].x,
      y0: points[i].y,
      cx1: points[i].x + dx / 3,
      cy1: points[i].y + (m[i] * dx) / 3,
      cx2: points[i + 1].x - dx / 3,
      cy2: points[i + 1].y - (m[i + 1] * dx) / 3,
      x1: points[i + 1].x,
      y1: points[i + 1].y,
    })
  }
  return segments
}

const fmt = (n: number) => (Number.isFinite(n) ? Number(n.toFixed(3)) : 0)

/** 平滑折线路径；空点 ⇒ ''（调用方据此跳过渲染），单点 ⇒ 只有 M。 */
export function monotonePath(points: readonly TrendPoint[]): string {
  if (!points || points.length === 0) return ''
  const head = `M${fmt(points[0].x)},${fmt(points[0].y)}`
  return points.length === 1 ? head : head + segmentsToPath(monotoneSegments(points))
}

/**
 * 堆叠面积路径：上边界正向平滑 → 落到下边界末端 → 下边界**反向**平滑 → Z。
 * `top` / `bottom` 必须同长（不同长时按较短者截断 —— 宁可少画一点也不画错）。
 */
export function stackedAreaPath(top: readonly TrendPoint[], bottom: readonly TrendPoint[]): string {
  const n = Math.min(top?.length ?? 0, bottom?.length ?? 0)
  if (n === 0) return ''
  const t = top.slice(0, n)
  const b = bottom.slice(0, n)
  if (n === 1) return `M${fmt(t[0].x)},${fmt(t[0].y)}L${fmt(b[0].x)},${fmt(b[0].y)}Z`

  const topSegs = monotoneSegments(t)
  const bottomSegs = monotoneSegments(b)
  let path = `M${fmt(t[0].x)},${fmt(t[0].y)}${segmentsToPath(topSegs)}`
  path += `L${fmt(b[n - 1].x)},${fmt(b[n - 1].y)}`
  // 下边界反向遍历：每段反向即交换两端点与控制点。
  for (let i = bottomSegs.length - 1; i >= 0; i--) {
    const s = bottomSegs[i]
    path += `C${fmt(s.cx2)},${fmt(s.cy2)},${fmt(s.cx1)},${fmt(s.cy1)},${fmt(s.x0)},${fmt(s.y0)}`
  }
  return `${path}Z`
}

function segmentsToPath(segments: readonly CubicSegment[]): string {
  let out = ''
  for (const s of segments) {
    out += `C${fmt(s.cx1)},${fmt(s.cy1)},${fmt(s.cx2)},${fmt(s.cy2)},${fmt(s.x1)},${fmt(s.y1)}`
  }
  return out
}
