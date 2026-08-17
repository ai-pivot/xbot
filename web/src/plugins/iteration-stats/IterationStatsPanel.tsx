/**
 * xbot.iteration-stats —— 内置迭代指标插件。
 *
 * 通过 `container: 'iteration'` 的 view 贡献点注入到每个迭代的渲染处，
 * 用 useIterationStats() 读取当前迭代的 token / TTFT / tool 耗时 / 实时
 * tokens-per-sec，渲染一条紧凑的指标行。纯公开 API（无宿主后门）。
 */
import { useIterationStats } from '@/plugin-runtime/iteration-render'
import type { IterationStats, LiveStreamStats } from '@/plugin-api'

function fmtMs(ms?: number): string {
  if (!ms || ms <= 0) return '—'
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

function fmtTokens(n?: number): string {
  if (n === undefined || n <= 0) return '—'
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k`
  return String(n)
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <span className="inline-flex items-center gap-1 whitespace-nowrap">
      <span className="text-[10px] uppercase tracking-wide text-text-muted">{label}</span>
      <span className="font-mono text-xs text-text-secondary">{value}</span>
    </span>
  )
}

function IterationStatsRow({ stats }: { stats: IterationStats }) {
  return (
    <div className="flex items-center gap-3 rounded-md border border-[var(--border)] bg-[var(--bg-elevated)] px-2 py-1 text-xs">
      <span className="font-mono font-semibold text-text-primary">#{stats.iteration}</span>
      <Metric label="tokens" value={fmtTokens(stats.tokens)} />
      <Metric label="ttft" value={fmtMs(stats.ttftMs)} />
      <Metric label="tools" value={fmtMs(stats.toolMs)} />
      {stats.tokensPerSec !== undefined && stats.tokensPerSec > 0 && (
        <Metric label="tok/s" value={stats.tokensPerSec.toFixed(0)} />
      )}
    </div>
  )
}

function LiveStatsRow({ live }: { live: LiveStreamStats }) {
  const tps = live.tokensPerSec
  return (
    <div className="flex items-center gap-3 rounded px-2 py-1 text-[11px]">
      <span className="inline-flex items-center gap-1.5 whitespace-nowrap">
        <span className="relative flex h-2 w-2">
          <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-400 opacity-60" />
          <span className="relative inline-flex h-2 w-2 rounded-full bg-emerald-500" />
        </span>
        <span className="font-mono text-xs font-semibold text-emerald-600">
          {tps !== undefined && tps > 0 ? `${tps.toFixed(0)} tok/s` : 'streaming'}
        </span>
      </span>
      {live.ttftMs !== undefined && live.ttftMs > 0 && (
        <span className="text-xs text-text-muted">ttft {fmtMs(live.ttftMs)}</span>
      )}
    </div>
  )
}

/** 内置视图组件：由 <IterationSlot> 渲染并注入数据。 */
export function IterationStatsPanel() {
  const { stats, live } = useIterationStats()
  if (stats) return <IterationStatsRow stats={stats} />
  if (live) return <LiveStatsRow live={live} />
  return null
}