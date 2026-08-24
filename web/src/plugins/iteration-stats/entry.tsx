/**
 * xbot.iteration-stats —— 独立 ESM 插件入口（顶栏 / 状态栏徽章）。
 *
 * 此模块由 PluginRuntime 通过 `/plugins/xbot.iteration-stats/web/index.js`
 * 动态 import 加载（与第三方插件完全相同的路径）。它不 import 任何宿主
 * 内部模块 —— React 和全局实时指标通过 window 全局获取（宿主在
 * iteration-render.tsx 中暴露）。
 */
const w = window as unknown as {
  React: typeof import('react')
  __xbot_iteration__: {
    getGlobalLiveStats: () => LiveStreamStats
    subscribeGlobalLiveStats: (cb: () => void) => () => void
  }
}

interface LiveStreamStats {
  tokensPerSec?: number
  ttftMs?: number
  completionTokens?: number
}

const { getGlobalLiveStats, subscribeGlobalLiveStats } = w.__xbot_iteration__
const React = w.React

function fmtMs(ms?: number): string {
  if (!ms || ms <= 0) return ''
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

export default function IterStatsBadge() {
  const live = React.useSyncExternalStore(subscribeGlobalLiveStats, getGlobalLiveStats)
  const tps = live.tokensPerSec
  if (!tps || tps <= 0) return null
  // 只显示 tok/s + ttft，不再渲染绿点（用户要求去掉绿点 ping）。
  const children = [
    React.createElement('span', { key: 'tps', className: 'font-mono text-[10px] sm:text-xs font-semibold text-emerald-600 dark:text-emerald-400' },
      `${tps.toFixed(0)} tok/s`),
  ]
  // ttft 手机/桌面都渲染（用户要求手机端也显示）。字号 10px（手机）保持紧凑，
  // 桌面 12px。不再按 isTouch 隐藏。
  if (live.ttftMs !== undefined && live.ttftMs > 0) {
    children.push(React.createElement('span', { key: 'ttft', className: 'text-[9px] sm:text-xs text-muted-foreground/70 tabular-nums' },
      `· ttft ${fmtMs(live.ttftMs)}`))
  }
  return React.createElement('span', { className: 'inline-flex items-center gap-1 sm:gap-1.5 whitespace-nowrap' }, ...children)
}
