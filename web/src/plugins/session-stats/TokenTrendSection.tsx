/**
 * TokenTrendSection —— 趋势卡片（毛玻璃容器 + 粒度分段控件 + 摘要 + 图表）。
 *
 * 数据面（两级，优先全量）：
 *   1. `loadBuckets`（`get_session_usage_buckets`）—— 服务端在 SQL 里对**全量**
 *      iteration_history 分桶聚合 ⇒ 覆盖整段历史，没有"未覆盖区"。
 *   2. 回落到 per-iteration 明细（`get_session_usage_stats.recent_iterations`）——
 *      ⛔ 服务端明细被 `ORDER BY id DESC LIMIT ≤500` 截断，所以窗口越宽、越可能
 *      "左边没有数据"。这里**如实呈现**：未覆盖的时间桶不画（斜纹留空 + 标注），
 *      绝不画成"零用量"；底部给出行数 / 覆盖区间 / 未解析行数等数据质量信息。
 *      （旧后端没有 buckets RPC 时走这条，降级逻辑**必须保留**。）
 *
 * 聚合全部在 ./tokenTrend（纯函数、单测覆盖）；本组件只管取数状态与外观。
 */
import { useEffect, useMemo, useState } from 'react'
import { CalendarDays, CalendarRange, Clock, Database } from 'lucide-react'
import type { UsageBucket, UsageIterationRow } from '@/plugin-api'
import {
  browserTzOffsetMinutes,
  bucketizeUsageTrend,
  bucketizeUsageTrendFromBuckets,
  summarizeTrend,
  trendGranularitySpec,
  formatBucketLabel,
  TREND_GRANULARITY_ORDER,
  type TrendGranularity,
} from './tokenTrend'
import { formatCount, formatRatio, formatTokenCount } from './format'
import { t } from './i18n'
import { TokenTrendChart, trendCoverageLabel } from './TokenTrendChart'

/** 粒度按钮的文案与图标（窗口描述由 spec 推导，避免两处硬编码）。 */
const GRANULARITY_META: Record<TrendGranularity, { icon: typeof Clock; labelKey: string; label: string; windowKey: string; window: string }> = {
  minute: { icon: Clock, labelKey: 'trend.granularity.minute', label: '分钟', windowKey: 'trend.window.minute', window: '最近 {{n}} 分钟' },
  hour: { icon: CalendarDays, labelKey: 'trend.granularity.hour', label: '小时', windowKey: 'trend.window.hour', window: '最近 {{n}} 小时' },
  day: { icon: CalendarRange, labelKey: 'trend.granularity.day', label: '天', windowKey: 'trend.window.day', window: '最近 {{n}} 天' },
}

/**
 * 请求窗口余量：服务端用**自己的** now 计算窗口，可能比本地的 `now` 晚一个桶
 * （响应必在请求之后）。多要 2 个桶再由前端按本地窗口裁剪 ⇒ 本地窗口恒被覆盖，
 * 不会因为一两个桶的时钟偏差把窗口左端画成空的。
 */
const BUCKET_WINDOW_MARGIN = 2

export interface TokenTrendSectionProps {
  /** per-iteration 明细（顺序无关）—— `loadBuckets` 不可用时的回落数据源。 */
  readonly rows: readonly UsageIterationRow[]
  /**
   * 窗口右端基准（"数据是什么时候取的"）—— 由调用方显式传入：
   * 渲染期调用 `Date.now()` 违反 react-hooks/purity（组件必须是纯函数），
   * 而且用"取数时刻"当窗口右端语义更准（窗口与数据快照对齐）。
   */
  readonly now: number
  /**
   * 服务端全量分桶取数（`get_session_usage_buckets`）。`count` 已含窗口余量。
   * 返回 null / 抛错（旧后端没有该 RPC / 请求失败）⇒ 自动回落到 `rows` 明细路径。
   */
  readonly loadBuckets?: (
    granularity: TrendGranularity,
    count: number,
  ) => Promise<readonly UsageBucket[] | null>
}

export function TokenTrendSection({ rows, now, loadBuckets }: TokenTrendSectionProps) {
  const [granularity, setGranularity] = useState<TrendGranularity>('hour')
  const tzOffsetMinutes = useMemo(() => browserTzOffsetMinutes(), [])
  /** 服务端全量桶；null = 不可用（走明细回落路径）。 */
  const [serverBuckets, setServerBuckets] = useState<readonly UsageBucket[] | null>(null)

  // 粒度切换 / 重新取数（now 变化）时重拉全量桶。失败即降级（绝不阻塞渲染）。
  useEffect(() => {
    if (!loadBuckets || !(now > 0)) {
      setServerBuckets(null)
      return
    }
    let cancelled = false
    const count = trendGranularitySpec(granularity).windowBuckets + BUCKET_WINDOW_MARGIN
    loadBuckets(granularity, count)
      .then((result) => {
        if (!cancelled) setServerBuckets(Array.isArray(result) ? result : null)
      })
      .catch(() => {
        if (!cancelled) setServerBuckets(null)
      })
    return () => {
      cancelled = true
    }
  }, [loadBuckets, granularity, now])

  const trend = useMemo(
    () =>
      serverBuckets !== null
        ? bucketizeUsageTrendFromBuckets(serverBuckets, granularity, now, tzOffsetMinutes)
        : bucketizeUsageTrend(rows, granularity, now, tzOffsetMinutes),
    [serverBuckets, rows, granularity, tzOffsetMinutes, now],
  )
  const summary = useMemo(() => summarizeTrend(trend), [trend])

  const spec = trendGranularitySpec(granularity)
  const meta = GRANULARITY_META[granularity]
  const coverage = trendCoverageLabel(trend)
  const peakLabel = summary.peak
    ? formatBucketLabel(summary.peak.start, granularity, tzOffsetMinutes).primary
    : '—'

  return (
    <section
      data-testid="trend-section"
      className="relative overflow-hidden rounded-xl border border-border/50 bg-bg-secondary/20 p-3 shadow-sm backdrop-blur-xl"
    >
      {/* 装饰性顶部高光（纯视觉，不挡交互） */}
      <div aria-hidden className="pointer-events-none absolute inset-x-0 top-0 h-24 bg-gradient-to-b from-sky-500/[0.07] via-violet-500/[0.03] to-transparent" />

      <header className="relative mb-2 flex flex-wrap items-center justify-between gap-2">
        <div className="min-w-0">
          <h3 className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
            {t('trend.title', 'token 趋势')}
          </h3>
          <p className="truncate text-[10px] text-muted-foreground/80">
            {t(meta.windowKey, meta.window, { n: spec.windowBuckets })}
            {trend.dataSource === 'buckets'
              ? ` · ${t('trend.fullCoverage', '全量聚合')}`
              : coverage
                ? ` · ${t('trend.coverage', '覆盖')} ${coverage}`
                : ''}
          </p>
        </div>
        <GranularitySwitch value={granularity} onChange={setGranularity} />
      </header>

      {/* 摘要条 */}
      <div className="relative mb-2 grid grid-cols-2 gap-2 sm:grid-cols-4">
        <MiniStat
          label={t('trend.summaryTotal', '窗口总量')}
          value={formatTokenCount(summary.total)}
          sub={t('trend.callsCount', '{{n}} 次调用', { n: formatCount(summary.calls) })}
        />
        <MiniStat
          label={t('trend.cacheRateAxis', '缓存命中率')}
          value={formatRatio(summary.cacheRate)}
          sub={`${formatTokenCount(summary.cached)} / ${formatTokenCount(summary.input)}`}
          tone="text-amber-500"
        />
        <MiniStat
          label={t('trend.peakBucket', '峰值区间')}
          value={peakLabel}
          sub={summary.peak ? formatTokenCount(summary.peak.total) : '—'}
        />
        <MiniStat
          label={t('trend.activeBuckets', '活跃区间')}
          value={`${summary.activeBuckets}`}
          sub={t('trend.bucketsCount', '/ {{n}} 个时间桶', { n: spec.windowBuckets })}
        />
      </div>

      <div className="relative">
        <TokenTrendChart trend={trend} />
      </div>

      {/* 数据质量 / 诚实说明（有情况才出现，不刷屏） */}
      {(summary.uncoveredBuckets > 0 || trend.unparsableRows > 0 || trend.sampleSize > 0) && (
        <footer className="relative mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-border/40 pt-1.5 text-[10px] text-muted-foreground">
          {trend.dataSource === 'buckets' ? (
            <span data-testid="trend-bucket-source" className="flex items-center gap-1">
              <Database className="size-3" />
              {t('trend.bucketSource', '服务端全量分桶 · {{n}} 个非空时间桶', { n: formatCount(trend.sampleSize) })}
            </span>
          ) : (
            <span className="flex items-center gap-1">
              <Database className="size-3" />
              {t('trend.sampleSize', '明细 {{n}} 行', { n: formatCount(trend.sampleSize) })}
            </span>
          )}
          {summary.uncoveredBuckets > 0 && (
            <span data-testid="trend-uncovered-note">
              {t('trend.uncoveredNote', '{{n}} 个更早的时间桶不在明细内（未按 0 渲染）', { n: summary.uncoveredBuckets })}
            </span>
          )}
          {trend.unparsableRows > 0 && (
            <span className="text-destructive" data-testid="trend-unparsable-note">
              {t('trend.unparsableNote', '{{n}} 行时间戳无法解析，已忽略', { n: trend.unparsableRows })}
            </span>
          )}
        </footer>
      )}
    </section>
  )
}

/** 粒度分段控件（点一下切换；当前项 aria-pressed + 高亮）。 */
function GranularitySwitch({
  value,
  onChange,
}: {
  value: TrendGranularity
  onChange: (g: TrendGranularity) => void
}) {
  return (
    <div
      data-testid="trend-granularity"
      role="group"
      aria-label={t('trend.granularityLabel', '时间粒度')}
      className="flex shrink-0 items-center gap-0.5 rounded-lg border border-border/50 bg-bg-secondary/50 p-0.5"
    >
      {TREND_GRANULARITY_ORDER.map((g) => {
        const meta = GRANULARITY_META[g]
        const Icon = meta.icon
        const active = g === value
        return (
          <button
            key={g}
            type="button"
            data-testid={`trend-granularity-${g}`}
            aria-pressed={active}
            onClick={() => onChange(g)}
            className={`flex items-center gap-1 rounded-md px-2 py-0.5 text-[10px] transition-colors ${
              active
                ? 'bg-bg-primary text-foreground shadow-sm'
                : 'text-muted-foreground hover:bg-bg-secondary/70 hover:text-foreground'
            }`}
          >
            <Icon className="size-3" />
            {t(meta.labelKey, meta.label)}
          </button>
        )
      })}
    </div>
  )
}

function MiniStat({ label, value, sub, tone }: { label: string; value: string; sub?: string; tone?: string }) {
  return (
    <div className="min-w-0 rounded-lg border border-border/40 bg-bg-primary/40 px-2 py-1.5">
      <div className="truncate text-[9px] uppercase tracking-wide text-muted-foreground">{label}</div>
      <div className={`truncate font-mono text-sm font-semibold tabular-nums ${tone ?? ''}`} title={value}>
        {value}
      </div>
      {sub ? <div className="truncate font-mono text-[9px] text-muted-foreground">{sub}</div> : null}
    </div>
  )
}
