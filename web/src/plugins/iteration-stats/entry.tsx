/**
 * xbot.iteration-stats —— 独立 ESM 插件入口（状态栏徽章 + 趋势面板）。
 *
 * 本模块由 PluginRuntime 通过 `/plugins/xbot.iteration-stats/web/index.js`
 * 动态 import 加载（与第三方插件完全相同的路径）。它**不 import 任何宿主
 * 内部模块的运行时值** —— React 与实时指标桥都从 window 全局获取（宿主在
 * plugin-runtime 中注入，见 bridge.ts），RPC / i18n / config / events 能力
 * 在 `activate(ctx)` 时注入 bridge 的模块级单例。
 *
 * 单入口服务两个 view（宿主 `PluginRuntime.loadViewComponent` 的解析顺序是
 * `mod[view.id]` → `mod.default`）：
 *   - `xbot.iteration-stats.badge`（容器 status_bar_right）→ 实时 tok/s 徽章
 *   - `xbot.iteration-stats.trend`（容器 right_sidebar）→ 多粒度用量趋势面板
 * ⛔ 因此**故意不导出 default** —— 有 default 时宿主会把它当成所有视图的组件
 * （趋势面板会渲染成徽章）。
 *
 * 配置（showTTFT）经 bridge 的 config 单例读写：`wireConfig()` 读一次 +
 * 订阅变更（设置面板改 showTTFT 后徽章实时生效，无需重载）。
 */
import { React, getConfigSnapshot, setPluginCtx, subscribeConfig, wireConfig, wireRefreshEvents } from './bridge'
import { IterationStatsTrendPanel } from './TrendPanel'

// ── 实时指标桥（宿主注入 window.__xbot_iteration__）────────────────────────

interface LiveStreamStats {
  tokensPerSec?: number
  ttftMs?: number
  completionTokens?: number
}

interface LiveStatsBridge {
  getGlobalLiveStats: () => LiveStreamStats
  subscribeGlobalLiveStats: (cb: () => void) => () => void
}

function getLiveBridge(): LiveStatsBridge | null {
  const b = (window as unknown as { __xbot_iteration__?: LiveStatsBridge }).__xbot_iteration__
  return b && typeof b.getGlobalLiveStats === 'function' && typeof b.subscribeGlobalLiveStats === 'function'
    ? b
    : null
}

const NOOP_UNSUBSCRIBE = () => {}
const EMPTY_LIVE_STATS: LiveStreamStats = {}

/**
 * activate(ctx)：注入插件能力（i18n / rpc / config / events）。
 * ctx 由 PluginRuntime 传入（buildContext 构建）。
 */
export function activate(ctx: unknown): void {
  setPluginCtx(ctx)
  // showTTFT 配置：读一次 + 订阅变更（徽章 useSyncExternalStore 消费）。
  wireConfig()
  // turn.ended / session.switched → 趋势面板自动刷新（无 events 权限时静默降级）。
  wireRefreshEvents()
}

function fmtMs(ms?: number): string {
  if (!ms || ms <= 0) return ''
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

/** 状态栏徽章：流式期间显示 tok/s（+ 可选 ttft）。非流式无实时指标 ⇒ 不渲染。 */
export function IterStatsBadge() {
  const bridge = React.useMemo(() => getLiveBridge(), [])
  const subscribe = bridge ? bridge.subscribeGlobalLiveStats : () => NOOP_UNSUBSCRIBE
  const snapshot = bridge ? bridge.getGlobalLiveStats : () => EMPTY_LIVE_STATS
  const live = React.useSyncExternalStore(subscribe, snapshot)
  const cfg = React.useSyncExternalStore(subscribeConfig, getConfigSnapshot)

  const tps = live?.tokensPerSec
  // 只在 streaming（tok/s > 0）时显示 —— 非流式没有实时指标。
  if (!tps || tps <= 0) return null
  const ttft = live?.ttftMs
  const hasTTFT = cfg.showTTFT && ttft !== undefined && ttft > 0

  return (
    <span className="inline-flex items-center gap-1 whitespace-nowrap sm:gap-1.5">
      {/* 手机版：紧凑单行 pill（去掉 ttft 前缀词）。 */}
      <span className="font-mono text-[9px] font-semibold tabular-nums whitespace-nowrap text-emerald-600 sm:hidden dark:text-emerald-400">
        {`${tps.toFixed(0)}t/s${hasTTFT ? ` · ${fmtMs(ttft)}` : ''}`}
      </span>
      {/* 桌面版：完整「tok/s · ttft X」。 */}
      <span className="hidden font-mono text-xs font-semibold tabular-nums whitespace-nowrap text-emerald-600 sm:inline dark:text-emerald-400">
        {`${tps.toFixed(0)} tok/s${hasTTFT ? ` · ttft ${fmtMs(ttft)}` : ''}`}
      </span>
    </span>
  )
}

// 视图组件按 view.id 命名导出（宿主 mod[view.id] 解析）——两个 view 共用
// 同一份 index.js 产物，无需第二个 esbuild 入口。
export {
  IterStatsBadge as 'xbot.iteration-stats.badge',
  IterationStatsTrendPanel as 'xbot.iteration-stats.trend',
}
