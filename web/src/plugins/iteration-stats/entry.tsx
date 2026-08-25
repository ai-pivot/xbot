/**
 * xbot.iteration-stats —— 独立 ESM 插件入口（顶栏 / 状态栏徽章）。
 *
 * 此模块由 PluginRuntime 通过 `/plugins/xbot.iteration-stats/web/index.js`
 * 动态 import 加载（与第三方插件完全相同的路径）。它不 import 任何宿主
 * 内部模块 —— React 和全局实时指标通过 window 全局获取（宿主在
 * iteration-render.tsx 中暴露）。
 *
 * 配置（showTTFT）经 `activate(ctx)` 读写：ctx.config（需 'config' 权限）
 * 读取并订阅变化（onConfigChange），存到模块级 store 供徽章 useSyncExternalStore
 * 消费 —— 设置面板改 showTTFT 后实时生效（无需重载徽章）。
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

interface PluginConfig {
  showTTFT: boolean
}

const { getGlobalLiveStats, subscribeGlobalLiveStats } = w.__xbot_iteration__
const React = w.React

// ── 模块级配置 store（供徽章 useSyncExternalStore 订阅）──────────────────────
const DEFAULT_CONFIG: PluginConfig = { showTTFT: true }
let __config: PluginConfig = DEFAULT_CONFIG
const __configListeners = new Set<() => void>()

function getConfigSnapshot(): PluginConfig {
  return __config
}
function subscribeConfig(cb: () => void): () => void {
  __configListeners.add(cb)
  return () => {
    __configListeners.delete(cb)
  }
}

/**
 * activate(ctx)：读配置并订阅变更（实时生效）。
 * ctx 由 PluginRuntime 传入（buildContext 构建，含 config）。
 */
export function activate(ctx: unknown): void {
  const cfg = (ctx as { config: { get(): Promise<Record<string, unknown>>; onConfigChange(cb: (c: Record<string, unknown>) => void): () => void } }).config
  const apply = (c: Record<string, unknown>) => {
    __config = { showTTFT: c?.showTTFT !== false }
    __configListeners.forEach((f) => f())
  }
  void cfg.get().then(apply).catch(() => {})
  cfg.onConfigChange(apply)
}

function fmtMs(ms?: number): string {
  if (!ms || ms <= 0) return ''
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

export default function IterStatsBadge() {
  const live = React.useSyncExternalStore(subscribeGlobalLiveStats, getGlobalLiveStats)
  const cfg = React.useSyncExternalStore(subscribeConfig, getConfigSnapshot)
  const tps = live.tokensPerSec
  // 只在 streaming（tok/s > 0）时显示 —— 非流式无实时指标。
  if (!tps || tps <= 0) return null
  const showTTFT = cfg.showTTFT
  const ttft = live.ttftMs
  const hasTTFT = showTTFT && ttft !== undefined && ttft > 0

  const children = [
    // 手机版：紧凑单行 pill（9px，去掉 ttft 前缀词），sm 以下显示。
    React.createElement(
      'span',
      { key: 'ttft', className: 'sm:hidden font-mono text-[9px] font-semibold text-emerald-600 dark:text-emerald-400 tabular-nums whitespace-nowrap' },
      `${tps.toFixed(0)}t/s${hasTTFT ? ` · ${fmtMs(ttft)}` : ''}`,
    ),
    // 桌面版：完整「tok/s · ttft X」（12px），sm 及以上显示。
    React.createElement(
      'span',
      { key: 'desktop', className: 'hidden sm:inline font-mono text-xs font-semibold text-emerald-600 dark:text-emerald-400 tabular-nums whitespace-nowrap' },
      `${tps.toFixed(0)} tok/s${hasTTFT ? ` · ttft ${fmtMs(ttft)}` : ''}`,
    ),
  ]
  return React.createElement('span', { className: 'inline-flex items-center gap-1 sm:gap-1.5 whitespace-nowrap' }, ...children)
}
