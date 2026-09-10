/**
 * xbot.session-stats 的视图组件——统计面板（同一 view 双形态）。
 *
 * 一个 view（xbot.session-stats.panel，container: right_sidebar）承载两种形态：
 * - 侧边栏（窄，< 640px）：紧凑指标卡（input/output/cache/TTFT/TPOT/迭代明细）。
 * - 主编辑区（宽，>= 640px）：点击侧边栏「统计详情」按钮经 ctx.ui.openViewTab 把
 *   同一 view 以 editor tab 打开（openEditorViewTab 支持 container 任意——
 *   全宽渲染），此时切换为完整统计面板：当前会话四联卡（含缓存命中圆环）+
 *   全部会话汇总（get_user_token_usage）+ 分日期聚合（get_daily_token_usage，
 *   按天堆叠柱状图 + 明细表，7/30/90 天切换）+ 按模型分组 + 最近迭代表。
 *   图表只在主编辑区渲染，侧边栏不放图表。
 *
 * 数据：get_session_usage_stats（iteration_history v59 聚合）为主；
 * get_user_token_usage / get_daily_token_usage 提供跨会话聚合。
 *
 * 刷新：会话切换（activeSession 变化）、turn.ended 事件（activate 订阅 →
 * 模块级信号）、手动刷新按钮、时间范围切换。requestRef 递增防竞态。
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useSessionStore } from '@/hooks/useSessionStore'
import { usePluginRuntime } from '@/plugin-runtime'
import type { DailyTokenUsage, TenantUsageStats, UserTokenUsage } from '@/plugin-api'
import { useI18n } from '@/providers/i18n'
import { Button } from '@/components/ui/button'
import { BarChart3, Loader2, RefreshCw } from 'lucide-react'
import { subscribeStatsRefresh } from './sessionStats'

// ── 格式化 ─────────────────────────────────────────────────────────────────

/** token 数缩写：12,345 → 12.3k；1,234,567 → 1.23M。 */
function fmtTokens(n: number): string {
  if (!n) return '0'
  if (n >= 1_000_000_000) return `${(n / 1_000_000_000).toFixed(2)}B`
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(2)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`
  return String(n)
}

function fmtInt(n: number): string {
  return (n ?? 0).toLocaleString('en-US')
}

/** 毫秒缩写：850 → 850ms；12,300 → 12.3s；75,000 → 1m05s；10,320,000 → 2h52m。 */
function fmtMs(ms: number): string {
  if (!ms) return '—'
  if (ms < 1_000) return `${Math.round(ms)}ms`
  if (ms < 60_000) return `${(ms / 1_000).toFixed(1)}s`
  const h = Math.floor(ms / 3_600_000)
  const m = Math.floor((ms % 3_600_000) / 60_000)
  if (h > 0) return `${h}h${String(m).padStart(2, '0')}m`
  const s = Math.round((ms % 60_000) / 1_000)
  return `${m}m${String(s).padStart(2, '0')}s`
}

function fmtPct(numerator: number, denominator: number): string {
  if (!denominator) return '—'
  return `${((numerator / denominator) * 100).toFixed(1)}%`
}

/** 长日期（2026-09-09）→ MM-DD（柱状图 X 轴）。 */
function shortDate(d: string): string {
  return d.length >= 10 ? d.slice(5, 10) : d
}

// ── 宽容器（主编辑区）展示组件 ─────────────────────────────────────────────

/** 环形进度（缓存命中率 / 占比类指标）。 */
function Ring({
  percent,
  value,
  label,
  color = 'stroke-emerald-500',
}: {
  percent: number
  value: string
  label: string
  color?: string
}) {
  const p = Math.max(0, Math.min(100, percent))
  const r = 30
  const c = 2 * Math.PI * r
  return (
    <div className="relative size-[74px] shrink-0">
      <svg viewBox="0 0 74 74" className="size-full -rotate-90">
        <circle cx="37" cy="37" r={r} fill="none" strokeWidth="7" className="stroke-border/60" />
        <circle
          cx="37"
          cy="37"
          r={r}
          fill="none"
          strokeWidth="7"
          strokeLinecap="round"
          strokeDasharray={c}
          strokeDashoffset={c * (1 - p / 100)}
          className={color}
        />
      </svg>
      <div className="absolute inset-0 flex flex-col items-center justify-center">
        <span className="font-mono text-sm font-semibold tabular-nums">{value}</span>
        <span className="text-[9px] text-muted-foreground">{label}</span>
      </div>
    </div>
  )
}

/** 横向堆叠条 + 图例（上下文构成 / 用量构成）。 */
function StackedBar({
  segments,
}: {
  segments: Array<{ label: string; value: number; color: string }>
}) {
  const total = segments.reduce((s, x) => s + (x.value || 0), 0)
  return (
    <div className="space-y-1.5">
      <div className="flex h-3 w-full overflow-hidden rounded-full bg-bg-secondary">
        {total > 0 &&
          segments.map((s) =>
            s.value > 0 ? (
              <div
                key={s.label}
                className={s.color}
                style={{ width: `${(s.value / total) * 100}%` }}
                title={`${s.label} ${fmtTokens(s.value)}`}
              />
            ) : null,
          )}
      </div>
      <div className="flex flex-wrap gap-x-3 gap-y-1">
        {segments.map((s) => (
          <div key={s.label} className="flex items-center gap-1.5 text-[10px]">
            <span className={`size-2 shrink-0 rounded-sm ${s.color}`} />
            <span className="text-muted-foreground">{s.label}</span>
            <span className="font-mono tabular-nums">{fmtTokens(s.value)}</span>
            <span className="font-mono tabular-nums text-muted-foreground">
              {total > 0 ? fmtPct(s.value, total) : '—'}
            </span>
          </div>
        ))}
      </div>
    </div>
  )
}

/** 分日期堆叠柱状图（每根柱：output 紫 / 未命中 input 蓝 / 命中 cache 绿，自底向上）。 */
function DailyBars({
  data,
}: {
  data: Array<{ date: string; input: number; cached: number; output: number }>
}) {
  const max = Math.max(1, ...data.map((d) => d.input + d.output))
  return (
    <div className="flex h-[132px] items-end gap-[3px]">
      {data.map((d) => {
        const h = (d.input + d.output) / max
        const uncached = Math.max(d.input - d.cached, 0)
        return (
          <div
            key={d.date}
            className="flex h-full flex-1 flex-col justify-end"
            title={`${d.date}\nin ${fmtTokens(d.input)} · cache ${fmtTokens(d.cached)} (${fmtPct(
              d.cached,
              d.input,
            )})\nout ${fmtTokens(d.output)}`}
          >
            <div
              className="flex w-full flex-col justify-end overflow-hidden rounded-sm"
              style={{ height: `${Math.max(h * 100, 2)}%` }}
            >
              <div className="w-full bg-violet-500/80" style={{ flexGrow: d.output || 0.0001 }} />
              <div className="w-full bg-sky-500/80" style={{ flexGrow: uncached || 0.0001 }} />
              <div className="w-full bg-emerald-500/80" style={{ flexGrow: d.cached || 0.0001 }} />
            </div>
          </div>
        )
      })}
    </div>
  )
}

/** 宽容器区块卡片。 */
function Card({
  title,
  right,
  className = '',
  children,
}: {
  title: string
  right?: React.ReactNode
  className?: string
  children: React.ReactNode
}) {
  return (
    <section className={`rounded-lg border border-border/50 bg-bg-secondary/25 p-3 ${className}`}>
      <header className="mb-2 flex items-center justify-between gap-2">
        <h3 className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">{title}</h3>
        {right}
      </header>
      {children}
    </section>
  )
}

/** 宽容器指标项（label + 大数字 + 可选 sub）。 */
function Stat({ label, value, sub, tone }: { label: string; value: string; sub?: string; tone?: string }) {
  return (
    <div className="min-w-0">
      <div className="truncate text-[10px] text-muted-foreground">{label}</div>
      <div className={`truncate font-mono text-base font-semibold tabular-nums ${tone ?? ''}`} title={value}>
        {value}
      </div>
      {sub ? <div className="truncate font-mono text-[10px] text-muted-foreground">{sub}</div> : null}
    </div>
  )
}

// ── 窄容器（侧边栏）指标卡 ─────────────────────────────────────────────────

function Metric({ label, value, sub, title }: { label: string; value: string; sub?: string; title?: string }) {
  return (
    // min-h 统一卡片高度（2 行/3 行卡片视觉对齐）；truncate + title 防长模型名
    // 换行撑高（旧版 deepseek-v4.1-flash-expires-on-0910 会换 2-3 行导致同行卡片不齐）。
    <div className="flex min-h-[56px] min-w-0 flex-col justify-between rounded-lg bg-bg-secondary/50 px-3 py-2">
      <div className="truncate text-[10px] uppercase tracking-wide text-muted-foreground">{label}</div>
      <div className="truncate font-mono text-sm font-semibold tabular-nums" title={title ?? value}>
        {value}
      </div>
      {sub ? <div className="truncate font-mono text-[10px] text-muted-foreground">{sub}</div> : null}
    </div>
  )
}

// ── 主组件 ─────────────────────────────────────────────────────────────────

export function SessionStatsPanel() {
  const runtime = usePluginRuntime()
  const activeSession = useSessionStore().activeSession
  const { t } = useI18n()

  // 容器宽度自适应：>= 640px 视为主编辑区形态（完整统计 + 图表），否则侧边栏紧凑形态。
  const rootRef = useRef<HTMLDivElement>(null)
  const [wide, setWide] = useState(false)
  useEffect(() => {
    const el = rootRef.current
    if (!el) return
    const ro = new ResizeObserver((entries) => {
      for (const e of entries) setWide(e.contentRect.width >= 640)
    })
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  const [stats, setStats] = useState<TenantUsageStats | null>(null)
  const [userUsage, setUserUsage] = useState<UserTokenUsage | null>(null)
  const [daily, setDaily] = useState<DailyTokenUsage[]>([])
  const [days, setDays] = useState(30)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const requestRef = useRef(0)

  const load = useCallback(async () => {
    const id = ++requestRef.current
    setLoading(true)
    setError('')
    try {
      const [sessionRes, userRes, dailyRes] = await Promise.all([
        activeSession
          ? runtime.rpc.call('get_session_usage_stats', {
              channel: activeSession.channel,
              chat_id: activeSession.chatID,
              limit: 40,
            })
          : Promise.resolve(null),
        runtime.rpc.call('get_user_token_usage', {} as never),
        runtime.rpc.call('get_daily_token_usage', { days }),
      ])
      if (id !== requestRef.current) return
      setStats(sessionRes)
      setUserUsage(userRes)
      setDaily(Array.isArray(dailyRes) ? dailyRes : [])
    } catch (e) {
      if (id !== requestRef.current) return
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (id === requestRef.current) setLoading(false)
    }
  }, [runtime, activeSession, days])

  // 会话切换 / 时间范围切换 / 手动刷新时加载。
  useEffect(() => {
    void load()
  }, [load])

  // turn.ended 自动刷新（activate 订阅 → 模块级信号）。load 存 ref 防 stale closure。
  const loadRef = useRef(load)
  loadRef.current = load
  useEffect(() => subscribeStatsRefresh(() => void loadRef.current()), [])

  // 分日期数据按天聚合（原始维度是 date × model）。
  const byDate = useMemo(() => {
    const m = new Map<string, { date: string; input: number; cached: number; output: number; calls: number }>()
    for (const d of daily) {
      const cur = m.get(d.date) ?? { date: d.date, input: 0, cached: 0, output: 0, calls: 0 }
      cur.input += d.input_tokens || 0
      cur.cached += d.cached_tokens || 0
      cur.output += d.output_tokens || 0
      cur.calls += d.llm_call_count || 0
      m.set(d.date, cur)
    }
    return [...m.values()].sort((a, b) => a.date.localeCompare(b.date))
  }, [daily])

  const dailyTotal = useMemo(
    () =>
      byDate.reduce(
        (acc, d) => ({
          input: acc.input + d.input,
          cached: acc.cached + d.cached,
          output: acc.output + d.output,
          calls: acc.calls + d.calls,
        }),
        { input: 0, cached: 0, output: 0, calls: 0 },
      ),
    [byDate],
  )

  const sessionTotal = stats ? stats.input_tokens + stats.output_tokens : 0
  const cacheRate = stats ? fmtPct(stats.cached_tokens, stats.input_tokens) : '—'

  // 侧边栏 → 主编辑区：把同一 view 以 editor tab 打开（openViewTab 支持
  // container 任意——editor tab 全宽渲染；同 key 聚焦已有 tab）。
  const openOverview = () => {
    runtime.ui?.openViewTab?.({
      viewId: 'xbot.session-stats.panel',
      title: t('plugins.sessionStats.openOverview'),
      icon: 'chart',
      key: 'xbot.session-stats.panel',
    })
  }

  return (
    <div ref={rootRef} className="flex h-full flex-col">
      {/* 工具栏：窄=「统计详情」展开入口 + 刷新；宽=时间范围切换 + 刷新。 */}
      <div className="flex items-center justify-between gap-1 border-b border-border/40 px-1.5 py-1">
        {wide ? (
          <div className="flex items-center gap-1">
            {[7, 30, 90].map((d) => (
              <Button
                key={d}
                size="sm"
                variant={days === d ? 'secondary' : 'ghost'}
                className="h-6 px-2 text-[10px]"
                onClick={() => setDays(d)}
              >
                {t('plugins.sessionStats.daysRange', { days: d })}
              </Button>
            ))}
          </div>
        ) : (
          <Button
            size="sm"
            variant="ghost"
            className="h-6 gap-1 px-1.5 text-[10px]"
            title={t('plugins.sessionStats.openOverviewHint')}
            onClick={openOverview}
          >
            <BarChart3 className="size-3.5" />
            {t('plugins.sessionStats.openOverview')}
          </Button>
        )}
        <Button
          size="sm"
          variant="ghost"
          className="size-6 p-0"
          title={t('plugins.sessionStats.refresh')}
          onClick={() => void load()}
        >
          {loading ? (
            <Loader2 className="size-3.5 animate-spin" />
          ) : (
            <RefreshCw className="size-3.5" />
          )}
        </Button>
      </div>

      <div className={wide ? 'flex-1 overflow-y-auto p-3' : 'flex-1 space-y-3 overflow-y-auto p-3'}>
        {error ? (
          <div className="py-4 text-center text-sm text-destructive">{error}</div>
        ) : wide ? (
          /* ════════ 宽容器：完整统计面板（主编辑区） ════════ */
          <div className="space-y-3">
            {/* ── Row 1：当前会话四联卡 ── */}
            <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-4">
              <Card title={t('plugins.sessionStats.overviewSession')}>
                <div className="grid grid-cols-2 gap-x-3 gap-y-2">
                  <Stat label={t('plugins.sessionStats.turns')} value={fmtInt(stats?.turn_count ?? 0)} />
                  <Stat label={t('plugins.sessionStats.iterations')} value={fmtInt(stats?.iteration_count ?? 0)} />
                  <Stat label={t('plugins.sessionStats.input')} value={fmtTokens(stats?.input_tokens ?? 0)} />
                  <Stat label={t('plugins.sessionStats.output')} value={fmtTokens(stats?.output_tokens ?? 0)} />
                  <Stat
                    label={t('plugins.sessionStats.cacheHit')}
                    value={fmtTokens(stats?.cached_tokens ?? 0)}
                    sub={cacheRate}
                    tone="text-emerald-600 dark:text-emerald-400"
                  />
                  <Stat label={t('plugins.sessionStats.llmDuration')} value={fmtMs(stats?.llm_total_ms ?? 0)} />
                </div>
              </Card>

              <Card title={t('plugins.sessionStats.tokenBreakdown')}>
                <div className="flex items-center gap-4">
                  <Ring
                    percent={stats && stats.input_tokens ? (stats.cached_tokens / stats.input_tokens) * 100 : 0}
                    value={cacheRate}
                    label={t('plugins.sessionStats.cacheRate')}
                  />
                  <div className="min-w-0 flex-1 space-y-1.5">
                    {(
                      [
                        { label: t('plugins.sessionStats.input'), value: stats?.input_tokens ?? 0, dot: 'bg-sky-500' },
                        { label: t('plugins.sessionStats.cacheHit'), value: stats?.cached_tokens ?? 0, dot: 'bg-emerald-500' },
                        { label: t('plugins.sessionStats.output'), value: stats?.output_tokens ?? 0, dot: 'bg-violet-500' },
                      ] as const
                    ).map((row) => (
                      <div key={row.label} className="flex items-center justify-between gap-2 text-[10px]">
                        <span className="flex items-center gap-1.5">
                          <span className={`size-2 rounded-sm ${row.dot}`} />
                          <span className="text-muted-foreground">{row.label}</span>
                        </span>
                        <span className="flex items-baseline gap-1.5">
                          <span className="font-mono tabular-nums">{fmtTokens(row.value)}</span>
                          <span className="w-9 text-right font-mono tabular-nums text-muted-foreground">
                            {sessionTotal ? fmtPct(row.value, sessionTotal) : '—'}
                          </span>
                        </span>
                      </div>
                    ))}
                    <div className="flex items-center justify-between gap-2 border-t border-border/40 pt-1.5 text-[10px]">
                      <span className="text-muted-foreground">{t('plugins.sessionStats.total')}</span>
                      <span className="font-mono font-semibold tabular-nums">{fmtTokens(sessionTotal)}</span>
                    </div>
                  </div>
                </div>
              </Card>

              <Card title={t('plugins.sessionStats.performance')}>
                <div className="grid grid-cols-3 gap-2">
                  <Stat label="TTFT" value={fmtMs(stats?.avg_ttft_ms ?? 0)} />
                  <Stat label="TPOT" value={stats?.avg_tpot_ms ? `${stats.avg_tpot_ms.toFixed(0)}ms` : '—'} />
                  <Stat label="tok/s" value={stats?.avg_tokens_per_sec ? stats.avg_tokens_per_sec.toFixed(0) : '—'} />
                </div>
                <div className="mt-2 border-t border-border/40 pt-2">
                  <Stat
                    label={t('plugins.sessionStats.currentModel')}
                    value={stats?.current_model || '—'}
                    sub={stats?.session_created_at ? `since ${stats.session_created_at.slice(0, 16).replace('T', ' ')}` : undefined}
                  />
                </div>
              </Card>

              <Card title={t('plugins.sessionStats.contextUsage')}>
                <div className="mb-2 flex items-baseline gap-1.5">
                  <span className="font-mono text-lg font-semibold tabular-nums">
                    {fmtTokens(stats?.last_prompt_tokens ?? 0)}
                  </span>
                  <span className="text-[10px] text-muted-foreground">
                    + {fmtTokens(stats?.last_completion_tokens ?? 0)}
                  </span>
                </div>
                <StackedBar
                  segments={[
                    { label: t('plugins.sessionStats.prompt'), value: stats?.last_prompt_tokens ?? 0, color: 'bg-sky-500' },
                    { label: t('plugins.sessionStats.completion'), value: stats?.last_completion_tokens ?? 0, color: 'bg-violet-500' },
                  ]}
                />
              </Card>
            </div>

            {/* ── Row 2：全部会话汇总 ── */}
            <Card
              title={t('plugins.sessionStats.allSessions')}
              right={
                userUsage?.sender_id ? (
                  <span className="font-mono text-[10px] text-muted-foreground">{userUsage.sender_id.slice(0, 12)}…</span>
                ) : undefined
              }
            >
              <div className="grid grid-cols-2 gap-x-4 gap-y-2 sm:grid-cols-3 xl:grid-cols-6">
                <Stat label={t('plugins.sessionStats.input')} value={fmtTokens(userUsage?.input_tokens ?? 0)} />
                <Stat label={t('plugins.sessionStats.output')} value={fmtTokens(userUsage?.output_tokens ?? 0)} />
                <Stat
                  label={t('plugins.sessionStats.cacheHit')}
                  value={fmtTokens(userUsage?.cached_tokens ?? 0)}
                  sub={fmtPct(userUsage?.cached_tokens ?? 0, userUsage?.input_tokens ?? 0)}
                  tone="text-emerald-600 dark:text-emerald-400"
                />
                <Stat label={t('plugins.sessionStats.total')} value={fmtTokens(userUsage?.total_tokens ?? 0)} />
                <Stat label={t('plugins.sessionStats.conversations')} value={fmtInt(userUsage?.conversation_count ?? 0)} />
                <Stat label={t('plugins.sessionStats.llmCalls')} value={fmtInt(userUsage?.llm_call_count ?? 0)} />
              </div>
            </Card>

            {/* ── Row 3：分日期趋势 + 明细 ── */}
            <div className="grid grid-cols-1 gap-3 xl:grid-cols-5">
              <Card
                title={t('plugins.sessionStats.dailyTrend')}
                className="xl:col-span-3"
                right={
                  <div className="flex items-center gap-2 text-[10px] text-muted-foreground">
                    <span className="flex items-center gap-1">
                      <span className="size-2 rounded-sm bg-sky-500/80" />
                      {t('plugins.sessionStats.input')}
                    </span>
                    <span className="flex items-center gap-1">
                      <span className="size-2 rounded-sm bg-emerald-500/80" />
                      {t('plugins.sessionStats.cacheHit')}
                    </span>
                    <span className="flex items-center gap-1">
                      <span className="size-2 rounded-sm bg-violet-500/80" />
                      {t('plugins.sessionStats.output')}
                    </span>
                  </div>
                }
              >
                {byDate.length === 0 ? (
                  <div className="py-10 text-center text-xs text-muted-foreground">
                    {t('plugins.sessionStats.noData')}
                  </div>
                ) : (
                  <>
                    <DailyBars data={byDate} />
                    <div className="mt-1 flex justify-between font-mono text-[9px] text-muted-foreground">
                      <span>{shortDate(byDate[0].date)}</span>
                      {byDate.length > 2 && <span>{shortDate(byDate[Math.floor(byDate.length / 2)].date)}</span>}
                      <span>{shortDate(byDate[byDate.length - 1].date)}</span>
                    </div>
                  </>
                )}
              </Card>

              <Card title={t('plugins.sessionStats.dailyDetail')} className="xl:col-span-2">
                <div className="max-h-[152px] overflow-y-auto">
                  <table className="w-full whitespace-nowrap font-mono text-[10px] tabular-nums">
                    <thead className="sticky top-0 bg-bg-secondary/80 backdrop-blur">
                      <tr className="text-muted-foreground">
                        <th className="px-1 py-1 text-left font-medium">{t('plugins.sessionStats.date')}</th>
                        <th className="px-1 py-1 text-right font-medium">In</th>
                        <th className="px-1 py-1 text-right font-medium">Cache</th>
                        <th className="px-1 py-1 text-right font-medium">Out</th>
                        <th className="px-1 py-1 text-right font-medium">Calls</th>
                      </tr>
                    </thead>
                    <tbody>
                      {[...byDate].reverse().map((d) => (
                        <tr key={d.date} className="border-t border-border/30">
                          <td className="px-1 py-1 text-left text-muted-foreground">{shortDate(d.date)}</td>
                          <td className="px-1 py-1 text-right">{fmtTokens(d.input)}</td>
                          <td className="px-1 py-1 text-right text-emerald-600 dark:text-emerald-400">
                            {d.input ? fmtPct(d.cached, d.input) : '—'}
                          </td>
                          <td className="px-1 py-1 text-right">{fmtTokens(d.output)}</td>
                          <td className="px-1 py-1 text-right">{fmtInt(d.calls)}</td>
                        </tr>
                      ))}
                      {byDate.length > 0 && (
                        <tr className="border-t border-border/60 font-semibold">
                          <td className="px-1 py-1 text-left">{t('plugins.sessionStats.total')}</td>
                          <td className="px-1 py-1 text-right">{fmtTokens(dailyTotal.input)}</td>
                          <td className="px-1 py-1 text-right">{fmtPct(dailyTotal.cached, dailyTotal.input)}</td>
                          <td className="px-1 py-1 text-right">{fmtTokens(dailyTotal.output)}</td>
                          <td className="px-1 py-1 text-right">{fmtInt(dailyTotal.calls)}</td>
                        </tr>
                      )}
                    </tbody>
                  </table>
                </div>
              </Card>
            </div>

            {/* ── Row 4：按模型 + 最近迭代 ── */}
            <div className="grid grid-cols-1 gap-3 xl:grid-cols-5">
              {stats?.by_model && stats.by_model.filter((m) => m.model).length > 0 && (
                <Card title={t('plugins.sessionStats.byModel')} className="xl:col-span-2">
                  <div className="space-y-1.5">
                    {stats.by_model
                      .filter((m) => m.model)
                      .map((m) => (
                        <div key={m.model} className="rounded-md bg-bg-secondary/40 px-2.5 py-1.5">
                          <div className="flex items-center justify-between gap-2">
                            <span className="truncate font-mono text-[11px] font-medium" title={m.model}>
                              {m.model}
                            </span>
                            <span className="shrink-0 font-mono text-[10px] text-muted-foreground">
                              {fmtInt(m.iterations)} iters
                            </span>
                          </div>
                          <div className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 font-mono text-[10px] tabular-nums text-muted-foreground">
                            <span>in {fmtTokens(m.input_tokens)}</span>
                            <span>cache {fmtTokens(m.cached_tokens)}</span>
                            <span>out {fmtTokens(m.output_tokens)}</span>
                            <span>ttft {fmtMs(m.avg_ttft_ms)}</span>
                            <span>tpot {m.avg_tpot_ms ? `${m.avg_tpot_ms.toFixed(0)}ms` : '—'}</span>
                          </div>
                        </div>
                      ))}
                  </div>
                </Card>
              )}

              {stats?.recent_iterations && stats.recent_iterations.length > 0 && (
                <Card
                  title={t('plugins.sessionStats.recentIterations', { count: stats.recent_iterations.length })}
                  className={
                    stats.by_model && stats.by_model.filter((m) => m.model).length > 0 ? 'xl:col-span-3' : 'xl:col-span-5'
                  }
                >
                  <div className="max-h-[220px] overflow-y-auto">
                    <table className="w-full whitespace-nowrap font-mono text-[10px] tabular-nums">
                      <thead className="sticky top-0 bg-bg-secondary/80 backdrop-blur">
                        <tr className="text-muted-foreground">
                          <th className="px-1.5 py-1 text-left font-medium">T.I</th>
                          <th className="px-1.5 py-1 text-left font-medium">{t('plugins.sessionStats.model')}</th>
                          <th className="px-1.5 py-1 text-right font-medium">In</th>
                          <th className="px-1.5 py-1 text-right font-medium">Cache</th>
                          <th className="px-1.5 py-1 text-right font-medium">Out</th>
                          <th className="px-1.5 py-1 text-right font-medium">TTFT</th>
                          <th className="px-1.5 py-1 text-right font-medium">TPOT</th>
                          <th className="px-1.5 py-1 text-right font-medium">tok/s</th>
                        </tr>
                      </thead>
                      <tbody>
                        {stats.recent_iterations.map((it, i) => (
                          <tr key={`${it.turn_id}-${it.iteration}-${i}`} className="border-t border-border/30">
                            <td className="px-1.5 py-1 text-left text-muted-foreground">
                              {it.turn_id}.{it.iteration}
                            </td>
                            <td className="max-w-[180px] truncate px-1.5 py-1 text-left" title={it.model}>
                              {it.model || '—'}
                            </td>
                            <td className="px-1.5 py-1 text-right">{fmtTokens(it.input_tokens)}</td>
                            <td className="px-1.5 py-1 text-right text-emerald-600 dark:text-emerald-400">
                              {it.cached_tokens ? fmtPct(it.cached_tokens, it.input_tokens) : '—'}
                            </td>
                            <td className="px-1.5 py-1 text-right">{fmtTokens(it.output_tokens)}</td>
                            <td className="px-1.5 py-1 text-right">{fmtMs(it.ttft_ms)}</td>
                            <td className="px-1.5 py-1 text-right">{it.tpot_ms ? `${it.tpot_ms}ms` : '—'}</td>
                            <td className="px-1.5 py-1 text-right">{it.tokens_per_sec || '—'}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                </Card>
              )}
            </div>
          </div>
        ) : /* ════════ 窄容器：紧凑面板（侧边栏，原版内容） ════════ */
        !activeSession && !loading ? (
          <div className="py-8 text-center text-sm text-muted-foreground">{t('plugins.sessionStats.noActiveSession')}</div>
        ) : !stats ? (
          loading ? (
            <div className="flex items-center justify-center py-8">
              <Loader2 className="size-4 animate-spin text-muted-foreground" />
            </div>
          ) : (
            <div className="py-8 text-center text-sm text-muted-foreground">{t('plugins.sessionStats.noData')}</div>
          )
        ) : (
          <>
            {/* ── 用量指标 ── */}
            <div className="grid grid-cols-2 gap-1.5">
              <Metric label={t('plugins.sessionStats.input')} value={fmtTokens(stats.input_tokens)} />
              <Metric label={t('plugins.sessionStats.output')} value={fmtTokens(stats.output_tokens)} />
              <Metric
                label={t('plugins.sessionStats.cacheHit')}
                value={fmtTokens(stats.cached_tokens)}
                sub={t('plugins.sessionStats.cacheRate', { rate: cacheRate })}
              />
              <Metric label={t('plugins.sessionStats.llmDuration')} value={fmtMs(stats.llm_total_ms)} sub={t('plugins.sessionStats.iterationsTurns', { count: stats.iteration_count, turns: stats.turn_count })} />
            </div>

            {/* ── 性能指标 ── */}
            <div className="grid grid-cols-3 gap-1.5">
              <Metric label="Avg TTFT" value={fmtMs(stats.avg_ttft_ms)} />
              <Metric label="Avg TPOT" value={stats.avg_tpot_ms ? `${stats.avg_tpot_ms.toFixed(0)}ms` : '—'} />
              <Metric
                label="Avg tok/s"
                value={stats.avg_tokens_per_sec ? stats.avg_tokens_per_sec.toFixed(0) : '—'}
              />
            </div>

            {/* ── 上下文水位 + 模型 ── */}
            <div className="grid grid-cols-2 gap-1.5">
              <Metric label={t('plugins.sessionStats.contextLevel')} value={fmtTokens(stats.last_prompt_tokens)} sub={`completion ${fmtTokens(stats.last_completion_tokens)}`} />
              <Metric label={t('plugins.sessionStats.currentModel')} value={stats.current_model || '—'} sub={stats.session_created_at ? `since ${stats.session_created_at.slice(5, 16)}` : undefined} />
            </div>

            {/* ── per-model 分组（多模型时才显示） ── */}
            {stats.by_model && stats.by_model.filter((m) => m.model).length > 1 && (
              <div>
                <div className="mb-1 text-[10px] uppercase tracking-wide text-muted-foreground">{t('plugins.sessionStats.byModel')}</div>
                <div className="space-y-1">
                  {stats.by_model.map((m) => (
                    // 两行布局（模型名 / 指标）——旧版单行塞 4 个指标，329px 窄栏下
                    // 右侧数字被压到换行或溢出（用户："拥挤"）。
                    <div key={m.model} className="rounded-lg bg-bg-secondary/40 px-2.5 py-1.5">
                      <div className="truncate font-mono text-[11px] font-medium" title={m.model}>
                        {m.model}
                      </div>
                      <div className="mt-0.5 flex flex-wrap gap-x-2.5 font-mono text-[10px] tabular-nums text-muted-foreground">
                        <span>in {fmtTokens(m.input_tokens)}</span>
                        <span>out {fmtTokens(m.output_tokens)}</span>
                        <span>cache {fmtTokens(m.cached_tokens)}</span>
                        <span>{m.iterations} iters</span>
                      </div>
                    </div>
                  ))}
                </div>
              </div>
            )}

            {/* ── 最近迭代明细 ── */}
            {stats.recent_iterations && stats.recent_iterations.length > 0 && (
              <div>
                <div className="mb-1 text-[10px] uppercase tracking-wide text-muted-foreground">
                  {t('plugins.sessionStats.recentIterations', { count: stats.recent_iterations.length })}
                </div>
                <div className="overflow-x-auto rounded-md border border-border">
                  <table className="w-full whitespace-nowrap font-mono text-[10px] tabular-nums">
                    <thead>
                      <tr className="border-b border-border text-muted-foreground">
                        <th className="px-1.5 py-1 text-left font-medium">T.I</th>
                        <th className="px-1.5 py-1 text-right font-medium">In</th>
                        <th className="px-1.5 py-1 text-right font-medium">Cache</th>
                        <th className="px-1.5 py-1 text-right font-medium">Out</th>
                        <th className="px-1.5 py-1 text-right font-medium">TTFT</th>
                        <th className="px-1.5 py-1 text-right font-medium">TPOT</th>
                        <th className="px-1.5 py-1 text-right font-medium">tok/s</th>
                      </tr>
                    </thead>
                    <tbody>
                      {stats.recent_iterations.map((it, i) => (
                        <tr key={`${it.turn_id}-${it.iteration}-${i}`} className="border-b border-border/50 last:border-0">
                          <td className="px-1.5 py-1 text-left text-muted-foreground">
                            {it.turn_id}.{it.iteration}
                          </td>
                          <td className="px-1.5 py-1 text-right">{fmtTokens(it.input_tokens)}</td>
                          <td className="px-1.5 py-1 text-right text-emerald-600 dark:text-emerald-400">
                            {it.cached_tokens ? fmtPct(it.cached_tokens, it.input_tokens) : '—'}
                          </td>
                          <td className="px-1.5 py-1 text-right">{fmtTokens(it.output_tokens)}</td>
                          <td className="px-1.5 py-1 text-right">{fmtMs(it.ttft_ms)}</td>
                          <td className="px-1.5 py-1 text-right">
                            {it.tpot_ms ? `${it.tpot_ms}ms` : '—'}
                          </td>
                          <td className="px-1.5 py-1 text-right">{it.tokens_per_sec || '—'}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </div>
            )}
          </>
        )}
      </div>
    </div>
  )
}
