/**
 * TokenTrendChart —— token 趋势图（堆叠平滑面积 + 缓存命中率曲线 + 十字线 tooltip）。
 *
 * 表现契约：
 *   - 三个序列自底向上堆叠：缓存命中（emerald）/ 未命中输入（sky）/ 输出（violet），
 *     总高 = input + output；面积用**单调三次插值**平滑（不过冲，不画假峰）。
 *   - 缓存命中率单独一条 amber 虚线（右轴 0..100%）；**桶内没有输入 token ⇒ 断开**
 *     （不连线、不显示 0% —— "无数据"与"命中率 0%"是两件事）。
 *   - hover：整列高亮 + 十字线 + 各序列端点 + 浮层明细。
 *   - **只画明细覆盖区间**：服务端明细是 `ORDER BY id DESC LIMIT ≤500`，更早的桶我们
 *     不知道有没有用量 ⇒ 左侧未覆盖区用斜纹留空并标注，绝不画成"零用量"。
 *   - 降级：窗口内无调用 ⇒ 空状态；仅 1 个桶有数据 ⇒ 标记点 + 提示（面积图无意义）。
 *
 * 纯计算（分桶/摘要/路径/标签）全在 ./tokenTrend（纯函数、有单测）；本文件只做布局。
 */
import { useEffect, useId, useMemo, useRef, useState } from 'react'
import { Activity, TrendingUp } from 'lucide-react'
import {
  buildAxis,
  formatBucketLabel,
  formatSpanLabel,
  monotonePath,
  stackedAreaPath,
  summarizeTrend,
  type TokenBucket,
  type TokenTrend,
  type TrendPoint,
} from './tokenTrend'
import { formatCount, formatRatio, formatTokenCount } from './format'
import { t } from './i18n'

const DEFAULT_HEIGHT = 208
const PAD = { top: 14, right: 46, bottom: 24, left: 50 }
/** 单点降级阈值：只有 1 个桶有数据时面积图无意义。 */
const SINGLE_POINT_BUCKETS = 1

/** 序列配色（自底向上）。 */
const SERIES = [
  { key: 'cached', color: '#10b981', dot: 'bg-emerald-500', label: () => t('trend.series.cached', '缓存命中') },
  { key: 'uncached', color: '#0ea5e9', dot: 'bg-sky-500', label: () => t('trend.series.uncached', '未命中输入') },
  { key: 'output', color: '#8b5cf6', dot: 'bg-violet-500', label: () => t('trend.series.output', '输出') },
] as const

const RATE_COLOR = '#fbbf24'
const INK = {
  grid: 'rgba(148, 163, 184, 0.22)',
  text: 'rgba(148, 163, 184, 1)',
  crosshair: 'rgba(148, 163, 184, 0.85)',
  band: 'rgba(148, 163, 184, 0.10)',
  hatch: 'rgba(148, 163, 184, 0.38)',
} as const

/** 容器宽度（ResizeObserver）；jsdom/无布局环境回落 640，保证坐标系可渲染可测。 */
function useElementWidth(ref: React.RefObject<HTMLElement | null>, fallback = 640): number {
  const [width, setWidth] = useState(fallback)
  useEffect(() => {
    const el = ref.current
    if (!el || typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver((entries) => {
      const w = entries[0]?.contentRect?.width ?? 0
      if (w > 0) setWidth(w)
    })
    ro.observe(el)
    return () => ro.disconnect()
  }, [ref])
  return width
}

export interface TokenTrendChartProps {
  readonly trend: TokenTrend
  /** 图表高度（px）。 */
  readonly height?: number
}

export function TokenTrendChart({ trend, height = DEFAULT_HEIGHT }: TokenTrendChartProps) {
  const wrapRef = useRef<HTMLDivElement>(null)
  const width = useElementWidth(wrapRef)
  const uid = useId().replace(/[:]/g, '')
  const [hover, setHover] = useState<number | null>(null)
  const buckets = trend.buckets
  const n = buckets.length
  const summary = useMemo(() => summarizeTrend(trend), [trend])

  const plotW = Math.max(60, width - PAD.left - PAD.right)
  const plotH = Math.max(60, height - PAD.top - PAD.bottom)
  const bandW = plotW / Math.max(1, n)
  const xFor = (i: number) => PAD.left + bandW * (i + 0.5)

  const axis = useMemo(
    () => buildAxis(Math.max(0, ...buckets.map((b) => b.total)), 4),
    [buckets],
  )
  const yFor = (v: number) => PAD.top + plotH - (v / axis.max) * plotH
  const yForRate = (r: number) => PAD.top + plotH - r * plotH

  // 只画覆盖区间（未覆盖区在左侧，用斜纹标注）。
  const areaStart = useMemo(() => buckets.findIndex((b) => b.covered), [buckets])
  const spanIdx = useMemo(
    () => Array.from({ length: Math.max(0, n - Math.max(0, areaStart)) }, (_, k) => Math.max(0, areaStart) + k),
    [n, areaStart],
  )

  const geometry = useMemo(() => {
    const base = spanIdx.map((i) => ({ x: xFor(i), y: yFor(0) }))
    const cachedTop = spanIdx.map((i) => ({ x: xFor(i), y: yFor(buckets[i].cached) }))
    const inputTop = spanIdx.map((i) => ({ x: xFor(i), y: yFor(buckets[i].cached + buckets[i].uncached) }))
    const totalTop = spanIdx.map((i) => ({ x: xFor(i), y: yFor(buckets[i].total) }))
    // 命中率曲线按**连续非空段**断开（桶内无输入 token ⇒ 不连线）
    const rateRuns: TrendPoint[][] = []
    let run: TrendPoint[] = []
    for (const i of spanIdx) {
      const rate = buckets[i].cacheRate
      if (rate === null) {
        if (run.length) rateRuns.push(run)
        run = []
        continue
      }
      run.push({ x: xFor(i), y: yForRate(rate) })
    }
    if (run.length) rateRuns.push(run)
    return { base, cachedTop, inputTop, totalTop, rateRuns }
  }, [spanIdx, buckets, axis.max, width, height])

  const xTicks = useMemo(() => sampleIndices(n, 4), [n])

  // ── 空状态：窗口内一次调用都没有 -------------------------------------------
  if (summary.activeBuckets === 0) {
    return <TrendEmptyState trend={trend} summaryIsEmpty height={height} />
  }

  const hoverBucket: TokenBucket | null = hover !== null ? buckets[hover] ?? null : null

  return (
    <div ref={wrapRef} className="relative select-none" data-testid="token-trend-chart">
      <svg
        width={width}
        height={height}
        className="block overflow-visible"
        role="img"
        aria-label={t('trend.chartAria', 'token 用量趋势图')}
      >
        <defs>
          {SERIES.map((s) => (
            <linearGradient key={s.key} id={`${uid}-${s.key}`} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor={s.color} stopOpacity="0.55" />
              <stop offset="100%" stopColor={s.color} stopOpacity="0.04" />
            </linearGradient>
          ))}
          <linearGradient id={`${uid}-rate`} x1="0" y1="0" x2="1" y2="0">
            <stop offset="0%" stopColor={RATE_COLOR} stopOpacity="0.35" />
            <stop offset="100%" stopColor={RATE_COLOR} stopOpacity="0.95" />
          </linearGradient>
          <pattern id={`${uid}-hatch`} width="6" height="6" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
            <line x1="0" y1="0" x2="0" y2="6" stroke={INK.hatch} strokeWidth="1.5" />
          </pattern>
        </defs>

        {/* ── 网格 + Y 轴刻度 ── */}
        {axis.ticks.map((v) => (
          <g key={`y-${v}`}>
            <line x1={PAD.left} y1={yFor(v)} x2={PAD.left + plotW} y2={yFor(v)} stroke={INK.grid} strokeWidth="1" strokeDasharray={v === 0 ? undefined : '3 4'} />
            <text x={PAD.left - 6} y={yFor(v) + 3} textAnchor="end" fontSize="9" fill={INK.text} fontFamily="ui-monospace, monospace">
              {formatTokenCount(v)}
            </text>
          </g>
        ))}
        {/* 右轴：缓存命中率 0 / 50 / 100% */}
        {[0, 0.5, 1].map((r) => (
          <text key={`r-${r}`} x={PAD.left + plotW + 6} y={yForRate(r) + 3} fontSize="9" fill={RATE_COLOR} opacity="0.75" fontFamily="ui-monospace, monospace">
            {Math.round(r * 100)}%
          </text>
        ))}

        {/* ── 未覆盖区（明细被 LIMIT 截断 / 会话当时不存在）── */}
        {areaStart > 0 && (
          <g data-testid="trend-uncovered">
            <rect
              x={PAD.left}
              y={PAD.top}
              width={Math.max(1, xFor(areaStart) - bandW / 2 - PAD.left)}
              height={plotH}
              fill={`url(#${uid}-hatch)`}
              opacity="0.55"
            />
            <text x={PAD.left + 4} y={PAD.top + 11} fontSize="9" fill={INK.text}>
              {t('trend.uncovered', '未覆盖')}
            </text>
          </g>
        )}

        {/* ── 堆叠面积（自底向上：缓存 / 未命中输入 / 输出）── */}
        {geometry.base.length > 1 && (
          <>
            <path d={stackedAreaPath(geometry.cachedTop, geometry.base)} fill={`url(#${uid}-cached)`} stroke={SERIES[0].color} strokeWidth="1.2" strokeOpacity="0.75" />
            <path d={stackedAreaPath(geometry.inputTop, geometry.cachedTop)} fill={`url(#${uid}-uncached)`} stroke={SERIES[1].color} strokeWidth="1.2" strokeOpacity="0.75" />
            <path d={stackedAreaPath(geometry.totalTop, geometry.inputTop)} fill={`url(#${uid}-output)`} stroke={SERIES[2].color} strokeWidth="1.2" strokeOpacity="0.75" />
          </>
        )}

        {/* ── 缓存命中率曲线 ── */}
        {geometry.rateRuns.map((run, i) =>
          run.length > 1 ? (
            <path key={`rate-${i}`} d={monotonePath(run)} fill="none" stroke={`url(#${uid}-rate)`} strokeWidth="1.6" strokeDasharray="5 3" strokeLinecap="round" />
          ) : (
            <circle key={`rate-dot-${i}`} cx={run[0].x} cy={run[0].y} r="2.4" fill={RATE_COLOR} />
          ),
        )}

        {/* ── 单点降级：标记点（面积图退化成一条线）── */}
        {summary.activeBuckets === SINGLE_POINT_BUCKETS && summary.peak && (
          <g data-testid="trend-single-point-marker">
            <line x1={xFor(buckets.indexOf(summary.peak))} y1={PAD.top} x2={xFor(buckets.indexOf(summary.peak))} y2={PAD.top + plotH} stroke={RATE_COLOR} strokeWidth="1" strokeOpacity="0.5" />
            <circle cx={xFor(buckets.indexOf(summary.peak))} cy={yFor(summary.peak.total)} r="4" fill={SERIES[2].color} stroke="white" strokeWidth="1.5" />
          </g>
        )}

        {/* ── X 轴标签 ── */}
        {xTicks.map((i) => {
          const label = formatBucketLabel(buckets[i].start, trend.granularity, trend.tzOffsetMinutes)
          return (
            <text key={`x-${i}`} x={xFor(i)} y={height - 8} textAnchor="middle" fontSize="9" fill={INK.text} fontFamily="ui-monospace, monospace">
              {label.primary}
            </text>
          )
        })}

        {/* ── hover：整列 + 十字线 + 端点 ── */}
        {hover !== null && hoverBucket && (
          <g pointerEvents="none" data-testid="trend-crosshair">
            <rect x={PAD.left + bandW * hover} y={PAD.top} width={bandW} height={plotH} fill={INK.band} />
            <line x1={xFor(hover)} y1={PAD.top} x2={xFor(hover)} y2={PAD.top + plotH} stroke={INK.crosshair} strokeWidth="1" strokeDasharray="3 3" />
            {hoverBucket.covered && (
              <>
                <circle cx={xFor(hover)} cy={yFor(hoverBucket.cached)} r="2.8" fill={SERIES[0].color} />
                <circle cx={xFor(hover)} cy={yFor(hoverBucket.cached + hoverBucket.uncached)} r="2.8" fill={SERIES[1].color} />
                <circle cx={xFor(hover)} cy={yFor(hoverBucket.total)} r="2.8" fill={SERIES[2].color} />
              </>
            )}
          </g>
        )}

        {/* hover 命中面 */}
        <rect
          data-testid="trend-hover-surface"
          x={PAD.left}
          y={PAD.top}
          width={plotW}
          height={plotH}
          fill="transparent"
          style={{ cursor: 'crosshair' }}
          onPointerMove={(e) => {
            const rect = e.currentTarget.getBoundingClientRect()
            const w = rect.width || plotW
            const rel = e.clientX - rect.left
            const idx = Math.min(n - 1, Math.max(0, Math.floor((rel / w) * n)))
            setHover(idx)
          }}
          onPointerLeave={() => setHover(null)}
        />
      </svg>

      {hover !== null && hoverBucket && (
        <TrendTooltip
          bucket={hoverBucket}
          granularity={trend.granularity}
          tzOffsetMinutes={trend.tzOffsetMinutes}
          total={summary.total}
          left={xFor(hover)}
          width={width}
        />
      )}

      <TrendLegend summary={summary} singlePoint={summary.activeBuckets === SINGLE_POINT_BUCKETS} />
    </div>
  )
}

// ── 浮层 / 图例 / 空态 ─────────────────────────────────────────────────────

function TrendTooltip({
  bucket,
  granularity,
  tzOffsetMinutes,
  total,
  left,
  width,
}: {
  bucket: TokenBucket
  granularity: TokenTrend['granularity']
  tzOffsetMinutes: number
  total: number
  left: number
  width: number
}) {
  const label = formatBucketLabel(bucket.start, granularity, tzOffsetMinutes)
  // 贴边翻转：浮层宽度固定 190px，避免超出容器左右边界。
  const tipW = 190
  const clamped = Math.min(Math.max(left, tipW / 2 + 4), Math.max(width - tipW / 2 - 4, tipW / 2 + 4))
  return (
    <div
      data-testid="trend-tooltip"
      className="pointer-events-none absolute top-1 z-10 -translate-x-1/2 rounded-lg border border-border/60 bg-popover/95 px-2.5 py-2 font-mono text-[10px] tabular-nums shadow-lg backdrop-blur-md"
      style={{ left: clamped, width: tipW }}
    >
      <div className="mb-1 flex items-baseline justify-between gap-2">
        <span className="font-semibold">{label.primary}</span>
        <span className="text-muted-foreground">{label.secondary}</span>
      </div>
      {!bucket.covered ? (
        <div className="text-muted-foreground">{t('trend.bucketUncovered', '该区间不在明细覆盖范围内')}</div>
      ) : (
        <div className="space-y-0.5">
          {SERIES.map((s, i) => (
            <div key={s.key} className="flex items-center justify-between gap-2">
              <span className="flex items-center gap-1.5">
                <span className={`size-2 rounded-sm ${s.dot}`} />
                <span className="text-muted-foreground">{s.label()}</span>
              </span>
              <span>
                {formatTokenCount(i === 0 ? bucket.cached : i === 1 ? bucket.uncached : bucket.output)}
                {total > 0 && (
                  <span className="ml-1 text-muted-foreground">
                    {formatRatio((i === 0 ? bucket.cached : i === 1 ? bucket.uncached : bucket.output) / total, 0)}
                  </span>
                )}
              </span>
            </div>
          ))}
          <div className="mt-1 flex items-center justify-between gap-2 border-t border-border/50 pt-1">
            <span className="text-muted-foreground">{t('trend.bucketTotal', '本桶合计')}</span>
            <span className="font-semibold">{formatTokenCount(bucket.total)}</span>
          </div>
          <div className="flex items-center justify-between gap-2">
            <span className="text-muted-foreground">{t('trend.cacheRate', '缓存命中率')}</span>
            <span style={{ color: RATE_COLOR }}>{formatRatio(bucket.cacheRate)}</span>
          </div>
          <div className="flex items-center justify-between gap-2">
            <span className="text-muted-foreground">{t('trend.calls', '调用次数')}</span>
            <span>{formatCount(bucket.calls)}</span>
          </div>
        </div>
      )}
    </div>
  )
}

function TrendLegend({
  summary,
  singlePoint,
}: {
  summary: ReturnType<typeof summarizeTrend>
  singlePoint: boolean
}) {
  return (
    <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-[10px]">
      {SERIES.map((s, i) => {
        const value = i === 0 ? summary.cached : i === 1 ? summary.uncached : summary.output
        return (
          <span key={s.key} className="flex items-center gap-1.5">
            <span className={`size-2 shrink-0 rounded-sm ${s.dot}`} />
            <span className="text-muted-foreground">{s.label()}</span>
            <span className="font-mono tabular-nums">{formatTokenCount(value)}</span>
          </span>
        )
      })}
      <span className="flex items-center gap-1.5">
        <span className="h-0.5 w-3 shrink-0 rounded-full" style={{ background: RATE_COLOR }} />
        <span className="text-muted-foreground">{t('trend.cacheRate', '缓存命中率')}</span>
        <span className="font-mono tabular-nums" style={{ color: RATE_COLOR }}>
          {formatRatio(summary.cacheRate)}
        </span>
      </span>
      {singlePoint && (
        <span data-testid="trend-single-point-note" className="ml-auto flex items-center gap-1 text-muted-foreground">
          <TrendingUp className="size-3" />
          {t('trend.singlePoint', '该时间窗内只有 1 个时间桶有数据')}
        </span>
      )}
    </div>
  )
}

function TrendEmptyState({
  trend,
  height,
}: {
  trend: TokenTrend
  summaryIsEmpty: boolean
  height: number
}) {
  // 两种"没数据"要说清楚是哪一种：窗口内确实没调用 vs 明细根本不在窗口内。
  const hasSample = trend.sampleSize > 0
  return (
    <div
      data-testid="trend-empty"
      className="flex flex-col items-center justify-center gap-1.5 rounded-lg border border-dashed border-border/60 text-xs text-muted-foreground"
      style={{ height }}
    >
      <Activity className="size-6 opacity-40" />
      <div>{t('trend.empty', '该时间窗内没有调用记录')}</div>
      <div className="text-[10px] opacity-80">
        {hasSample
          ? t('trend.emptyHintSample', '明细里有 {{n}} 条记录，但都不在这个时间窗内 —— 试试更粗的粒度', { n: trend.sampleSize })
          : t('trend.emptyHint', '本会话还没有落库的调用明细')}
      </div>
    </div>
  )
}

/** 均匀采样桶下标（用于 X 轴标签）—— 保证首尾必取。 */
function sampleIndices(n: number, count: number): number[] {
  if (n <= 0) return []
  if (n === 1) return [0]
  const wanted = Math.max(2, Math.min(count, n))
  const out: number[] = []
  for (let i = 0; i < wanted; i++) {
    out.push(Math.round((i * (n - 1)) / (wanted - 1)))
  }
  return [...new Set(out)]
}

/** 供 Section 复用的覆盖区间标题（"覆盖 09-18 13:00 ~ 09-19 12:00"）。 */
export function trendCoverageLabel(trend: TokenTrend): string | null {
  if (trend.oldestSampleAt === null || trend.newestSampleAt === null) return null
  return `${formatSpanLabel(trend.oldestSampleAt, trend.granularity, trend.tzOffsetMinutes)} ~ ${formatSpanLabel(
    trend.newestSampleAt,
    trend.granularity,
    trend.tzOffsetMinutes,
  )}`
}
