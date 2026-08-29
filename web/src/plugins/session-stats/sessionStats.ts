/**
 * xbot.session-stats — 内置前端插件：当前会话用量统计面板。
 *
 * 数据源：get_session_usage_stats 核心 RPC（iteration_history v59 聚合 ——
 * per-iteration input/cached tokens + model，按 tenant 聚合出 input/output/
 * cache 命中、TTFT/TPOT 均值、上下文水位与最近迭代明细）。
 *
 * 形态：内置插件（静态 import + activateBuiltin + builtin: 视图），
 * 同 xbot.skill-manager 范式。RPC 经 runtime.rpc.call 无点号直传 /api/rpc。
 *
 * 自动刷新：activate 订阅 turn.ended 事件（宿主 SSE 桥 emitPluginEvent 派发），
 * turn 结束（usage 已落库）后广播模块级刷新信号，面板订阅
 * （iteration-stats __configListeners 同模式）→ 重拉聚合数据。
 */
import type { PluginContext, PluginManifest, Disposable } from '@/plugin-api'

// ── 模块级刷新信号（activate 的 event handler → 面板组件）──────────────────
// 事件 handler 在 React 树外，经模块级 Set 广播；面板挂载时订阅、卸载时退订。
const refreshListeners = new Set<() => void>()

function notifyStatsRefresh(): void {
  refreshListeners.forEach((f) => f())
}

/** 面板订阅 turn.ended 驱动的刷新信号。 */
export function subscribeStatsRefresh(cb: () => void): () => void {
  refreshListeners.add(cb)
  return () => {
    refreshListeners.delete(cb)
  }
}

export const manifest = {
  id: 'xbot.session-stats',
  name: 'Session Stats',
  version: '0.1.0',
  description: '当前会话用量统计：token / cache 命中 / TTFT / TPOT / 迭代明细',
  permissions: ['rpc', 'ui', 'events'] as const,
  contributes: [
    {
      kind: 'view',
      id: 'xbot.session-stats.panel',
      container: 'right_sidebar',
      title: '统计',
      icon: 'chart',
      entry: 'builtin:xbot.session-stats.panel',
    },
  ],
} satisfies PluginManifest

export function activate(ctx: PluginContext<typeof manifest.permissions>): Disposable | void {
  // turn 结束后 usage 已写入 iteration_history，广播刷新信号让面板重拉聚合。
  // progress.iteration 不刷 —— 迭代中途 usage 行尚未落库，刷了也是旧数据。
  if (ctx.events) {
    ctx.events.on('turn.ended', () => {
      notifyStatsRefresh()
    })
  }
  return () => {}
}
