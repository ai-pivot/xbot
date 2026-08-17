/**
 * xbot.iteration-stats —— 独立 ESM 插件入口。
 *
 * 此模块由 PluginRuntime 通过 `/plugins/xbot.iteration-stats/web/index.js`
 * 动态 import 加载（与第三方插件完全相同的路径）。它不 import 任何宿主
 * 内部模块 —— React 和 useIterationStats 通过 window 全局获取（宿主在
 * iteration-render.tsx 中暴露）。
 *
 * 构建方式：esbuild --bundle --format=esm --jsx=transform
 * React 不打包（external），运行时从 window.React 获取。
 */

// 从 window 获取宿主暴露的 API（不 import 内部模块）。
const w = window as unknown as {
  React: typeof import('react')
  __xbot_iteration__: { useIterationStats: () => IterationRenderData }
}

interface IterationStats {
  iteration: number
  tokens?: number
  ttftMs?: number
  tokensPerSec?: number
  toolMs?: number
}

interface LiveStreamStats {
  tokensPerSec?: number
  ttftMs?: number
  completionTokens?: number
}

interface IterationRenderData {
  stats?: IterationStats
  live?: LiveStreamStats
}

const { useIterationStats } = w.__xbot_iteration__
const React = w.React

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
  return React.createElement('span', { className: 'inline-flex items-center gap-1 whitespace-nowrap' },
    React.createElement('span', { className: 'text-[10px] uppercase tracking-wide text-text-muted' }, label),
    React.createElement('span', { className: 'font-mono text-xs text-text-secondary' }, value),
  )
}

function IterationStatsRow({ stats }: { stats: IterationStats }) {
  const children = [
    React.createElement('span', { key: 'iter', className: 'font-mono font-semibold text-text-primary' }, `#${stats.iteration}`),
    React.createElement(Metric, { key: 'tokens', label: 'tokens', value: fmtTokens(stats.tokens) }),
    React.createElement(Metric, { key: 'ttft', label: 'ttft', value: fmtMs(stats.ttftMs) }),
    React.createElement(Metric, { key: 'tools', label: 'tools', value: fmtMs(stats.toolMs) }),
  ]
  if (stats.tokensPerSec !== undefined && stats.tokensPerSec > 0) {
    children.push(React.createElement(Metric, { key: 'tps', label: 'tok/s', value: stats.tokensPerSec.toFixed(0) }))
  }
  return React.createElement('div', { className: 'flex items-center gap-3 rounded-md border border-[var(--border)] bg-[var(--bg-elevated)] px-2 py-1 text-xs' }, ...children)
}

function LiveStatsRow({ live }: { live: LiveStreamStats }) {
  const tps = live.tokensPerSec
  const dot = React.createElement('span', { className: 'relative flex h-2 w-2' },
    React.createElement('span', { className: 'absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-400 opacity-60' }),
    React.createElement('span', { className: 'relative inline-flex h-2 w-2 rounded-full bg-emerald-500' }),
  )
  const label = React.createElement('span', { className: 'font-mono text-xs font-semibold text-emerald-600' },
    tps !== undefined && tps > 0 ? `${tps.toFixed(0)} tok/s` : 'streaming')
  const main = React.createElement('span', { className: 'inline-flex items-center gap-1.5 whitespace-nowrap' }, dot, label)
  const children = [main]
  if (live.ttftMs !== undefined && live.ttftMs > 0) {
    children.push(React.createElement('span', { className: 'text-xs text-text-muted' }, `ttft ${fmtMs(live.ttftMs)}`))
  }
  return React.createElement('div', { className: 'flex items-center gap-3 rounded px-2 py-1 text-[11px]' }, ...children)
}

export default function IterationStatsPanel() {
  const { stats, live } = useIterationStats()
  if (stats) return React.createElement(IterationStatsRow, { stats })
  if (live) return React.createElement(LiveStatsRow, { live })
  return null
}
