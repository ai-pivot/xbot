/**
 * xbot.iteration-stats —— 多粒度趋势面板（view: xbot.iteration-stats.trend）。
 *
 * 形态：右侧栏插件面板（容器 right_sidebar），数据来自核心 RPC
 * `get_session_usage_stats`（iteration_history v59 聚合）。
 *
 * 图表是**手写 SVG**（本 bundle 只有 React 一个外部依赖，不引入图表库）：
 *   - 主图：堆叠面积（缓存命中输入 / 未命中输入 / 输出），三层各自渐变填充；
 *   - 副图：TTFT / TPOT 折线（各自独立缩放，形状可比；hover 显示真实值）；
 *   - hover：十字线 + tooltip（时间桶 + 全指标）；
 *   - 未覆盖区（服务端 LIMIT 500 截断 / 会话当时不存在）：**斜纹留空 + 图例说明**，
 *     绝不当 0 用量画（那是在编数据）；桶内无性能样本的桶同样断开而不是画 0。
 *
 * 状态机：waiting（会话身份未就绪）→ loading → ready / error；空数据有独立空态，
 * 只有 1 个数据点时降级成"单点卡"（曲线至少需要两个点，不画假曲线）。
 */
import {
  React,
  SessionNotReadyError,
  fetchSessionUsage,
  pluginLocale,
  resolveSession,
  subscribeRefresh,
  t,
  type SessionIdentity,
  type SessionUsageStats,
} from './bridge'
import {
  GRANULARITY_ORDER,
  aggregateTrend,
  browserTzOffsetMinutes,
  coveredBucketCount,
  filledBucketCount,
  formatBucketLabel,
  formatBucketTitle,
  formatCoverageRange,
  formatDuration,
  formatRate,
  formatStamp,
  formatTokens,
  hasUncoveredWindow,
  pointerBucketIndex,
  seriesPeak,
  seriesToPoints,
  smoothBandPath,
  smoothLinePath,
  type Granularity,
  type TrendBucket,
  type TrendSeries,
} from './trend'

const { useCallback, useEffect, useMemo, useRef, useState } = React

// ── 视觉常量（固定 hex：插件产物不含宿主主题变量，深浅色都可读）─────────────

const COLORS = {
  cached: '#14b8a6',
  uncached: '#818cf8',
  output: '#f59e0b',
  ttft: '#38bdf8',
  tpot: '#c084fc',
} as const

/** 图表坐标空间（用 preserveAspectRatio="none" 拉伸到容器，路径用相对坐标）。 */
const VIEW_W = 100
const VIEW_H = 100
/** 折线/边界的描边宽度（vector-effect=non-scaling-stroke 下是真实像素）。 */
const STROKE = 1.5

// ── 小组件 ─────────────────────────────────────────────────────────────────

function MetricChip({
  label,
  value,
  hint,
  accent,
}: {
  label: string
  value: string
  hint?: string
  accent?: string
}) {
  return (
    <div
      className="min-w-0 flex-1 rounded-lg border border-border/50 bg-bg-secondary/40 px-2.5 py-1.5"
      title={hint}
    >
      <div className="truncate text-[10px] leading-tight text-muted-foreground">{label}</div>
      <div
        className="truncate font-mono text-sm font-semibold tabular-nums"
        style={accent ? { color: accent } : undefined}
      >
        {value}
      </div>
    </div>
  )
}

function LegendDot({ color, label, value }: { color: string; label: string; value?: string }) {
  return (
    <span className="inline-flex items-center gap-1.5 text-[10px] leading-none">
      <span className="size-2 shrink-0 rounded-sm" style={{ background: color }} />
      <span className="text-muted-foreground">{label}</span>
      {value ? <span className="font-mono tabular-nums">{value}</span> : null}
    </span>
  )
}

/** 骨架屏（首次加载）：与最终布局同形，避免高度跳动。 */
function Skeleton() {
  return (
    <div className="space-y-2 p-3" data-testid="iter-stats-loading">
      <div className="h-4 w-28 animate-pulse rounded bg-bg-secondary" />
      <div className="h-[120px] animate-pulse rounded-lg bg-bg-secondary/70" />
      <div className="h-[70px] animate-pulse rounded-lg bg-bg-secondary/50" />
    </div>
  )
}

/** 状态卡（waiting / error / empty / single 共用的外壳）。 */
function StateCard({
  testid,
  icon,
  title,
  hint,
  action,
}: {
  testid: string
  icon: string
  title: string
  hint?: string
  action?: React.ReactNode
}) {
  return (
    <div
      className="m-3 rounded-xl border border-dashed border-border/70 bg-bg-secondary/30 px-4 py-6 text-center"
      data-testid={testid}
    >
      <div className="text-2xl leading-none" aria-hidden="true">
        {icon}
      </div>
      <div className="mt-2 text-sm font-medium">{title}</div>
      {hint ? <div className="mt-1 text-[11px] leading-relaxed text-muted-foreground">{hint}</div> : null}
      {action ? <div className="mt-3 flex justify-center">{action}</div> : null}
    </div>
  )
}

// ── 主图：堆叠面积 + hover 十字线 + tooltip ─────────────────────────────────

interface ChartProps {
  series: TrendSeries
  hover: number | null
  onHover: (index: number | null) => void
}

function bandValues(buckets: readonly TrendBucket[], pick: (b: TrendBucket) => number): (number | null)[] {
  return buckets.map((b) => (b.covered ? pick(b) : null))
}

function TokenAreaChart({ series, hover, onHover }: ChartProps) {
  const buckets = series.buckets
  const peak = seriesPeak(series)
  // 未覆盖桶不画（null 断开）：LIMIT 截断的历史不能冒充"没用量"。
  const zero = buckets.map(() => 0)
  const cachedTop = bandValues(buckets, (b) => b.cached)
  const inputTop = bandValues(buckets, (b) => b.cached + b.uncached)
  const totalTop = bandValues(buckets, (b) => b.total)
  const maxValue = Math.max(peak.total, 1)

  const toPoints = (values: (number | null)[]) => seriesToPoints(values, VIEW_W, VIEW_H, maxValue)
  const bands: Array<{ key: string; lower: (number | null)[]; upper: (number | null)[]; color: string }> = [
    { key: 'cached', lower: zero, upper: cachedTop, color: COLORS.cached },
    { key: 'uncached', lower: cachedTop, upper: inputTop, color: COLORS.uncached },
    { key: 'output', lower: inputTop, upper: totalTop, color: COLORS.output },
  ]

  const hoverPoint = hover !== null ? buckets[hover] : undefined
  const hoverY =
    hoverPoint && hoverPoint.covered ? VIEW_H - (Math.min(hoverPoint.total, maxValue) / maxValue) * VIEW_H : null
  const hoverLeft = hover !== null && buckets.length > 1 ? `${(hover / (buckets.length - 1)) * 100}%` : null

  const handleMove = (event: React.MouseEvent<HTMLDivElement>) => {
    const rect = event.currentTarget.getBoundingClientRect()
    onHover(pointerBucketIndex(event.clientX, rect.left, rect.width, buckets.length))
  }

  const yTicks = [maxValue, maxValue / 2, 0]
  const xTickIdx = buildTickIndices(buckets.length, 5)

  return (
    <div className="space-y-1.5" data-testid="iter-token-chart">
      <div className="flex items-baseline justify-between gap-2">
        <span className="text-[11px] font-medium">{t('section.tokens', 'Token 用量')}</span>
        <span className="font-mono text-[10px] text-muted-foreground">
          {t('peak', '峰值')} {formatTokens(peak.total)}
        </span>
      </div>
      <div className="flex gap-1.5">
        {/* Y 轴刻度（HTML：SVG 被拉伸，文字放里面会变形） */}
        <div className="flex w-9 shrink-0 flex-col justify-between py-[1px] text-right font-mono text-[9px] leading-none text-muted-foreground">
          {yTicks.map((v, i) => (
            <span key={i}>{formatTokens(v)}</span>
          ))}
        </div>
        <div
          className="relative h-[132px] min-w-0 flex-1 cursor-crosshair rounded-lg border border-border/40 bg-gradient-to-b from-bg-secondary/50 to-bg-secondary/10"
          onMouseMove={handleMove}
          onMouseLeave={() => onHover(null)}
          data-testid="iter-chart-surface"
        >
          <svg
            viewBox={`0 0 ${VIEW_W} ${VIEW_H}`}
            preserveAspectRatio="none"
            className="size-full overflow-visible"
            aria-hidden="true"
          >
            <defs>
              {bands.map((band) => (
                <linearGradient key={band.key} id={`iter-grad-${band.key}`} x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor={band.color} stopOpacity="0.55" />
                  <stop offset="100%" stopColor={band.color} stopOpacity="0.06" />
                </linearGradient>
              ))}
            </defs>
            {/* 水平网格线 */}
            {[0, 0.5, 1].map((f) => (
              <line
                key={f}
                x1="0"
                x2={VIEW_W}
                y1={f * VIEW_H}
                y2={f * VIEW_H}
                stroke="currentColor"
                strokeOpacity="0.12"
                strokeDasharray="2 3"
                vectorEffect="non-scaling-stroke"
              />
            ))}
            {bands.map((band) => {
              const d = smoothBandPath(toPoints(band.upper), toPoints(band.lower))
              if (!d) return null
              return <path key={band.key} d={d} fill={`url(#iter-grad-${band.key})`} stroke="none" />
            })}
            {/* 堆叠总量轮廓线（让"总量"这一层更清晰） */}
            <path
              d={smoothLinePath(toPoints(totalTop))}
              fill="none"
              stroke={COLORS.output}
              strokeOpacity="0.85"
              strokeWidth={STROKE}
              vectorEffect="non-scaling-stroke"
              strokeLinejoin="round"
              strokeLinecap="round"
            />
            {/* hover 水平基准线 */}
            {hoverY !== null ? (
              <line
                x1="0"
                x2={VIEW_W}
                y1={hoverY}
                y2={hoverY}
                stroke={COLORS.output}
                strokeOpacity="0.5"
                strokeDasharray="3 3"
                vectorEffect="non-scaling-stroke"
              />
            ) : null}
          </svg>

          {/* 未覆盖区（样本未延伸到此处）：斜纹留空 + 不画数值 */}
          <UnknownRegions series={series} />

          {/* hover 十字线 + 数据点 + tooltip（HTML 层，避免被非等比缩放拉伸） */}
          {hoverLeft !== null ? (
            <div
              className="pointer-events-none absolute inset-y-0 w-px bg-foreground/40"
              style={{ left: hoverLeft }}
              data-testid="iter-hover-crosshair"
            />
          ) : null}
          {hoverLeft !== null && hoverY !== null ? (
            <div
              className="pointer-events-none absolute size-1.5 -translate-x-1/2 -translate-y-1/2 rounded-full border border-white/80"
              style={{
                left: hoverLeft,
                top: `${(hoverY / VIEW_H) * 100}%`,
                background: COLORS.output,
              }}
            />
          ) : null}
          {hoverPoint ? (
            <BucketTooltip bucket={hoverPoint} series={series} index={hover!} total={buckets.length} />
          ) : null}
        </div>
      </div>
      {/* X 轴标签 */}
      <div className="flex justify-between pl-[42px] font-mono text-[9px] leading-none text-muted-foreground">
        {xTickIdx.map((i) => (
          <span key={i}>{formatBucketLabel(buckets[i]!.start, series.granularity, series.tzOffsetMinutes)}</span>
        ))}
      </div>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 pl-[42px]">
        <LegendDot color={COLORS.cached} label={t('legend.cached', '缓存命中输入')} />
        <LegendDot color={COLORS.uncached} label={t('legend.uncached', '未命中输入')} />
        <LegendDot color={COLORS.output} label={t('legend.output', '输出')} />
        {hasUncoveredWindow(series) ? (
          <LegendDot color="repeating-linear-gradient(45deg,#94a3b8 0 2px,transparent 2px 5px)" label={`${t('legend.unknown', '无样本')}（${t('legend.unknownHint', '未纳入统计，非 0 用量')}）`} />
        ) : null}
      </div>
    </div>
  )
}

/** 未覆盖区间（前缀 + 尾段）——斜纹覆盖，明确区别于"有覆盖的空桶（真实的 0）"。 */
function UnknownRegions({ series }: { series: TrendSeries }) {
  const n = series.buckets.length
  if (n === 0 || !hasUncoveredWindow(series)) return null
  const runs: Array<{ from: number; to: number }> = []
  let start: number | null = null
  for (let i = 0; i < n; i++) {
    const uncovered = !series.buckets[i]!.covered
    if (uncovered && start === null) start = i
    if (!uncovered && start !== null) {
      runs.push({ from: start, to: i - 1 })
      start = null
    }
  }
  if (start !== null) runs.push({ from: start, to: n - 1 })
  return (
    <>
      {runs.map((run) => (
        <div
          key={`${run.from}-${run.to}`}
          className="pointer-events-none absolute inset-y-0"
          data-testid="iter-stats-unknown-region"
          title={t('unknownRegion.title', '无样本数据（超出 500 条明细覆盖范围，不代表 0 用量）')}
          style={{
            left: `${(run.from / n) * 100}%`,
            width: `${((run.to - run.from + 1) / n) * 100}%`,
            backgroundImage:
              'repeating-linear-gradient(45deg, rgba(148,163,184,0.28) 0 2px, rgba(148,163,184,0) 2px 6px)',
          }}
        />
      ))}
    </>
  )
}

function BucketTooltip({
  bucket,
  series,
  index,
  total,
}: {
  bucket: TrendBucket
  series: TrendSeries
  index: number
  total: number
}) {
  const alignRight = index > total * 0.6
  const rows: Array<{ label: string; value: string; color?: string }> = [
    { label: t('metric.input', '输入 token'), value: formatTokens(bucket.input) },
    { label: t('metric.cached', '缓存命中'), value: formatTokens(bucket.cached), color: COLORS.cached },
    { label: t('metric.output', '输出 token'), value: formatTokens(bucket.output), color: COLORS.output },
    { label: t('metric.iterations', '迭代'), value: String(bucket.iterations) },
    {
      label: t('metric.cacheRate', '命中率'),
      value: formatRate(bucket.cacheRate) ?? t('value.none', '—'),
    },
    {
      label: t('metric.ttft', 'TTFT'),
      value: formatDuration(bucket.ttftMs) ?? t('value.none', '—'),
      color: COLORS.ttft,
    },
    {
      label: t('metric.tpot', 'TPOT'),
      value: formatDuration(bucket.tpotMs) ?? t('value.none', '—'),
      color: COLORS.tpot,
    },
    {
      label: t('metric.tokensPerSec', '吞吐'),
      value: bucket.tokensPerSec ? `${Math.round(bucket.tokensPerSec)} tok/s` : t('value.none', '—'),
    },
  ]
  return (
    <div
      className="pointer-events-none absolute top-1 z-20 w-[168px] rounded-lg border border-border/70 bg-popover/95 p-2 shadow-lg backdrop-blur-sm"
      data-testid="iter-stats-tooltip"
      style={alignRight ? { right: '4px' } : { left: '4px' }}
    >
      <div className="mb-1 font-mono text-[10px] font-semibold tabular-nums">
        {formatBucketTitle(bucket.start, series.granularity, series.tzOffsetMinutes)}
      </div>
      <div className="space-y-0.5">
        {rows.map((r) => (
          <div key={r.label} className="flex items-center justify-between gap-2 text-[10px]">
            <span className="text-muted-foreground">{r.label}</span>
            <span className="font-mono tabular-nums" style={r.color ? { color: r.color } : undefined}>
              {r.value}
            </span>
          </div>
        ))}
      </div>
      {!bucket.covered ? (
        <div className="mt-1 border-t border-border/50 pt-1 text-[9px] text-amber-500">
          {t('tooltip.unknown', '该时间段无样本（不代表 0 用量）')}
        </div>
      ) : null}
    </div>
  )
}

// ── 副图：性能折线（TTFT / TPOT）──────────────────────────────────────────

function PerfLineChart({ series, hover, onHover }: ChartProps) {
  const buckets = series.buckets
  const peak = seriesPeak(series)
  // 两条线各自独立缩放（TTFT ~10^3ms / TPOT ~10^1ms 混在同轴会压成一条线）。
  const ttftPts = seriesToPoints(bucketField(buckets, 'ttftMs'), VIEW_W, VIEW_H, Math.max(peak.ttftMs, 1))
  const tpotPts = seriesToPoints(bucketField(buckets, 'tpotMs'), VIEW_W, VIEW_H, Math.max(peak.tpotMs, 1))

  const handleMove = (event: React.MouseEvent<HTMLDivElement>) => {
    const rect = event.currentTarget.getBoundingClientRect()
    onHover(pointerBucketIndex(event.clientX, rect.left, rect.width, buckets.length))
  }
  const hoverLeft = hover !== null && buckets.length > 1 ? `${(hover / (buckets.length - 1)) * 100}%` : null

  return (
    <div className="space-y-1.5" data-testid="iter-perf-chart">
      <div className="flex items-baseline justify-between gap-2">
        <span className="text-[11px] font-medium">{t('section.perf', '性能（TTFT / TPOT）')}</span>
        <span className="truncate text-[9px] text-muted-foreground">{t('perf.note', '两条曲线各自独立缩放')}</span>
      </div>
      <div
        className="relative h-[76px] rounded-lg border border-border/40 bg-gradient-to-b from-bg-secondary/40 to-bg-secondary/10"
        onMouseMove={handleMove}
        onMouseLeave={() => onHover(null)}
        data-testid="iter-perf-surface"
      >
        <svg
          viewBox={`0 0 ${VIEW_W} ${VIEW_H}`}
          preserveAspectRatio="none"
          className="size-full overflow-visible"
          aria-hidden="true"
        >
          {[0, 0.5, 1].map((f) => (
            <line
              key={f}
              x1="0"
              x2={VIEW_W}
              y1={f * VIEW_H}
              y2={f * VIEW_H}
              stroke="currentColor"
              strokeOpacity="0.12"
              strokeDasharray="2 3"
              vectorEffect="non-scaling-stroke"
            />
          ))}
          <path
            d={smoothLinePath(ttftPts)}
            fill="none"
            stroke={COLORS.ttft}
            strokeWidth={STROKE}
            vectorEffect="non-scaling-stroke"
            strokeLinejoin="round"
            strokeLinecap="round"
          />
          <path
            d={smoothLinePath(tpotPts)}
            fill="none"
            stroke={COLORS.tpot}
            strokeWidth={STROKE}
            vectorEffect="non-scaling-stroke"
            strokeLinejoin="round"
            strokeLinecap="round"
          />
        </svg>
        {hoverLeft !== null ? (
          <div
            className="pointer-events-none absolute inset-y-0 w-px bg-foreground/40"
            style={{ left: hoverLeft }}
            data-testid="iter-perf-crosshair"
          />
        ) : null}
      </div>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <LegendDot
          color={COLORS.ttft}
          label={`${t('metric.ttft', 'TTFT')} · ${t('peak', '峰值')}`}
          value={formatDuration(peak.ttftMs) ?? t('value.none', '—')}
        />
        <LegendDot
          color={COLORS.tpot}
          label={`${t('metric.tpot', 'TPOT')} · ${t('peak', '峰值')}`}
          value={formatDuration(peak.tpotMs) ?? t('value.none', '—')}
        />
      </div>
    </div>
  )
}

function bucketField(buckets: readonly TrendBucket[], field: 'ttftMs' | 'tpotMs'): (number | null)[] {
  return buckets.map((b) => {
    if (!b.covered) return null
    const v = b[field]
    return v === null ? null : v
  })
}

// ── 单点降级卡 ─────────────────────────────────────────────────────────────

function SinglePointCard({ series }: { series: TrendSeries }) {
  const bucket = series.buckets.find((b) => b.iterations > 0)
  if (!bucket) return null
  return (
    <div
      className="space-y-2 rounded-xl border border-border/60 bg-gradient-to-br from-bg-secondary/60 to-bg-secondary/20 p-3"
      data-testid="iter-stats-single"
    >
      <div className="flex items-baseline justify-between gap-2">
        <span className="text-[11px] font-medium">{t('single.title', '仅 1 个数据点')}</span>
        <span className="font-mono text-[10px] tabular-nums text-muted-foreground">
          {formatBucketTitle(bucket.start, series.granularity, series.tzOffsetMinutes)}
        </span>
      </div>
      <div className="grid grid-cols-2 gap-1.5 sm:grid-cols-4">
        <MetricChip label={t('metric.input', '输入 token')} value={formatTokens(bucket.input)} />
        <MetricChip
          label={t('metric.cached', '缓存命中')}
          value={formatTokens(bucket.cached)}
          accent={COLORS.cached}
        />
        <MetricChip label={t('metric.output', '输出 token')} value={formatTokens(bucket.output)} accent={COLORS.output} />
        <MetricChip
          label={t('metric.cacheRate', '命中率')}
          value={formatRate(bucket.cacheRate) ?? t('value.none', '—')}
        />
      </div>
      <div className="text-[10px] leading-relaxed text-muted-foreground">
        {t('single.hint', '数据不足以绘制趋势曲线（至少需要两个时间桶有样本）。')}
      </div>
    </div>
  )
}

// ── 面板主体 ───────────────────────────────────────────────────────────────

type Status = 'waiting' | 'loading' | 'ready' | 'error'

function buildTickIndices(count: number, wanted: number): number[] {
  if (count <= 0) return []
  if (count === 1) return [0]
  const n = Math.min(wanted, count)
  const out: number[] = []
  for (let i = 0; i < n; i++) {
    const idx = Math.round((i / (n - 1)) * (count - 1))
    if (out[out.length - 1] !== idx) out.push(idx)
  }
  return out
}

export function IterationStatsTrendPanel() {
  const [identity, setIdentity] = useState<SessionIdentity | null>(() => resolveSession())
  const [status, setStatus] = useState<Status>(() => (resolveSession() ? 'loading' : 'waiting'))
  const [stats, setStats] = useState<SessionUsageStats | null>(null)
  const [error, setError] = useState('')
  const [granularity, setGranularity] = useState<Granularity>('hour')
  const [nowMs, setNowMs] = useState(() => Date.now())
  const [busy, setBusy] = useState(false)
  const [hover, setHover] = useState<number | null>(null)
  const reqRef = useRef(0)

  const load = useCallback(async () => {
    const req = ++reqRef.current
    const chat = resolveSession()
    setIdentity(chat)
    if (!chat) {
      setStatus('waiting')
      return
    }
    setBusy(true)
    try {
      const res = await fetchSessionUsage()
      if (reqRef.current !== req) return
      setStats(res)
      setNowMs(Date.now())
      setStatus('ready')
      setError('')
    } catch (err) {
      if (reqRef.current !== req) return
      if (err instanceof SessionNotReadyError) {
        setStatus('waiting')
        return
      }
      setError(err instanceof Error ? err.message : String(err))
      setStatus('error')
    } finally {
      if (reqRef.current === req) setBusy(false)
    }
  }, [])

  // 初次加载 + turn.ended / session.switched 触发的刷新。
  useEffect(() => {
    void load()
    return subscribeRefresh(() => void load())
  }, [load])

  // 身份未就绪时轮询重读（布局恢复的 tab 可能先于会话加载 mount）。
  useEffect(() => {
    if (identity) return
    const timer = window.setInterval(() => void load(), 3000)
    return () => window.clearInterval(timer)
  }, [identity, load])

  const tz = useMemo(() => browserTzOffsetMinutes(), [])
  // locale 只作诊断标记（data-locale）：文案由 t() 实时读 ctx.i18n 解析 ——
  // 宿主切语言会重渲染插件视图，届时取到的就是新语言。
  const locale = pluginLocale()
  const rows = stats?.recent_iterations ?? []
  const series = useMemo(
    () => aggregateTrend(rows, granularity, nowMs, tz),
    [rows, granularity, nowMs, tz],
  )
  const filled = filledBucketCount(series)
  const peak = useMemo(() => seriesPeak(series), [series])
  const coverage = formatCoverageRange(series)
  const total = series.totals

  if (status === 'waiting') {
    return (
      <div className="h-full overflow-y-auto" data-locale={locale} data-testid="iter-stats-trend">
        <StateCard
          testid="iter-stats-waiting"
          icon="⏳"
          title={t('waiting.title', '等待会话就绪…')}
          hint={t('waiting.hint', '拿到当前会话身份后会自动加载统计（无需手动操作）。')}
        />
      </div>
    )
  }

  if (status === 'loading' && !stats) {
    return (
      <div className="h-full overflow-y-auto" data-locale={locale} data-testid="iter-stats-trend">
        <Skeleton />
      </div>
    )
  }

  if (status === 'error') {
    return (
      <div className="h-full overflow-y-auto" data-locale={locale} data-testid="iter-stats-trend">
        <StateCard
          testid="iter-stats-error"
          icon="⚠️"
          title={t('error.title', '加载失败')}
          hint={error}
          action={
            <button
              type="button"
              className="rounded-md border border-border/70 px-2.5 py-1 text-[11px] hover:bg-bg-secondary"
              onClick={() => void load()}
            >
              {t('retry', '重试')}
            </button>
          }
        />
      </div>
    )
  }

  return (
    <div
      className="flex h-full flex-col gap-2.5 overflow-y-auto p-2.5"
      data-locale={locale}
      data-testid="iter-stats-trend"
    >
      {/* 标题行：粒度切换 + 刷新 */}
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="truncate text-xs font-semibold">{t('title', '会话用量趋势')}</div>
        <div className="flex shrink-0 items-center gap-1.5">
          <div
            className="flex overflow-hidden rounded-md border border-border/60 bg-bg-secondary/40"
            role="group"
            aria-label={t('granularityLabel', '粒度')}
            data-testid="iter-granularity"
          >
            {GRANULARITY_ORDER.map((g) => (
              <button
                key={g}
                type="button"
                data-granularity={g}
                aria-pressed={granularity === g}
                className={`px-2 py-0.5 text-[10px] transition-colors ${
                  granularity === g ? 'bg-primary/15 font-semibold text-primary' : 'text-muted-foreground hover:bg-bg-secondary'
                }`}
                onClick={() => {
                  setGranularity(g)
                  setHover(null)
                }}
              >
                {t(`granularity.${g}`, g === 'minute' ? '分钟' : g === 'hour' ? '小时' : '天')}
              </button>
            ))}
          </div>
          <button
            type="button"
            className="rounded-md border border-border/60 px-1.5 py-0.5 text-[11px] leading-none text-muted-foreground hover:bg-bg-secondary disabled:opacity-50"
            onClick={() => void load()}
            disabled={busy}
            title={t('refresh', '刷新')}
            data-testid="iter-refresh"
          >
            {busy ? '◌' : '↻'}
          </button>
        </div>
      </div>

      {/* 总览 chips */}
      <div className="grid grid-cols-2 gap-1.5 sm:grid-cols-4">
        <MetricChip label={t('overview.total', '输入合计')} value={formatTokens(total.input)} />
        <MetricChip
          label={t('metric.cacheRate', '命中率')}
          value={formatRate(total.cacheRate) ?? t('value.none', '—')}
          accent={COLORS.cached}
          hint={`${t('metric.cached', '缓存命中')} ${formatTokens(total.cached)}`}
        />
        <MetricChip label={t('metric.output', '输出 token')} value={formatTokens(total.output)} accent={COLORS.output} />
        <MetricChip
          label={t('metric.iterations', '迭代')}
          value={String(total.iterations)}
          hint={`${t('peak', '峰值')} ${formatTokens(peak.total)} / ${t('granularity.' + granularity, granularity)}`}
        />
      </div>

      {filled === 0 ? (
        <StateCard
          testid="iter-stats-empty"
          icon="📉"
          title={t('empty.title', '当前窗口没有用量数据')}
          hint={
            series.oldestSampleAt !== null
              ? t('empty.staleHint', '最近一次迭代在 {{at}}（不在当前窗口内）—— 试试更粗的粒度。', {
                  at: formatStamp(series.oldestSampleAt, tz),
                })
              : t('empty.hint', '本会话还没有 LLM 迭代记录 —— 发一条消息后即可看到趋势。')
          }
        />
      ) : filled === 1 ? (
        <SinglePointCard series={series} />
      ) : (
        <div className="space-y-3 rounded-xl border border-border/60 bg-card/60 p-2.5 shadow-sm backdrop-blur-sm">
          <TokenAreaChart series={series} hover={hover} onHover={setHover} />
          <div className="h-px bg-border/50" />
          <PerfLineChart series={series} hover={hover} onHover={setHover} />
        </div>
      )}

      {/* 覆盖范围：诚实标注 LIMIT 500 的边界 */}
      <div className="space-y-0.5 text-[9px] leading-relaxed text-muted-foreground" data-testid="iter-stats-coverage">
        {coverage ? (
          <div>
            {t('coverage.range', '仅最近 {{count}} 次迭代：{{range}}', {
              count: series.sampleSize,
              range: coverage,
            })}
          </div>
        ) : null}
        <div>
          {t('coverage.hint', '单次查询最多返回 500 条迭代明细，更早的历史不在此图内（未知 ≠ 0）。')}
        </div>
        {series.unparsableRows > 0 ? (
          <div className="text-amber-500">
            {t('coverage.unparsable', '{{count}} 条明细时间戳无法解析，已跳过。', {
              count: series.unparsableRows,
            })}
          </div>
        ) : null}
        <div>
          {t('coverage.buckets', '覆盖桶 {{covered}} / {{total}}', {
            covered: coveredBucketCount(series),
            total: series.buckets.length,
          })}
        </div>
      </div>
    </div>
  )
}

export default IterationStatsTrendPanel
