/**
 * xbot.iteration-stats —— 独立 ESM 插件入口（顶栏 / 状态栏徽章）。
 *
 * 此模块由 PluginRuntime 通过 `/plugins/xbot.iteration-stats/web/index.js`
 * 动态 import 加载（与第三方插件完全相同的路径）。它不 import 任何宿主
 * 内部模块 —— React 和全局实时指标通过 window 全局获取（宿主在
 * iteration-render.tsx 中暴露）。
 *
 * 徽章显示在 `status_bar_right` 容器（移动端顶栏 + 桌面端 InfoBar），
 * 数据源是 `window.__xbot_iteration__.useGlobalLiveStats` —— 宿主把当前
 * 流式指标（tok/s、ttft）作为通用实时指标桥暴露，任何状态栏插件可订阅，
 * 不限于迭代统计（解耦：主项目只提供数据桥 + 渲染容器，不写插件 UI）。
 *
 * 构建方式：esbuild --bundle --format=esm --jsx=transform
 * React 不打包（external），运行时从 window.React 获取。
 */

// 从 window 获取宿主暴露的 API（不 import 内部模块）。
const w = window as unknown as {
  React: typeof import('react')
  __xbot_iteration__: { useGlobalLiveStats: () => LiveStreamStats }
}

interface LiveStreamStats {
  tokensPerSec?: number
  ttftMs?: number
  completionTokens?: number
}

const { useGlobalLiveStats } = w.__xbot_iteration__
const React = w.React

function fmtMs(ms?: number): string {
  if (!ms || ms <= 0) return ''
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

/**
 * 顶栏 / 状态栏实时生成指标徽章：
 *   - streaming 时：绿点 ping + tok/s（+ ttft）
 *   - idle（无 tok/s）：隐藏，不占顶栏空间
 * 数据来自全局实时指标桥（useGlobalLiveStats），非迭代处 Context。
 */
export default function IterStatsBadge() {
  const live = useGlobalLiveStats()
  const tps = live.tokensPerSec
  if (!tps || tps <= 0) return null
  const children = [
    React.createElement('span', { key: 'dot', className: 'relative flex h-2 w-2' },
      React.createElement('span', { className: 'absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-400 opacity-70' }),
      React.createElement('span', { className: 'relative inline-flex h-2 w-2 rounded-full bg-emerald-500' }),
    ),
    React.createElement('span', { key: 'tps', className: 'font-mono text-xs font-semibold text-emerald-600 dark:text-emerald-400' },
      `${tps.toFixed(0)} tok/s`),
  ]
  if (live.ttftMs !== undefined && live.ttftMs > 0) {
    children.push(React.createElement('span', { key: 'ttft', className: 'text-muted-foreground/70 tabular-nums' },
      `· ttft ${fmtMs(live.ttftMs)}`))
  }
  return React.createElement('span', { className: 'inline-flex items-center gap-1.5 whitespace-nowrap' }, ...children)
}
