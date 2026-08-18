/**
 * xbot.iteration-stats —— 内置迭代指标插件。
 *
 * 通过 `container: 'iteration'` 的 view 贡献点注入到每个迭代的渲染处，
 * 用 useIterationStats() 读取当前迭代的 token / TTFT / tool 耗时 / 实时
 * tokens-per-sec，渲染一条紧凑的指标行。纯公开 API（无宿主后门）。
 */
import { useIterationStats } from '@/plugin-runtime/iteration-render'
import type { LiveStreamStats } from '@/plugin-api'

function fmtMs(ms?: number): string {
  if (!ms || ms <= 0) return '—'
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

function LiveStatsRow({ live }: { live: LiveStreamStats }) {
  const tps = live.tokensPerSec
  // IterationSlot 只在 LiveIteration（streaming 中）渲染 → live 存在即表示
  // 正在流式生成。有 tkps 显示实时速度；stream_stats 尚未到达（首个 SSE 帧
  // 或后端未带）时也显示 streaming 占位 —— 用户明确要求"只要开始收到 SSE
  // 没数据时也该显示 streaming"。
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
  const { live } = useIterationStats()
  // 只在 live（streaming 中）显示实时指标，committed 迭代不再逐条渲染 stats
  // 卡片 —— 那正是「每个迭代都显示很烦」的来源。live 只有一个（当前正在
  // streaming 的迭代），显示在上方不打扰；tok/s + ttft 本身就是要的实时反馈。
  if (live) return <LiveStatsRow live={live} />
  return null
}