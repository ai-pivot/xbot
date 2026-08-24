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
  const children = [
      React.createElement('span', { key: 'dot', className: 'relative flex h-2 w-2' },
      React.createElement('span', { className: 'absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-400 opacity-70' }),
      React.createElement('span', { className: 'relative inline-flex h-2 w-2 rounded-full bg-emerald-500' }),
    ),
    React.createElement('span', { key: 'tps', className: 'font-mono text-[10px] sm:text-xs font-semibold text-emerald-600 dark:text-emerald-400' },
      `${tps.toFixed(0)} tok/s`),
  ]
  // 手机上只显示 tok/s（去 ttft）——header 空间有限，避免占满；桌面保留 ttft。
  const isTouch = typeof window !== 'undefined' && window.matchMedia?.('(hover: none) and (pointer: coarse)').matches
  if (!isTouch && live.ttftMs !== undefined && live.ttftMs > 0) {
    children.push(React.createElement('span', { key: 'ttft', className: 'hidden sm:inline text-muted-foreground/70 tabular-nums' },
      `· ttft ${fmtMs(live.ttftMs)}`))
  }
  return React.createElement('span', { className: 'inline-flex items-center gap-1 sm:gap-1.5 whitespace-nowrap' }, ...children)
}
